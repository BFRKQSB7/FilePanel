package auth

import (
	"net/http/httptest"
	"testing"

	"filepanel/internal/store"
)

func testAuth(t *testing.T) *Auth {
	t.Helper()
	s, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return New(s)
}

func TestInitializationTokenSingleUseAndLoginSession(t *testing.T) {
	a := testAuth(t)
	token, err := a.CreateInitToken()
	if err != nil {
		t.Fatal(err)
	}
	if err := a.Initialize("wrong", "a-long-password-123"); err == nil {
		t.Fatal("invalid initialization token accepted")
	}
	if err := a.Initialize(token, "a-long-password-123"); err != nil {
		t.Fatal(err)
	}
	if err := a.Initialize(token, "another-long-password"); err == nil {
		t.Fatal("initialization token replay accepted")
	}

	r := httptest.NewRequest("POST", "/api/login", nil)
	r.RemoteAddr = "127.0.0.1:1234"
	session, csrf, err := a.Login(r, "a-long-password-123")
	if err != nil {
		t.Fatal(err)
	}
	if len(csrf) < 32 {
		t.Fatal("missing CSRF token")
	}
	if _, ok := a.Validate(session); !ok {
		t.Fatal("new session is not valid")
	}
	if err := a.Logout(session); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Validate(session); ok {
		t.Fatal("session remains valid after logout")
	}
}

func TestChangePasswordInvalidatesAllSessions(t *testing.T) {
	a := testAuth(t)
	token, err := a.CreateInitToken()
	if err != nil {
		t.Fatal(err)
	}
	if err = a.Initialize(token, "old-password-long"); err != nil {
		t.Fatal(err)
	}
	r := httptest.NewRequest("POST", "/api/login", nil)
	r.RemoteAddr = "192.0.2.10:2345"
	session, _, err := a.Login(r, "old-password-long")
	if err != nil {
		t.Fatal(err)
	}
	if err = a.ChangePassword("old-password-long", "new-password-long"); err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Validate(session); ok {
		t.Fatal("password change did not revoke session")
	}
	if _, _, err := a.Login(r, "old-password-long"); err == nil {
		t.Fatal("old password still accepted")
	}
	if _, _, err := a.Login(r, "new-password-long"); err != nil {
		t.Fatal(err)
	}
}
