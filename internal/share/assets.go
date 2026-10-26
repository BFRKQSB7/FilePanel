package share

import (
	"encoding/json"
	"errors"
	"filepanel/internal/files"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

var imageTypes = map[string]bool{"image/png": true, "image/jpeg": true, "image/gif": true, "image/webp": true}

// AddAsset 保存一张本机图片素材。类型按内容嗅探（不信任文件名/声明类型），不接受 SVG。
func (m *Manager) AddAsset(fileID string, src io.Reader, names ...string) (Asset, error) {
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
	name := "图片"
	if len(names) > 0 {
		name = filepath.Base(strings.ReplaceAll(names[0], "\\", "/"))
		if len([]rune(name)) > 120 {
			name = string([]rune(name)[:120])
		}
	}
	// Enforce the limit at insert time too, including simultaneous uploads.
	result, err := m.db.DB.Exec("INSERT INTO assets(id,file_id,object_key,mime,size,created_at,name) SELECT ?,?,?,?,?,?,? WHERE (SELECT COUNT(*) FROM assets WHERE file_id=?) < ?", id, fileID, id, mime, len(data), now, name, fileID, MaxAssetsPerFile)
	if err != nil {
		_ = os.Remove(path)
		return Asset{}, err
	}
	if n, err := result.RowsAffected(); err != nil || n != 1 {
		_ = os.Remove(path)
		return Asset{}, errors.New("每个资源最多 8 张图片")
	}
	return Asset{ID: id, FileID: fileID, Mime: mime, Size: int64(len(data)), CreatedAt: now, Name: name}, nil
}

func (m *Manager) Assets(fileID string) ([]Asset, error) {
	rows, err := m.db.DB.Query("SELECT id,file_id,mime,size,created_at,name FROM assets WHERE file_id=? ORDER BY created_at,id", fileID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Asset{}
	for rows.Next() {
		var a Asset
		if err := rows.Scan(&a.ID, &a.FileID, &a.Mime, &a.Size, &a.CreatedAt, &a.Name); err != nil {
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
	if publicOnly {
		sh, err := m.Get(a.FileID)
		if err != nil || len(assetUses(a.ID, sh.CoverAsset, sh.GalleryAssets, MarkdownAssets(sh.Markdown))) == 0 {
			return a, nil, ErrNotFound
		}
	}
	data, err := os.ReadFile(filepath.Join(m.dir, key))
	if err != nil {
		return a, nil, ErrNotFound
	}
	return a, data, nil
}

func (m *Manager) RemoveAsset(fileID, id string) error {
	return m.removeAsset(fileID, id, true)
}

var ErrAssetInUse = errors.New("图片仍被分享设置引用，请先在分享编辑中移除封面、正文和截图引用并保存，再删除")

func (m *Manager) removeAsset(fileID, id string, protect bool) error {
	tx, err := m.db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var key, cover, markdown, gallery string
	if err := tx.QueryRow(`SELECT a.object_key,s.cover_asset_id,s.markdown,s.gallery_assets FROM assets a JOIN shares s ON s.file_id=a.file_id WHERE a.id=? AND a.file_id=?`, id, fileID).Scan(&key, &cover, &markdown, &gallery); err != nil {
		return ErrNotFound
	}
	var shots []string
	if err := json.Unmarshal([]byte(gallery), &shots); err != nil {
		return err
	}
	if protect && len(assetUses(id, cover, shots, MarkdownAssets(markdown))) > 0 {
		return ErrAssetInUse
	}
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
		_ = m.removeAsset(fileID, a.ID, false)
	}
}

func assetUses(id, cover string, gallery []string, body map[string]bool) []string {
	uses := []string{}
	if cover == id {
		uses = append(uses, "封面")
	}
	if body[id] {
		uses = append(uses, "正文")
	}
	for _, shot := range gallery {
		if shot == id {
			uses = append(uses, "截图")
			break
		}
	}
	return uses
}

func AssetUsage(sh Share) map[string][]string {
	refs := MarkdownAssets(sh.Markdown)
	ids := map[string]bool{sh.CoverAsset: true}
	for id := range refs {
		ids[id] = true
	}
	for _, id := range sh.GalleryAssets {
		ids[id] = true
	}
	delete(ids, "")
	uses := map[string][]string{}
	for id := range ids {
		uses[id] = assetUses(id, sh.CoverAsset, sh.GalleryAssets, refs)
	}
	return uses
}

type LibraryAsset struct {
	Asset
	FileName string   `json:"file_name"`
	Enabled  bool     `json:"enabled"`
	Uses     []string `json:"uses"`
}

type LibraryPage struct {
	Items   []LibraryAsset `json:"items"`
	Total   int            `json:"total"`
	Bytes   int64          `json:"bytes"`
	Used    int            `json:"used"`
	Matched int            `json:"matched"`
	Page    int            `json:"page"`
}

// Library streams metadata and returns at most 24 cards; images are lazy loaded.
// ponytail: O(n) reference scan; index references if large libraries make this costly.
func (m *Manager) Library(query, status, fileID string, page int) (LibraryPage, error) {
	out := LibraryPage{Items: []LibraryAsset{}, Page: page}
	rows, err := m.db.DB.Query(`SELECT a.id,a.file_id,a.mime,a.size,a.created_at,a.name,f.display_name,s.enabled,s.cover_asset_id,s.markdown,s.gallery_assets FROM assets a JOIN files f ON f.id=a.file_id JOIN shares s ON s.file_id=a.file_id ORDER BY a.file_id,a.created_at DESC,a.id`)
	if err != nil {
		return out, err
	}
	defer rows.Close()
	query = strings.ToLower(strings.TrimSpace(query))
	var previous string
	var body map[string]bool
	for rows.Next() {
		var a LibraryAsset
		var enabled int
		var cover, markdown, gallery string
		if err := rows.Scan(&a.ID, &a.FileID, &a.Mime, &a.Size, &a.CreatedAt, &a.Name, &a.FileName, &enabled, &cover, &markdown, &gallery); err != nil {
			return out, err
		}
		a.Enabled = enabled == 1
		var shots []string
		if err := json.Unmarshal([]byte(gallery), &shots); err != nil {
			return out, err
		}
		if previous != a.FileID {
			body = MarkdownAssets(markdown)
			previous = a.FileID
		}
		a.Uses = assetUses(a.ID, cover, shots, body)
		out.Total++
		out.Bytes += a.Size
		used := len(a.Uses) > 0
		if used {
			out.Used++
		}
		if (fileID != "" && fileID != a.FileID) || (status == "used" && !used) || (status == "unused" && used) || !strings.Contains(strings.ToLower(a.Name+" "+a.FileName), query) {
			continue
		}
		out.Matched++
		if out.Matched > (page-1)*24 && len(out.Items) < 24 {
			out.Items = append(out.Items, a)
		}
	}
	return out, rows.Err()
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
