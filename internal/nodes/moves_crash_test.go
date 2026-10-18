package nodes

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"filepanel/internal/auth"
	"filepanel/internal/download"
	"filepanel/internal/files"
	"filepanel/internal/share"
	"filepanel/internal/store"
	"filepanel/internal/usage"
)

// The child is terminated inside the real worker, including inside an HTTP
// source stream. No defer, database close, lease release or error handler runs.
func TestMoveCrashProcess(t *testing.T) {
	root := os.Getenv("FILEPANEL_CRASH_ROOT")
	if root == "" {
		t.Skip("subprocess helper")
	}
	db, e := store.Open(root)
	must(t, e)
	defer db.Close()
	f, e := files.New(db, root, 8<<20, 1)
	must(t, e)
	m, e := usage.New(db, nil)
	must(t, e)
	d, e := download.New(db, f, m)
	must(t, e)
	sh, e := share.New(db, f)
	must(t, e)
	p, e := NewPanel(db, f, sh, auth.New(db), m, d)
	must(t, e)
	mux := http.NewServeMux()
	p.Routes(mux)
	server := httptest.NewServer(mux)
	defer server.Close()
	signal := os.Getenv("FILEPANEL_CRASH_SIGNAL")
	must(t, os.WriteFile(signal+".ready", []byte(server.URL), 0600))
	deadline := time.Now().Add(10 * time.Second)
	for {
		if _, e = os.Stat(signal + ".start"); e == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("parent did not start crash worker")
		}
		time.Sleep(10 * time.Millisecond)
	}
	p.MoveFault = func(stage string) error {
		if stage == os.Getenv("FILEPANEL_CRASH_STAGE") {
			os.Exit(86)
		}
		return nil
	}
	p.ReconcileMoves(context.Background())
	t.Fatal("worker did not reach requested crash point")
}

func TestMoveProcessCrashRecovery(t *testing.T) {
	for _, stage := range []string{"during_transfer", "after_transfer", "after_verify", "before_switch", "after_switch", "cleanup"} {
		t.Run(stage, func(t *testing.T) {
			h := fixture(t)
			target := h.peer(true)
			payload := bytes.Repeat([]byte("survives ungraceful exit\n"), 16384)
			item := h.file(nil, payload)
			task, e := h.p.StartMove(context.Background(), random(), item.ID, target.n.Identity.ID)
			must(t, e)
			root := h.p.Files.DataDir()
			must(t, h.p.DB.Close())
			signal := filepath.Join(t.TempDir(), "crash")
			child := exec.Command(os.Args[0], "-test.run=^TestMoveCrashProcess$", "-test.timeout=30s")
			child.Env = append(os.Environ(), "FILEPANEL_CRASH_ROOT="+root, "FILEPANEL_CRASH_SIGNAL="+signal, "FILEPANEL_CRASH_STAGE="+stage)
			var output bytes.Buffer
			child.Stdout = &output
			child.Stderr = &output
			must(t, child.Start())
			defer child.Process.Kill()
			var ready []byte
			deadline := time.Now().Add(10 * time.Second)
			for {
				ready, e = os.ReadFile(signal + ".ready")
				if e == nil {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("crash helper failed to start")
				}
				time.Sleep(10 * time.Millisecond)
			}
			endpoint, e := url.Parse(string(ready))
			must(t, e)
			proxyMux := http.NewServeMux()
			proxyMux.Handle("/", httputil.NewSingleHostReverseProxy(endpoint))
			h.handler.Store(http.Handler(proxyMux))
			must(t, os.WriteFile(signal+".start", nil, 0600))
			e = child.Wait()
			exit, ok := e.(*exec.ExitError)
			if !ok || exit.ExitCode() != 86 {
				t.Fatalf("wrong child result: %v %s", e, output.String())
			}
			reopenMovePanel(t, h)
			// The persisted primary must be readable before reconciliation runs.
			got, status := h.get(item, nil, "")
			if status != 200 || !bytes.Equal(got, payload) {
				t.Fatal("lost complete file at crash boundary")
			}
			result := runTask(t, h, task)
			if result.State != "done" {
				t.Fatalf("restart failed %+v", result)
			}
			got, status = h.get(item, nil, "")
			if status != 200 || !bytes.Equal(got, payload) {
				t.Fatal("restart changed content")
			}
		})
	}
}
