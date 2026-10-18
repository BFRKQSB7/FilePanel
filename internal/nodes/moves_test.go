package nodes

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"filepanel/internal/auth"
	"filepanel/internal/download"
	"filepanel/internal/files"
	"filepanel/internal/share"
	"filepanel/internal/store"
	"filepanel/internal/usage"
)

func runTask(t *testing.T, h *harness, m Move) Move {
	t.Helper()
	h.p.ReconcileMoves(context.Background())
	got, e := h.p.Move(m.ID)
	must(t, e)
	return got
}
func retryTask(t *testing.T, h *harness, m Move) Move {
	t.Helper()
	must(t, h.p.RetryMove(m.ID))
	return runTask(t, h, m)
}
func reopenMovePanel(t *testing.T, h *harness) {
	t.Helper()
	root := h.p.Files.DataDir()
	must(t, h.p.DB.Close())
	db, e := store.Open(root)
	must(t, e)
	t.Cleanup(func() { db.Close() })
	f, e := files.New(db, root, 8<<20, 1)
	must(t, e)
	meter, e := usage.New(db, nil)
	must(t, e)
	dl, e := download.New(db, f, meter)
	must(t, e)
	sh, e := share.New(db, f)
	must(t, e)
	p, e := NewPanel(db, f, sh, auth.New(db), meter, dl)
	must(t, e)
	h.p = p
	mux := http.NewServeMux()
	p.Routes(mux)
	h.handler.Store(http.Handler(mux))
}
func TestMoveRoundTripStableIdentity(t *testing.T) {
	h := fixture(t)
	a, b := h.peer(true), h.peer(true)
	data := bytes.Repeat([]byte("immutable move\n"), 8192)
	v := h.file(nil, data)
	original, e := h.p.Shares.Get(v.ID)
	must(t, e)
	for _, target := range []string{a.n.Identity.ID, b.n.Identity.ID, "", a.n.Identity.ID, ""} {
		m, e := h.p.StartMove(context.Background(), random(), v.ID, target)
		must(t, e)
		got := runTask(t, h, m)
		if got.State != "done" {
			t.Fatalf("%+v", got)
		}
		meta, e := h.p.Files.Metadata(v.ID)
		must(t, e)
		if meta.StorageNode != target || meta.SHA256 != v.SHA256 {
			t.Fatal("identity changed")
		}
		sh, e := h.p.Shares.Get(v.ID)
		must(t, e)
		if sh.Slug != original.Slug || sh.AuthVersion != original.AuthVersion || sh.Version != original.Version {
			t.Fatal("share changed")
		}
		body, status := h.get(v, nil, "")
		if status != 200 || !bytes.Equal(body, data) {
			t.Fatal("panel hash mismatch")
		}
		body, status = h.get(v, b, "")
		if status != 200 || !bytes.Equal(body, data) {
			t.Fatal("relay hash mismatch")
		}
		// These reads own durable remote leases. Expire only after all actual streams
		// have completed, before moving this object again in the test.
		_, e = h.p.DB.DB.Exec("UPDATE node_transfer_leases SET until=0")
		must(t, e)
		items, e := h.p.Files.List()
		must(t, e)
		if len(items) != 1 || items[0].ID != v.ID {
			t.Fatal("staged target became public")
		}
		must(t, h.p.Files.Recover())
		must(t, a.n.Files.Recover())
		must(t, b.n.Files.Recover())
	}
	must(t, h.p.Delete(context.Background(), v.ID))
}
func TestMoveFaultsRestartAndRetry(t *testing.T) {
	for _, stage := range []string{"during_transfer", "after_transfer", "after_verify", "before_switch", "after_switch", "cleanup"} {
		for _, remote := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s_remote_%v", stage, remote), func(t *testing.T) {
				h := fixture(t)
				a, b := h.peer(true), h.peer(true)
				data := bytes.Repeat([]byte("survives crash"), 3000)
				var src *peer
				if remote {
					src = a
				}
				v := h.file(src, data)
				m, e := h.p.StartMove(context.Background(), random(), v.ID, b.n.Identity.ID)
				must(t, e)
				hit := false
				h.p.MoveFault = func(point string) error {
					if point == stage {
						hit = true
						return errors.New("injected crash")
					}
					return nil
				}
				got := runTask(t, h, m)
				if !hit || got.Error == "" {
					t.Fatalf("fault not hit %+v", got)
				}
				body, status := h.get(v, nil, "")
				if status != 200 || !bytes.Equal(body, data) {
					t.Fatal("lost complete active copy")
				}
				// Recover storage and replace the panel object to drop all process state.
				must(t, h.p.Files.Recover())
				must(t, a.n.Files.Recover())
				must(t, b.n.Files.Recover())
				reopenMovePanel(t, h)
				_, e = h.p.DB.DB.Exec("UPDATE node_transfer_leases SET until=0")
				must(t, e)
				got = retryTask(t, h, m)
				if got.State != "done" {
					t.Fatalf("recovery failed %+v", got)
				}
				body, status = h.get(v, nil, "")
				if status != 200 || !bytes.Equal(body, data) {
					t.Fatal("recovery hash mismatch")
				}
			})
		}
	}
}
func TestMoveFencingCancelAndDuplicate(t *testing.T) {
	h := fixture(t)
	a := h.peer(true)
	v := h.file(nil, []byte("unique source"))
	key := random()
	m, e := h.p.StartMove(context.Background(), key, v.ID, a.n.Identity.ID)
	must(t, e)
	duplicate, e := h.p.StartMove(context.Background(), key, v.ID, a.n.Identity.ID)
	must(t, e)
	if duplicate.ID != m.ID {
		t.Fatal("duplicate task")
	}
	if _, e = h.p.StartMove(context.Background(), random(), v.ID, a.n.Identity.ID); e == nil {
		t.Fatal("parallel move accepted")
	}
	if e = h.p.Delete(context.Background(), v.ID); e == nil {
		t.Fatal("delete raced move")
	}
	m.Fence = 1
	_, e = h.p.DB.DB.Exec("UPDATE file_moves SET fence=1,state='transferring' WHERE id=?", m.ID)
	must(t, e)
	ticket, e := h.p.operation(m, "move_write", a.n.Identity.ID, m.TargetObject)
	must(t, e)
	must(t, h.p.CancelMove(m.ID))
	if e = h.p.switchMove(m); e == nil {
		t.Fatal("stale worker switched")
	}
	req, e := http.NewRequest("PUT", a.server.URL+prefix+"/object", strings.NewReader("late worker"))
	must(t, e)
	req.Header.Set("Authorization", "Bearer "+ticket)
	res, e := h.c.Do(req)
	must(t, e)
	res.Body.Close()
	if res.StatusCode < 400 {
		t.Fatal("stale write authorized")
	}
	got := runTask(t, h, m)
	if got.State != "cancelled" {
		t.Fatalf("%+v", got)
	}
	body, status := h.get(v, nil, "")
	if status != 200 || string(body) != "unique source" {
		t.Fatal("cancel removed source")
	}
}
func TestMoveReaderAndCleanupRetry(t *testing.T) {
	h := fixture(t)
	a, b := h.peer(true), h.peer(true)
	v := h.file(a, []byte("reader lease"))
	sh, e := h.p.Shares.Get(v.ID)
	must(t, e)
	tr, e := h.p.NewDownload(context.Background(), v.ID, "", "127.0.0.1", "", sh.AuthVersion)
	must(t, e)
	// Reserve a real source permit, as a streaming response does.
	_, e = h.p.permit(moveNode(t, h, a), Check{Transfer: tr.ID, Role: "source", Start: true})
	must(t, e)
	_, reader, e := a.n.Files.OpenObject(v.ID, v.SHA256)
	must(t, e)
	m, e := h.p.StartMove(context.Background(), random(), v.ID, b.n.Identity.ID)
	must(t, e)
	got := runTask(t, h, m)
	if got.State != "cleaning" || got.Error == "" {
		t.Fatalf("reader not respected %+v", got)
	}
	if _, e = a.n.Files.ObjectMetadata(v.ID); e != nil {
		t.Fatal("source removed with lease")
	}
	// Renewal after switch remains valid; starting a new old-source session fails.
	_, e = h.p.permit(moveNode(t, h, a), Check{Transfer: tr.ID, Role: "source", Start: false})
	must(t, e)
	if _, e = h.p.permit(moveNode(t, h, a), Check{Transfer: tr.ID, Role: "source", Start: true}); e == nil {
		t.Fatal("new old-source session allowed")
	}
	if e = h.p.CancelMove(m.ID); e == nil {
		t.Fatal("post-switch cancel accepted")
	}
	must(t, reader.Close())
	_, e = h.p.DB.DB.Exec("UPDATE node_transfer_leases SET until=0")
	must(t, e)
	got = retryTask(t, h, m)
	if got.State != "done" {
		t.Fatalf("%+v", got)
	}
	if _, e = a.n.Files.ObjectMetadata(v.ID); e == nil {
		t.Fatal("source not retired")
	}
}
func moveNode(t *testing.T, h *harness, a *peer) Node {
	t.Helper()
	n, e := h.p.Get(a.n.Identity.ID)
	must(t, e)
	return n
}
func TestMoveLocalReaderCleanupAndLowSpace(t *testing.T) {
	h := fixture(t)
	a := h.peer(true)
	v := h.file(nil, []byte("held local source"))
	_, r, e := h.p.Files.Open(v.ID)
	must(t, e)
	m, e := h.p.StartMove(context.Background(), random(), v.ID, a.n.Identity.ID)
	must(t, e)
	got := runTask(t, h, m)
	if got.State != "cleaning" {
		t.Fatalf("%+v", got)
	}
	b, e := io.ReadAll(r)
	must(t, e)
	if string(b) != "held local source" {
		t.Fatal("read broken")
	}
	must(t, r.Close())
	got = retryTask(t, h, m)
	if got.State != "done" {
		t.Fatalf("%+v", got)
	}
	// A nonzero impossible reserve forces controlled preflight rejection.
	limited, e := files.New(h.p.DB, h.p.Files.DataDir(), 8<<20, ^uint64(0)-1)
	must(t, e)
	h.p.Files = limited
	m, e = h.p.StartMove(context.Background(), random(), v.ID, "")
	must(t, e)
	got = runTask(t, h, m)
	if got.State != "preflight" || got.Error == "" {
		t.Fatalf("low space not refused %+v", got)
	}
	body, status := h.get(v, nil, "")
	if status != 200 || string(body) != "held local source" {
		t.Fatal("low space lost source")
	}
}
func TestMoveConcurrentStartDeleteAndStaleSwitch(t *testing.T) {
	h := fixture(t)
	a := h.peer(true)
	v := h.file(nil, bytes.Repeat([]byte("concurrency"), 2048))
	var wg sync.WaitGroup
	wg.Add(2)
	var m Move
	var startErr, deleteErr error
	go func() {
		defer wg.Done()
		m, startErr = h.p.StartMove(context.Background(), random(), v.ID, a.n.Identity.ID)
	}()
	go func() { defer wg.Done(); deleteErr = h.p.Delete(context.Background(), v.ID) }()
	wg.Wait()
	if startErr == nil && deleteErr == nil {
		t.Fatal("both move and delete succeeded")
	}
	if startErr == nil {
		got := runTask(t, h, m)
		if got.State != "done" {
			t.Fatalf("%+v", got)
		}
	}
}
func TestMoveDirectFallbackAndCorruptTarget(t *testing.T) {
	h := fixture(t)
	a, b := h.peer(true), h.peer(true)
	v := h.file(a, []byte("verified only"))
	// Advertise a source CA that the target cannot parse. Panel uses its cached,
	// already verified source client; the fallback remains a real TLS stream.
	_, e := h.p.client(moveNode(t, h, a))
	must(t, e)
	_, e = h.p.DB.DB.Exec("UPDATE nodes SET ca='invalid CA' WHERE id=?", a.n.Identity.ID)
	must(t, e)
	m, e := h.p.StartMove(context.Background(), random(), v.ID, b.n.Identity.ID)
	must(t, e)
	got := runTask(t, h, m)
	if got.State != "done" || got.Route != "panel" {
		t.Fatalf("fallback %+v", got)
	}
	// Corrupt a staged local target after transfer; verification cannot switch.
	m, e = h.p.StartMove(context.Background(), random(), v.ID, "")
	must(t, e)
	h.p.MoveFault = func(stage string) error {
		if stage == "after_transfer" {
			return os.WriteFile(filepath.Join(h.p.Files.DataDir(), "objects", "data", m.TargetObject), []byte("corrupt"), 0600)
		}
		return nil
	}
	got = runTask(t, h, m)
	if got.State != "verifying" || got.Error == "" {
		t.Fatalf("corruption switched %+v", got)
	}
	meta, e := h.p.Files.Metadata(v.ID)
	must(t, e)
	if meta.StorageNode != b.n.Identity.ID {
		t.Fatal("corrupt target active")
	}
}
func TestMoveQuotaRefusalRetainsSource(t *testing.T) {
	h := fixture(t)
	a := h.peer(true)
	v := h.file(nil, bytes.Repeat([]byte("quota"), 20000))
	cfg, e := a.n.Meter.Config()
	must(t, e)
	cfg.QuotaBytes = 1024
	cfg.Protect = true
	cfg.Basis = "total"

	n := moveNode(t, h, a)
	n.Policy = cfg
	must(t, h.p.Update(n.ID, n))
	must(t, a.n.Heartbeat(context.Background()))
	m, e := h.p.StartMove(context.Background(), random(), v.ID, a.n.Identity.ID)
	must(t, e)
	got := runTask(t, h, m)
	if got.State == "done" || got.Error == "" {
		t.Fatalf("quota ignored %+v", got)
	}
	if _, e = h.p.Files.Metadata(v.ID); e != nil {
		t.Fatal("source lost")
	}
	// Give back only unconfirmed grants; confirmed bytes remain charged.
	st, e := a.n.Meter.Status()
	must(t, e)
	if st.Period.Received > 1024 {
		t.Fatal("budget exceeded")
	}
}

func TestMoveCancelVerifiedTargetAndRetryBudget(t *testing.T) {
	for _, local := range []bool{false, true} {
		t.Run(fmt.Sprintf("local_target_%v", local), func(t *testing.T) {
			h := fixture(t)
			a, b := h.peer(true), h.peer(true)
			v := h.file(a, bytes.Repeat([]byte("cancel verified target"), 4096))
			target := b.n.Identity.ID
			if local {
				target = ""
			}
			m, e := h.p.StartMove(context.Background(), random(), v.ID, target)
			must(t, e)
			h.p.MoveFault = func(stage string) error {
				if stage == "after_verify" {
					return errors.New("pause before switch")
				}
				return nil
			}
			got := runTask(t, h, m)
			if got.State != "verifying" {
				t.Fatalf("%+v", got)
			}
			before, e := b.n.Meter.Status()
			must(t, e)
			// The retry reuses the immutable, verified target without reserving and
			// transferring the same payload a second time.
			got = retryTask(t, h, m)
			if got.State != "verifying" {
				t.Fatalf("%+v", got)
			}
			after, e := b.n.Meter.Status()
			must(t, e)
			if before.Period.Received != after.Period.Received {
				t.Fatal("verified retry transferred again")
			}
			must(t, h.p.CancelMove(m.ID))
			got = runTask(t, h, m)
			if got.State != "cancelled" {
				t.Fatalf("%+v", got)
			}
			manager := b.n.Files
			if local {
				manager = h.p.Files
			}
			if _, e = manager.ObjectMetadata(m.TargetObject); e == nil {
				t.Fatal("cancel left a committed temporary target")
			}
			if _, e = a.n.Files.ObjectMetadata(v.ID); e != nil {
				t.Fatal("cancel lost original source")
			}
		})
	}
}

func TestDeleteMovedRemotePhysicalObject(t *testing.T) {
	h := fixture(t)
	a := h.peer(true)
	v := h.file(nil, []byte("delete the current physical object"))
	task, e := h.p.StartMove(context.Background(), random(), v.ID, a.n.Identity.ID)
	must(t, e)
	task = runTask(t, h, task)
	if task.State != "done" {
		t.Fatalf("%+v", task)
	}
	must(t, h.p.Delete(context.Background(), v.ID))
	if _, e = a.n.Files.ObjectMetadata(task.TargetObject); e == nil {
		t.Fatal("moved object leaked after deletion")
	}
	if _, e = h.p.Files.Metadata(v.ID); e == nil {
		t.Fatal("deleted file stayed available")
	}
}

func TestMoveVerificationUsesDataDeadline(t *testing.T) {
	h := fixture(t)
	a := h.peer(true)
	v := h.file(nil, []byte("slow disk verification"))
	proxy := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "GET" && r.URL.Path == prefix+"/object" {
			time.Sleep(75 * time.Millisecond)
		}
		a.n.Handler().ServeHTTP(w, r)
	}))
	defer proxy.Close()
	_, e := h.p.DB.DB.Exec("UPDATE nodes SET endpoint=? WHERE id=?", proxy.URL, a.n.Identity.ID)
	must(t, e)
	c, e := h.p.client(moveNode(t, h, a))
	must(t, e)
	c.Transport.(*http.Transport).ResponseHeaderTimeout = 20 * time.Millisecond
	task, e := h.p.StartMove(context.Background(), random(), v.ID, a.n.Identity.ID)
	must(t, e)
	got := runTask(t, h, task)
	if got.State != "done" {
		t.Fatalf("data verification inherited control timeout %+v", got)
	}
}
