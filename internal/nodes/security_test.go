package nodes

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"filepanel/internal/share"
	"filepanel/internal/usage"
)

func TestEachHopQuotaStopsActiveTransfer(t *testing.T) {
	for _, hop := range []string{"source", "relay", "panel"} {
		t.Run(hop, func(t *testing.T) {
			h := fixture(t)
			src, relay := h.peer(true), h.peer(false)
			body := bytes.Repeat([]byte("budget"), 20000)
			v := h.file(src, body)
			meter := src.n.Meter
			if hop == "relay" {
				meter = relay.n.Meter
			} else if hop == "panel" {
				meter = h.p.Meter
			}
			cfg := usage.DefaultConfig()
			before, e := meter.Status()
			must(t, e)
			cfg.QuotaBytes = before.Period.Sent + 20000
			cfg.IPPercent = 100
			must(t, meter.SetConfig(cfg))
			if hop != "panel" {
				n := src
				if hop == "relay" {
					n = relay
				}
				row, e := h.p.Get(n.n.Identity.ID)
				must(t, e)
				row.Policy = cfg
				must(t, h.p.Update(row.ID, row))
				must(t, n.n.Heartbeat(context.Background()))
			}
			sh, e := h.p.Shares.Get(v.ID)
			must(t, e)
			relayID := relay.n.Identity.ID
			if hop == "panel" {
				relayID = ""
			}
			tr, e := h.p.NewDownload(context.Background(), v.ID, relayID, "127.0.0.1", "", sh.AuthVersion)
			must(t, e)
			if hop == "panel" {
				req, e := h.p.SourceRequest(context.Background(), v.ID, "127.0.0.1", "", sh.AuthVersion)
				must(t, e)
				w := &quotaResponse{header: http.Header{}}
				r, _ := http.NewRequest("GET", "https://panel/d/file", nil)
				_ = h.p.Download.Serve(w, r, req)
				if w.n >= len(body) {
					t.Fatal("panel quota ignored")
				}
			} else {
				res, e := h.c.Get(h.p.TicketURL(tr))
				must(t, e)
				got, _ := io.ReadAll(res.Body)
				res.Body.Close()
				if len(got) >= len(body) {
					t.Fatal("hop quota ignored")
				}
			}
			st, e := meter.Status()
			must(t, e)
			if st.Period.Sent > cfg.Limit() {
				t.Fatalf("sent %d > limit %d", st.Period.Sent, cfg.Limit())
			}
		})
	}
}

type quotaResponse struct {
	header http.Header
	n      int
}

func (w *quotaResponse) Header() http.Header         { return w.header }
func (w *quotaResponse) WriteHeader(int)             {}
func (w *quotaResponse) Write(b []byte) (int, error) { w.n += len(b); return len(b), nil }

func TestTicketLifetimeFilePermissionAndClockLease(t *testing.T) {
	h := fixture(t)
	src, relay := h.peer(true), h.peer(false)
	v := h.file(src, []byte("private-bound-file"))
	sh, e := h.p.Shares.Get(v.ID)
	must(t, e)
	tr, e := h.p.NewDownload(context.Background(), v.ID, relay.n.Identity.ID, "127.0.0.1", "", sh.AuthVersion)
	must(t, e)
	u := h.p.TicketURL(tr)
	wrong := strings.Replace(u, "/d/"+v.ID, "/d/unrelated-file", 1)
	res, e := h.c.Get(wrong)
	must(t, e)
	res.Body.Close()
	if res.StatusCode < 400 {
		t.Fatal("file-scoped ticket crossed file boundary")
	}
	req, _ := http.NewRequest("GET", u, nil)
	req.Header.Set("X-Forwarded-For", "198.51.100.200")
	res, e = h.c.Do(req)
	must(t, e)
	res.Body.Close()
	if res.StatusCode != 200 {
		t.Fatal("untrusted forwarded header influenced visitor identity")
	}
	saved := h.p.Now
	h.p.Now = func() time.Time { return saved().Add(306 * time.Second) }
	if _, e = h.p.permit(mustNode(t, h.p, relay.n.Identity.ID), Check{Nonce: random(), Transfer: tr.ID, Role: "relay", IP: tr.IP, Start: true}); e == nil {
		t.Fatal("initial 300 second token accepted after skew")
	}
	h.p.Now = saved
	row, e := h.p.Get(src.n.Identity.ID)
	must(t, e)
	ctrl := h.p.control(row, random())
	must(t, src.n.accept(ctrl, ctrl.Nonce))
	base := src.n.Now()
	src.n.Now = func() time.Time { return base.Add(36 * time.Second) }
	if src.n.valid() {
		t.Fatal("control lease exceeded TTL and skew")
	}
	if src.n.accept(ctrl, random()) == nil {
		t.Fatal("replayed control reply accepted")
	}
	src.n.Now = time.Now
	_, _, e = h.p.Shares.Update(v.ID, share.Input{Enabled: false})
	must(t, e)
	if h.p.authorize(tr, false) == nil {
		t.Fatal("revoked share retained active permission")
	}
}
func mustNode(t *testing.T, p *Panel, id string) Node {
	t.Helper()
	n, e := p.Get(id)
	must(t, e)
	return n
}

func TestRegionCacheAndManualOverride(t *testing.T) {
	h := fixture(t)
	n := h.peer(false)
	_, e := h.p.DB.DB.Exec("UPDATE nodes SET public_ip='8.8.8.8' WHERE id=?", n.n.Identity.ID)
	must(t, e)
	var calls atomic.Int32
	h.p.GeoLookup = func(context.Context, string) (string, error) { calls.Add(1); return "自动地区", nil }
	must(t, h.p.RefreshRegion(context.Background(), n.n.Identity.ID))
	must(t, h.p.RefreshRegion(context.Background(), n.n.Identity.ID))
	if calls.Load() != 1 {
		t.Fatal("region cache bypassed")
	}
	cfg := mustNode(t, h.p, n.n.Identity.ID)
	cfg.Region = "手动地区"
	cfg.ManualRegion = true
	must(t, h.p.Update(cfg.ID, cfg))
	h.p.GeoLookup = func(context.Context, string) (string, error) { return "", errors.New("offline") }
	must(t, h.p.RefreshRegion(context.Background(), cfg.ID))
	if got := mustNode(t, h.p, cfg.ID); got.Region != "手动地区" {
		t.Fatal("manual region overwritten")
	}
}

func TestWrongNewPanelAndFailedAddressMigrationRetainIdentity(t *testing.T) {
	h := fixture(t)
	src := h.peer(true)
	v := h.file(src, []byte("keep file"))
	other := fixture(t)
	original := src.n.Identity
	must(t, h.p.ConfigureEndpoint(other.server.URL, other.ca))
	if h.p.Rebind(context.Background(), original.ID) == nil {
		t.Fatal("different panel identity accepted during address change")
	}
	if src.n.Identity.Panel != original.Panel || src.n.Identity.PanelID != original.PanelID {
		t.Fatal("failed rebind changed identity")
	}
	saved := h.handler.Load()
	h.handler.Store(other.handler.Load())
	if src.n.Heartbeat(context.Background()) == nil {
		t.Fatal("same-address new panel adopted node")
	}
	h.handler.Store(saved)
	must(t, h.p.ConfigureEndpoint(h.server.URL, h.ca))
	must(t, src.n.Heartbeat(context.Background()))
	if _, f, e := src.n.Files.Open(v.ID); e != nil {
		t.Fatal("identity failure lost file")
	} else {
		f.Close()
	}
	ticket, e := h.p.NewTicket()
	must(t, e)
	if Enroll(context.Background(), h.p.DB, Identity{Panel: h.server.URL, PanelID: h.p.ID(), CA: h.ca}, ticket, src.server.URL, h.ca) == nil {
		t.Fatal("panel database reused as a node")
	}
}

func TestMissingValidatedUploadRecoversWithoutPublishing(t *testing.T) {
	h := fixture(t)
	src := h.peer(true)
	tr := Transfer{ID: random(), Action: "upload", File: random(), Source: src.n.Identity.ID, Name: "not-committed.bin", Session: h.session, Expires: time.Now().Add(time.Hour).Unix(), State: "validated"}
	must(t, h.p.save(tr))
	h.p.Reconcile(context.Background())
	got, e := h.p.transfer(tr.ID)
	must(t, e)
	if got.State != "failed" {
		t.Fatal("uncommitted validated transfer kept retrying forever")
	}
	if _, e = h.p.ExportMigration("separate-migration-password"); e != nil {
		t.Fatal("failed transfer blocked export", e)
	}
}

func TestObjectOperationLocksCancelAndAllowIndependentFiles(t *testing.T) {
	n := &Daemon{}
	unlock, e := n.lockObject(context.Background(), "first")
	must(t, e)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, e = n.lockObject(ctx, "first"); !errors.Is(e, context.Canceled) {
		t.Fatal("same object lock ignored cancellation")
	}
	second, e := n.lockObject(context.Background(), "second")
	must(t, e)
	second()
	unlock()
	if len(n.objects) != 0 {
		t.Fatal("object lock map leaked")
	}
}

func TestStorageNodeCanBeItsOwnExitWithoutLoopbackTraffic(t *testing.T) {
	h := fixture(t)
	src := h.peer(true)
	payload := []byte("one node stores and serves")
	v := h.file(src, payload)
	must(t, h.p.Files.Rename(v.ID, "renamed.bin"))
	got, status := h.get(v, src, "")
	if status != 200 || !bytes.Equal(got, payload) {
		t.Fatal("same node exit failed")
	}
	st, e := src.n.Meter.Status()
	must(t, e)
	if st.Period.Sent != int64(len(payload)) || st.Period.Received != int64(len(payload)) || st.Period.Downloads != 1 {
		t.Fatalf("self relay produced fictitious traffic: %+v", st.Period)
	}
}

func TestEmptyRevokedNodeRemovalClearsReportsAndDefault(t *testing.T) {
	h := fixture(t)
	relay := h.peer(false)
	id := relay.n.Identity.ID
	must(t, h.p.SetSettings(Settings{DefaultRelay: id, Fallback: true}))
	if h.p.RemoveNode(id) == nil {
		t.Fatal("active node removed")
	}
	must(t, h.p.Revoke(id))
	must(t, h.p.RemoveNode(id))
	if _, e := h.p.Get(id); e == nil {
		t.Fatal("revoked empty node retained")
	}
	cfg, e := h.p.Settings()
	must(t, e)
	if cfg.DefaultRelay != "" || !cfg.Fallback {
		t.Fatal("removed default relay left invalid settings")
	}
}
