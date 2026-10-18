package files

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
)

// Staged objects have their own immutable identity and never enter the file list.
func (m *Manager) UploadStaged(ctx context.Context, id, name string, size int64, src io.Reader, check func() error) (Item, error) {
	if _, err := m.db.DB.Exec("INSERT OR IGNORE INTO object_staging(id) VALUES(?)", id); err != nil {
		return Item{}, err
	}
	if _, err := m.db.DB.Exec("DELETE FROM uploads WHERE id=? AND state='failed'", id); err != nil {
		return Item{}, err
	}
	return m.UploadID(ctx, id, name, size, src, check)
}

func (m *Manager) ObjectMetadata(key string) (Item, error) {
	var v Item
	err := m.db.DB.QueryRow("SELECT id,display_name,received_size,sha256,created_at FROM uploads WHERE object_key=? AND state='available'", key).Scan(&v.ID, &v.Name, &v.Size, &v.SHA256, &v.CreatedAt)
	return v, err
}

func (m *Manager) OpenObject(key, sha string) (Item, *ReadHandle, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	v, err := m.ObjectMetadata(key)
	if err != nil {
		return v, nil, err
	}
	if v.SHA256 != sha {
		return v, nil, errors.New("对象版本冲突")
	}
	info, err := os.Lstat(filepath.Join(m.objects, key))
	if err != nil {
		return v, nil, err
	}
	if !info.Mode().IsRegular() {
		return v, nil, errors.New("对象不是普通文件")
	}
	f, err := os.Open(filepath.Join(m.objects, key))
	if err != nil {
		return v, nil, err
	}
	m.readers[key]++
	return v, &ReadHandle{File: f, manager: m, id: key}, nil
}

func (m *Manager) VerifyObject(key, sha string, size int64) error {
	_, f, err := m.OpenObject(key, sha)
	if err != nil {
		return err
	}
	defer f.Close()
	h := sha256.New()
	n, err := io.Copy(h, f)
	if err != nil {
		return err
	}
	if n != size || hex.EncodeToString(h.Sum(nil)) != sha {
		return errors.New("目标大小或 SHA-256 校验失败")
	}
	return nil
}

// CleanupObject never waits for readers while holding a panel operation lock.
// The node may remove its active-looking record only after a live panel permit.
func (m *Manager) CleanupObject(key, sha string, node bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	var active int
	if !node {
		if err := m.db.DB.QueryRow("SELECT COUNT(*) FROM files WHERE object_key=? AND state='available'", key).Scan(&active); err != nil {
			return err
		}
		if active > 0 {
			return errors.New("拒绝删除活动主位置")
		}
	}
	if m.readers[key] > 0 {
		return errors.New("源读租约尚未结束")
	}
	v, err := m.ObjectMetadata(key)
	if err != nil {
		var retired int
		if e := m.db.DB.QueryRow("SELECT COUNT(*) FROM uploads WHERE object_key=? AND state='failed' AND error='object retired' AND sha256=?", key, sha).Scan(&retired); e != nil {
			return e
		}
		if retired > 0 {
			if e := os.Remove(filepath.Join(m.objects, key)); e != nil && !errors.Is(e, os.ErrNotExist) {
				return e
			}
			_, e := m.db.DB.Exec("DELETE FROM files WHERE id=? AND object_key=?", key, key)
			return e
		}
		// A never-started target is safe to acknowledge without scanning other files.
		if e := m.db.DB.QueryRow("SELECT COUNT(*) FROM uploads WHERE object_key=?", key).Scan(&active); e != nil {
			return e
		}
		if active == 0 {
			return nil
		}
		if e := m.db.DB.QueryRow("SELECT COUNT(*) FROM uploads WHERE object_key=? AND state='failed'", key).Scan(&active); e != nil {
			return e
		}
		if active > 0 {
			return nil
		}
		return err
	}
	if v.SHA256 != sha {
		return errors.New("清理对象版本冲突，保留文件")
	}
	// Retire before unlink: a crash cannot republish a deliberately retired object.
	if _, err = m.db.DB.Exec("UPDATE uploads SET state='failed',error='object retired' WHERE object_key=? AND sha256=?", key, sha); err != nil {
		return err
	}
	if err = os.Remove(filepath.Join(m.objects, key)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	_, err = m.db.DB.Exec("DELETE FROM files WHERE id=? AND object_key=?", v.ID, key)
	return err
}
