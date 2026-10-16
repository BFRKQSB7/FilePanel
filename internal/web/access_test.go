package web

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"

	"filepanel/internal/access"
	"filepanel/internal/download"
	"filepanel/internal/usage"
)

func TestAccessAPIRequiresSessionCSRFAndKeepsCandidateIsolated(t *testing.T) {
	e := newEnv(t)
	if got := requestJSON(t, e.handler, "GET", "/api/access", nil, "", ""); got.Code != 401 {
		t.Fatal(got.Code)
	}
	in := access.Input{Mode: "manual", URL: "http://127.0.0.1:8080"}
	if got := requestJSON(t, e.handler, "POST", "/api/access/check", in, e.cookie, ""); got.Code != 403 {
		t.Fatal(got.Code)
	}
	got := requestJSON(t, e.handler, "POST", "/api/access/check", in, e.cookie, e.csrf)
	if got.Code != 200 {
		t.Fatal(got.Body.String())
	}
	var v access.View
	_ = json.Unmarshal(got.Body.Bytes(), &v)
	if v.Active != nil || v.Candidate == nil || v.Candidate.Report.Ready {
		t.Fatal("private address check promoted candidate")
	}
	if v.Busy || len(v.Candidate.Report.Records) != 0 || !strings.Contains(v.Candidate.Report.Guide, "无需添加 DNS") {
		t.Fatal("IP guidance or completed-check status is incorrect")
	}
	if got = requestJSON(t, e.handler, "POST", "/api/access/apply", nil, e.cookie, ""); got.Code != 403 {
		t.Fatal(got.Code)
	}
	if got = e.get("/healthz", nil, false); got.Code != 200 {
		t.Fatal("old HTTP entry unavailable")
	}
	if got = e.get("/.well-known/filepanel/not-current", nil, false); got.Code != 404 {
		t.Fatal("unguarded proof endpoint")
	}
}

func TestCanonicalURLsIgnoreHostAndForwardedHeaders(t *testing.T) {
	e := newEnv(t)
	item := e.upload("canonical.bin", []byte("private and public remain available"))
	sh := e.publish(item.ID, "Canonical", true)
	state := `{"active":{"mode":"manual","url":"https://files.example:8443"}}`
	if err := e.store.SetSetting("access_state", state); err != nil {
		t.Fatal(err)
	}
	m, err := access.New(e.store, e.files, access.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer m.Close()
	e.deps.Access = m
	e.handler = NewServer(e.deps, false)
	headers := map[string]string{"Host": "attacker.example", "X-Forwarded-Host": "evil.example", "X-Forwarded-Proto": "http", "Forwarded": "host=evil.example;proto=http"}
	for _, path := range []string{"/", "/s/" + sh.Slug} {
		res := e.get(path, headers, false)
		if res.Code != 200 || !strings.Contains(res.Body.String(), `rel="canonical" href="https://files.example:8443`) {
			t.Fatalf("canonical page %s: %d", path, res.Code)
		}
		if strings.Contains(res.Body.String(), "evil.example") || strings.Contains(res.Body.String(), "attacker.example") {
			t.Fatal("forged host used")
		}
	}
	view := requestJSON(t, e.handler, "GET", "/api/files/"+item.ID+"/share", nil, e.cookie, "")
	if !strings.Contains(view.Body.String(), `"url":"https://files.example:8443/s/`) {
		t.Fatal(view.Body.String())
	}
	res := e.get("/s/"+sh.Slug+"/get", headers, false)
	if res.Code != 302 || !strings.HasPrefix(res.Header().Get("Location"), "https://files.example:8443/d/") {
		t.Fatal("download URL did not use canonical base")
	}
	if res = e.get("/api/files/"+item.ID, nil, true); res.Code != 200 {
		t.Fatal("private admin download regressed")
	}
}

func TestProxyHeadersNeedExplicitTrust(t *testing.T) {
	e := newEnv(t)
	login := func(remote, proto string) *httptest.ResponseRecorder {
		r := httptest.NewRequest("POST", "/api/login", strings.NewReader(`{"password":"test-password-long"}`))
		r.RemoteAddr = remote
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-Forwarded-Proto", proto)
		r.Header.Set("X-Forwarded-For", "198.51.100.8")
		w := httptest.NewRecorder()
		e.handler.ServeHTTP(w, r)
		return w
	}
	if res := login("203.0.113.1:4000", "https"); res.Code != 200 || res.Result().Cookies()[0].Secure {
		t.Fatal("untrusted proto accepted")
	}
	cfg := usage.DefaultConfig()
	cfg.TrustedProxies = []string{"127.0.0.1/32"}
	if err := e.deps.Meter.SetConfig(cfg); err != nil {
		t.Fatal(err)
	}
	if res := login("127.0.0.1:4000", "https"); res.Code != 200 || !res.Result().Cookies()[0].Secure {
		t.Fatal("trusted HTTPS did not set secure cookie")
	}
	if res := login("127.0.0.1:4000", "https, http"); res.Code != 200 || res.Result().Cookies()[0].Secure {
		t.Fatal("ambiguous proto accepted")
	}
	r := httptest.NewRequest("GET", "/", nil)
	r.RemoteAddr = "203.0.113.1:4000"
	r.Header.Set("X-Forwarded-For", "198.51.100.8")
	if download.ClientIP(r, cfg) != "203.0.113.1" {
		t.Fatal("untrusted XFF accepted")
	}
	r.RemoteAddr = "127.0.0.1:4000"
	r.Header.Set("X-Forwarded-For", "198.51.100.99, 198.51.100.8")
	if download.ClientIP(r, cfg) != "198.51.100.8" {
		t.Fatal("trusted chain resolved forged left entry")
	}
}
