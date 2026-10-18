package nodes

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"filepanel/internal/files"
	"filepanel/internal/share"
	"golang.org/x/crypto/scrypt"
)

const MaxMigration = 4 << 20

type migrationNode struct {
	Node       Node   `json:"node"`
	Credential string `json:"credential"`
}
type migrationFile struct {
	File     files.Item   `json:"file"`
	Share    *share.Share `json:"share,omitempty"`
	ObjectID string       `json:"object_id,omitempty"`
}
type migrationBundle struct {
	Version  int             `json:"version"`
	Key      string          `json:"key"`
	Nodes    []migrationNode `json:"nodes"`
	Files    []migrationFile `json:"files"`
	Settings Settings        `json:"settings"`
}
type migrationCipher struct {
	Version int    `json:"version"`
	Salt    string `json:"salt"`
	Nonce   string `json:"nonce"`
	Data    string `json:"data"`
}
type MigrationSummary struct {
	Nodes   int    `json:"nodes"`
	Files   int    `json:"files"`
	PanelID string `json:"panel_id"`
	Warning string `json:"warning"`
}

func migrationAEAD(password string, salt []byte) (cipher.AEAD, error) {
	if len(password) < 12 || len(password) > 1024 || len(salt) != 16 {
		return nil, errors.New("迁移密码至少 12 字节")
	}
	key, e := scrypt.Key([]byte(password), salt, 16384, 8, 1, 32)
	if e != nil {
		return nil, e
	}
	block, e := aes.NewCipher(key)
	if e != nil {
		return nil, e
	}
	return cipher.NewGCM(block)
}
func (p *Panel) ExportMigration(password string) ([]byte, error) {
	var moving int
	if e := p.DB.DB.QueryRow("SELECT COUNT(*) FROM file_moves WHERE state NOT IN ('done','cancelled')").Scan(&moving); e != nil {
		return nil, e
	}
	if moving > 0 {
		return nil, errors.New("仍有文件移动或源清理未完成")
	}
	var active int
	if e := p.DB.DB.QueryRow("SELECT COUNT(*) FROM node_transfers WHERE (action='upload' AND state IN ('pending','validated')) OR (action='delete' AND state='deleting')").Scan(&active); e != nil {
		return nil, e
	}
	if active > 0 {
		return nil, errors.New("仍有远端上传或删除未完成，请先完成或取消后导出")
	}
	p.mu.Lock()
	key := append([]byte(nil), p.key...)
	p.mu.Unlock()
	b := migrationBundle{Version: 1, Key: base64.RawURLEncoding.EncodeToString(key)}
	var e error
	b.Settings, e = p.Settings()
	if e != nil {
		return nil, e
	}
	list, e := p.List()
	if e != nil {
		return nil, e
	}
	if len(list) > 64 {
		return nil, errors.New("单个迁移包最多 64 个节点")
	}
	for _, n := range list {
		b.Nodes = append(b.Nodes, migrationNode{n, n.Credential})
	}
	items, e := p.Files.List()
	if e != nil {
		return nil, e
	}
	for _, v := range items {
		if v.StorageNode == "" {
			continue
		}
		entry := migrationFile{File: v}
		entry.ObjectID, e = p.activeObject(v.ID)
		if e != nil {
			return nil, e
		}
		if s, e := p.Shares.Get(v.ID); e == nil {
			s.CoverAsset = ""
			entry.Share = &s
		}
		b.Files = append(b.Files, entry)
	}
	plain := []byte(raw(b))
	if len(plain) > MaxMigration/2 {
		return nil, errors.New("节点元数据超过迁移包上限")
	}
	salt := make([]byte, 16)
	if _, e = rand.Read(salt); e != nil {
		return nil, e
	}
	aead, e := migrationAEAD(password, salt)
	if e != nil {
		return nil, e
	}
	nonce := make([]byte, aead.NonceSize())
	if _, e = rand.Read(nonce); e != nil {
		return nil, e
	}
	encrypted := aead.Seal(nil, nonce, plain, []byte("FilePanel node migration v1"))
	return json.Marshal(migrationCipher{1, base64.RawStdEncoding.EncodeToString(salt), base64.RawStdEncoding.EncodeToString(nonce), base64.RawStdEncoding.EncodeToString(encrypted)})
}
func unpackMigration(data []byte, password string) (migrationBundle, error) {
	var b migrationBundle
	var c migrationCipher
	if len(data) > MaxMigration || json.Unmarshal(data, &c) != nil || c.Version != 1 {
		return b, ErrDenied
	}
	salt, e := base64.RawStdEncoding.DecodeString(c.Salt)
	if e != nil {
		return b, ErrDenied
	}
	nonce, e := base64.RawStdEncoding.DecodeString(c.Nonce)
	if e != nil {
		return b, ErrDenied
	}
	aead, e := migrationAEAD(password, salt)
	if e != nil {
		return b, e
	}
	ciphertext, e := base64.RawStdEncoding.DecodeString(c.Data)
	if e != nil || len(nonce) != aead.NonceSize() {
		return b, ErrDenied
	}
	plain, e := aead.Open(nil, nonce, ciphertext, []byte("FilePanel node migration v1"))
	if e != nil {
		return b, errors.New("迁移包密码错误或内容已损坏")
	}
	if json.Unmarshal(plain, &b) != nil || b.Version != 1 || len(b.Nodes) > 64 || len(b.Files) > 10000 {
		return b, ErrDenied
	}
	key, e := base64.RawURLEncoding.DecodeString(b.Key)
	if e != nil || len(key) != ed25519.PrivateKeySize {
		return b, ErrDenied
	}
	derived := ed25519.NewKeyFromSeed(key[:ed25519.SeedSize])
	if string(derived) != string(key) {
		return b, ErrDenied
	}
	nodes := map[string]bool{}
	for _, n := range b.Nodes {
		if n.Node.ID == "" || nodes[n.Node.ID] || n.Credential == "" || Endpoint(n.Node.Endpoint) != nil || n.Node.Policy.Validate() != nil {
			return b, ErrDenied
		}
		if _, e := Client(n.Node.CA); e != nil {
			return b, e
		}
		nodes[n.Node.ID] = true
	}
	files := map[string]bool{}
	slugs := map[string]bool{}
	for _, f := range b.Files {
		v := f.File
		if len(v.ID) < 24 || files[v.ID] || !nodes[v.StorageNode] || len(v.SHA256) != 64 || v.Size < 0 {
			return b, ErrDenied
		}
		files[v.ID] = true
		if s := f.Share; s != nil {
			if s.FileID != v.ID || s.Slug == "" || slugs[s.Slug] || s.AuthVersion < 1 {
				return b, ErrDenied
			}
			slugs[s.Slug] = true
		}
	}
	if b.Settings.DefaultRelay != "" && !nodes[b.Settings.DefaultRelay] {
		return b, ErrDenied
	}
	return b, nil
}
func (p *Panel) ImportMigration(data []byte, password, endpoint, ca string, apply bool) (MigrationSummary, error) {
	var result MigrationSummary
	if Endpoint(endpoint) != nil {
		return result, ErrDenied
	}
	if _, e := Client(ca); e != nil {
		return result, e
	}
	b, e := unpackMigration(data, password)
	if e != nil {
		return result, e
	}
	key, _ := base64.RawURLEncoding.DecodeString(b.Key)
	result = MigrationSummary{Nodes: len(b.Nodes), Files: len(b.Files), PanelID: base64.RawURLEncoding.EncodeToString(ed25519.PrivateKey(key).Public().(ed25519.PublicKey)), Warning: "需停止旧面板后使用新面板；迁移包不含本机文件、图片素材、站点外观与 HTTPS 私钥。节点文件与节点持久用量保留；节点地址须从新面板可达。"}
	p.mu.Lock()
	defer p.mu.Unlock()
	tx, e := p.DB.DB.Begin()
	if e != nil {
		return result, e
	}
	defer tx.Rollback()
	var count int
	if e = tx.QueryRow("SELECT (SELECT COUNT(*) FROM nodes)+(SELECT COUNT(*) FROM files)").Scan(&count); e != nil {
		return result, e
	}
	if count != 0 {
		return result, errors.New("只允许导入没有文件和节点记录的新面板")
	}
	if !apply {
		return result, nil
	}
	if e = p.Files.CheckSpace(uint64(len(data))*4 + 1<<20); e != nil {
		return result, e
	}
	for _, entry := range b.Nodes {
		n := entry.Node
		_, e = tx.Exec(`INSERT INTO nodes(id,credential_hash,credential,endpoint,ca,storage,relay,revoked,version,note,region,manual_region,policy) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, n.ID, hash(entry.Credential), entry.Credential, n.Endpoint, n.CA, n.Storage, n.Relay, n.Revoked, n.Version, n.Note, n.Region, n.ManualRegion, raw(n.Policy))
		if e != nil {
			return result, e
		}
		if !n.Revoked {
			if _, e = tx.Exec("INSERT INTO node_rebinds(node_id) VALUES(?)", n.ID); e != nil {
				return result, e
			}
		}
	}
	for _, entry := range b.Files {
		v := entry.File
		_, e = tx.Exec(`INSERT INTO files(id,object_key,display_name,size,sha256,state,created_at,updated_at) VALUES(?,?,?,?,?,'available',?,?)`, v.ID, "remote:"+v.ID, v.Name, v.Size, v.SHA256, v.CreatedAt, p.Now().Unix())
		if e != nil {
			return result, e
		}
		if _, e = tx.Exec("INSERT INTO node_locations(file_id,node_id,sha256) VALUES(?,?,?)", v.ID, v.StorageNode, v.SHA256); e != nil {
			return result, e
		}
		if entry.ObjectID != "" {
			if _, e = tx.Exec("INSERT INTO file_objects(file_id,object_id) VALUES(?,?)", v.ID, entry.ObjectID); e != nil {
				return result, e
			}
		}
		if s := entry.Share; s != nil {
			_, e = tx.Exec(`INSERT INTO shares(file_id,slug,title,version,summary,markdown,button_text,enabled,featured,auth_version,created_at,updated_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, v.ID, s.Slug, s.Title, s.Version, s.Summary, s.Markdown, s.ButtonText, s.Enabled, s.Featured, s.AuthVersion, p.Now().Unix(), s.UpdatedAt)
			if e != nil {
				return result, e
			}
		}
	}
	for k, v := range map[string]string{"node_panel_key": b.Key, "node_panel_endpoint": strings.TrimRight(endpoint, "/"), "node_panel_ca": ca, "node_settings": raw(b.Settings)} {
		if _, e = tx.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", k, v); e != nil {
			return result, e
		}
	}
	if e = tx.Commit(); e != nil {
		return result, e
	}
	p.key = ed25519.PrivateKey(key)
	return result, nil
}

type Rebind struct {
	Endpoint string `json:"endpoint"`
	CA       string `json:"ca"`
	Nonce    string `json:"nonce"`
	NodeID   string `json:"node_id"`
	Until    int64  `json:"until"`
}

func (p *Panel) Rebind(ctx context.Context, id string) error {
	n, e := p.Get(id)
	if e != nil || n.Revoked {
		return ErrDenied
	}
	endpoint, ok, e := p.DB.Setting("node_panel_endpoint")
	if e != nil || !ok || Endpoint(endpoint) != nil {
		return ErrDenied
	}
	ca, _, _ := p.DB.Setting("node_panel_ca")
	c, e := p.client(n)
	if e != nil {
		return e
	}
	in := Rebind{Endpoint: endpoint, CA: ca, Nonce: random(), NodeID: id, Until: p.Now().Add(300 * time.Second).Unix()}
	e = call(ctx, c, "POST", n.Endpoint, prefix+"/rebind", n.Credential, p.sign(in), nil)
	if e != nil {
		return e
	}
	_, e = p.DB.DB.Exec("DELETE FROM node_rebinds WHERE node_id=?", id)
	return e
}
func (p *Panel) proof(w http.ResponseWriter, r *http.Request) {
	n, e := p.authenticated(r)
	var in struct {
		Nonce string `json:"nonce"`
	}
	if e != nil || decode(r, &in) != nil || len(in.Nonce) < 24 {
		fail(w)
		return
	}
	send(w, 200, p.sign(p.control(n, in.Nonce)))
}
func (n *Daemon) rebind(w http.ResponseWriter, r *http.Request) {
	id, _ := n.connection()
	if hash(bearer(r)) != hash(id.Credential) {
		fail(w)
		return
	}
	var signed Envelope
	var in Rebind
	if decode(r, &signed) != nil || unseal(id.PanelID, signed, &in) != nil || in.NodeID != id.ID || Endpoint(in.Endpoint) != nil || in.Until < n.Now().Unix() || in.Until > n.Now().Add(300*time.Second+Skew).Unix() {
		fail(w)
		return
	}
	c, e := Client(in.CA)
	if e != nil {
		fail(w)
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
	defer cancel()
	nonce := random()
	var out Envelope
	if call(ctx, c, "POST", in.Endpoint, prefix+"/proof", id.Credential, map[string]string{"nonce": nonce}, &out) != nil {
		fail(w)
		return
	}
	var control Control
	if unseal(id.PanelID, out, &control) != nil || n.accept(control, nonce) != nil {
		fail(w)
		return
	}
	id.Panel, id.CA = strings.TrimRight(in.Endpoint, "/"), in.CA
	n.mu.Lock()
	e = n.DB.SetSetting("node_identity", raw(id))
	if e == nil {
		n.Identity = id
		old := n.client
		n.client = c
		old.CloseIdleConnections()
	}
	n.mu.Unlock()
	if e != nil {
		fail(w)
		return
	}
	send(w, 200, map[string]bool{"reconnected": true})
}
func (p *Panel) sign(v any) Envelope { p.mu.Lock(); defer p.mu.Unlock(); return seal(p.key, v) }
func (p *Panel) ticket(id, role string) string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return makeTicket(p.key, id, role)
}
