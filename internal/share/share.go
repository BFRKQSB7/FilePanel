// Package share 管理公开发布：稳定 slug、授权版本、展示内容与本机图片素材。
//
// 状态契约：
//   - 开启主页展示（Featured）必然开启分享（Enabled）；
//   - 关闭分享同时取消主页展示并把 AuthVersion 加 1，旧凭证与活动租约随之失效；
//   - slug 创建后不再改变，改名/换存储不影响稳定链接。
package share

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"filepanel/internal/files"
	"filepanel/internal/store"
)

const (
	MaxAssetBytes    = 2 << 20
	MaxAssetsPerFile = 8
	DefaultButton    = "立即下载"
)

var (
	ErrNotFound = errors.New("分享不存在")
	ErrRevoked  = errors.New("分享已关闭或授权已变化")
)

type Share struct {
	FileID       string   `json:"file_id"`
	Slug         string   `json:"slug"`
	Title        string   `json:"title"`
	Version      string   `json:"version"`
	Summary      string   `json:"summary"`
	Markdown     string   `json:"markdown"`
	ButtonText   string   `json:"button_text"`
	CoverAsset   string   `json:"cover_asset_id"`
	Enabled      bool     `json:"enabled"`
	Featured     bool     `json:"featured"`
	AuthVersion  int64    `json:"auth_version"`
	UpdatedAt    int64    `json:"updated_at"`
	PageTemplate string   `json:"page_template"`
	RelayMode    string   `json:"relay_mode"`
	Relays       []string `json:"relays"`
	// 来自文件（只读）
	FileName string `json:"file_name"`
	Size     int64  `json:"size"`
	SHA256   string `json:"sha256"`
}

type Input struct {
	Title        string   `json:"title"`
	Version      string   `json:"version"`
	Summary      string   `json:"summary"`
	Markdown     string   `json:"markdown"`
	ButtonText   string   `json:"button_text"`
	CoverAsset   string   `json:"cover_asset_id"`
	Enabled      bool     `json:"enabled"`
	Featured     bool     `json:"featured"`
	PageTemplate string   `json:"page_template"`
	RelayMode    string   `json:"relay_mode"`
	Relays       []string `json:"relays"`
}

type Asset struct {
	ID        string `json:"id"`
	FileID    string `json:"file_id"`
	Mime      string `json:"mime"`
	Size      int64  `json:"size"`
	CreatedAt int64  `json:"created_at"`
}

type Manager struct {
	db    *store.Store
	files *files.Manager
	dir   string
}

func New(s *store.Store, f *files.Manager) (*Manager, error) {
	dir := filepath.Join(f.DataDir(), "assets")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	m := &Manager{db: s, files: f, dir: dir}
	return m, m.recoverAssets()
}

// recoverAssets 清理没有数据库记录的素材残留（崩溃或文件级联删除后）。
func (m *Manager) recoverAssets() error {
	rows, err := m.db.DB.Query("SELECT object_key FROM assets")
	if err != nil {
		return err
	}
	known := map[string]bool{}
	for rows.Next() {
		var key string
		if err := rows.Scan(&key); err != nil {
			rows.Close()
			return err
		}
		known[key] = true
	}
	rows.Close()
	entries, err := os.ReadDir(m.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !known[e.Name()] {
			_ = os.Remove(filepath.Join(m.dir, e.Name()))
		}
	}
	return nil
}

func randomString(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return base64.RawURLEncoding.EncodeToString(b)
}

const selectShare = `SELECT s.file_id,s.slug,s.title,s.version,s.summary,s.markdown,s.button_text,s.cover_asset_id,s.enabled,s.featured,s.auth_version,s.updated_at,f.display_name,f.size,f.sha256,s.page_template,s.relay_mode,s.relays
FROM shares s JOIN files f ON f.id=s.file_id AND f.state='available'`

func scanShare(row interface{ Scan(...any) error }) (Share, error) {
	var s Share
	var enabled, featured int
	var relays string
	err := row.Scan(&s.FileID, &s.Slug, &s.Title, &s.Version, &s.Summary, &s.Markdown, &s.ButtonText, &s.CoverAsset, &enabled, &featured, &s.AuthVersion, &s.UpdatedAt, &s.FileName, &s.Size, &s.SHA256, &s.PageTemplate, &s.RelayMode, &relays)
	if err == nil {
		err = json.Unmarshal([]byte(relays), &s.Relays)
	}
	s.Enabled, s.Featured = enabled == 1, featured == 1
	if errors.Is(err, sql.ErrNoRows) {
		err = ErrNotFound
	}
	return s, err
}

// Ensure 为一个可用文件创建默认私有的分享记录（已存在则原样返回）。
func (m *Manager) Ensure(fileID string) (Share, error) {
	var exists int
	if err := m.db.DB.QueryRow("SELECT 1 FROM files WHERE id=? AND state='available'", fileID).Scan(&exists); err != nil {
		return Share{}, ErrNotFound
	}
	now := time.Now().Unix()
	if _, err := m.db.DB.Exec("INSERT OR IGNORE INTO shares(file_id,slug,created_at,updated_at) VALUES(?,?,?,?)", fileID, randomString(9), now, now); err != nil {
		return Share{}, err
	}
	return m.Get(fileID)
}

func (m *Manager) Get(fileID string) (Share, error) {
	return scanShare(m.db.DB.QueryRow(selectShare+" WHERE s.file_id=?", fileID))
}

// BySlug 只返回已开启分享的记录；私有记录对匿名访问等同不存在。
func (m *Manager) BySlug(slug string) (Share, error) {
	return scanShare(m.db.DB.QueryRow(selectShare+" WHERE s.slug=? AND s.enabled=1", slug))
}

// Authorized 校验开启状态与授权版本；用于下载入口和租约续租。
func (m *Manager) Authorized(fileID string, authVersion int64) error {
	var enabled int
	var current int64
	err := m.db.DB.QueryRow("SELECT s.enabled,s.auth_version FROM shares s JOIN files f ON f.id=s.file_id AND f.state='available' WHERE s.file_id=?", fileID).Scan(&enabled, &current)
	if err != nil || enabled != 1 || current != authVersion {
		return ErrRevoked
	}
	return nil
}

func clip(s string, max int, name string) (string, error) {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\x00", ""))
	if utf8.RuneCountInString(s) > max {
		return "", errors.New(name + "过长")
	}
	return s, nil
}

// Update 保存内容与展示状态，返回更新后的记录以及是否撤销了授权（调用者据此终止活动租约）。
func (m *Manager) Update(fileID string, in Input) (Share, bool, error) {
	cur, err := m.Ensure(fileID)
	if err != nil {
		return Share{}, false, err
	}
	var fields [5]string
	for i, spec := range []struct {
		value string
		max   int
		name  string
	}{{in.Title, 80, "标题"}, {in.Version, 40, "版本"}, {in.Summary, 300, "简介"}, {in.Markdown, 20000, "正文"}, {in.ButtonText, 20, "按钮文字"}} {
		v := spec.value
		if spec.name != "正文" {
			v = strings.ReplaceAll(strings.ReplaceAll(v, "\n", " "), "\r", " ")
		}
		if fields[i], err = clip(v, spec.max, spec.name); err != nil {
			return cur, false, err
		}
	}
	if in.Featured {
		in.Enabled = true
	}
	if in.PageTemplate == "" {
		in.PageTemplate = cur.PageTemplate
	}
	switch in.PageTemplate {
	case "standard", "compact", "showcase":
	default:
		return cur, false, errors.New("未知下载页面模板")
	}
	if in.RelayMode == "" {
		in.RelayMode, in.Relays = cur.RelayMode, cur.Relays
	}
	if in.RelayMode != "all" && in.RelayMode != "selected" {
		return cur, false, errors.New("未知下载线路模式")
	}
	if in.RelayMode == "selected" && (len(in.Relays) == 0 || len(in.Relays) > 64) {
		return cur, false, errors.New("请选择 1 至 64 条下载线路")
	}
	if in.Relays == nil {
		in.Relays = []string{}
	}
	if in.RelayMode == "all" {
		in.Relays = []string{}
	}
	relays, _ := json.Marshal(in.Relays)
	oldRelays, _ := json.Marshal(cur.Relays)
	if in.CoverAsset != "" {
		var n int
		if err := m.db.DB.QueryRow("SELECT 1 FROM assets WHERE id=? AND file_id=?", in.CoverAsset, fileID).Scan(&n); err != nil {
			return cur, false, errors.New("封面素材不属于该资源")
		}
	}
	revoked := cur.Enabled && (!in.Enabled || in.RelayMode != cur.RelayMode || string(relays) != string(oldRelays))
	version := cur.AuthVersion
	if revoked {
		version++
	}
	b := func(v bool) int {
		if v {
			return 1
		}
		return 0
	}
	if _, err := m.db.DB.Exec(`UPDATE shares SET title=?,version=?,summary=?,markdown=?,button_text=?,cover_asset_id=?,enabled=?,featured=?,auth_version=?,updated_at=?,page_template=?,relay_mode=?,relays=? WHERE file_id=?`,
		fields[0], fields[1], fields[2], fields[3], fields[4], in.CoverAsset, b(in.Enabled), b(in.Featured && in.Enabled), version, time.Now().Unix(), in.PageTemplate, in.RelayMode, string(relays), fileID); err != nil {
		return cur, false, err
	}
	updated, err := m.Get(fileID)
	return updated, revoked, err
}

func (m *Manager) AllowsRelay(file, relay string) bool {
	s, err := m.Get(file)
	if err != nil || !s.Enabled {
		return false
	}
	if s.RelayMode == "all" {
		return true
	}
	if relay == "" {
		relay = "local"
	}
	for _, id := range s.Relays {
		if id == relay {
			return true
		}
	}
	return false
}

// BatchMode changes authorization only. A stale or invalid selection rolls back
// the entire operation; saved content, slugs and page templates stay untouched.
func (m *Manager) BatchMode(ids []string, mode string) error {
	if len(ids) == 0 || len(ids) > 500 {
		return errors.New("每次请选择 1 至 500 个已分享文件")
	}
	if mode != "private" && mode != "link" && mode != "public" {
		return errors.New("未知分享方式")
	}
	tx, err := m.db.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	seen := map[string]bool{}
	for _, id := range ids {
		if id == "" || seen[id] {
			return errors.New("文件选择重复或无效")
		}
		seen[id] = true
		res, err := tx.Exec(`UPDATE shares SET enabled=?,featured=?,auth_version=auth_version+CASE WHEN ?='private' THEN 1 ELSE 0 END,updated_at=? WHERE file_id=? AND enabled=1 AND EXISTS(SELECT 1 FROM files WHERE id=? AND state='available')`, mode != "private", mode == "public", mode, time.Now().Unix(), id, id)
		if err != nil {
			return err
		}
		count, err := res.RowsAffected()
		if err != nil {
			return err
		}
		if count != 1 {
			return errors.New("所选文件分享状态已变化，请刷新后重新选择；本次未修改任何文件")
		}
	}
	return tx.Commit()
}

// Revoke 关闭分享并提升授权版本（用于删除文件前）。
func (m *Manager) Revoke(fileID string) error {
	_, err := m.db.DB.Exec("UPDATE shares SET enabled=0,featured=0,auth_version=auth_version+1,updated_at=? WHERE file_id=?", time.Now().Unix(), fileID)
	return err
}

// Summary 用于管理列表。
type Summary struct {
	Enabled  bool   `json:"shared"`
	Featured bool   `json:"featured"`
	Slug     string `json:"slug"`
}

func (m *Manager) Summaries() (map[string]Summary, error) {
	rows, err := m.db.DB.Query("SELECT file_id,slug,enabled,featured FROM shares")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[string]Summary{}
	for rows.Next() {
		var id string
		var s Summary
		var e, f int
		if err := rows.Scan(&id, &s.Slug, &e, &f); err != nil {
			return nil, err
		}
		s.Enabled, s.Featured = e == 1, f == 1
		out[id] = s
	}
	return out, rows.Err()
}

const PageSize = 20

func likePattern(q string) string {
	return "%" + strings.NewReplacer(`\`, `\`, `%`, `\%`, `_`, `\_`).Replace(q) + "%"
}

// Public 返回主页展示的资源：仅 Featured（必然 Enabled），更新时间倒序，默认每页 20 条。
func (m *Manager) Public(query string, page int) ([]Share, int, error) {
	if page < 1 {
		page = 1
	}
	where, args := " WHERE s.enabled=1 AND s.featured=1", []any{}
	if q := strings.TrimSpace(query); q != "" {
		like := likePattern(q)
		where += ` AND (s.title LIKE ? ESCAPE '\' OR s.summary LIKE ? ESCAPE '\' OR s.version LIKE ? ESCAPE '\' OR f.display_name LIKE ? ESCAPE '\')`
		args = append(args, like, like, like, like)
	}
	var total int
	if err := m.db.DB.QueryRow("SELECT COUNT(*) FROM shares s JOIN files f ON f.id=s.file_id AND f.state='available'"+where, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	rows, err := m.db.DB.Query(selectShare+where+" ORDER BY s.updated_at DESC,s.file_id LIMIT ? OFFSET ?", append(args, PageSize, (page-1)*PageSize)...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	items := []Share{}
	for rows.Next() {
		s, err := scanShare(rows)
		if err != nil {
			return nil, 0, err
		}
		items = append(items, s)
	}
	return items, total, rows.Err()
}
