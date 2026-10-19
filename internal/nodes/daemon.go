package nodes

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"sync"
	"time"

	"filepanel/internal/download"
	"filepanel/internal/files"
	"filepanel/internal/release"
	"filepanel/internal/store"
	"filepanel/internal/usage"
)

// Enroll refuses to overwrite an existing identity, including when the panel
// address is reused. Trust roots and expected signing identity are explicit.
func Enroll(ctx context.Context, db *store.Store, identity Identity, ticket, endpoint, nodeCA string) error {
	if _, ok, e := db.Setting("node_identity"); e != nil {
		return e
	} else if ok {
		return errors.New("节点已绑定面板；拒绝覆盖已有身份")
	}
	for _, k := range []string{"node_panel_key", "admin_password_hash", "init_token_hash"} {
		if _, ok, e := db.Setting(k); e != nil {
			return e
		} else if ok {
			return errors.New("接入节点必须使用独立的空数据目录，不能复用面板目录")
		}
	}
	var count int
	if e := db.DB.QueryRow("SELECT COUNT(*) FROM files").Scan(&count); e != nil {
		return e
	}
	if count > 0 {
		return errors.New("接入节点必须使用独立的空数据目录")
	}
	if Endpoint(identity.Panel) != nil || Endpoint(endpoint) != nil {
		return ErrDenied
	}
	c, e := Client(identity.CA)
	if e != nil {
		return e
	}
	var out Envelope
	if e = call(ctx, c, "POST", identity.Panel, prefix+"/join", "", JoinRequest{Ticket: ticket, Endpoint: endpoint, CA: nodeCA}, &out); e != nil {
		return e
	}
	var got Identity
	if e = unseal(identity.PanelID, out, &got); e != nil {
		return e
	}
	if got.PanelID != identity.PanelID || got.ID == "" || got.Credential == "" {
		return ErrDenied
	}
	got.Panel, got.CA = identity.Panel, identity.CA
	return db.SetSetting("node_identity", raw(got))
}

type Daemon struct {
	DB       *store.Store
	Files    *files.Manager
	Meter    *usage.Meter
	Download *download.Service
	Identity Identity
	client   *http.Client
	Now      func() time.Time
	mu       sync.Mutex
	control  Control
	deadline time.Time
	objectMu sync.Mutex
	objects  map[string]*objectLock
}

type objectLock struct {
	gate  chan struct{}
	users int
}

func (n *Daemon) lockObject(ctx context.Context, id string) (func(), error) {
	n.objectMu.Lock()
	if n.objects == nil {
		n.objects = map[string]*objectLock{}
	}
	entry := n.objects[id]
	if entry == nil {
		entry = &objectLock{gate: make(chan struct{}, 1)}
		n.objects[id] = entry
	}
	entry.users++
	n.objectMu.Unlock()
	drop := func() {
		n.objectMu.Lock()
		entry.users--
		if entry.users == 0 {
			delete(n.objects, id)
		}
		n.objectMu.Unlock()
	}
	select {
	case entry.gate <- struct{}{}:
		return func() { <-entry.gate; drop() }, nil
	case <-ctx.Done():
		drop()
		return nil, ctx.Err()
	}
}

func (n *Daemon) connection() (Identity, *http.Client) {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.Identity, n.client
}

func NewDaemon(db *store.Store, f *files.Manager, m *usage.Meter, d *download.Service) (*Daemon, error) {
	rawID, ok, e := db.Setting("node_identity")
	if e != nil || !ok {
		return nil, errors.New("请先执行 node join")
	}
	var id Identity
	if e = json.Unmarshal([]byte(rawID), &id); e != nil {
		return nil, e
	}
	if Endpoint(id.Panel) != nil {
		return nil, ErrDenied
	}
	c, e := Client(id.CA)
	if e != nil {
		return nil, e
	}
	n := &Daemon{DB: db, Files: f, Meter: m, Download: d, Identity: id, client: c, Now: time.Now}
	// Retain persisted policy and highest accepted version, but never a live lease
	// across restart. A control round trip is required before serving content.
	if s, ok, e := db.Setting("node_control"); e != nil {
		return nil, e
	} else if ok {
		if e = json.Unmarshal([]byte(s), &n.control); e != nil {
			return nil, e
		}
	}
	return n, nil
}
func (n *Daemon) accept(c Control, nonce string) error {
	n.mu.Lock()
	defer n.mu.Unlock()
	now := n.Now()
	if c.PanelID != n.Identity.PanelID || c.NodeID != n.Identity.ID || c.Nonce != nonce || c.Until < now.Unix()-5 || c.Until > now.Add(TTL+Skew).Unix() || c.Version < n.control.Version || c.Policy.Validate() != nil {
		return ErrDenied
	}
	if raw(c.Policy) != raw(n.control.Policy) {
		if e := n.Meter.SetConfig(c.Policy); e != nil {
			return e
		}
	}
	if e := n.DB.SetSetting("node_control", raw(c)); e != nil {
		return e
	}
	n.control = c
	n.deadline = now.Add(TTL)
	if signed := time.Unix(c.Until, 0); signed.Before(n.deadline) {
		n.deadline = signed
	}
	return nil
}
func (n *Daemon) valid() bool {
	n.mu.Lock()
	defer n.mu.Unlock()
	return !n.deadline.IsZero() && !n.Now().After(n.deadline.Add(Skew))
}
func (n *Daemon) heartbeat(ctx context.Context) error {
	id, client := n.connection()
	st, e := n.Meter.Status()
	if e != nil {
		return e
	}
	today, e := n.Meter.Today()
	if e != nil {
		return e
	}
	total, free, _, e := n.DB.DiskInfo(n.Files.DataDir())
	if e != nil {
		return e
	}
	// Sequence persists across restart; heartbeat execution is single-threaded.
	var seq int64
	if s, ok, _ := n.DB.Setting("node_report_seq"); ok {
		_ = json.Unmarshal([]byte(s), &seq)
	}
	seq++
	if e = n.DB.SetSetting("node_report_seq", raw(seq)); e != nil {
		return e
	}
	nonce := random()
	var out Envelope
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	e = call(ctx, client, "POST", id.Panel, prefix+"/heartbeat", id.Credential, Heartbeat{Nonce: nonce, Report: Report{Sequence: seq, Period: st.Period, Today: today, Total: total, Free: free}}, &out)
	if e != nil {
		return e
	}
	var c Control
	if e = unseal(id.PanelID, out, &c); e != nil {
		return e
	}
	return n.accept(c, nonce)
}
func (n *Daemon) Heartbeat(ctx context.Context) error { return n.heartbeat(ctx) }

// Run uses bounded exponential backoff without logging every failed heartbeat.
func (n *Daemon) Run(ctx context.Context) {
	delay := time.Duration(0)
	for {
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		e := n.heartbeat(ctx)
		if e == nil {
			delay = Renew
		} else {
			if delay < time.Second {
				delay = time.Second
			} else {
				delay *= 2
			}
			if delay > 30*time.Second {
				delay = 30 * time.Second
			}
		}
	}
}
func (n *Daemon) permission(ctx context.Context, ticket, role, ip string, start bool) (Permit, error) {
	id, client := n.connection()
	var out Permit
	t, e := readTicket(id.PanelID, ticket)
	if e != nil || t.Role != role {
		return out, ErrDenied
	}
	// A fresh request always contacts the panel even with a cached control lease.
	nonce := random()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var signed Envelope
	e = call(ctx, client, "POST", id.Panel, prefix+"/check", id.Credential, Check{Nonce: nonce, Transfer: t.ID, Role: role, IP: ip, Start: start}, &signed)
	if e != nil {
		return out, e
	}
	if e = unseal(id.PanelID, signed, &out); e != nil {
		return out, e
	}
	return out, n.accept(out.Control, nonce)
}
func (n *Daemon) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+prefix+"/health", func(w http.ResponseWriter, r *http.Request) {
		id, _ := n.connection()
		if hash(bearer(r)) != hash(id.Credential) {
			fail(w)
			return
		}
		send(w, 200, map[string]any{"id": id.ID, "panel_id": id.PanelID, "version": release.Version, "protocol": release.Protocol, "schema": release.Schema})
	})
	mux.HandleFunc("GET "+prefix+"/update", n.update)
	mux.HandleFunc("POST "+prefix+"/update", n.update)
	mux.HandleFunc("POST "+prefix+"/rebind", n.rebind)
	mux.HandleFunc("PUT "+prefix+"/object", n.upload)
	mux.HandleFunc("POST "+prefix+"/pull", n.pull)
	mux.HandleFunc("GET "+prefix+"/object", n.metadata)
	mux.HandleFunc("DELETE "+prefix+"/object", n.remove)
	mux.HandleFunc("GET "+prefix+"/source", n.source)
	mux.HandleFunc("GET /d/{id}", n.relay)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "no-referrer")
		mux.ServeHTTP(w, r)
	})
}
func (n *Daemon) pull(w http.ResponseWriter, r *http.Request) {
	p, e := n.permission(r.Context(), bearer(r), "source", "", true)
	if e != nil || p.Action != "move_write" || Endpoint(p.Source) != nil {
		fail(w)
		return
	}
	if e = n.Files.CheckSpace(uint64(p.File.Size)); e != nil {
		fail(w)
		return
	}
	if e = n.Meter.Available("panel"); e != nil {
		fail(w)
		return
	}
	c, e := Client(p.SourceCA)
	if e != nil {
		fail(w)
		return
	}
	defer c.CloseIdleConnections()
	src := &remoteReader{ctx: r.Context(), client: c, endpoint: p.Source, ticket: p.Ticket, size: p.File.Size}
	defer src.Close()
	if _, e = src.Seek(0, io.SeekStart); e != nil {
		fail(w)
		return
	}
	copy := r.Clone(r.Context())
	copy.Body = src
	copy.ContentLength = p.File.Size
	n.upload(w, copy)
}
func (n *Daemon) upload(w http.ResponseWriter, r *http.Request) {
	p, e := n.permission(r.Context(), bearer(r), "source", "", true)
	if e != nil || (p.Action != "upload" && p.Action != "move_write") || !p.Storage {
		fail(w)
		return
	}
	unlock, e := n.lockObject(r.Context(), p.File.ID)
	if e != nil {
		fail(w)
		return
	}
	defer unlock()
	if v, e := n.Files.ObjectMetadata(p.File.ID); e == nil {
		send(w, 200, v)
		return
	}
	// A failed attempt may retry the same immutable operation ID. Available and
	// verified states are never reset; recovery completes verified uploads first.
	if _, e = n.DB.DB.Exec("DELETE FROM uploads WHERE id=? AND state='failed'", p.File.ID); e != nil {
		fail(w)
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	done := make(chan struct{})
	defer close(done)
	go func() {
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		next := n.Now().Add(Renew)
		for {
			select {
			case <-done:
				return
			case <-ctx.Done():
				return
			case <-ticker.C:
				if !n.valid() {
					cancel()
					_ = r.Body.Close()
					return
				}
				if !n.Now().Before(next) {
					if _, e := n.permission(ctx, bearer(r), "source", "", false); e != nil {
						cancel()
						_ = r.Body.Close()
						return
					}
					next = n.Now().Add(Renew)
				}
			}
		}
	}()
	stream, e := n.Meter.BeginReceived("panel", "node_upload", -1)
	if e != nil {
		fail(w)
		return
	}
	defer stream.Close(false)
	upload := n.Files.UploadID
	expected := int64(-1)
	if p.Action == "move_write" {
		upload = n.Files.UploadStaged
		expected = p.File.Size
		if e = n.Files.CheckSpace(uint64(expected)); e != nil {
			fail(w)
			return
		}
	}
	v, e := upload(ctx, p.File.ID, p.File.Name, expected, &sentReader{source: r.Body, stream: stream}, func() error { _, e := n.permission(ctx, bearer(r), "source", "", false); return e })
	if e != nil {
		fail(w)
		return
	}
	send(w, 201, v)
}
func (n *Daemon) metadata(w http.ResponseWriter, r *http.Request) {
	p, e := n.permission(r.Context(), bearer(r), "source", "", true)
	if e != nil || (p.Action != "upload" && p.Action != "move_write") {
		fail(w)
		return
	}
	unlock, e := n.lockObject(r.Context(), p.File.ID)
	if e != nil {
		fail(w)
		return
	}
	defer unlock()
	v, e := n.Files.ObjectMetadata(p.File.ID)
	if e != nil {
		if errors.Is(e, sql.ErrNoRows) {
			send(w, http.StatusNotFound, map[string]string{"error": "对象尚未提交"})
		} else {
			fail(w)
		}
		return
	}
	if p.Action == "move_write" {
		if n.Files.VerifyObject(p.File.ID, p.File.SHA256, p.File.Size) != nil {
			fail(w)
			return
		}
	}
	send(w, 200, v)
}
func (n *Daemon) remove(w http.ResponseWriter, r *http.Request) {
	p, e := n.permission(r.Context(), bearer(r), "source", "", true)
	if e != nil || (p.Action != "delete" && p.Action != "move_clean") {
		fail(w)
		return
	}
	obj := p.ObjectID
	if obj == "" {
		obj = p.File.ID
	}
	unlock, e := n.lockObject(r.Context(), obj)
	if e != nil {
		fail(w)
		return
	}
	defer unlock()
	if _, e = n.permission(r.Context(), bearer(r), "source", "", true); e != nil {
		fail(w)
		return
	}
	if p.Action == "move_clean" {
		if e = n.Files.CleanupObject(obj, p.File.SHA256, true); e != nil {
			fail(w)
			return
		}
		send(w, 200, map[string]bool{"deleted": true})
		return
	}
	if v, e := n.Files.ObjectMetadata(obj); e == nil && v.SHA256 != p.File.SHA256 {
		fail(w)
		return
	}
	n.Download.Revoke(p.File.ID)
	if e = n.Files.CleanupObject(obj, p.File.SHA256, true); e != nil {
		fail(w)
		return
	}
	send(w, 200, map[string]bool{"deleted": true})
}
func (n *Daemon) source(w http.ResponseWriter, r *http.Request) {
	n.serve(w, r, bearer(r), "source", "")
}
func (n *Daemon) relay(w http.ResponseWriter, r *http.Request) {
	cfg, e := n.Meter.Config()
	if e != nil {
		fail(w)
		return
	}
	n.serve(w, r, r.URL.Query().Get("t"), "relay", download.ClientIP(r, cfg))
}
func (n *Daemon) serve(w http.ResponseWriter, r *http.Request, ticket, role, ip string) {
	p, e := n.permission(r.Context(), ticket, role, ip, true)
	if e != nil || (p.Action != "download" && p.Action != "move_read") || (role == "relay" && r.PathValue("id") != p.File.ID) {
		fail(w)
		return
	}
	category := "node_source"
	if role == "source" {
		ip = p.VisitorIP
	}
	if role == "relay" {
		category = usage.CategoryPublic
	}
	req := download.Request{FileID: p.File.ID, SHA: p.File.SHA256, Name: p.File.Name, IP: ip, Category: category, Transit: role == "source", Revalidate: func() error {
		if !n.valid() {
			return ErrDenied
		}
		_, e := n.permission(r.Context(), ticket, role, ip, false)
		return e
	}}
	id, _ := n.connection()
	if role == "source" || (role == "relay" && p.File.StorageNode == id.ID) {
		obj := p.ObjectID
		if obj == "" {
			obj = p.File.ID
		}
		req.Source = func(context.Context) (files.Item, download.ReadSeekCloser, error) {
			v, f, e := n.Files.OpenObject(obj, p.File.SHA256)
			v.ID = p.File.ID
			v.Name = p.File.Name
			return v, f, e
		}
	}
	if role == "relay" && p.File.StorageNode != id.ID {
		if Endpoint(p.Source) != nil {
			fail(w)
			return
		}
		c, e := Client(p.SourceCA)
		if e != nil {
			fail(w)
			return
		}
		defer c.CloseIdleConnections()
		req.Source = func(ctx context.Context) (files.Item, download.ReadSeekCloser, error) {
			return p.File, &remoteReader{ctx: ctx, client: c, endpoint: p.Source, ticket: p.Ticket, size: p.File.Size, meter: n.Meter, ip: ip, category: "node_relay"}, nil
		}
	}
	if e = n.Download.Serve(w, r, req); e != nil {
		fail(w)
	}
}
