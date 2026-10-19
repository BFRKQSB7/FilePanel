package files

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
	"unicode"

	"filepanel/internal/store"
)

const DefaultMaxUpload int64 = 10 << 30
const defaultReserve uint64 = 16 << 20
const chunkSize = 64 << 10

type Item struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	CreatedAt   int64  `json:"created_at"`
	StorageNode string `json:"storage_node,omitempty"`
}

type Manager struct {
	db          *store.Store
	root        string
	objects     string
	temp        string
	maxUpload   int64
	reserve     uint64
	mu          sync.Mutex
	cond        *sync.Cond
	readers     map[string]int
	reservedNow uint64
	diskInfo    func(string) (uint64, uint64, uint64, error)
}

func New(s *store.Store, dataDir string, maxUpload int64, reserve uint64) (*Manager, error) {
	if maxUpload <= 0 {
		maxUpload = DefaultMaxUpload
	}
	root := filepath.Join(dataDir, "objects")
	objects := filepath.Join(root, "data")
	temp := filepath.Join(root, "tmp")
	for _, dir := range []string{root, objects, temp} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return nil, fmt.Errorf("创建存储目录失败: %w", err)
		}
		info, err := os.Lstat(dir)
		if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return nil, fmt.Errorf("存储目录不是安全的普通目录: %s", dir)
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return nil, err
		}
	}
	if reserve == 0 {
		reserve = defaultReserve
	}
	m := &Manager{db: s, root: root, objects: objects, temp: temp, maxUpload: maxUpload, reserve: reserve, readers: make(map[string]int)}
	m.diskInfo = s.DiskInfo
	m.cond = sync.NewCond(&m.mu)
	if err := m.Recover(); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *Manager) DataDir() string {
	return filepath.Dir(m.root)
}

func randomID() (string, error) {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b[:]), nil
}

func cleanName(name string) (string, error) {
	name = strings.TrimSpace(strings.ReplaceAll(strings.ReplaceAll(name, "\\", "/"), "\x00", ""))
	name = filepath.Base(name)
	name = strings.TrimSpace(name)
	if name == "" || name == "." || name == ".." {
		return "", errors.New("文件名不能为空")
	}
	var out strings.Builder
	for _, r := range name {
		if unicode.IsControl(r) {
			continue
		}
		out.WriteRune(r)
	}
	name = strings.TrimSpace(out.String())
	if name == "" || len([]byte(name)) > 255 {
		return "", errors.New("文件名为空或超过 255 字节")
	}
	return name, nil
}

func (m *Manager) reserveChunk(n uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	total, free, _, err := m.diskInfo(m.root)
	if err != nil {
		return fmt.Errorf("读取磁盘空间失败: %w", err)
	}
	minimum := m.reserve
	if percent := total / 20; percent > minimum {
		minimum = percent
	}
	if free < minimum || m.reservedNow > free-minimum || n > free-minimum-m.reservedNow {
		return errors.New("可用空间不足，已保留系统安全空间")
	}
	m.reservedNow += n
	return nil
}

func (m *Manager) releaseChunk(n uint64) {
	m.mu.Lock()
	if n > m.reservedNow {
		m.reservedNow = 0
	} else {
		m.reservedNow -= n
	}
	m.mu.Unlock()
}

func (m *Manager) Upload(ctx context.Context, name string, expected int64, src io.Reader) (Item, error) {
	return m.UploadChecked(ctx, name, expected, src, nil)
}

// UploadChecked validates the enclosing request before marking any bytes as
// verified. An error leaves only a failed upload, never a file recovery can publish.
func (m *Manager) UploadChecked(ctx context.Context, name string, expected int64, src io.Reader, check func() error) (item Item, err error) {
	id, err := randomID()
	if err != nil {
		return item, err
	}
	return m.UploadID(ctx, id, name, expected, src, check)
}

// UploadID allows an authenticated transfer to use a stable, opaque object ID.
// The caller serializes retries; existing uploads are never silently replaced.
func (m *Manager) UploadID(ctx context.Context, id, name string, expected int64, src io.Reader, check func() error) (item Item, err error) {
	if len(id) < 24 || len(id) > 64 || strings.ContainsAny(id, "/\\.") {
		return item, errors.New("无效对象 ID")
	}
	for _, c := range id {
		if !(c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '-' || c == '_') {
			return item, errors.New("无效对象 ID")
		}
	}
	name, err = cleanName(name)
	if err != nil {
		return item, err
	}
	if expected > m.maxUpload {
		return item, errors.New("文件超过单文件大小上限")
	}
	objKey := id
	tempKey := id + ".part"
	now := time.Now().Unix()
	var expectedValue any
	if expected >= 0 {
		expectedValue = expected
	}
	if _, err = m.db.DB.Exec(`INSERT INTO uploads(id,object_key,temp_key,display_name,expected_size,state,created_at,updated_at) VALUES(?,?,?,?,?,'receiving',?,?)`, id, objKey, tempKey, name, expectedValue, now, now); err != nil {
		return item, err
	}
	tempPath := filepath.Join(m.temp, tempKey)
	f, err := os.OpenFile(tempPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		m.failUpload(id, tempPath, err)
		return item, fmt.Errorf("创建临时文件失败: %w", err)
	}
	keep := false
	defer func() {
		if !keep {
			_ = f.Close()
			m.failUpload(id, tempPath, err)
		}
	}()
	h := sha256.New()
	buf := make([]byte, chunkSize)
	var size int64
	for {
		if err = ctx.Err(); err != nil {
			return item, err
		}
		n, readErr := src.Read(buf)
		if n > 0 {
			if size+int64(n) > m.maxUpload {
				err = errors.New("文件超过单文件大小上限")
				return item, err
			}
			if expected >= 0 && size+int64(n) > expected {
				err = errors.New("上传内容超过声明大小")
				return item, err
			}
			if err = m.reserveChunk(uint64(n)); err != nil {
				return item, err
			}
			written, writeErr := f.Write(buf[:n])
			m.releaseChunk(uint64(n))
			if writeErr != nil {
				err = writeErr
				return item, fmt.Errorf("写入临时文件失败: %w", err)
			}
			if written != n {
				err = io.ErrShortWrite
				return item, err
			}
			_, _ = h.Write(buf[:n])
			size += int64(n)
		}
		if readErr != nil {
			if errors.Is(readErr, io.EOF) {
				break
			}
			err = readErr
			return item, fmt.Errorf("接收上传内容失败: %w", err)
		}
		if n == 0 {
			continue
		}
	}
	if expected >= 0 && size != expected {
		err = errors.New("上传大小与声明值不一致")
		return item, err
	}
	if check != nil {
		if err = check(); err != nil {
			return item, err
		}
	}
	if err = ctx.Err(); err != nil {
		return item, err
	}
	if err = f.Sync(); err != nil {
		return item, fmt.Errorf("同步临时文件失败: %w", err)
	}
	if err = f.Close(); err != nil {
		return item, fmt.Errorf("关闭临时文件失败: %w", err)
	}
	sha := hex.EncodeToString(h.Sum(nil))
	if _, err = m.db.DB.Exec("UPDATE uploads SET received_size=?,sha256=?,state='verified',updated_at=? WHERE id=? AND state='receiving'", size, sha, time.Now().Unix(), id); err != nil {
		return item, err
	}
	if err = os.Rename(tempPath, filepath.Join(m.objects, objKey)); err != nil {
		return item, fmt.Errorf("提交对象文件失败: %w", err)
	}
	if err = m.finishVerified(id, objKey, name, size, sha, now); err != nil {
		return item, err
	}
	keep = true
	return Item{ID: id, Name: name, Size: size, SHA256: sha, CreatedAt: now}, nil
}

func (m *Manager) failUpload(id, tempPath string, cause error) {
	_ = os.Remove(tempPath)
	message := "上传失败"
	if cause != nil {
		message = cause.Error()
	}
	_, _ = m.db.DB.Exec("UPDATE uploads SET state='failed',error=?,updated_at=? WHERE id=? AND state='receiving'", message, time.Now().Unix(), id)
}

func (m *Manager) finishVerified(id, objKey, name string, size int64, sha string, created int64) error {
	tx, err := m.db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT INTO files(id,object_key,display_name,size,sha256,state,created_at,updated_at) SELECT ?,?,?,?,?,'available',?,? WHERE NOT EXISTS(SELECT 1 FROM object_staging WHERE id=?) ON CONFLICT(id) DO NOTHING`, id, objKey, name, size, sha, created, time.Now().Unix(), id); err != nil {
		return err
	}
	if _, err = tx.Exec("UPDATE uploads SET state='available',updated_at=? WHERE id=?", time.Now().Unix(), id); err != nil {
		return err
	}
	return tx.Commit()
}

type ReadHandle struct {
	*os.File
	manager *Manager
	id      string
	once    sync.Once
}

func (h *ReadHandle) Close() error {
	err := h.File.Close()
	h.once.Do(func() {
		h.manager.mu.Lock()
		h.manager.readers[h.id]--
		if h.manager.readers[h.id] <= 0 {
			delete(h.manager.readers, h.id)
			h.manager.cond.Broadcast()
		}
		h.manager.mu.Unlock()
	})
	return err
}

func (m *Manager) Open(id string) (Item, *ReadHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var item Item
	var key string
	err := m.db.DB.QueryRow("SELECT id,object_key,display_name,size,sha256,created_at FROM files WHERE id=? AND state='available'", id).Scan(&item.ID, &key, &item.Name, &item.Size, &item.SHA256, &item.CreatedAt)
	if err != nil {
		return item, nil, err
	}
	if strings.HasPrefix(key, "remote:") {
		return item, nil, errors.New("文件保存在远端节点")
	}
	path := filepath.Join(m.objects, key)
	info, err := os.Lstat(path)
	if err != nil {
		return item, nil, err
	}
	if !info.Mode().IsRegular() || info.Mode()&os.ModeSymlink != 0 {
		return item, nil, errors.New("存储对象不是普通文件")
	}
	f, err := os.Open(path)
	if err != nil {
		return item, nil, err
	}
	m.readers[key]++
	return item, &ReadHandle{File: f, manager: m, id: key}, nil
}

func (m *Manager) List() ([]Item, error) {
	rows, err := m.db.DB.Query("SELECT f.id,f.display_name,f.size,f.sha256,f.created_at,COALESCE(l.node_id,'') FROM files f LEFT JOIN node_locations l ON l.file_id=f.id WHERE f.state='available' ORDER BY f.created_at DESC,f.id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]Item, 0)
	for rows.Next() {
		var item Item
		if err := rows.Scan(&item.ID, &item.Name, &item.Size, &item.SHA256, &item.CreatedAt, &item.StorageNode); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (m *Manager) Metadata(id string) (Item, error) {
	var v Item
	err := m.db.DB.QueryRow(`SELECT f.id,f.display_name,f.size,f.sha256,f.created_at,COALESCE(l.node_id,'') FROM files f LEFT JOIN node_locations l ON l.file_id=f.id WHERE f.id=? AND f.state='available'`, id).Scan(&v.ID, &v.Name, &v.Size, &v.SHA256, &v.CreatedAt, &v.StorageNode)
	return v, err
}

func (m *Manager) Rename(id, name string) error {
	name, err := cleanName(name)
	if err != nil {
		return err
	}
	result, err := m.db.DB.Exec("UPDATE files SET display_name=?,updated_at=? WHERE id=? AND state='available'", name, time.Now().Unix(), id)
	if err != nil {
		return err
	}
	n, _ := result.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

func (m *Manager) Remove(id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var key string
	err := m.db.DB.QueryRow("SELECT object_key FROM files WHERE id=? AND state IN ('available','deleting')", id).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if strings.HasPrefix(key, "remote:") {
		return errors.New("远端文件须通过节点生命周期删除")
	}
	result, err := m.db.DB.Exec("UPDATE files SET state='deleting',updated_at=? WHERE id=? AND object_key=?", time.Now().Unix(), id, key)
	if err != nil {
		return err
	}
	if n, _ := result.RowsAffected(); n != 1 {
		return errors.New("文件主位置已变化，请重试删除")
	}
	for m.readers[key] > 0 {
		m.cond.Wait()
	}
	if err = os.Remove(filepath.Join(m.objects, key)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("对象清理失败，将在后续恢复重试: %w", err)
	}
	_, err = m.db.DB.Exec("DELETE FROM files WHERE id=? AND state='deleting'", id)
	return err
}

func (m *Manager) Recover() error {
	rows, err := m.db.DB.Query("SELECT id,object_key,temp_key,display_name,expected_size,received_size,sha256,state,created_at FROM uploads")
	if err != nil {
		return err
	}
	type upload struct {
		id, objectKey, tempKey, name, state string
		sha                                 sql.NullString
		expected, size                      sql.NullInt64
		created                             int64
	}
	var all []upload
	for rows.Next() {
		var u upload
		if err := rows.Scan(&u.id, &u.objectKey, &u.tempKey, &u.name, &u.expected, &u.size, &u.sha, &u.state, &u.created); err != nil {
			rows.Close()
			return err
		}
		all = append(all, u)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return err
	}
	rows.Close()
	knownTemp, knownObjects := map[string]bool{}, map[string]bool{}
	for _, u := range all {
		knownTemp[u.tempKey], knownObjects[u.objectKey] = true, true
		tempPath, objectPath := filepath.Join(m.temp, u.tempKey), filepath.Join(m.objects, u.objectKey)
		switch u.state {
		case "receiving", "failed":
			_ = os.Remove(tempPath)
			if u.state == "receiving" {
				_, err = m.db.DB.Exec("UPDATE uploads SET state='failed',error='服务重启时上传未完成',updated_at=? WHERE id=?", time.Now().Unix(), u.id)
				if err != nil {
					return err
				}
			}
		case "verified":
			if _, err := os.Stat(objectPath); errors.Is(err, os.ErrNotExist) {
				if _, tempErr := os.Stat(tempPath); tempErr == nil {
					if err = os.Rename(tempPath, objectPath); err != nil {
						return err
					}
				} else {
					_, err = m.db.DB.Exec("UPDATE uploads SET state='failed',error='已校验对象缺失',updated_at=? WHERE id=?", time.Now().Unix(), u.id)
					if err != nil {
						return err
					}
					continue
				}
			} else if err != nil {
				return err
			}
			f, openErr := os.Open(objectPath)
			if openErr != nil {
				return openErr
			}
			h := sha256.New()
			got, copyErr := io.Copy(h, f)
			closeErr := f.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			if got != u.size.Int64 || !u.sha.Valid || hex.EncodeToString(h.Sum(nil)) != u.sha.String {
				_, err = m.db.DB.Exec("UPDATE uploads SET state='failed',error='恢复校验失败',updated_at=? WHERE id=?", time.Now().Unix(), u.id)
				if err != nil {
					return err
				}
				continue
			}
			if err = m.finishVerified(u.id, u.objectKey, u.name, got, u.sha.String, u.created); err != nil {
				return err
			}
		case "available":
			if _, err := os.Stat(objectPath); errors.Is(err, os.ErrNotExist) {
				_, err = m.db.DB.Exec("UPDATE files SET state='failed',updated_at=? WHERE id=? AND object_key=? AND state='available'", time.Now().Unix(), u.id, u.objectKey)
				if err != nil {
					return err
				}
			} else if err != nil {
				return err
			}
		}
	}
	files, err := m.db.DB.Query("SELECT id,object_key FROM files WHERE state='deleting' AND NOT EXISTS (SELECT 1 FROM node_locations l WHERE l.file_id=files.id)")
	if err != nil {
		return err
	}
	type deletingFile struct{ id, key string }
	var deleting []deletingFile
	for files.Next() {
		var item deletingFile
		if err := files.Scan(&item.id, &item.key); err != nil {
			files.Close()
			return err
		}
		deleting = append(deleting, item)
	}
	if err := files.Err(); err != nil {
		files.Close()
		return err
	}
	if err := files.Close(); err != nil {
		return err
	}
	for _, item := range deleting {
		if err := os.Remove(filepath.Join(m.objects, item.key)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if _, err := m.db.DB.Exec("DELETE FROM files WHERE id=? AND state='deleting'", item.id); err != nil {
			return err
		}
	}
	entries, err := os.ReadDir(m.temp)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !knownTemp[entry.Name()] {
			_ = os.Remove(filepath.Join(m.temp, entry.Name()))
		}
	}
	entries, err = os.ReadDir(m.objects)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		if !knownObjects[entry.Name()] {
			_ = os.Remove(filepath.Join(m.objects, entry.Name()))
		}
	}
	return nil
}

// CheckSpace 检查再写入 n 字节后是否仍保留系统安全空间（不占用预留）。供小型素材写入使用。
func (m *Manager) CheckSpace(n uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	total, free, _, err := m.diskInfo(m.root)
	if err != nil {
		return fmt.Errorf("读取磁盘空间失败: %w", err)
	}
	minimum := m.reserve
	if percent := total / 20; percent > minimum {
		minimum = percent
	}
	if free < minimum || m.reservedNow > free-minimum || n > free-minimum-m.reservedNow {
		return errors.New("可用空间不足，已保留系统安全空间")
	}
	return nil
}

// ReserveSpace keeps small metadata/material writes in the same in-flight
// budget as uploads. Release is idempotent and must be deferred by the caller.
func (m *Manager) ReserveSpace(n uint64) (func(), error) {
	if e := m.reserveChunk(n); e != nil {
		return nil, e
	}
	var once sync.Once
	return func() { once.Do(func() { m.releaseChunk(n) }) }, nil
}

// HoldSpace lets the existing narrow test spaces remain usable while real
// managers reserve bytes across the complete filesystem write.
func HoldSpace(s interface{ CheckSpace(uint64) error }, n uint64) (func(), error) {
	if r, ok := s.(interface{ ReserveSpace(uint64) (func(), error) }); ok {
		return r.ReserveSpace(n)
	}
	if e := s.CheckSpace(n); e != nil {
		return nil, e
	}
	return func() {}, nil
}
