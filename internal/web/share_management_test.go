package web

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"filepanel/internal/share"
)

func TestBatchSharingAtomicityTemplatesAndAuthorization(t *testing.T) {
	e := newEnv(t)
	a, b, c := e.upload("a.txt", []byte("a")), e.upload("b.txt", []byte("b")), e.upload("private.txt", []byte("private"))
	for _, id := range []string{a.ID, b.ID} {
		_, _, err := e.deps.Shares.Update(id, share.Input{Title: "preserve metadata", PageTemplate: "compact", Featured: true})
		if err != nil {
			t.Fatal(err)
		}
	}
	before, _ := e.deps.Shares.Get(a.ID)
	token := e.token(before, "203.0.113.7")
	call := func(ids []string, mode string, csrf bool) *httptest.ResponseRecorder {
		data, _ := json.Marshal(map[string]any{"ids": ids, "mode": mode})
		req := httptest.NewRequest(http.MethodPost, "/api/shares/batch", bytes.NewReader(data))
		req.AddCookie(&http.Cookie{Name: "fp_session", Value: e.cookie})
		if csrf {
			req.Header.Set("X-CSRF-Token", e.csrf)
		}
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		return rec
	}
	if rec := call([]string{a.ID}, "private", false); rec.Code != 403 {
		t.Fatalf("batch without CSRF: %d", rec.Code)
	}
	if rec := call([]string{a.ID, c.ID}, "link", true); rec.Code != 409 {
		t.Fatalf("stale selection: %d", rec.Code)
	}
	if current, _ := e.deps.Shares.Get(a.ID); !current.Featured {
		t.Fatal("failed batch partially committed")
	}
	if rec := call([]string{a.ID, b.ID}, "link", true); rec.Code != 200 {
		t.Fatalf("batch link: %d %s", rec.Code, rec.Body.String())
	}
	current, _ := e.deps.Shares.Get(a.ID)
	if !current.Enabled || current.Featured || current.PageTemplate != "compact" || current.Title != before.Title || current.Slug != before.Slug || current.AuthVersion != before.AuthVersion {
		t.Fatalf("batch changed content: %+v", current)
	}
	if rec := e.get("/", nil, false); strings.Contains(rec.Body.String(), "/s/"+before.Slug) {
		t.Fatal("batch link share listed publicly")
	}
	if rec := call([]string{a.ID, b.ID}, "public", true); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if rec := e.get("/", nil, false); !strings.Contains(rec.Body.String(), "/s/"+before.Slug) {
		t.Fatal("batch public share absent")
	}
	if rec := call([]string{a.ID, b.ID}, "private", true); rec.Code != 200 {
		t.Fatal(rec.Code)
	}
	if rec := e.get("/d/"+a.ID+"?t="+token, nil, false); rec.Code != 410 {
		t.Fatalf("old batch-cancelled token: %d", rec.Code)
	}
	if _, err := e.files.Metadata(a.ID); err != nil {
		t.Fatal("batch cancellation removed the file")
	}
}

func TestShareTemplatesAndSelectedRelayCannotBeBypassed(t *testing.T) {
	e := newEnv(t)
	v := e.upload("template.txt", []byte("template download"))
	for _, layout := range []string{"standard", "compact", "showcase"} {
		sh, _, err := e.deps.Shares.Update(v.ID, share.Input{Title: "safe <script>title</script>", Enabled: true, PageTemplate: layout, RelayMode: "selected", Relays: []string{"local"}})
		if err != nil {
			t.Fatal(err)
		}
		rec := e.get("/s/"+sh.Slug, nil, false)
		body := rec.Body.String()
		if rec.Code != 200 || !strings.Contains(body, "layout-"+layout) || strings.Contains(body, "<script>title</script>") || !strings.Contains(body, "relay=local") {
			t.Fatalf("unsafe or missing template %s: %d", layout, rec.Code)
		}
		if strings.Contains(body, `role="search"`) || strings.Contains(body, `href="/"`) || strings.Contains(body, `<a class="brand"`) {
			t.Fatalf("private %s exposes public navigation", layout)
		}
		if rec = e.get("/s/"+sh.Slug+"/get?relay=forbidden", nil, false); rec.Code != 403 {
			t.Fatalf("forged unselected relay: %d", rec.Code)
		}
		token := e.token(sh, "203.0.113.7")
		if rec = e.get("/d/"+v.ID+"?t="+token+"&relay=forbidden", nil, false); rec.Code != 403 {
			t.Fatalf("direct download bypassed selected relay: %d", rec.Code)
		}
	}
	if _, _, err := e.deps.Shares.Update(v.ID, share.Input{Enabled: true, PageTemplate: "<script>"}); err == nil {
		t.Fatal("untrusted template accepted")
	}
}

func TestPublicNavigationAndPrivateErrorIsolation(t *testing.T) {
	e := newEnv(t)
	f := e.upload("navigation.txt", []byte("navigation"))
	sh, _, err := e.deps.Shares.Update(f.ID, share.Input{Featured: true})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/", "/s/" + sh.Slug} {
		body := e.get(path, nil, false).Body.String()
		if !strings.Contains(body, `role="search"`) || !strings.Contains(body, `<a class="brand"`) {
			t.Fatalf("public navigation missing: %s", path)
		}
	}
	_, _, err = e.deps.Shares.Update(f.ID, share.Input{Enabled: true, RelayMode: "selected", Relays: []string{"local"}})
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{"/s/missing", "/s/" + sh.Slug + "/get?relay=missing", "/d/" + f.ID + "?t=invalid"} {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.Header.Set("Accept", "text/html")
		rec := httptest.NewRecorder()
		e.handler.ServeHTTP(rec, req)
		body := rec.Body.String()
		if rec.Code < 400 {
			t.Fatalf("expected error for %s", path)
		}
		if strings.Contains(body, `role="search"`) || strings.Contains(body, `href="/"`) || strings.Contains(body, `<a class="brand"`) {
			t.Fatalf("error exposes public navigation: %s", path)
		}
	}
}
