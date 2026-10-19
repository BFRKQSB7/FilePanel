package profile

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"sync"
	"time"

	"filepanel/internal/files"
	"filepanel/internal/store"
)

// Space 是素材落盘共用的空间预算检查（由 files.Manager 实现）。
type Space interface{ CheckSpace(n uint64) error }

const stagedTTL = 30 * time.Minute

var (
	ErrStagedGone = &UserError{Msg: "待确认的导入已过期或不存在，请重新上传配置包"}
	tokenRE       = regexp.MustCompile(`^[A-Za-z0-9_-]{24}$`)
	setRE         = regexp.MustCompile(`^s[0-9a-f]{16}$`)
)

type assetMeta struct {
	File   string `json:"file"`
	Mime   string `json:"mime"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// stored 是 site_profile.config 中的 JSON。
type stored struct {
	Fields Fields             `json:"fields"`
	Assets map[Role]assetMeta `json:"assets"`
}

type state struct {
	rev    int64
	set    string
	fields Fields
	assets map[Role]assetMeta
	view   View
}

type stagedSet struct {
	dir     string
	fields  Fields
	assets  map[Role]assetMeta
	created time.Time
}

// hooks 仅供测试注入故障；生产环境全为 nil。panic 用来模拟进程在该点崩溃。
type hooks struct {
	afterRename  func() error
	beforeCommit func() error
	afterCommit  func()
}

type Manager struct {
	db      *store.Store
	sets    string
	staging string
	space   Space
	now     func() time.Time
	hooks   hooks

	mu     sync.RWMutex
	cur    state
	staged map[string]*stagedSet
}

// New 打开站点配置，清理崩溃遗留的暂存与未被引用的素材目录。
func New(db *store.Store, dataDir string, space Space) (*Manager, error) {
	root := filepath.Join(dataDir, "profile")
	m := &Manager{db: db, sets: filepath.Join(root, "sets"), staging: filepath.Join(root, "staging"), space: space,
		now: time.Now, staged: map[string]*stagedSet{}}
	for _, dir := range []string{m.sets, m.staging} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建个性化目录失败: %w", err)
		}
	}
	if err := m.load(); err != nil {
		return nil, err
	}
	m.recover()
	return m, nil
}

func (m *Manager) urls(rev int64, assets map[Role]assetMeta) map[Role]string {
	out := map[Role]string{}
	for role := range assets {
		out[role] = "/site/" + string(role) + "?v=" + strconv.FormatInt(rev, 10)
	}
	return out
}

func (m *Manager) load() error {
	var rev, updated int64
	var set, cfg string
	err := m.db.DB.QueryRow("SELECT revision,asset_set,config,updated_at FROM site_profile WHERE id=1").Scan(&rev, &set, &cfg, &updated)
	if errors.Is(err, sql.ErrNoRows) {
		_, err = m.db.DB.Exec("INSERT OR IGNORE INTO site_profile(id,revision,asset_set,config,updated_at) VALUES(1,0,'','{}',0)")
		rev, set, cfg = 0, "", "{}"
	}
	if err != nil {
		return fmt.Errorf("读取站点个性化配置失败: %w", err)
	}
	var s stored
	if err := json.Unmarshal([]byte(cfg), &s); err != nil {
		return fmt.Errorf("站点个性化配置已损坏: %w", err)
	}
	if set != "" && !setRE.MatchString(set) {
		return fmt.Errorf("站点个性化素材目录名无效")
	}
	next := state{rev: rev, set: set, fields: s.Fields, assets: map[Role]assetMeta{}}
	// 数据库登记的素材若缺失或大小不符，只在内存中降级为“未设置”，不让装饰性数据阻止服务启动。
	for role, meta := range s.Assets {
		if _, ok := ParseRole(string(role)); !ok || set == "" || meta.File != fileName(role, meta.Mime) {
			continue
		}
		if info, err := os.Stat(filepath.Join(m.sets, set, meta.File)); err == nil && info.Mode().IsRegular() && info.Size() == meta.Size {
			next.assets[role] = meta
		}
	}
	next.view = NewView(next.fields, m.urls(rev, next.assets))
	next.view.Revision = rev
	m.cur = next
	return nil
}

// recover 删除暂存区的全部内容，以及除当前版本外的素材目录。
func (m *Manager) recover() {
	if entries, err := os.ReadDir(m.staging); err == nil {
		for _, e := range entries {
			_ = os.RemoveAll(filepath.Join(m.staging, e.Name()))
		}
	}
	if entries, err := os.ReadDir(m.sets); err == nil {
		for _, e := range entries {
			if e.Name() != m.cur.set {
				_ = os.RemoveAll(filepath.Join(m.sets, e.Name()))
			}
		}
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

// View 返回公开页面使用的品牌信息。
func (m *Manager) View() View {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.cur.view
}

// Snapshot 是管理端展示用的当前配置。
type Snapshot struct {
	Revision int64              `json:"revision"`
	Fields   Fields             `json:"fields"`
	Assets   map[Role]AssetInfo `json:"assets"`
}

type AssetInfo struct {
	Mime string `json:"mime"`
	Size int64  `json:"size"`
	URL  string `json:"url"`
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s := Snapshot{Revision: m.cur.rev, Fields: m.cur.fields, Assets: map[Role]AssetInfo{}}
	for role, meta := range m.cur.assets {
		s.Assets[role] = AssetInfo{Mime: meta.Mime, Size: meta.Size, URL: m.urls(m.cur.rev, m.cur.assets)[role]}
	}
	return s
}

// Asset 读取当前版本的一张素材。读取与切换互斥，因此不会读到正在被删除的旧目录。
func (m *Manager) Asset(role Role) (string, []byte, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.assetLocked(role)
}

func (m *Manager) assetLocked(role Role) (string, []byte, error) {
	meta, ok := m.cur.assets[role]
	if !ok {
		return "", nil, os.ErrNotExist
	}
	data, err := os.ReadFile(filepath.Join(m.sets, m.cur.set, meta.File))
	if err != nil || int64(len(data)) != meta.Size {
		return "", nil, os.ErrNotExist
	}
	return meta.Mime, data, nil
}

// Change 描述表单对一个素材角色的修改；未出现的角色保持不变。
type Change struct {
	Remove bool
	Data   []byte // 非空表示替换
}

type Edit struct {
	Fields  Fields
	Changes map[Role]Change
}

// Compose 把表单修改合成为完整候选配置：校验文字与新图片，沿用未修改的素材。
func (m *Manager) Compose(e Edit) (Candidate, error) {
	fields, err := e.Fields.Normalize()
	if err != nil {
		return Candidate{}, err
	}
	cand := Candidate{Fields: fields, Blobs: map[Role]Blob{}}
	m.mu.RLock() // 沿用的素材必须来自同一个版本，读取期间不允许切换
	defer m.mu.RUnlock()
	for _, role := range Roles {
		ch, changed := e.Changes[role]
		switch {
		case changed && len(ch.Data) > 0:
			mime, err := ValidateImage(role, ch.Data)
			if err != nil {
				return Candidate{}, err
			}
			cand.Blobs[role] = Blob{Mime: mime, Data: ch.Data}
		case changed && ch.Remove:
		default:
			if mime, data, err := m.assetLocked(role); err == nil {
				cand.Blobs[role] = Blob{Mime: mime, Data: data}
			}
		}
	}
	return cand, nil
}

// Export 生成当前配置的 ZIP 包。
func (m *Manager) Export() ([]byte, int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	blobs := map[Role]Blob{}
	for _, role := range Roles {
		if mime, data, err := m.assetLocked(role); err == nil {
			blobs[role] = Blob{Mime: mime, Data: data}
		}
	}
	pkg, err := BuildPackage(m.cur.fields, blobs)
	return pkg, m.cur.rev, err
}

// Stage 把导入的候选配置写入隔离暂存区并返回确认令牌。同一时间只保留一份待确认导入。
// 写入前先过空间预算；任何失败都会清理暂存目录，不触碰当前素材。
func (m *Manager) Stage(c Candidate) (string, error) {
	m.discardAll()
	return m.stage(c)
}

func (m *Manager) stage(c Candidate) (string, error) {
	release, err := files.HoldSpace(m.space, c.TotalBytes())
	if err != nil {
		return "", fmt.Errorf("%w（%v）", ErrSpace, err)
	}
	defer release()
	var raw [18]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	token := base64.RawURLEncoding.EncodeToString(raw[:])
	dir := filepath.Join(m.staging, token)
	if err := os.Mkdir(dir, 0o700); err != nil {
		return "", err
	}
	fail := func(err error) (string, error) { _ = os.RemoveAll(dir); return "", err }
	assets := map[Role]assetMeta{}
	for role, b := range c.Blobs {
		sum := sha256.Sum256(b.Data)
		meta := assetMeta{File: fileName(role, b.Mime), Mime: b.Mime, Size: int64(len(b.Data)), SHA256: hex.EncodeToString(sum[:])}
		if err := writeSynced(filepath.Join(dir, meta.File), b.Data); err != nil {
			return fail(err)
		}
		assets[role] = meta
	}
	m.mu.Lock()
	m.sweepLocked()
	m.staged[token] = &stagedSet{dir: dir, fields: c.Fields, assets: assets, created: m.now()}
	m.mu.Unlock()
	return token, nil
}

func writeSynced(path string, data []byte) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	return err
}

func (m *Manager) sweepLocked() {
	for token, s := range m.staged {
		if m.now().Sub(s.created) > stagedTTL {
			_ = os.RemoveAll(s.dir)
			delete(m.staged, token)
		}
	}
}

func (m *Manager) discardAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for token, s := range m.staged {
		_ = os.RemoveAll(s.dir)
		delete(m.staged, token)
	}
}

// Discard 放弃一份待确认的导入。
func (m *Manager) Discard(token string) {
	if !tokenRE.MatchString(token) {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if s, ok := m.staged[token]; ok {
		_ = os.RemoveAll(s.dir)
		delete(m.staged, token)
	}
}

// Commit 直接应用一份候选配置（表单保存）：暂存后走与导入相同的原子切换。
func (m *Manager) Commit(c Candidate) (View, error) {
	token, err := m.stage(c)
	if err != nil {
		return View{}, err
	}
	return m.Apply(token)
}

// Apply 原子地把暂存配置切换为当前配置：
//  1. 复核暂存文件的大小与哈希；
//  2. 整个暂存目录一次 rename 成新的版本化素材目录；
//  3. 一条 UPDATE 同时切换 revision、素材目录与配置；
//  4. 提交后才删除旧目录。
//
// 第 3 步之前的任何失败或崩溃，旧配置完整；之后，新配置完整。
func (m *Manager) Apply(token string) (View, error) {
	if !tokenRE.MatchString(token) {
		return View{}, ErrStagedGone
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sweepLocked()
	st, ok := m.staged[token]
	if !ok {
		return View{}, ErrStagedGone
	}
	for _, meta := range st.assets {
		data, err := os.ReadFile(filepath.Join(st.dir, meta.File))
		sum := sha256.Sum256(data)
		if err != nil || int64(len(data)) != meta.Size || hex.EncodeToString(sum[:]) != meta.SHA256 {
			_ = os.RemoveAll(st.dir)
			delete(m.staged, token)
			return View{}, fmt.Errorf("暂存素材校验失败，当前配置未改变，请重新操作")
		}
	}
	setID := "s" + randomHex(8)
	final := filepath.Join(m.sets, setID)
	if err := os.Rename(st.dir, final); err != nil {
		return View{}, fmt.Errorf("切换素材目录失败，当前配置未改变: %w", err)
	}
	rollback := func(err error) (View, error) {
		_ = os.RemoveAll(final)
		delete(m.staged, token)
		return View{}, fmt.Errorf("应用失败，当前配置未改变: %w", err)
	}
	if h := m.hooks.afterRename; h != nil {
		if err := h(); err != nil {
			return rollback(err)
		}
	}
	syncDir(m.sets)
	cfg, err := json.Marshal(stored{Fields: st.fields, Assets: st.assets})
	if err != nil {
		return rollback(err)
	}
	next := state{rev: m.cur.rev + 1, set: setID, fields: st.fields, assets: st.assets}
	if h := m.hooks.beforeCommit; h != nil {
		if err := h(); err != nil {
			return rollback(err)
		}
	}
	res, err := m.db.DB.Exec("UPDATE site_profile SET revision=?,asset_set=?,config=?,updated_at=? WHERE id=1 AND revision=?",
		next.rev, setID, string(cfg), m.now().Unix(), m.cur.rev)
	if err == nil {
		var n int64
		if n, err = res.RowsAffected(); err == nil && n != 1 {
			err = errors.New("配置已被其他操作修改")
		}
	}
	if err != nil {
		return rollback(err)
	}
	if h := m.hooks.afterCommit; h != nil {
		h()
	}
	old := m.cur.set
	next.view = NewView(next.fields, m.urls(next.rev, next.assets))
	next.view.Revision = next.rev
	m.cur = next
	delete(m.staged, token)
	// 旧版本已无任何引用（数据库与内存都指向新目录，且持有写锁时没有读者），可以安全删除。
	if old != "" && old != setID && setRE.MatchString(old) {
		_ = os.RemoveAll(filepath.Join(m.sets, old))
	}
	return next.view, nil
}

// syncDir 尽力把目录项落盘；Windows 不支持对目录 Sync，忽略其错误。
func syncDir(path string) {
	if f, err := os.Open(path); err == nil {
		_ = f.Sync()
		_ = f.Close()
	}
}
