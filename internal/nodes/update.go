package nodes

import (
	"context"
	"encoding/json"
	"filepanel/internal/install"
	"net/http"
)

// Update is explicitly one node at a time and requires a fresh control lease
// and a live TLS/identity probe. Offline nodes are never queued silently.
func (p *Panel) SoftwareUpdate(ctx context.Context, id string, apply bool) (json.RawMessage, error) {
	n, e := p.Get(id)
	if e != nil || n.Revoked || p.Now().Unix() > n.LastSeen+30 {
		return nil, ErrDenied
	}
	if e = p.Probe(ctx, id); e != nil {
		return nil, e
	}
	c, e := p.client(n)
	if e != nil {
		return nil, e
	}
	method := "GET"
	if apply {
		method = "POST"
	}
	var out json.RawMessage
	e = call(ctx, c, method, n.Endpoint, prefix+"/update", n.Credential, nil, &out)
	return out, e
}
func (n *Daemon) update(w http.ResponseWriter, r *http.Request) {
	id, _ := n.connection()
	if hash(bearer(r)) != hash(id.Credential) || !n.valid() || r.ContentLength != 0 {
		fail(w)
		return
	}
	b, e := install.AgentCall(r.Context(), r.Method == "POST")
	if e != nil {
		http.Error(w, e.Error(), 503)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if r.Method == "POST" {
		w.WriteHeader(202)
	}
	w.Write(b)
}
