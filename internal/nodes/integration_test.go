package nodes

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"filepanel/internal/auth"
	"filepanel/internal/download"
	"filepanel/internal/files"
	"filepanel/internal/share"
	"filepanel/internal/store"
	"filepanel/internal/usage"
)

type harness struct {
	t       *testing.T
	p       *Panel
	server  *httptest.Server
	ca      string
	c       *http.Client
	session string
	handler atomic.Value
	offline atomic.Bool
}
type peer struct {
	n      *Daemon
	server *httptest.Server
	dir    string
}

func fixture(t *testing.T) *harness {
	t.Helper()
	dir := t.TempDir()
	db, e := store.Open(dir)
	must(t, e)
	t.Cleanup(func() { db.Close() })
	f, e := files.New(db, dir, 8<<20, 1)
	must(t, e)
	m, e := usage.New(db, nil)
	must(t, e)
	d, e := download.New(db, f, m)
	must(t, e)
	sh, e := share.New(db, f)
	must(t, e)
	a := auth.New(db)
	token, e := a.CreateInitToken()
	must(t, e)
	must(t, a.Initialize(token, "nodes-test-password"))
	session, _, e := a.Login(httptest.NewRequest("POST", "https://example.test/login", nil), "nodes-test-password")
	must(t, e)
	p, e := NewPanel(db, f, sh, a, m, d)
	must(t, e)
	h := &harness{t: t, p: p, session: session}
	mux := http.NewServeMux()
	p.Routes(mux)
	h.handler.Store(http.Handler(mux))
	h.server = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if h.offline.Load() && (r.URL.Path == prefix+"/check" || r.URL.Path == prefix+"/heartbeat") {
			fail(w)
			return
		}
		h.handler.Load().(http.Handler).ServeHTTP(w, r)
	}))
	t.Cleanup(h.server.Close)
	h.ca = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: h.server.Certificate().Raw}))
	h.c, e = Client(h.ca)
	must(t, e)
	t.Cleanup(h.c.CloseIdleConnections)
	must(t, p.ConfigureEndpoint(h.server.URL, h.ca))
	return h
}
func must(t *testing.T, e error) {
	t.Helper()
	if e != nil {
		t.Fatal(e)
	}
}
func (h *harness) peer(storage bool) *peer {
	h.t.Helper()
	t := h.t
	dir := t.TempDir()
	db, e := store.Open(dir)
	must(t, e)
	t.Cleanup(func() { db.Close() })
	f, e := files.New(db, dir, 8<<20, 1)
	must(t, e)
	m, e := usage.New(db, nil)
	must(t, e)
	d, e := download.New(db, f, m)
	must(t, e)
	var holder atomic.Value
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { holder.Load().(http.Handler).ServeHTTP(w, r) }))
	t.Cleanup(server.Close)
	ticket, e := h.p.NewTicket()
	must(t, e)
	must(t, Enroll(context.Background(), db, Identity{Panel: h.server.URL, PanelID: h.p.ID(), CA: h.ca}, ticket, server.URL, h.ca))
	n, e := NewDaemon(db, f, m, d)
	must(t, e)
	holder.Store(n.Handler())
	must(t, n.Heartbeat(context.Background()))
	cfg, e := h.p.Get(n.Identity.ID)
	must(t, e)
	cfg.Storage = storage
	must(t, h.p.Update(cfg.ID, cfg))
	must(t, n.Heartbeat(context.Background()))
	must(t, h.p.Probe(context.Background(), cfg.ID))
	return &peer{n, server, dir}
}
func (h *harness) file(storage *peer, body []byte) files.Item {
	h.t.Helper()
	var v files.Item
	var e error
	if storage == nil {
		v, e = h.p.Files.Upload(context.Background(), "payload.bin", int64(len(body)), bytes.NewReader(body))
	} else {
		v, e = h.p.Upload(context.Background(), storage.n.Identity.ID, "payload.bin", h.session, bytes.NewReader(body), func() error { return nil })
	}
	must(h.t, e)
	_, _, e = h.p.Shares.Update(v.ID, share.Input{Enabled: true, Featured: true})
	must(h.t, e)
	return v
}
func (h *harness) get(v files.Item, relay *peer, rng string) ([]byte, int) {
	h.t.Helper()
	sh, e := h.p.Shares.Get(v.ID)
	must(h.t, e)
	relayID := ""
	if relay != nil {
		relayID = relay.n.Identity.ID
	}
	tr, e := h.p.NewDownload(context.Background(), v.ID, relayID, "127.0.0.1", "", sh.AuthVersion)
	must(h.t, e)
	if relay != nil {
		req, e := http.NewRequest("GET", h.p.TicketURL(tr), nil)
		must(h.t, e)
		if rng != "" {
			req.Header.Set("Range", rng)
		}
		res, e := h.c.Do(req)
		must(h.t, e)
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		must(h.t, e)
		return b, res.StatusCode
	}
	req := httptest.NewRequest("GET", "https://panel.test/d/"+v.ID, nil)
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	rec := httptest.NewRecorder()
	r := download.Request{FileID: v.ID, SHA: v.SHA256, IP: "127.0.0.1", Category: usage.CategoryPublic, Revalidate: func() error { return h.p.authorize(tr, false) }}
	if tr.Source != "" {
		r.Source = h.p.RemoteSource(tr)
	}
	must(h.t, h.p.Download.Serve(rec, req, r))
	return rec.Body.Bytes(), rec.Code
}
func TestFourStorageRelayPathsAndUsage(t *testing.T) {
	for _, remote := range []bool{false, true} {
		for _, relayRemote := range []bool{false, true} {
			t.Run(fmt.Sprintf("storage_%v_relay_%v", remote, relayRemote), func(t *testing.T) {
				h := fixture(t)
				storage, relay := h.peer(true), h.peer(false)
				body := bytes.Repeat([]byte("hash-and-stream\n"), 8192)
				var source, out *peer
				if remote {
					source = storage
				}
				if relayRemote {
					out = relay
				}
				v := h.file(source, body)
				got, status := h.get(v, out, "")
				if status != 200 || !bytes.Equal(got, body) {
					t.Fatalf("full transfer status=%d len=%d", status, len(got))
				}
				sum := sha256.Sum256(got)
				if hex.EncodeToString(sum[:]) != v.SHA256 {
					t.Fatal("hash mismatch")
				}
				got, status = h.get(v, out, "bytes=-123")
				if status != 206 || !bytes.Equal(got, body[len(body)-123:]) {
					t.Fatal("tail Range mismatch")
				}
				st, e := h.p.Meter.Status()
				must(t, e)
				src, e := storage.n.Meter.Status()
				must(t, e)
				exit, e := relay.n.Meter.Status()
				must(t, e)
				if remote && src.Period.Received != int64(len(body)) {
					t.Fatalf("source received %d", src.Period.Received)
				}
				if relayRemote {
					if exit.Period.Sent != int64(len(body)+123) || exit.Period.Received < int64(len(body)+123) || exit.Period.Downloads != 1 {
						t.Fatalf("exit counters %+v", exit.Period)
					}
					if st.Period.Downloads != 0 {
						t.Fatal("panel source counted visitor download")
					}
				} else if st.Period.Downloads != 1 {
					t.Fatal("panel exit missing download")
				}
				if remote && src.Period.Downloads != 0 {
					t.Fatal("source counted visitor download")
				}
				must(t, storage.n.Heartbeat(context.Background()))
				today, e := storage.n.Meter.Today()
				must(t, e)
				if today.Sent != src.Period.Sent || today.Received != src.Period.Received {
					t.Fatalf("daily counters differ: %+v vs %+v", today, src.Period)
				}
				all, e := h.p.List()
				must(t, e)
				for _, node := range all {
					if node.ID == storage.n.Identity.ID && (node.Report == nil || node.Report.Today.Sent != today.Sent || node.Report.Today.Received != today.Received) {
						t.Fatalf("daily report missing: %+v", node.Report)
					}
				}
				node, e := h.p.Get(storage.n.Identity.ID)
				must(t, e)
				report := Report{Sequence: 999, Period: src.Period, Total: 100, Free: 50}
				in := Heartbeat{Nonce: random(), Report: report}
				must(t, call(context.Background(), h.c, "POST", h.server.URL, prefix+"/heartbeat", node.Credential, in, nil))
				must(t, call(context.Background(), h.c, "POST", h.server.URL, prefix+"/heartbeat", node.Credential, in, nil))
				var sent int64
				must(t, h.p.DB.DB.QueryRow("SELECT sent FROM node_reports WHERE node_id=? AND period=?", node.ID, src.Period.Start).Scan(&sent))
				if sent != src.Period.Sent {
					t.Fatal("duplicate report double counted")
				}
			})
		}
	}
}

func TestEnrollmentTrustReplayRevocationAndCapabilities(t *testing.T) {
	h := fixture(t)
	ticket, e := h.p.NewTicket()
	must(t, e)
	ctx := context.Background()
	newDB := func() *store.Store {
		db, e := store.Open(t.TempDir())
		must(t, e)
		t.Cleanup(func() { db.Close() })
		return db
	}
	if Enroll(ctx, newDB(), Identity{Panel: h.server.URL, PanelID: h.p.ID()}, ticket, "https://127.0.0.1:9999", h.ca) == nil {
		t.Fatal("untrusted TLS accepted")
	}
	db := newDB()
	must(t, Enroll(ctx, db, Identity{Panel: h.server.URL, PanelID: h.p.ID(), CA: h.ca}, ticket, "https://127.0.0.1:9999", h.ca))
	if Enroll(ctx, newDB(), Identity{Panel: h.server.URL, PanelID: h.p.ID(), CA: h.ca}, ticket, "https://127.0.0.1:9999", h.ca) == nil {
		t.Fatal("ticket replay accepted")
	}
	if Enroll(ctx, db, Identity{Panel: h.server.URL, PanelID: random(), CA: h.ca}, "x", "https://127.0.0.1:9999", h.ca) == nil {
		t.Fatal("existing identity overwritten")
	}
	ticket, e = h.p.NewTicket()
	must(t, e)
	if Enroll(ctx, newDB(), Identity{Panel: h.server.URL, PanelID: random(), CA: h.ca}, ticket, "https://127.0.0.1:9999", h.ca) == nil {
		t.Fatal("wrong panel identity accepted")
	}
	src, relay := h.peer(true), h.peer(false)
	body := []byte("retained unique remote file")
	v := h.file(src, body)
	node, e := h.p.Get(src.n.Identity.ID)
	must(t, e)
	node.Storage = false
	node.Relay = false
	must(t, h.p.Update(node.ID, node))
	must(t, src.n.Heartbeat(ctx))
	if _, e = h.p.Upload(ctx, node.ID, "blocked.txt", h.session, strings.NewReader("no"), func() error { return nil }); e == nil {
		t.Fatal("Storage off accepted new write")
	}
	got, _ := h.get(v, relay, "")
	if !bytes.Equal(got, body) {
		t.Fatal("capability off broke existing source")
	}
	if h.p.RemoveNode(node.ID) == nil {
		t.Fatal("unique file node removed")
	}
	must(t, h.p.Revoke(node.ID))
	if src.n.Heartbeat(ctx) == nil {
		t.Fatal("revoked credentials accepted")
	}
	sh, e := h.p.Shares.Get(v.ID)
	must(t, e)
	if _, e = h.p.NewDownload(ctx, v.ID, relay.n.Identity.ID, "127.0.0.1", "", sh.AuthVersion); e == nil {
		t.Fatal("revoked source authorized")
	}
	if _, r, e := src.n.Files.Open(v.ID); e != nil {
		t.Fatal("revocation deleted node file")
	} else {
		r.Close()
	}
}

type slowResponse struct {
	mu     sync.Mutex
	n      int
	first  chan struct{}
	once   sync.Once
	header http.Header
}

func (w *slowResponse) Header() http.Header { return w.header }
func (w *slowResponse) WriteHeader(int)     {}
func (w *slowResponse) Write(p []byte) (int, error) {
	w.once.Do(func() { close(w.first) })
	time.Sleep(5 * time.Millisecond)
	w.mu.Lock()
	w.n += len(p)
	w.mu.Unlock()
	return len(p), nil
}
func (w *slowResponse) SetWriteDeadline(time.Time) error { return nil }
func TestDisconnectStopsNewRangeAndActiveResponse(t *testing.T) {
	h := fixture(t)
	source, relay := h.peer(true), h.peer(false)
	body := bytes.Repeat([]byte("lease"), 400000)
	v := h.file(source, body)
	relay.n.Download.RenewEvery = 20 * time.Millisecond
	relay.n.Download.LeaseTTL = 100 * time.Millisecond
	relay.n.Download.Skew = 0
	sh, e := h.p.Shares.Get(v.ID)
	must(t, e)
	tr, e := h.p.NewDownload(context.Background(), v.ID, relay.n.Identity.ID, "127.0.0.1", "", sh.AuthVersion)
	must(t, e)
	u := h.p.TicketURL(tr)
	req := httptest.NewRequest("GET", u, nil)
	req.RemoteAddr = "127.0.0.1:3456"
	w := &slowResponse{first: make(chan struct{}), header: http.Header{}}
	done := make(chan struct{})
	go func() { relay.n.Handler().ServeHTTP(w, req); close(done) }()
	select {
	case <-w.first:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not begin")
	}
	h.offline.Store(true)
	start := time.Now()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("active response exceeded lease upper bound")
	}
	w.mu.Lock()
	count := w.n
	w.mu.Unlock()
	if count >= len(body) || time.Since(start) > time.Second {
		t.Fatal("offline transfer not stopped")
	}
	request, e := http.NewRequest("GET", u, nil)
	must(t, e)
	request.Header.Set("Range", "bytes=20-")
	res, e := h.c.Do(request)
	must(t, e)
	res.Body.Close()
	if res.StatusCode < 400 {
		t.Fatal("offline new Range accepted cached ticket")
	}
	h.offline.Store(false)
	must(t, source.n.Heartbeat(context.Background()))
	must(t, relay.n.Heartbeat(context.Background()))
	got, _ := h.get(v, relay, "")
	if !bytes.Equal(got, body) {
		t.Fatal("reconnection damaged file")
	}
	before, e := source.n.Meter.Status()
	must(t, e)
	restarted, e := usage.New(source.n.DB, nil)
	must(t, e)
	after, e := restarted.Status()
	must(t, e)
	if before.Period.Sent != after.Period.Sent || before.Period.Received != after.Period.Received {
		t.Fatal("usage lost on restart")
	}
}

func TestMigrationAfterOldPanelStops(t *testing.T) {
	old := fixture(t)
	source, relay := old.peer(true), old.peer(false)
	body := []byte("survives the old panel uninstall")
	v := old.file(source, body)
	original, e := old.p.Shares.Get(v.ID)
	must(t, e)
	must(t, old.p.SetSettings(Settings{DefaultRelay: relay.n.Identity.ID, Fallback: true}))
	password := "separate-migration-password"
	archive, e := old.p.ExportMigration(password)
	must(t, e)
	if bytes.Contains(archive, []byte(source.n.Identity.Credential)) || bytes.Contains(archive, []byte(old.p.ID())) {
		t.Fatal("migration leaked plaintext identity")
	}
	next := fixture(t)
	if _, e = next.p.ImportMigration(archive, "wrong-password", next.server.URL, next.ca, false); e == nil {
		t.Fatal("bad password accepted")
	}
	bad := append([]byte(nil), archive...)
	bad[len(bad)/2] ^= 1
	if _, e = next.p.ImportMigration(bad, password, next.server.URL, next.ca, false); e == nil {
		t.Fatal("damaged package accepted")
	}
	summary, e := next.p.ImportMigration(archive, password, next.server.URL, next.ca, false)
	must(t, e)
	if summary.Nodes != 2 || summary.Files != 1 || next.p.ID() == old.p.ID() {
		t.Fatal("preview mutated identity or wrong counts")
	}
	old.server.Close()
	_, e = next.p.ImportMigration(archive, password, next.server.URL, next.ca, true)
	must(t, e)
	if next.p.ID() != old.p.ID() {
		t.Fatal("identity not restored")
	}
	must(t, next.p.Rebind(context.Background(), source.n.Identity.ID))
	must(t, next.p.Rebind(context.Background(), relay.n.Identity.ID))
	must(t, source.n.Heartbeat(context.Background()))
	must(t, relay.n.Heartbeat(context.Background()))
	must(t, next.p.Probe(context.Background(), source.n.Identity.ID))
	must(t, next.p.Probe(context.Background(), relay.n.Identity.ID))
	got, _ := next.get(v, relay, "")
	if !bytes.Equal(got, body) {
		t.Fatal("migrated transfer corrupted")
	}
	restored, e := next.p.Shares.Get(v.ID)
	must(t, e)
	if restored.Slug != original.Slug {
		t.Fatal("migration changed slug")
	}
	if source.n.Identity.Panel != next.server.URL {
		t.Fatal("node did not persist new panel address")
	}
	restarted, e := NewDaemon(source.n.DB, source.n.Files, source.n.Meter, source.n.Download)
	must(t, e)
	if restarted.Identity.Panel != next.server.URL || restarted.valid() {
		t.Fatal("bad persisted rebind or restart reused lease")
	}
	if _, e = next.p.ImportMigration(archive, password, next.server.URL, next.ca, true); e == nil {
		t.Fatal("nonempty panel overwritten")
	}
}

func TestRemoteDeleteRecoveryAndRejectedUpload(t *testing.T) {
	h := fixture(t)
	source := h.peer(true)
	if _, e := h.p.Upload(context.Background(), source.n.Identity.ID, "invalid.bin", h.session, strings.NewReader("rejected"), func() error { return fmt.Errorf("multipart invalid") }); e == nil {
		t.Fatal("rejected enclosing request committed")
	}
	list, e := source.n.Files.List()
	must(t, e)
	if len(list) != 0 {
		t.Fatal("rejected upload became available")
	}
	v := h.file(source, []byte("delete once"))
	h.offline.Store(true)
	if h.p.Delete(context.Background(), v.ID) == nil {
		t.Fatal("delete unexpectedly succeeded offline")
	}
	_, e = files.New(h.p.DB, h.p.Files.DataDir(), 8<<20, 1)
	must(t, e)
	var count int
	must(t, h.p.DB.DB.QueryRow("SELECT COUNT(*) FROM node_locations WHERE file_id=?", v.ID).Scan(&count))
	if count != 1 {
		t.Fatal("local recovery discarded offline unique file")
	}
	h.offline.Store(false)
	h.p.Reconcile(context.Background())
	if _, e = source.n.Files.Metadata(v.ID); e == nil {
		t.Fatal("retry did not delete source")
	}
	must(t, h.p.Delete(context.Background(), v.ID))
	if _, e = h.p.Files.Metadata(v.ID); e == nil {
		t.Fatal("delete retained published record")
	}
}
