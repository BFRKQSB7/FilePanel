package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"filepanel/internal/store"
	"golang.org/x/crypto/bcrypt"
)

const (
	passwordKey = "admin_password_hash"
	tokenKey    = "init_token_hash"
	initTTL     = 30 * time.Minute
	sessionTTL  = 12 * time.Hour
)

type Auth struct {
	store    *store.Store
	mu       sync.Mutex
	attempts map[string][]time.Time
}

type Session struct {
	CSRF string
}

func New(s *store.Store) *Auth {
	return &Auth{store: s, attempts: make(map[string][]time.Time)}
}

func randomToken(n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(b), nil
}

func digest(value string) []byte {
	sum := sha256.Sum256([]byte(value))
	return sum[:]
}

func (a *Auth) Initialized() (bool, error) {
	_, ok, err := a.store.Setting(passwordKey)
	return ok, err
}

// CreateInitToken rotates and replaces any previous unconsumed token. Return it only to the caller once.
func (a *Auth) CreateInitToken() (string, error) {
	initialized, err := a.Initialized()
	if err != nil {
		return "", err
	}
	if initialized {
		return "", errors.New("管理员已初始化")
	}
	token, err := randomToken(32)
	if err != nil {
		return "", err
	}
	tx, err := a.store.DB.Begin()
	if err != nil {
		return "", err
	}
	defer tx.Rollback()
	if _, err := tx.Exec("INSERT INTO settings(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", tokenKey, base64.RawStdEncoding.EncodeToString(digest(token))); err != nil {
		return "", err
	}
	if _, err := tx.Exec("INSERT INTO settings(key,value) VALUES('init_token_expires',?) ON CONFLICT(key) DO UPDATE SET value=excluded.value", fmt.Sprint(time.Now().Add(initTTL).Unix())); err != nil {
		return "", err
	}
	return token, tx.Commit()
}

func (a *Auth) Initialize(token, password string) error {
	if len(password) < 12 || len(password) > 72 {
		return errors.New("密码长度须为 12 至 72 字节")
	}
	passwordHash, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := a.store.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	err = tx.QueryRow("SELECT 1 FROM settings WHERE key=?", passwordKey).Scan(&exists)
	if err == nil {
		return errors.New("管理员已初始化")
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var stored string
	if err := tx.QueryRow("SELECT value FROM settings WHERE key=?", tokenKey).Scan(&stored); err != nil {
		return errors.New("初始化令牌无效或已过期")
	}
	var expires int64
	if err := tx.QueryRow("SELECT value FROM settings WHERE key='init_token_expires'").Scan(&expires); err != nil || time.Now().Unix() > expires {
		return errors.New("初始化令牌无效或已过期")
	}
	candidate, err := base64.RawStdEncoding.DecodeString(stored)
	if err != nil || !equal(candidate, digest(token)) {
		return errors.New("初始化令牌无效或已过期")
	}
	if _, err := tx.Exec("INSERT INTO settings(key,value) VALUES(?,?)", passwordKey, string(passwordHash)); err != nil {
		return err
	}
	if _, err := tx.Exec("DELETE FROM settings WHERE key IN (?, 'init_token_expires')", tokenKey); err != nil {
		return err
	}
	return tx.Commit()
}

func equal(a, b []byte) bool {
	if len(a) != len(b) {
		return false
	}
	var diff byte
	for i := range a {
		diff |= a[i] ^ b[i]
	}
	return diff == 0
}

func (a *Auth) allowAttempt(ip string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	now := time.Now()
	recent := a.attempts[ip][:0]
	for _, t := range a.attempts[ip] {
		if now.Sub(t) < time.Minute {
			recent = append(recent, t)
		}
	}
	if len(recent) >= 5 {
		a.attempts[ip] = recent
		return false
	}
	a.attempts[ip] = append(recent, now)
	return true
}

func (a *Auth) Login(r *http.Request, password string) (string, string, error) {
	ip, _, _ := net.SplitHostPort(r.RemoteAddr)
	if ip == "" {
		ip = r.RemoteAddr
	}
	if !a.allowAttempt(ip) {
		return "", "", errors.New("登录尝试过多，请一分钟后再试")
	}
	hash, ok, err := a.store.Setting(passwordKey)
	if err != nil {
		return "", "", err
	}
	if !ok || bcrypt.CompareHashAndPassword([]byte(hash), []byte(password)) != nil {
		return "", "", errors.New("密码错误")
	}
	token, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	csrf, err := randomToken(32)
	if err != nil {
		return "", "", err
	}
	expires := time.Now().Add(sessionTTL).Unix()
	if _, err := a.store.DB.Exec("INSERT INTO sessions(token_hash,csrf_token,expires_at,created_at) VALUES(?,?,?,?)", digest(token), csrf, expires, time.Now().Unix()); err != nil {
		return "", "", err
	}
	return token, csrf, nil
}

func (a *Auth) Validate(token string) (Session, bool) {
	var session Session
	if token == "" {
		return session, false
	}
	err := a.store.DB.QueryRow("SELECT csrf_token FROM sessions WHERE token_hash=? AND expires_at>?", digest(token), time.Now().Unix()).Scan(&session.CSRF)
	return session, err == nil
}

func (a *Auth) Logout(token string) error {
	_, err := a.store.DB.Exec("DELETE FROM sessions WHERE token_hash=?", digest(token))
	return err
}

func (a *Auth) ChangePassword(oldPassword, newPassword string) error {
	if len(newPassword) < 12 || len(newPassword) > 72 {
		return errors.New("新密码长度须为 12 至 72 字节")
	}
	oldHash, ok, err := a.store.Setting(passwordKey)
	if err != nil || !ok || bcrypt.CompareHashAndPassword([]byte(oldHash), []byte(oldPassword)) != nil {
		return errors.New("当前密码错误")
	}
	newHash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	tx, err := a.store.DB.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.Exec("UPDATE settings SET value=? WHERE key=?", string(newHash), passwordKey); err != nil {
		return err
	}
	if _, err = tx.Exec("DELETE FROM sessions"); err != nil {
		return err
	}
	return tx.Commit()
}

func SessionCookie(w http.ResponseWriter, token string, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: "fp_session", Value: token, Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: int(sessionTTL.Seconds())})
}

func ClearCookie(w http.ResponseWriter, secure bool) {
	http.SetCookie(w, &http.Cookie{Name: "fp_session", Value: "", Path: "/", HttpOnly: true, Secure: secure, SameSite: http.SameSiteStrictMode, MaxAge: -1})
}

func Cookie(r *http.Request) string {
	c, err := r.Cookie("fp_session")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(c.Value)
}
