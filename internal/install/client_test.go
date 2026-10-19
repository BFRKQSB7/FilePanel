package install

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestMaintenanceGateAndBoundedWorkerWait(t *testing.T) {
	p := filepath.Join(t.TempDir(), "maintenance")
	t.Setenv("FILEPANEL_MAINTENANCE", p)
	if e := os.WriteFile(p, []byte("maintenance"), 0600); e != nil {
		t.Fatal(e)
	}
	called := false
	h := MaintenanceGate(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { called = true; w.WriteHeader(200) }))
	for _, path := range []string{"/api/files", "/_node/v1/object", "/d/id"} {
		w := httptest.NewRecorder()
		h.ServeHTTP(w, httptest.NewRequest("POST", path, nil))
		if w.Code != 503 || called {
			t.Fatal("request bypassed maintenance", path, w.Code)
		}
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, httptest.NewRequest("GET", "/healthz", nil))
	if w.Code != 200 || !called {
		t.Fatal("health blocked")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if WaitForMaintenance(ctx) {
		t.Fatal("worker passed durable gate")
	}
	if e := os.Remove(p); e != nil {
		t.Fatal(e)
	}
	if !WaitForMaintenance(context.Background()) {
		t.Fatal("worker remained blocked")
	}
}
