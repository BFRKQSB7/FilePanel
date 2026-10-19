package share

import (
	"errors"
	"filepanel/internal/files"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"
)

var imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// AddAsset 保存一张本机图片素材。类型按内容嗅探（不信任文件名/声明类型），不接受 SVG。
func (m *Manager) AddAsset(fileID string, src io.Reader) (Asset, error) {
	if _, err := m.Ensure(fileID); err != nil {
		return Asset{}, err
	}
	var count int
	if err := m.db.DB.QueryRow("SELECT COUNT(*) FROM assets WHERE file_id=?", fileID).Scan(&count); err != nil {
		return Asset{}, err
	}
	if count >= MaxAssetsPerFile {
		return Asset{}, errors.New("每个资源最多 8 张图片")
	}
	data, err := io.ReadAll(io.LimitReader(src, MaxAssetBytes+1))
	if err != nil {
		return Asset{}, errors.New("读取图片失败")
	}
	if len(data) > MaxAssetBytes {
		return Asset{}, errors.New("图片不能超过 2 MiB")
	}
	if len(data) == 0 {
		return Asset{}, errors.New("图片为空")
	}
	mime := http.DetectContentType(data)
	if !imageTypes[mime] {
		return Asset{}, errors.New("仅支持 PNG、JPEG、GIF、WebP 图片")
	}
	release, err := files.HoldSpace(m.files, uint64(len(data)))
	if err != nil {
		return Asset{}, err
	}
	defer release()
	id := randomString(18)
	path := filepath.Join(m.dir, id)
	if err := os.WriteFile(path, data, 0600); err != nil {
		return Asset{}, err
	}
	now := time.Now().Unix()
	if _, err := m.db.DB.Exec("INSERT INTO assets(id,file_id,object_key,mime,size,created_at) VALUES(?,?,?,?,?,?)", id, fileID, id, mime, len(data), now); err != nil {
		_ = os.Remove(path)
		return Asset{}, err
	}
	return Asset{ID: id, FileID: fileID, Mime: mime, Size: int64(len(data)), CreatedAt: now}, nil
}

func (m *Manager) Assets(fileID string) ([]Asset, error) {
	rows, err := m.db.DB.Query("SELECT id,file_id,mime,size,created_at FROM assets WHERE file_id=? ORDER BY created_at,id", fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Asset{}
	for rows.Next() {
		var a Asset
		if err := rows.Scan(&a.ID, &a.FileID, &a.Mime, &a.Size, &a.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

// OpenAsset 返回素材内容。publicOnly 为 true 时，素材所属分享必须处于开启状态。
func (m *Manager) OpenAsset(id string, publicOnly bool) (Asset, []byte, error) {
	var a Asset
	var key string
	query := "SELECT a.id,a.file_id,a.object_key,a.mime,a.size,a.created_at FROM assets a WHERE a.id=?"
	if publicOnly {
		query = "SELECT a.id,a.file_id,a.object_key,a.mime,a.size,a.created_at FROM assets a JOIN shares s ON s.file_id=a.file_id AND s.enabled=1 JOIN files f ON f.id=a.file_id AND f.state='available' WHERE a.id=?"
	}
	if err := m.db.DB.QueryRow(query, id).Scan(&a.ID, &a.FileID, &key, &a.Mime, &a.Size, &a.CreatedAt); err != nil {
		return a, nil, ErrNotFound
	}
	data, err := os.ReadFile(filepath.Join(m.dir, key))
	if err != nil {
		return a, nil, ErrNotFound
	}
	return a, data, nil
}

func (m *Manager) RemoveAsset(fileID, id string) error {
	var key string
	if err := m.db.DB.QueryRow("SELECT object_key FROM assets WHERE id=? AND file_id=?", id, fileID).Scan(&key); err != nil {
		return ErrNotFound
	}
	tx, err := m.db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("DELETE FROM assets WHERE id=?", id); err != nil {
		return err
	}
	if _, err := tx.Exec("UPDATE shares SET cover_asset_id='',updated_at=? WHERE file_id=? AND cover_asset_id=?", time.Now().Unix(), fileID, id); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	_ = os.Remove(filepath.Join(m.dir, key))
	return nil
}

// RemoveFileAssets 删除某文件全部素材对象；数据库行随文件级联删除，磁盘残留由启动恢复兜底。
func (m *Manager) RemoveFileAssets(fileID string) {
	assets, _ := m.Assets(fileID)
	for _, a := range assets {
		_ = m.RemoveAsset(fileID, a.ID)
	}
}

// AssetOwned 返回 Markdown 渲染用的判断函数：只允许引用本分享自己的素材。
func (m *Manager) AssetOwned(fileID string) func(id string) bool {
	assets, _ := m.Assets(fileID)
	set := map[string]bool{}
	for _, a := range assets {
		set[a.ID] = true
	}
	return func(id string) bool { return set[id] }
}
