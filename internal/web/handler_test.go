package web

import (
	"bytes"
	"encoding/json"
	"fmt"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"filepanel/internal/auth"
	"filepanel/internal/files"
	"filepanel/internal/store"
)

func setupServer(t *testing.T) (*store.Store, http.Handler, *auth.Auth) {
	t.Helper()
	dir := t.TempDir()
	s, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	a := auth.New(s)
	m, err := files.New(s, dir, 1<<20, 1)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s, New(s, a, m, false), a
}

func loginForTest(t *testing.T, handler http.Handler, a *auth.Auth) (string, string) {
	t.Helper()
	token, err := a.CreateInitToken()
	if err != nil {
		t.Fatal(err)
	}
	res := requestJSON(t, handler, http.MethodPost, "/api/init", map[string]string{"token": token, "password": "test-password-long"}, "", "")
	if res.Code != http.StatusCreated {
		t.Fatalf("initialize: %d %s", res.Code, res.Body.String())
	}
	res = requestJSON(t, handler, http.MethodPost, "/api/login", map[string]string{"password": "test-password-long"}, "", "")
	if res.Code != http.StatusOK {
		t.Fatalf("login: %d %s", res.Code, res.Body.String())
	}
	var result struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(res.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	return res.Result().Cookies()[0].Value, result.CSRF
}

func TestStatusRestoresCSRFForExistingSession(t *testing.T) {
	_, handler, a := setupServer(t)
	cookie, csrf := loginForTest(t, handler, a)
	status := requestJSON(t, handler, http.MethodGet, "/api/status", nil, cookie, "")
	var result struct {
		Authenticated bool   `json:"authenticated"`
		CSRF          string `json:"csrf"`
	}
	if err := json.Unmarshal(status.Body.Bytes(), &result); err != nil {
		t.Fatal(err)
	}
	if !result.Authenticated || result.CSRF == "" || result.CSRF != csrf {
		t.Fatal("existing session cannot recover its CSRF token after a reload")
	}
	logout := requestJSON(t, handler, http.MethodPost, "/api/logout", nil, cookie, result.CSRF)
	if logout.Code != http.StatusOK {
		t.Fatalf("logout with recovered CSRF: %d", logout.Code)
	}
	for _, staleCookie := range []string{"", cookie} {
		status = requestJSON(t, handler, http.MethodGet, "/api/status", nil, staleCookie, "")
		var anonymous map[string]any
		if err := json.Unmarshal(status.Body.Bytes(), &anonymous); err != nil {
			t.Fatal(err)
		}
		if _, exposed := anonymous["csrf"]; exposed || anonymous["authenticated"] != false {
			t.Fatal("anonymous or revoked session received a CSRF token")
		}
	}
}

func TestRejectedMultipartDoesNotCommitFile(t *testing.T) {
	for _, malformed := range []bool{false, true} {
		t.Run(fmt.Sprintf("malformed_tail_%t", malformed), func(t *testing.T) {
			s, handler, a := setupServer(t)
			cookie, csrf := loginForTest(t, handler, a)
			var body bytes.Buffer
			writer := multipart.NewWriter(&body)
			part, err := writer.CreateFormFile("file", "first.txt")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write([]byte("first complete file")); err != nil {
				t.Fatal(err)
			}
			part, err = writer.CreateFormFile("file", "second.txt")
			if err != nil {
				t.Fatal(err)
			}
			if _, err := part.Write([]byte("second file")); err != nil {
				t.Fatal(err)
			}
			if err := writer.Close(); err != nil {
				t.Fatal(err)
			}
			payload := body.Bytes()
			if malformed {
				// The first part ends correctly, but the following part header is truncated.
				marker := []byte("\r\n--" + writer.Boundary() + "\r\n")
				pos := bytes.Index(payload, marker)
				if pos < 0 {
					t.Fatal("missing second part boundary")
				}
				payload = append(append([]byte(nil), payload[:pos+len(marker)]...), []byte("broken header\r\n\r\n")...)
			}
			req := httptest.NewRequest(http.MethodPost, "/api/files", bytes.NewReader(payload))
			req.Header.Set("Content-Type", writer.FormDataContentType())
			req.Header.Set("X-CSRF-Token", csrf)
			req.AddCookie(&http.Cookie{Name: "fp_session", Value: cookie})
			res := httptest.NewRecorder()
			handler.ServeHTTP(res, req)
			if res.Code != http.StatusBadRequest {
				t.Fatalf("invalid multipart status: %d %s", res.Code, res.Body.String())
			}
			var count int
			if err := s.DB.QueryRow("SELECT count(*) FROM files").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("rejected request committed %d files", count)
			}
			if err := s.DB.QueryRow("SELECT count(*) FROM uploads WHERE state IN ('receiving','verified','available')").Scan(&count); err != nil {
				t.Fatal(err)
			}
			if count != 0 {
				t.Fatalf("rejected request left %d recoverable uploads", count)
			}
		})
	}
}

func requestJSON(t *testing.T, handler http.Handler, method, path string, data any, cookie, csrf string) *httptest.ResponseRecorder {
	t.Helper()
	var body bytes.Buffer
	if data != nil {
		if err := json.NewEncoder(&body).Encode(data); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &body)
	req.RemoteAddr = "127.0.0.1:1234"
	if data != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if cookie != "" {
		req.AddCookie(&http.Cookie{Name: "fp_session", Value: cookie})
	}
	if csrf != "" {
		req.Header.Set("X-CSRF-Token", csrf)
	}
	res := httptest.NewRecorder()
	handler.ServeHTTP(res, req)
	return res
}

func TestHTTPAuthCSRFAndFileLifecycle(t *testing.T) {
	_, handler, a := setupServer(t)
	if got := requestJSON(t, handler, http.MethodGet, "/api/files", nil, "", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous list status=%d", got.Code)
	}
	if got := requestJSON(t, handler, http.MethodGet, "/api/data", nil, "", ""); got.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous data status=%d", got.Code)
	}

	token, err := a.CreateInitToken()
	if err != nil {
		t.Fatal(err)
	}
	init := requestJSON(t, handler, http.MethodPost, "/api/init", map[string]string{"token": token, "password": "correct-horse-battery"}, "", "")
	if init.Code != http.StatusCreated {
		t.Fatalf("initialize status=%d body=%s", init.Code, init.Body.String())
	}
	login := requestJSON(t, handler, http.MethodPost, "/api/login", map[string]string{"password": "correct-horse-battery"}, "", "")
	if login.Code != http.StatusOK {
		t.Fatalf("login status=%d body=%s", login.Code, login.Body.String())
	}
	var loginResult struct {
		CSRF string `json:"csrf"`
	}
	if err := json.Unmarshal(login.Body.Bytes(), &loginResult); err != nil {
		t.Fatal(err)
	}
	cookie := login.Result().Cookies()[0].Value
	if !strings.HasPrefix(loginResult.CSRF, " ") && loginResult.CSRF == "" {
		t.Fatal("missing csrf")
	}

	body := &bytes.Buffer{}
	mw := multipart.NewWriter(body)
	part, err := mw.CreateFormFile("file", "report.txt")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write([]byte("integration bytes"))
	_ = mw.Close()
	for _, csrf := range []string{"", loginResult.CSRF} {
		req := httptest.NewRequest(http.MethodPost, "/api/files", bytes.NewReader(body.Bytes()))
		req.RemoteAddr = "127.0.0.1:1234"
		req.Header.Set("Content-Type", mw.FormDataContentType())
		req.AddCookie(&http.Cookie{Name: "fp_session", Value: cookie})
		if csrf != "" {
			req.Header.Set("X-CSRF-Token", csrf)
		}
		res := httptest.NewRecorder()
		handler.ServeHTTP(res, req)
		if csrf == "" && res.Code != http.StatusForbidden {
			t.Fatalf("upload without csrf status=%d", res.Code)
		}
		if csrf != "" && res.Code != http.StatusCreated {
			t.Fatalf("upload status=%d body=%s", res.Code, res.Body.String())
		}
		if csrf != "" {
			var item struct {
				ID   string `json:"id"`
				Name string `json:"name"`
			}
			if err := json.Unmarshal(res.Body.Bytes(), &item); err != nil {
				t.Fatal(err)
			}
			list := requestJSON(t, handler, http.MethodGet, "/api/files", nil, cookie, "")
			if list.Code != http.StatusOK || !strings.Contains(list.Body.String(), item.ID) {
				t.Fatalf("file list status=%d body=%s", list.Code, list.Body.String())
			}
			download := httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/files/%s", item.ID), nil)
			download.AddCookie(&http.Cookie{Name: "fp_session", Value: cookie})
			downloadRes := httptest.NewRecorder()
			handler.ServeHTTP(downloadRes, download)
			if downloadRes.Code != http.StatusOK || downloadRes.Body.String() != "integration bytes" {
				t.Fatalf("download status=%d body=%s", downloadRes.Code, downloadRes.Body.String())
			}
			patch := requestJSON(t, handler, http.MethodPatch, "/api/files/"+item.ID, map[string]string{"name": "renamed.txt"}, cookie, loginResult.CSRF)
			if patch.Code != http.StatusOK {
				t.Fatalf("rename status=%d body=%s", patch.Code, patch.Body.String())
			}
			remove := requestJSON(t, handler, http.MethodDelete, "/api/files/"+item.ID, nil, cookie, loginResult.CSRF)
			if remove.Code != http.StatusOK {
				t.Fatalf("delete status=%d body=%s", remove.Code, remove.Body.String())
			}
		}
	}
	if logout := requestJSON(t, handler, http.MethodPost, "/api/logout", nil, cookie, loginResult.CSRF); logout.Code != http.StatusOK {
		t.Fatalf("logout status=%d", logout.Code)
	}
	if list := requestJSON(t, handler, http.MethodGet, "/api/files", nil, cookie, ""); list.Code != http.StatusUnauthorized {
		t.Fatalf("post-logout list status=%d", list.Code)
	}
}
