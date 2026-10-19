// Package backup creates private, bounded directory snapshots while the caller
// holds the data-directory lock and the service is stopped. No live DB copy.
package backup

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"filepanel/internal/release"
	"filepanel/internal/store"
	_ "modernc.org/sqlite"
)

const MaxFiles = 100000
const MaxBytes int64 = 1 << 40

type Entry struct {
	Path   string `json:"path"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}
type Manifest struct {
	Format      int     `json:"format"`
	Schema      int     `json:"schema"`
	Role        string  `json:"role"`
	RemoteFiles int     `json:"remote_files_not_included"`
	Entries     []Entry `json:"entries"`
}

// CheckSpace is shared by maintenance writes. Streaming writes recheck free
// space at each chunk; callers reserve the complete peak before stopping service.
func CheckSpace(path string, n, reserve uint64) error {
	var s store.Store
	total, free, _, e := s.DiskInfo(path)
	if e != nil {
		return e
	}
	if reserve < 16<<20 {
		reserve = 16 << 20
	}
	if total/20 > reserve {
		reserve = total / 20
	}
	if free < reserve || n > free-reserve {
		return errors.New("空间不足，保留非零安全余量及维护峰值")
	}
	return nil
}
func safe(s string) bool {
	return s != "" && s != "." && filepath.ToSlash(filepath.Clean(s)) == s && !strings.HasPrefix(s, "/") && !strings.ContainsAny(s, "\\:\x00") && s != ".." && !strings.HasPrefix(s, "../")
}
func ordinary(root, relative string) (string, error) {
	if !safe(relative) {
		return "", errors.New("备份路径越界")
	}
	p := root
	parts := strings.Split(relative, "/")
	for i, s := range parts {
		p = filepath.Join(p, s)
		st, e := os.Lstat(p)
		if e != nil {
			return "", e
		}
		if st.Mode()&os.ModeSymlink != 0 || (i < len(parts)-1 && !st.IsDir()) || (i == len(parts)-1 && !st.Mode().IsRegular()) {
			return "", errors.New("备份不允许链接或特殊文件")
		}
	}
	return p, nil
}
func copyFile(src, dst string, reserve uint64) (Entry, error) {
	var out Entry
	f, e := os.Open(src)
	if e != nil {
		return out, e
	}
	defer f.Close()
	if e = os.MkdirAll(filepath.Dir(dst), 0700); e != nil {
		return out, e
	}
	g, e := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if e != nil {
		return out, e
	}
	defer g.Close()
	h := sha256.New()
	buf := make([]byte, 64<<10)
	for {
		n, re := f.Read(buf)
		if n > 0 {
			if e = CheckSpace(filepath.Dir(dst), uint64(n), reserve); e != nil {
				return out, e
			}
			if _, e = g.Write(buf[:n]); e != nil {
				return out, e
			}
			h.Write(buf[:n])
			out.Size += int64(n)
			if out.Size > MaxBytes {
				return out, errors.New("备份文件超限")
			}
		}
		if re == io.EOF {
			break
		}
		if re != nil {
			return out, re
		}
	}
	if e = g.Sync(); e != nil {
		return out, e
	}
	out.SHA256 = hex.EncodeToString(h.Sum(nil))
	return out, nil
}
func exclude(s string) bool {
	return s == "filepanel.lock" || s == "filepanel.db" || s == "filepanel.db-wal" || s == "filepanel.db-shm" || s == "objects/tmp" || s == "profile/staging"
}
func paths(root string) ([]string, int64, error) {
	var list []string
	var size int64
	e := filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if exclude(rel) {
			if d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if d.IsDir() {
			return nil
		}
		st, e := d.Info()
		if e != nil {
			return e
		}
		if !st.Mode().IsRegular() || !safe(rel) {
			return errors.New("数据目录含链接或特殊文件，拒绝快照")
		}
		list = append(list, rel)
		size += st.Size()
		if len(list) >= MaxFiles || size > MaxBytes {
			return errors.New("备份规模超限")
		}
		return nil
	})
	return list, size, e
}
func Estimate(root string) (int64, error) {
	_, n, e := paths(root)
	if e != nil {
		return 0, e
	}
	for _, s := range []string{"filepanel.db", "filepanel.db-wal"} {
		st, er := os.Stat(filepath.Join(root, s))
		if er == nil {
			n += st.Size()
		} else if !os.IsNotExist(er) {
			return 0, er
		}
	}
	return n + 4<<20, nil
}
func openDB(path string) (*sql.DB, error) {
	return sql.Open("sqlite", "file:"+filepath.ToSlash(path)+"?mode=ro&_pragma=busy_timeout(5000)")
}

// Create never overwrites an existing backup. The final name appears only after
// every file and SQLite snapshot has been synced and verified.
func Create(root, dest string, reserve uint64) (m Manifest, err error) {
	root, _ = filepath.Abs(root)
	dest, _ = filepath.Abs(dest)
	if dest == root || strings.HasPrefix(dest, root+string(os.PathSeparator)) {
		return m, errors.New("备份须位于数据目录外")
	}
	if _, e := os.Lstat(dest); !os.IsNotExist(e) {
		return m, errors.New("备份目标必须不存在")
	}
	list, size, e := paths(root)
	if e != nil {
		return m, e
	}
	estimate, e := Estimate(root)
	if e != nil {
		return m, e
	}
	if e = CheckSpace(filepath.Dir(dest), uint64(estimate), reserve); e != nil {
		return m, e
	}
	stage, e := os.MkdirTemp(filepath.Dir(dest), ".filepanel-backup-")
	if e != nil {
		return m, e
	}
	defer os.RemoveAll(stage)
	db, e := openDB(filepath.Join(root, "filepanel.db"))
	if e != nil {
		return m, e
	}
	defer db.Close()
	m.Format = 1
	if e = db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&m.Schema); e != nil {
		return m, e
	}
	if m.Schema != release.Schema {
		return m, errors.New("备份 schema 不兼容")
	}
	var node int
	if e = db.QueryRow("SELECT COUNT(*) FROM settings WHERE key='node_identity'").Scan(&node); e != nil {
		return m, e
	}
	m.Role = "panel"
	if node != 0 {
		m.Role = "node"
	}
	if e = db.QueryRow("SELECT COUNT(*) FROM node_locations").Scan(&m.RemoteFiles); e != nil {
		return m, e
	}
	// VACUUM INTO includes committed WAL pages; no migration or recovery runs here.
	target := filepath.Join(stage, "filepanel.db")
	if _, e = db.Exec("VACUUM INTO '" + strings.ReplaceAll(filepath.ToSlash(target), "'", "''") + "'"); e != nil {
		return m, e
	}
	if e = os.Chmod(target, 0600); e != nil {
		return m, e
	}
	f, e := os.OpenFile(target, os.O_RDWR, 0600)
	if e != nil {
		return m, e
	}
	e = f.Sync()
	f.Close()
	if e != nil {
		return m, e
	}
	sum, n, e := release.Digest(target)
	if e != nil {
		return m, e
	}
	m.Entries = append(m.Entries, Entry{"filepanel.db", n, sum})
	_ = size
	for _, s := range list {
		src, e := ordinary(root, s)
		if e != nil {
			return m, e
		}
		v, e := copyFile(src, filepath.Join(stage, filepath.FromSlash(s)), reserve)
		if e != nil {
			return m, e
		}
		v.Path = s
		m.Entries = append(m.Entries, v)
	}
	b, e := json.MarshalIndent(m, "", "  ")
	if e != nil {
		return m, e
	}
	if e = os.WriteFile(filepath.Join(stage, "snapshot.json"), b, 0600); e != nil {
		return m, e
	}
	if _, e = Verify(stage); e != nil {
		return m, e
	}
	if e = syncTree(stage); e != nil {
		return m, e
	}
	return m, os.Rename(stage, dest)
}
func Verify(root string) (m Manifest, err error) {
	st, e := os.Lstat(root)
	if e != nil {
		return m, e
	}
	if !st.IsDir() || st.Mode()&os.ModeSymlink != 0 {
		return m, errors.New("备份须为普通目录")
	}
	meta, e := ordinary(root, "snapshot.json")
	if e != nil {
		return m, e
	}
	if e = release.Read(meta, &m, 32<<20); e != nil {
		return m, e
	}
	if m.Format != 1 || m.Schema != release.Schema || (m.Role != "panel" && m.Role != "node") || len(m.Entries) == 0 || len(m.Entries) > MaxFiles {
		return m, errors.New("备份格式/版本无效")
	}
	seen := map[string]bool{}
	var total int64
	for _, v := range m.Entries {
		if seen[v.Path] || v.Path == "snapshot.json" || exclude(v.Path) && v.Path != "filepanel.db" || v.Size < 0 || v.Size > MaxBytes-total {
			return m, errors.New("备份重复、临时路径或大小超限")
		}
		seen[v.Path] = true
		total += v.Size
		p, e := ordinary(root, v.Path)
		if e != nil {
			return m, e
		}
		h, n, e := release.Digest(p)
		if e != nil {
			return m, e
		}
		if h != v.SHA256 || n != v.Size {
			return m, fmt.Errorf("备份内容校验失败: %s", v.Path)
		}
	}
	if !seen["filepanel.db"] {
		return m, errors.New("缺少 SQLite 快照")
	}
	// Reject unlisted files, including links hidden beside listed objects.
	e = filepath.WalkDir(root, func(p string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if p == root {
			return nil
		}
		rel, _ := filepath.Rel(root, p)
		rel = filepath.ToSlash(rel)
		if d.IsDir() {
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 || rel != "snapshot.json" && !seen[rel] {
			return errors.New("备份包含未列出的文件或链接")
		}
		return nil
	})
	if e != nil {
		return m, e
	}
	db, e := openDB(filepath.Join(root, "filepanel.db"))
	if e != nil {
		return m, e
	}
	defer db.Close()
	var check string
	if e = db.QueryRow("PRAGMA integrity_check").Scan(&check); e != nil || check != "ok" {
		return m, errors.New("SQLite 完整性检查失败")
	}
	rows, e := db.Query("PRAGMA foreign_key_check")
	if e != nil {
		return m, e
	}
	bad := rows.Next()
	rows.Close()
	if bad {
		return m, errors.New("SQLite 外键检查失败")
	}
	var schema, node int
	if e = db.QueryRow("SELECT MAX(version) FROM schema_migrations").Scan(&schema); e != nil || schema != m.Schema {
		return m, errors.New("SQLite schema 不匹配")
	}
	if e = db.QueryRow("SELECT COUNT(*) FROM settings WHERE key='node_identity'").Scan(&node); e != nil || (node > 0) != (m.Role == "node") {
		return m, errors.New("备份身份不匹配")
	}
	rows, e = db.Query("SELECT object_key,size,sha256 FROM files WHERE state='available' AND object_key NOT LIKE 'remote:%'")
	if e != nil {
		return m, e
	}
	for rows.Next() {
		var key, hash string
		var n int64
		if e = rows.Scan(&key, &n, &hash); e != nil {
			rows.Close()
			return m, e
		}
		if !release.SafeName(key) {
			rows.Close()
			return m, errors.New("SQLite 对象路径越界")
		}
		p, e := ordinary(root, "objects/data/"+key)
		if e != nil {
			rows.Close()
			return m, e
		}
		s, size, e := release.Digest(p)
		if e != nil || s != hash || size != n {
			rows.Close()
			return m, errors.New("SQLite 引用对象损坏")
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return m, e
	}
	if e = verifyMaterials(db, root); e != nil {
		return m, e
	}
	return m, nil
}

func verifyMaterials(db *sql.DB, root string) error {
	rows, e := db.Query("SELECT object_key,size FROM assets")
	if e != nil {
		return e
	}
	for rows.Next() {
		var key string
		var size int64
		if e = rows.Scan(&key, &size); e != nil {
			rows.Close()
			return e
		}
		if !release.SafeName(key) {
			rows.Close()
			return errors.New("素材引用越界")
		}
		p, e := ordinary(root, "assets/"+key)
		if e != nil {
			rows.Close()
			return e
		}
		st, e := os.Stat(p)
		if e != nil || st.Size() != size {
			rows.Close()
			return errors.New("素材引用缺失或损坏")
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	var set, config string
	e = db.QueryRow("SELECT asset_set,config FROM site_profile WHERE id=1").Scan(&set, &config)
	if errors.Is(e, sql.ErrNoRows) {
		return nil
	}
	if e != nil {
		return e
	}
	var profile struct {
		Assets map[string]struct {
			File   string `json:"file"`
			Size   int64  `json:"size"`
			SHA256 string `json:"sha256"`
		} `json:"assets"`
	}
	if e = json.Unmarshal([]byte(config), &profile); e != nil {
		return e
	}
	for _, a := range profile.Assets {
		if !release.SafeName(set) || !release.SafeName(a.File) {
			return errors.New("个性化引用越界")
		}
		p, e := ordinary(root, "profile/sets/"+set+"/"+a.File)
		if e != nil {
			return e
		}
		h, n, e := release.Digest(p)
		if e != nil || h != a.SHA256 || n != a.Size {
			return errors.New("个性化素材引用缺失或损坏")
		}
	}
	return nil
}

// RestoreNew prepares and verifies a new isolated data directory. The caller
// stops/locks the current instance before switching; existing paths are refused.
func RestoreNew(src, dest string, reserve uint64) (Manifest, error) {
	m, e := Verify(src)
	if e != nil {
		return m, e
	}
	if _, e = os.Lstat(dest); !os.IsNotExist(e) {
		return m, errors.New("恢复目标须为不存在的隔离目录")
	}
	var size uint64
	for _, v := range m.Entries {
		size += uint64(v.Size)
	}
	if e = CheckSpace(filepath.Dir(dest), size+4<<20, reserve); e != nil {
		return m, e
	}
	stage, e := os.MkdirTemp(filepath.Dir(dest), ".filepanel-restore-")
	if e != nil {
		return m, e
	}
	defer os.RemoveAll(stage)
	for _, v := range m.Entries {
		srcPath, e := ordinary(src, v.Path)
		if e != nil {
			return m, e
		}
		got, e := copyFile(srcPath, filepath.Join(stage, filepath.FromSlash(v.Path)), reserve)
		if e != nil {
			return m, e
		}
		if got.Size != v.Size || got.SHA256 != v.SHA256 {
			return m, errors.New("复制期间备份发生变化")
		}
	}
	if e = syncTree(stage); e != nil {
		return m, e
	}
	return m, os.Rename(stage, dest)
}
