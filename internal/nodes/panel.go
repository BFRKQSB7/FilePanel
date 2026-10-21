package nodes

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"filepanel/internal/auth"
	"filepanel/internal/download"
	"filepanel/internal/files"
	"filepanel/internal/release"
	"filepanel/internal/share"
	"filepanel/internal/store"
	"filepanel/internal/usage"
)

type Panel struct {
	DB         *store.Store
	Files      *files.Manager
	Shares     *share.Manager
	Auth       *auth.Auth
	Meter      *usage.Meter
	Download   *download.Service
	key        ed25519.PrivateKey
	Now        func() time.Time
	GeoLookup  func(context.Context, string) (string, error)
	mu         sync.Mutex
	clients    map[string]*http.Client
	opsMu      sync.Mutex
	moveWorker sync.Mutex
	MoveFault  func(string) error
}

func NewPanel(db *store.Store, f *files.Manager, sh *share.Manager, a *auth.Auth, m *usage.Meter, d *download.Service) (*Panel, error) {
	if _, ok, e := db.Setting("node_identity"); e != nil {
		return nil, e
	} else if ok {
		return nil, errors.New("节点数据目录不能作为面板运行")
	}
	if _, e := db.DB.Exec("UPDATE node_transfers SET state='failed' WHERE action='upload' AND state='pending'"); e != nil {
		return nil, e
	}
	if _, e := db.DB.Exec("UPDATE file_moves SET fence=fence+1 WHERE state NOT IN ('done','cancelled')"); e != nil {
		return nil, e
	}
	s, ok, err := db.Setting("node_panel_key")
	if err != nil {
		return nil, err
	}
	var key ed25519.PrivateKey
	if ok {
		b, e := base64.RawURLEncoding.DecodeString(s)
		if e != nil || len(b) != ed25519.PrivateKeySize {
			return nil, errors.New("面板节点身份损坏")
		}
		key = ed25519.PrivateKey(b)
	} else {
		_, key, err = ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		if err = db.SetSetting("node_panel_key", base64.RawURLEncoding.EncodeToString(key)); err != nil {
			return nil, err
		}
	}
	return &Panel{DB: db, Files: f, Shares: sh, Auth: a, Meter: m, Download: d, key: key, Now: time.Now, clients: map[string]*http.Client{}}, nil
}
func (p *Panel) ID() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return base64.RawURLEncoding.EncodeToString(p.key.Public().(ed25519.PublicKey))
}
func (p *Panel) NewTicket() (string, error) {
	t := random()
	_, err := p.DB.DB.Exec("INSERT INTO node_tickets(hash,expires) VALUES(?,?)", hash(t), p.Now().Add(300*time.Second).Unix())
	return t, err
}

const nodeSelect = `SELECT id,endpoint,ca,storage,relay,revoked,version,note,region,manual_region,public_ip,last_seen,last_probe,probe_error,policy,credential FROM nodes`

func scanNode(r interface{ Scan(...any) error }) (Node, error) {
	var n Node
	var policy string
	e := r.Scan(&n.ID, &n.Endpoint, &n.CA, &n.Storage, &n.Relay, &n.Revoked, &n.Version, &n.Note, &n.Region, &n.ManualRegion, &n.PublicIP, &n.LastSeen, &n.LastProbe, &n.ProbeError, &policy, &n.Credential)
	if e != nil {
		return n, e
	}
	n.Policy = usage.DefaultConfig()
	e = json.Unmarshal([]byte(policy), &n.Policy)
	return n, e
}
func (p *Panel) Get(id string) (Node, error) {
	return scanNode(p.DB.DB.QueryRow(nodeSelect+" WHERE id=?", id))
}
func (p *Panel) authenticated(r *http.Request) (Node, error) {
	return scanNode(p.DB.DB.QueryRow(nodeSelect+" WHERE credential_hash=? AND revoked=0", hash(bearer(r))))
}
func (p *Panel) List() ([]Node, error) {
	rows, e := p.DB.DB.Query(nodeSelect + " ORDER BY public_ip,endpoint,id")
	if e != nil {
		return nil, e
	}
	all := []Node{}
	for rows.Next() {
		n, e := scanNode(rows)
		if e != nil {
			rows.Close()
			return nil, e
		}
		all = append(all, n)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}
	for i := range all {
		n := &all[i]
		n.Online = !n.Revoked && p.Now().Unix() <= n.LastSeen+int64(TTL/time.Second)
		n.DataReady = n.Online && n.LastProbe > 0 && n.ProbeError == "" && p.Now().Unix() <= n.LastProbe+60
		var report Report
		e = p.DB.DB.QueryRow(`SELECT sequence,period,sent,received,downloads,total,free FROM node_reports WHERE node_id=? ORDER BY period DESC LIMIT 1`, n.ID).Scan(&report.Sequence, &report.Period.Start, &report.Period.Sent, &report.Period.Received, &report.Period.Downloads, &report.Total, &report.Free)
		if e == nil {
			n.Report = &report
			_ = p.DB.DB.QueryRow("SELECT day,sent,received,downloads FROM node_days WHERE node_id=? ORDER BY day DESC LIMIT 1", n.ID).Scan(&report.Today.Start, &report.Today.Sent, &report.Today.Received, &report.Today.Downloads)
		}
		_ = p.DB.DB.QueryRow("SELECT EXISTS(SELECT 1 FROM node_rebinds WHERE node_id=?)", n.ID).Scan(&n.RebindPending)
	}
	return all, nil
}

type Settings struct {
	DefaultRelay string `json:"default_relay"`
	Fallback     bool   `json:"fallback"`
}

func (p *Panel) Settings() (Settings, error) {
	var s Settings
	b, ok, e := p.DB.Setting("node_settings")
	if e == nil && ok {
		e = json.Unmarshal([]byte(b), &s)
	}
	return s, e
}
func (p *Panel) SetSettings(s Settings) error {
	if s.DefaultRelay != "" {
		n, e := p.Get(s.DefaultRelay)
		if e != nil || n.Revoked || !n.Relay {
			return ErrDenied
		}
	}
	return p.DB.SetSetting("node_settings", raw(s))
}
func (p *Panel) Update(id string, n Node) error {
	if len(n.Note) > 300 || len(n.Region) > 200 || n.Policy.Validate() != nil {
		return errors.New("节点备注、地区或保护策略无效")
	}
	// Endpoint and CA are immutable: changing either requires a fresh enrollment.
	_, e := p.DB.DB.Exec(`UPDATE nodes SET storage=?,relay=?,note=?,region=?,manual_region=?,policy=?,version=version+1 WHERE id=? AND revoked=0`, n.Storage, n.Relay, n.Note, n.Region, n.ManualRegion, raw(n.Policy), id)
	return e
}
func (p *Panel) Revoke(id string) error {
	_, e := p.DB.DB.Exec("UPDATE nodes SET revoked=1,version=version+1 WHERE id=?", id)
	return e
}
func (p *Panel) RemoveNode(id string) error {
	tx, e := p.DB.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var revoked bool
	if e = tx.QueryRow("SELECT revoked FROM nodes WHERE id=?", id).Scan(&revoked); e != nil {
		return e
	}
	if !revoked {
		return errors.New("须先撤销节点凭据")
	}
	var count int
	if e = tx.QueryRow("SELECT (SELECT COUNT(*) FROM node_locations WHERE node_id=?)+(SELECT COUNT(*) FROM node_transfers WHERE source=? AND action='upload' AND state IN ('pending','validated'))", id, id).Scan(&count); e != nil {
		return e
	}
	if count > 0 {
		return errors.New("节点仍有关联文件或未完成上传，须先迁移或保留离线记录")
	}
	if e = tx.QueryRow("SELECT COUNT(*) FROM file_moves WHERE (source=? OR target=?) AND state NOT IN ('done','cancelled')", id, id).Scan(&count); e != nil {
		return e
	}
	if count > 0 {
		return errors.New("节点仍有移动或对象清理未完成，保留离线记录")
	}
	for _, table := range []string{"node_reports", "node_days", "node_rebinds"} {
		if _, e = tx.Exec("DELETE FROM "+table+" WHERE node_id=?", id); e != nil {
			return e
		}
	}
	var settings string
	if e = tx.QueryRow("SELECT value FROM settings WHERE key='node_settings'").Scan(&settings); e == nil {
		var cfg Settings
		if e = json.Unmarshal([]byte(settings), &cfg); e != nil {
			return e
		}
		if cfg.DefaultRelay == id {
			cfg.DefaultRelay = ""
			if _, e = tx.Exec("UPDATE settings SET value=? WHERE key='node_settings'", raw(cfg)); e != nil {
				return e
			}
		}
	} else if !errors.Is(e, sql.ErrNoRows) {
		return e
	}
	if _, e = tx.Exec("DELETE FROM nodes WHERE id=?", id); e != nil {
		return e
	}
	return tx.Commit()
}
func (p *Panel) client(n Node) (*http.Client, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if c := p.clients[n.ID]; c != nil {
		return c, nil
	}
	c, e := Client(n.CA)
	if e == nil {
		p.clients[n.ID] = c
	}
	return c, e
}
func (p *Panel) Probe(ctx context.Context, id string) error {
	n, e := p.Get(id)
	if e != nil || n.Revoked {
		return ErrDenied
	}
	c, e := p.client(n)
	if e != nil {
		return e
	}
	ctx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	var out struct {
		ID       string `json:"id"`
		PanelID  string `json:"panel_id"`
		Protocol int    `json:"protocol"`
		Schema   int    `json:"schema"`
	}
	e = call(ctx, c, "GET", n.Endpoint, prefix+"/health", n.Credential, nil, &out)
	if e == nil && (out.ID != n.ID || out.PanelID != p.ID() || out.Protocol != release.Protocol || out.Schema != release.Schema) {
		e = ErrDenied
	}
	message := ""
	if e != nil {
		message = "TLS / 身份 / 面板到节点路径未通过"
	}
	_, dbErr := p.DB.DB.Exec("UPDATE nodes SET last_probe=?,probe_error=? WHERE id=?", p.Now().Unix(), message, id)
	if e != nil {
		return e
	}
	return dbErr
}
func (p *Panel) usable(ctx context.Context, id string, storage bool) (Node, error) {
	n, e := p.Get(id)
	if e != nil || n.Revoked || p.Now().Unix() > n.LastSeen+30 || (storage && !n.Storage) || (!storage && !n.Relay) {
		return n, ErrDenied
	}
	if e = p.Probe(ctx, id); e != nil {
		return n, e
	}
	return n, nil
}

func (p *Panel) Routes(mux *http.ServeMux) {
	mux.HandleFunc("POST "+prefix+"/proof", p.proof)
	mux.HandleFunc("POST "+prefix+"/join", p.join)
	mux.HandleFunc("POST "+prefix+"/heartbeat", p.heartbeat)
	mux.HandleFunc("POST "+prefix+"/check", p.check)
	mux.HandleFunc("GET "+prefix+"/source", p.source)
}
func (p *Panel) join(w http.ResponseWriter, r *http.Request) {
	var in JoinRequest
	if decode(r, &in) != nil || Endpoint(in.Endpoint) != nil {
		fail(w)
		return
	}
	if _, e := Client(in.CA); e != nil {
		fail(w)
		return
	}
	id, credential := random(), random()
	tx, e := p.DB.DB.Begin()
	if e != nil {
		fail(w)
		return
	}
	defer tx.Rollback()
	res, e := tx.Exec("UPDATE node_tickets SET used=1 WHERE hash=? AND used=0 AND expires>=?", hash(in.Ticket), p.Now().Unix())
	if e != nil {
		fail(w)
		return
	}
	count, _ := res.RowsAffected()
	if count != 1 {
		fail(w)
		return
	}
	_, e = tx.Exec(`INSERT INTO nodes(id,credential_hash,credential,endpoint,ca,policy) VALUES(?,?,?,?,?,?)`, id, hash(credential), credential, strings.TrimRight(in.Endpoint, "/"), in.CA, raw(usage.DefaultConfig()))
	if e != nil || tx.Commit() != nil {
		fail(w)
		return
	}
	send(w, 201, p.sign(Identity{ID: id, Credential: credential, PanelID: p.ID()}))
}
func (p *Panel) control(n Node, nonce string) Control {
	return Control{PanelID: p.ID(), NodeID: n.ID, Nonce: nonce, Until: p.Now().Add(TTL).Unix(), Version: n.Version, Storage: n.Storage, Relay: n.Relay, Policy: n.Policy}
}
func (p *Panel) heartbeat(w http.ResponseWriter, r *http.Request) {
	n, e := p.authenticated(r)
	var in Heartbeat
	if e != nil || decode(r, &in) != nil || len(in.Nonce) < 24 || in.Report.Sequence < 1 || in.Report.Period.Sent < 0 || in.Report.Period.Received < 0 || in.Report.Period.Downloads < 0 {
		fail(w)
		return
	}
	cfg, _ := p.Meter.Config()
	ip := download.ClientIP(r, cfg)
	tx, e := p.DB.DB.Begin()
	if e != nil {
		fail(w)
		return
	}
	defer tx.Rollback()
	_, e = tx.Exec("UPDATE nodes SET last_seen=?,public_ip=? WHERE id=? AND revoked=0", p.Now().Unix(), ip, n.ID)
	if e == nil {
		_, e = tx.Exec(`INSERT INTO node_reports(node_id,period,sequence,sent,received,downloads,total,free) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(node_id,period) DO UPDATE SET sequence=excluded.sequence,sent=MAX(sent,excluded.sent),received=MAX(received,excluded.received),downloads=MAX(downloads,excluded.downloads),total=excluded.total,free=excluded.free WHERE excluded.sequence>sequence`, n.ID, in.Report.Period.Start, in.Report.Sequence, in.Report.Period.Sent, in.Report.Period.Received, in.Report.Period.Downloads, in.Report.Total, in.Report.Free)
	}
	if e == nil && in.Report.Today.Start > 0 && in.Report.Today.Sent >= 0 && in.Report.Today.Received >= 0 && in.Report.Today.Downloads >= 0 {
		_, e = tx.Exec(`INSERT INTO node_days(node_id,day,sequence,sent,received,downloads) VALUES(?,?,?,?,?,?) ON CONFLICT(node_id,day) DO UPDATE SET sequence=excluded.sequence,sent=MAX(sent,excluded.sent),received=MAX(received,excluded.received),downloads=MAX(downloads,excluded.downloads) WHERE excluded.sequence>sequence`, n.ID, in.Report.Today.Start, in.Report.Sequence, in.Report.Today.Sent, in.Report.Today.Received, in.Report.Today.Downloads)
	}
	if e != nil || tx.Commit() != nil {
		fail(w)
		return
	}
	send(w, 200, p.sign(p.control(n, in.Nonce)))
}
func (p *Panel) transfer(id string) (Transfer, error) {
	var t Transfer
	e := p.DB.DB.QueryRow(`SELECT id,action,file_id,source,relay,sha256,name,visitor_ip,auth_version,admin_session,expires,state FROM node_transfers WHERE id=?`, id).Scan(&t.ID, &t.Action, &t.File, &t.Source, &t.Relay, &t.SHA, &t.Name, &t.IP, &t.Version, &t.Session, &t.Expires, &t.State)
	return t, e
}
func (p *Panel) save(t Transfer) error {
	_, e := p.DB.DB.Exec(`INSERT INTO node_transfers(id,action,file_id,source,relay,sha256,name,visitor_ip,auth_version,admin_session,expires,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, t.ID, t.Action, t.File, t.Source, t.Relay, t.SHA, t.Name, t.IP, t.Version, t.Session, t.Expires, t.State)
	return e
}
func (p *Panel) authorize(t Transfer, start bool) error {
	if start && p.Now().Unix() > t.Expires+5 {
		return ErrDenied
	}
	if t.Action == "download" {
		for _, id := range []string{t.Source, t.Relay} {
			if id != "" {
				n, e := p.Get(id)
				if e != nil || n.Revoked || p.Now().Unix() > n.LastSeen+30 || (id == t.Relay && !n.Relay) {
					return ErrDenied
				}
			}
		}
		v, e := p.Files.Metadata(t.File)
		if e != nil || v.SHA256 != t.SHA {
			return ErrDenied
		}
		obj, oe := p.activeObject(t.File)
		if oe != nil {
			return ErrDenied
		}
		if (v.StorageNode != t.Source || obj != p.transferObject(t)) && (start || !p.oldReader(t)) {
			return ErrDenied
		}
		if t.Session != "" {
			if _, ok := p.Auth.Validate(t.Session); !ok {
				return ErrDenied
			}
		} else if p.Shares.Authorized(t.File, t.Version) != nil {
			return ErrDenied
		}
	} else if t.Action == "upload" {
		if t.State != "pending" && t.State != "validated" {
			return ErrDenied
		}
		if _, ok := p.Auth.Validate(t.Session); !ok && t.State != "validated" {
			return ErrDenied
		}
	} else if t.Action == "delete" {
		if t.State != "deleting" {
			return ErrDenied
		}
	} else {
		return ErrDenied
	}
	return nil
}
func (p *Panel) permit(n Node, in Check) (Permit, error) {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	if out, handled, e := p.movePermit(n, in); handled {
		return out, e
	}
	t, e := p.transfer(in.Transfer)
	if e != nil || p.authorize(t, in.Start) != nil {
		return Permit{}, ErrDenied
	}
	if in.Role == "relay" {
		if t.Relay != n.ID || !n.Relay || t.Action != "download" || in.IP != t.IP {
			return Permit{}, ErrDenied
		}
	} else if in.Role == "source" {
		if t.Source != n.ID {
			return Permit{}, ErrDenied
		}
		if t.Action == "upload" && in.Start && !n.Storage {
			return Permit{}, ErrDenied
		}
	} else {
		return Permit{}, ErrDenied
	}
	if _, e = p.DB.DB.Exec("INSERT INTO node_transfer_leases(transfer_id,until) VALUES(?,?) ON CONFLICT(transfer_id) DO UPDATE SET until=MAX(until,excluded.until)", t.ID, p.Now().Add(TTL+Skew).Unix()); e != nil {
		return Permit{}, e
	}
	out := Permit{Control: p.control(n, in.Nonce), Action: t.Action, File: files.Item{ID: t.File, Name: t.Name, SHA256: t.SHA}, Ticket: p.ticket(t.ID, "source"), VisitorIP: t.IP}
	out.ObjectID = p.transferObject(t)
	if t.Action == "download" {
		out.File, e = p.Files.Metadata(t.File)
		if e != nil {
			return out, e
		}
		if in.Role == "relay" {
			if t.Source == "" {
				base, ok, e := p.DB.Setting("node_panel_endpoint")
				if e != nil || !ok || Endpoint(base) != nil {
					return out, ErrDenied
				}
				out.Source = base
				out.SourceCA, _, _ = p.DB.Setting("node_panel_ca")
			} else {
				src, e := p.Get(t.Source)
				if e != nil || src.Revoked || p.Now().Unix() > src.LastSeen+30 {
					return out, ErrDenied
				}
				out.Source, out.SourceCA = src.Endpoint, src.CA
			}
		}
	}
	return out, nil
}
func (p *Panel) check(w http.ResponseWriter, r *http.Request) {
	n, e := p.authenticated(r)
	var in Check
	if e != nil || decode(r, &in) != nil || len(in.Nonce) < 24 {
		fail(w)
		return
	}
	out, e := p.permit(n, in)
	if e != nil {
		fail(w)
		return
	}
	send(w, 200, p.sign(out))
}

func (p *Panel) NewDownload(ctx context.Context, file, relay, ip, session string, version int64) (Transfer, error) {
	p.opsMu.Lock()
	defer p.opsMu.Unlock()
	v, e := p.Files.Metadata(file)
	if e != nil {
		return Transfer{}, e
	}
	if relay != "" {
		if _, e = p.usable(ctx, relay, false); e != nil {
			return Transfer{}, e
		}
	}
	if v.StorageNode != "" {
		n, e := p.Get(v.StorageNode)
		if e != nil || n.Revoked || p.Now().Unix() > n.LastSeen+30 {
			return Transfer{}, ErrDenied
		}
		if e = p.Probe(ctx, n.ID); e != nil {
			return Transfer{}, e
		}
	}
	t := Transfer{ID: random(), Action: "download", File: file, Source: v.StorageNode, Relay: relay, SHA: v.SHA256, Name: v.Name, IP: ip, Session: session, Version: version, Expires: p.Now().Add(300 * time.Second).Unix(), State: "active"}
	if e = p.save(t); e != nil {
		return t, e
	}
	obj, e := p.activeObject(file)
	if e != nil {
		return t, e
	}
	_, e = p.DB.DB.Exec("INSERT INTO transfer_objects(transfer_id,object_id) VALUES(?,?)", t.ID, obj)
	return t, e
}
func (p *Panel) TicketURL(t Transfer) string {
	n, e := p.Get(t.Relay)
	if e != nil {
		return ""
	}
	return n.Endpoint + "/d/" + t.File + "?t=" + p.ticket(t.ID, "relay")
}
func (p *Panel) RemoteSource(t Transfer) func(context.Context) (files.Item, download.ReadSeekCloser, error) {
	return func(ctx context.Context) (files.Item, download.ReadSeekCloser, error) {
		v, e := p.Files.Metadata(t.File)
		if e != nil {
			return v, nil, e
		}
		n, e := p.Get(t.Source)
		if e != nil || n.Revoked {
			return v, nil, ErrDenied
		}
		c, e := p.client(n)
		if e != nil {
			return v, nil, e
		}
		return v, &remoteReader{ctx: ctx, client: c, endpoint: n.Endpoint, ticket: p.ticket(t.ID, "source"), size: v.Size, meter: p.Meter, ip: t.IP, category: "panel_relay"}, nil
	}
}
func (p *Panel) SourceRequest(ctx context.Context, file, ip, session string, version int64) (download.Request, error) {
	t, e := p.NewDownload(ctx, file, "", ip, session, version)
	if e != nil {
		return download.Request{}, e
	}
	return download.Request{FileID: file, SHA: t.SHA, IP: ip, Category: usage.CategoryPublic, Source: p.RemoteSource(t), Revalidate: func() error { return p.authorize(t, false) }}, nil
}
func (p *Panel) source(w http.ResponseWriter, r *http.Request) {
	ticket, e := readTicket(p.ID(), bearer(r))
	if e != nil || ticket.Role != "source" {
		fail(w)
		return
	}
	p.opsMu.Lock()
	movePermit, handled, me := p.movePermit(Node{}, Check{Transfer: ticket.ID, Role: "source", Start: true})
	p.opsMu.Unlock()
	if handled {
		if me != nil || movePermit.Action != "move_read" {
			fail(w)
			return
		}
		e = p.Download.Serve(w, r, download.Request{FileID: movePermit.File.ID, SHA: movePermit.File.SHA256, IP: "move", Category: "move_source", Transit: true,
			Source: func(context.Context) (files.Item, download.ReadSeekCloser, error) {
				v, f, e := p.Files.OpenObject(movePermit.ObjectID, movePermit.File.SHA256)
				if e != nil {
					return v, nil, e
				}
				return v, &moveFaultReader{readSeekCloser: f, panel: p}, nil
			},
			Revalidate: func() error {
				p.opsMu.Lock()
				defer p.opsMu.Unlock()
				_, _, e := p.movePermit(Node{}, Check{Transfer: ticket.ID, Role: "source"})
				return e
			}})
		if e != nil {
			fail(w)
		}
		return
	}
	t, e := p.transfer(ticket.ID)
	if e != nil || t.Action != "download" || t.Source != "" || p.authorize(t, true) != nil {
		fail(w)
		return
	}
	n, e := p.Get(t.Relay)
	if e != nil || n.Revoked || !n.Relay || p.Now().Unix() > n.LastSeen+30 {
		fail(w)
		return
	}
	e = p.Download.Serve(w, r, download.Request{FileID: t.File, SHA: t.SHA, IP: t.IP, Category: "panel_source", Transit: true, Source: func(context.Context) (files.Item, download.ReadSeekCloser, error) {
		return p.Files.OpenObject(p.transferObject(t), t.SHA)
	}, Revalidate: func() error {
		n, e := p.Get(t.Relay)
		if e != nil || n.Revoked || !n.Relay || p.Now().Unix() > n.LastSeen+30 {
			return ErrDenied
		}
		return p.authorize(t, false)
	}})
	if e != nil {
		fail(w)
	}
}

func (p *Panel) Upload(ctx context.Context, node, name, session string, src io.Reader, check func() error) (files.Item, error) {
	n, e := p.usable(ctx, node, true)
	if e != nil {
		return files.Item{}, e
	}
	t := Transfer{ID: random(), Action: "upload", File: random(), Source: node, Name: name, Session: session, Expires: p.Now().Add(24 * time.Hour).Unix(), State: "pending"}
	if e = p.save(t); e != nil {
		return files.Item{}, e
	}
	defer func() {
		_, _ = p.DB.DB.Exec("UPDATE node_transfers SET state='failed' WHERE id=? AND state='pending'", t.ID)
	}()
	c, e := p.client(n)
	if e != nil {
		return files.Item{}, e
	}
	received, e := p.Meter.BeginReceived("upload:"+node, usage.CategoryUpload, -1)
	if e != nil {
		return files.Item{}, e
	}
	defer received.Close(false)
	body := &checkedReader{source: &sentReader{source: src, stream: received}, check: func() error {
		if e := check(); e != nil {
			return e
		}
		_, e := p.DB.DB.Exec("UPDATE node_transfers SET state='validated' WHERE id=? AND state='pending'", t.ID)
		return e
	}}
	stream, e := p.Meter.Begin("node:"+node, "panel_upload", -1)
	if e != nil {
		return files.Item{}, e
	}
	defer stream.Close(false)
	req, e := http.NewRequestWithContext(ctx, "PUT", n.Endpoint+prefix+"/object", &sentReader{source: body, stream: stream})
	if e != nil {
		return files.Item{}, e
	}
	req.Header.Set("Authorization", "Bearer "+p.ticket(t.ID, "source"))
	res, e := c.Do(req)
	if e != nil {
		return files.Item{}, errors.New("远端上传中断；已校验的上传将在重连后对账")
	}
	defer res.Body.Close()
	if res.StatusCode != 201 && res.StatusCode != 200 {
		_, _ = p.DB.DB.Exec("UPDATE node_transfers SET state='failed' WHERE id=? AND state IN ('pending','validated')", t.ID)
		return files.Item{}, ErrDenied
	}
	var item files.Item
	if e = json.NewDecoder(io.LimitReader(res.Body, 16<<10)).Decode(&item); e != nil {
		return item, e
	}
	return item, p.commitUpload(t, item)
}

type sentReader struct {
	source io.Reader
	stream *usage.Stream
}

func (r *sentReader) Read(b []byte) (int, error) {
	g, e := r.stream.Grant(len(b))
	if e != nil {
		return 0, e
	}
	n, e := r.source.Read(b[:g])
	r.stream.Confirm(n)
	return n, e
}
func (p *Panel) commitUpload(t Transfer, v files.Item) error {
	cur, e := p.transfer(t.ID)
	if e != nil || cur.State != "validated" || v.ID != t.File || v.Size < 0 || len(v.SHA256) != 64 {
		return ErrDenied
	}
	tx, e := p.DB.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.Exec(`INSERT INTO files(id,object_key,display_name,size,sha256,state,created_at,updated_at) VALUES(?,?,?,?,?,'available',?,?) ON CONFLICT(id) DO NOTHING`, v.ID, "remote:"+v.ID, v.Name, v.Size, v.SHA256, v.CreatedAt, p.Now().Unix())
	if e == nil {
		_, e = tx.Exec("INSERT OR IGNORE INTO node_locations(file_id,node_id,sha256) VALUES(?,?,?)", v.ID, t.Source, v.SHA256)
	}
	if e == nil {
		_, e = tx.Exec("UPDATE node_transfers SET state='committed',sha256=?,expires=? WHERE id=?", v.SHA256, p.Now().Unix(), t.ID)
	}
	if e != nil {
		return e
	}
	return tx.Commit()
}
func (p *Panel) Delete(ctx context.Context, id string) error {
	var active int
	if e := p.DB.DB.QueryRow("SELECT COUNT(*) FROM file_moves WHERE file_id=? AND state NOT IN ('done','cancelled')", id).Scan(&active); e != nil {
		return e
	}
	if active > 0 {
		return errors.New("文件移动尚未完成，请先完成或取消移动")
	}
	var previous string
	if p.DB.DB.QueryRow("SELECT id FROM node_transfers WHERE file_id=? AND action='delete' AND state='deleting'", id).Scan(&previous) == nil {
		t, e := p.transfer(previous)
		if e != nil {
			return e
		}
		return p.deleteTransfer(ctx, t)
	}
	tx, e := p.DB.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	var node, sha, name, obj string
	e = tx.QueryRow(`SELECT l.node_id,l.sha256,f.display_name,COALESCE(o.object_id,f.id) FROM node_locations l JOIN files f ON f.id=l.file_id LEFT JOIN file_objects o ON o.file_id=f.id WHERE l.file_id=? AND f.state='available'`, id).Scan(&node, &sha, &name, &obj)
	if errors.Is(e, sql.ErrNoRows) {
		tx.Rollback()
		return p.Files.Remove(id)
	}
	if e != nil {
		return e
	}
	t := Transfer{ID: random(), Action: "delete", File: id, Source: node, SHA: sha, Name: name, Expires: p.Now().Add(365 * 24 * time.Hour).Unix(), State: "deleting"}
	if _, e = tx.Exec("UPDATE files SET state='deleting' WHERE id=?", id); e != nil {
		return e
	}
	if _, e = tx.Exec(`INSERT INTO node_transfers(id,action,file_id,source,relay,sha256,name,visitor_ip,auth_version,admin_session,expires,state) VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, t.ID, t.Action, t.File, t.Source, t.Relay, t.SHA, t.Name, t.IP, t.Version, t.Session, t.Expires, t.State); e != nil {
		return e
	}
	if _, e = tx.Exec("INSERT INTO transfer_objects(transfer_id,object_id) VALUES(?,?)", t.ID, obj); e != nil {
		return e
	}
	if e = tx.Commit(); e != nil {
		return e
	}
	return p.deleteTransfer(ctx, t)
}
func (p *Panel) deleteTransfer(ctx context.Context, t Transfer) error {
	n, e := p.Get(t.Source)
	if e != nil || n.Revoked {
		return errors.New("远端删除待重连重试，离线记录已保留")
	}
	c, e := p.client(n)
	if e != nil {
		return e
	}
	e = call(ctx, c, "DELETE", n.Endpoint, prefix+"/object", p.ticket(t.ID, "source"), nil, nil)
	if e != nil {
		return errors.New("远端删除待重连重试，离线记录已保留")
	}
	tx, e := p.DB.DB.Begin()
	if e != nil {
		return e
	}
	defer tx.Rollback()
	_, e = tx.Exec("DELETE FROM files WHERE id=? AND state='deleting'", t.File)
	if e == nil {
		_, e = tx.Exec("UPDATE node_transfers SET state='deleted',expires=? WHERE id=?", p.Now().Unix(), t.ID)
	}
	if e != nil {
		return e
	}
	return tx.Commit()
}

// Reconcile retries only durable, validated work; pending uploads never publish.
func (p *Panel) Reconcile(ctx context.Context) {
	rows, e := p.DB.DB.Query("SELECT id FROM node_transfers WHERE state IN ('validated','deleting') LIMIT 32")
	if e != nil {
		return
	}
	var ids []string
	for rows.Next() {
		var id string
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	for _, id := range ids {
		if ctx.Err() != nil {
			return
		}
		t, e := p.transfer(id)
		if e != nil {
			continue
		}
		step, cancel := context.WithTimeout(ctx, 8*time.Second)
		if t.Action == "delete" {
			_ = p.deleteTransfer(step, t)
		} else {
			n, e := p.Get(t.Source)
			if e == nil && !n.Revoked {
				c, e := p.client(n)
				if e == nil {
					var v files.Item
					if err := call(step, c, "GET", n.Endpoint, prefix+"/object", p.ticket(t.ID, "source"), nil, &v); err == nil {
						_ = p.commitUpload(t, v)
					} else if errors.Is(err, ErrNotFound) {
						_, _ = p.DB.DB.Exec("UPDATE node_transfers SET state='failed' WHERE id=? AND state='validated'", t.ID)
					}
				}
			}
		}
		cancel()
	}
}
func (p *Panel) Run(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.ReconcileMoves(ctx)
			_, _ = p.DB.DB.Exec("DELETE FROM node_transfers WHERE id IN (SELECT id FROM node_transfers t WHERE expires+35<? AND state NOT IN ('validated','deleting') AND NOT EXISTS (SELECT 1 FROM node_transfer_leases l WHERE l.transfer_id=t.id AND l.until>=?) LIMIT 256)", p.Now().Unix(), p.Now().Unix())
			_, _ = p.DB.DB.Exec("DELETE FROM node_tickets WHERE expires<?", p.Now().Unix())
			p.Reconcile(ctx)
			if list, e := p.List(); e == nil {
				for _, n := range list {
					if !n.Revoked && !n.ManualRegion {
						_ = p.RefreshRegion(ctx, n.ID)
					}
				}
			}
			rows, e := p.DB.DB.Query("SELECT node_id FROM node_rebinds LIMIT 2")
			if e == nil {
				var ids []string
				for rows.Next() {
					var id string
					if rows.Scan(&id) == nil {
						ids = append(ids, id)
					}
				}
				rows.Close()
				for _, id := range ids {
					step, cancel := context.WithTimeout(ctx, 8*time.Second)
					_ = p.Rebind(step, id)
					cancel()
				}
			}
		}
	}
}
func itoa(n int64) string { return strconv.FormatInt(n, 10) }
func (p *Panel) ConfigureEndpoint(endpoint, ca string) error {
	if Endpoint(endpoint) != nil {
		return ErrDenied
	}
	if _, e := Client(ca); e != nil {
		return e
	}
	if e := p.DB.SetSetting("node_panel_endpoint", strings.TrimRight(endpoint, "/")); e != nil {
		return e
	}
	return p.DB.SetSetting("node_panel_ca", ca)
}
func (p *Panel) JoinCommand(endpoint, ticket string) string {
	ca, _, _ := p.DB.Setting("node_panel_ca")
	b, _ := json.MarshalIndent(map[string]string{"panel": endpoint, "panel_id": p.ID(), "ticket": ticket, "endpoint": "https://节点域名:映射端口", "ca": ca, "node_ca": ""}, "", "  ")
	return "umask 077\ncat > ./filepanel-join.json <<'FILEPANEL_JOIN'\n" + string(b) + "\nFILEPANEL_JOIN\n" +
		"curl -fL --proto '=https' --proto-redir '=https' https://github.com/BFRKQSB7/FilePanel/releases/download/v" + release.Version + "/install-online.sh -o ./filepanel-install.sh &&\n" +
		"(if [ \"$(id -u)\" = 0 ]; then sh ./filepanel-install.sh --instance node --role node --join-file ./filepanel-join.json; " +
		"else sudo sh ./filepanel-install.sh --instance node --role node --join-file ./filepanel-join.json; fi) &&\n" +
		"rm -f ./filepanel-join.json ./filepanel-install.sh"
}
