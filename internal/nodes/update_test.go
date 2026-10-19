package nodes

import (
	"context"
	"encoding/json"
	"encoding/pem"
	"filepanel/internal/release"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestNodeUpdateRejectsOfflineAndIncompatiblePeers(t *testing.T) {
	h := fixture(t)
	n := h.peer(true)
	id := n.n.Identity.ID
	var calls atomic.Int32
	var protocol atomic.Int32
	protocol.Store(release.Protocol)
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == prefix+"/health" {
			json.NewEncoder(w).Encode(map[string]any{"id": id, "panel_id": h.p.ID(), "protocol": protocol.Load(), "schema": release.Schema})
			return
		}
		calls.Add(1)
		json.NewEncoder(w).Encode(map[string]string{"phase": "accepted"})
	}))
	defer srv.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	_, e := h.p.DB.DB.Exec("UPDATE nodes SET endpoint=?,ca=? WHERE id=?", srv.URL, ca, id)
	must(t, e)
	if _, e = h.p.SoftwareUpdate(context.Background(), id, true); e != nil {
		t.Fatal(e)
	}
	if calls.Load() != 1 {
		t.Fatal("not explicitly dispatched")
	}
	protocol.Store(99)
	if _, e = h.p.SoftwareUpdate(context.Background(), id, true); e == nil {
		t.Fatal("incompatible node updated")
	}
	if calls.Load() != 1 {
		t.Fatal("incompatible update reached node")
	}
	protocol.Store(release.Protocol)
	_, e = h.p.DB.DB.Exec("UPDATE nodes SET last_seen=0 WHERE id=?", id)
	must(t, e)
	if _, e = h.p.SoftwareUpdate(context.Background(), id, true); e == nil {
		t.Fatal("offline node queued")
	}
	if calls.Load() != 1 {
		t.Fatal("offline update reached node")
	}
}
