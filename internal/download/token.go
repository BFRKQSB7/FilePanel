package download

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/netip"
	"strings"
	"time"

	"filepanel/internal/store"
	"filepanel/internal/usage"
)

const secretKey = "download_secret"

var (
	ErrInvalidToken = errors.New("下载链接无效")
	ErrExpiredToken = errors.New("下载链接已过期")
)

// Claims 是初次下载凭证：限定不可变文件版本（SHA-256）、分享授权版本、权限范围与来源。
type Claims struct {
	File        string `json:"f"`
	SHA         string `json:"h"`
	AuthVersion int64  `json:"v"`
	Scope       string `json:"s"`
	IP          string `json:"i"`
	Expires     int64  `json:"e"`
}

func loadSecret(s *store.Store) ([]byte, error) {
	value, ok, err := s.Setting(secretKey)
	if err != nil {
		return nil, err
	}
	if ok {
		if raw, err := base64.RawStdEncoding.DecodeString(value); err == nil && len(raw) == 32 {
			return raw, nil
		}
	}
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return nil, err
	}
	if err := s.SetSetting(secretKey, base64.RawStdEncoding.EncodeToString(raw)); err != nil {
		return nil, err
	}
	return raw, nil
}

func (s *Service) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, s.secret)
	mac.Write(payload)
	return mac.Sum(nil)
}

// Issue 签发初次凭证，默认 300 秒有效。凭证只授予“开始新请求”的资格，
// 已开始的响应由独立租约维持，不会因凭证到期被直接截断。
func (s *Service) Issue(c Claims) (string, time.Time) {
	expires := s.Now().Add(s.TokenTTL)
	c.Expires = expires.Unix()
	payload, _ := json.Marshal(c)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(s.sign(payload)), expires
}

// Verify 校验签名、文件、来源与有效期（含最多 Skew 的时钟容限）。授权版本由调用者对照分享记录检查。
func (s *Service) Verify(token, fileID, ip string) (Claims, error) {
	var c Claims
	body, sig, ok := strings.Cut(token, ".")
	if !ok {
		return c, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(body)
	if err != nil {
		return c, ErrInvalidToken
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil || !hmac.Equal(got, s.sign(payload)) {
		return c, ErrInvalidToken
	}
	if err := json.Unmarshal(payload, &c); err != nil {
		return c, ErrInvalidToken
	}
	if c.File == "" || c.File != fileID || c.IP != ip {
		return c, ErrInvalidToken
	}
	if s.Now().Unix() > c.Expires+int64(s.Skew/time.Second) {
		return c, ErrExpiredToken
	}
	return c, nil
}

// ClientIP 只在直连地址属于已配置的可信代理时才读取 X-Forwarded-For，
// 并从右向左取第一个非可信地址，因此访客无法伪造来源。
func ClientIP(r *http.Request, cfg usage.Config) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	remote, err := netip.ParseAddr(strings.TrimSpace(host))
	if err != nil {
		return host
	}
	remote = remote.Unmap()
	var trusted []netip.Prefix
	for _, item := range cfg.TrustedProxies {
		if p, err := usage.ParseProxy(item); err == nil {
			trusted = append(trusted, p)
		}
	}
	isTrusted := func(a netip.Addr) bool {
		for _, p := range trusted {
			if p.Contains(a) {
				return true
			}
		}
		return false
	}
	if !isTrusted(remote) {
		return remote.String()
	}
	parts := strings.Split(strings.Join(r.Header.Values("X-Forwarded-For"), ","), ",")
	for i := len(parts) - 1; i >= 0; i-- {
		a, err := netip.ParseAddr(strings.TrimSpace(parts[i]))
		if err != nil {
			break
		}
		a = a.Unmap()
		if !isTrusted(a) {
			return a.String()
		}
	}
	return remote.String()
}
