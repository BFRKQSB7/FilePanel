// Package access manages candidate addresses separately from the active entry.
package access

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const productionDirectory = "https://acme-v02.api.letsencrypt.org/directory"
const StagingDirectory = "https://acme-staging-v02.api.letsencrypt.org/directory"
const settingKey = "access_state"
const freshness = 5 * time.Minute

type Input struct {
	Mode        string `json:"mode"` // manual, proxy, auto
	URL         string `json:"url"`
	IPv4        string `json:"ipv4"`
	IPv6        string `json:"ipv6"`
	HTTPPort    int    `json:"http_port"` // public mapping, zero means unknown
	HTTPSPort   int    `json:"https_port"`
	ProxyDNS    bool   `json:"proxy_dns"`
	Email       string `json:"email"`
	AcceptTerms bool   `json:"accept_terms"`
}

func (in Input) normalize() (Input, error) {
	in.URL = strings.TrimRight(strings.TrimSpace(in.URL), "/")
	u, err := url.Parse(in.URL)
	if err != nil || u.Host == "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" || u.Opaque != "" {
		return in, errors.New("请输入完整访问地址（协议、主机及可选端口，不含路径、账号或查询参数）")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return in, errors.New("访问地址仅支持 HTTP 或 HTTPS")
	}
	host := strings.ToLower(u.Hostname())
	if ip, e := netip.ParseAddr(host); e == nil {
		if ip.Zone() != "" {
			return in, errors.New("不支持带作用域的 IP")
		}
	} else if !validDomain(host) {
		return in, errors.New("域名须为完整 DNS 名称；中文域名请填写 Punycode")
	}
	port := u.Port()
	if port != "" {
		n, e := strconv.Atoi(port)
		if e != nil || n < 1 || n > 65535 {
			return in, errors.New("端口应在 1 到 65535 之间")
		}
	}
	u.Host = host
	if strings.Contains(host, ":") {
		u.Host = "[" + host + "]"
	}
	if port != "" {
		u.Host = net.JoinHostPort(host, port)
	}
	in.URL = u.String()
	if in.Mode != "manual" && in.Mode != "proxy" && in.Mode != "auto" {
		return in, errors.New("请选择映射地址、已有反代或自动 HTTPS")
	}
	if in.Mode != "manual" && u.Scheme != "https" {
		return in, errors.New("反代与自动证书模式需要 HTTPS 地址")
	}
	if in.Mode == "auto" {
		if !validDomain(host) || net.ParseIP(host) != nil || (port != "" && port != "443") {
			return in, errors.New("HTTP-01 自动证书需要域名及公网 443 服务端口")
		}
		if in.ProxyDNS {
			return in, errors.New("自动直连证书模式请关闭代理 DNS，或选择已有反代模式")
		}
	}
	for _, p := range []int{in.HTTPPort, in.HTTPSPort} {
		if p < 0 || p > 65535 {
			return in, errors.New("公网映射端口应为 1–65535，留空表示未知")
		}
	}
	for i, value := range []string{in.IPv4, in.IPv6} {
		if value == "" {
			continue
		}
		a, e := netip.ParseAddr(value)
		if e != nil || a.Zone() != "" || (i == 0 && !a.Is4()) || (i == 1 && (!a.Is6() || a.Is4In6())) {
			return in, errors.New("已确认的 IPv4 / IPv6 格式不正确")
		}
	}
	in.Email = strings.TrimSpace(in.Email)
	if len(in.Email) > 254 || strings.ContainsAny(in.Email, "\r\n <>") || (in.Email != "" && !strings.Contains(in.Email, "@")) {
		return in, errors.New("证书通知邮箱格式不正确")
	}
	return in, nil
}

func validDomain(s string) bool {
	if len(s) > 253 || !strings.Contains(s, ".") {
		return false
	}
	for _, label := range strings.Split(s, ".") {
		if len(label) == 0 || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, c := range label {
			if !(c >= 'a' && c <= 'z' || c >= '0' && c <= '9' || c == '-') {
				return false
			}
		}
	}
	return true
}

type Check struct {
	Name   string    `json:"name"`
	State  string    `json:"state"` // pass, fail, unknown
	Detail string    `json:"detail"`
	At     time.Time `json:"at"`
}
type DNSRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}
type Report struct {
	At        time.Time   `json:"at"`
	Checks    []Check     `json:"checks"`
	Records   []DNSRecord `json:"records"`
	Addresses []string    `json:"addresses"`
	Ready     bool        `json:"ready"`
	Guide     string      `json:"guide"`
}
type Active struct {
	Input
	VerifiedAt time.Time `json:"verified_at"`
	Expires    time.Time `json:"expires"`
}
type Candidate struct {
	Input
	Stage  string `json:"stage"`
	Error  string `json:"error"`
	Report Report `json:"report"`
}
type Environment struct {
	OS              string   `json:"os"`
	Arch            string   `json:"arch"`
	Listen          string   `json:"listen"`
	TLSListen       string   `json:"tls_listen"`
	ChallengeListen string   `json:"challenge_listen"`
	IPv4            []string `json:"ipv4"`
	IPv6            []string `json:"ipv6"`
}
type View struct {
	Active       *Active     `json:"active"`
	Candidate    *Candidate  `json:"candidate"`
	Busy         bool        `json:"busy"`
	Stale        bool        `json:"stale"`
	Warning      string      `json:"warning"`
	RenewalError string      `json:"renewal_error"`
	LastRenewal  time.Time   `json:"last_renewal"`
	Environment  Environment `json:"environment"`
	Staging      bool        `json:"staging"`
}

func (r Report) requireReady() error {
	if !r.Ready {
		return fmt.Errorf("候选地址检查未通过，请按诊断修复后重试")
	}
	return nil
}
