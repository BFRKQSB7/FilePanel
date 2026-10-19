package web

import (
	"net/http"
	"testing"
)

func TestUpdateRequiresAuthenticationCSRFFixedAction(t *testing.T) {
	_, h, a := setupServer(t)
	if r := requestJSON(t, h, "GET", "/api/update", nil, "", ""); r.Code != http.StatusUnauthorized {
		t.Fatal(r.Code)
	}
	cookie, csrf := loginForTest(t, h, a)
	if r := requestJSON(t, h, "POST", "/api/update", nil, cookie, ""); r.Code != http.StatusForbidden {
		t.Fatal(r.Code)
	}
	if r := requestJSON(t, h, "POST", "/api/update", map[string]string{"url": "https://example.test/untrusted"}, cookie, csrf); r.Code != http.StatusBadRequest {
		t.Fatal(r.Code, r.Body.String())
	}
}
