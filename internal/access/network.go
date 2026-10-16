package access

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func hostname(raw string) string { u, _ := url.Parse(raw); return u.Hostname() }

func publicIP(ip net.IP) bool {
	// Reject local, metadata, documentation and multicast destinations. All
	// connections for deployment checks are pinned to the vetted DNS result.
	if !ip.IsGlobalUnicast() || ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsUnspecified() {
		return false
	}
	for _, cidr := range []string{"0.0.0.0/8", "100.64.0.0/10", "192.0.0.0/24", "192.0.2.0/24", "198.18.0.0/15", "198.51.100.0/24", "203.0.113.0/24", "240.0.0.0/4", "2001:db8::/32"} {
		_, n, _ := net.ParseCIDR(cidr)
		if n.Contains(ip) {
			return false
		}
	}
	return true
}

func (m *Manager) diagnose(ctx context.Context, in Input) Report {
	ctx, cancel := context.WithTimeout(ctx, 12*time.Second)
	defer cancel()
	r := Report{At: m.opts.Now(), Ready: true, Checks: []Check{}, Records: []DNSRecord{}, Addresses: []string{}}
	add := func(name, state, detail string) {
		r.Checks = append(r.Checks, Check{Name: name, State: state, Detail: detail, At: r.At})
		if state == "fail" {
			r.Ready = false
		}
	}
	host := hostname(in.URL)
	for i, network := range []string{"ip4", "ip6"} {
		kind, expected := "A", in.IPv4
		if i == 1 {
			kind, expected = "AAAA", in.IPv6
		}
		if expected != "" && net.ParseIP(host) == nil {
			r.Records = append(r.Records, DNSRecord{Name: host, Type: kind, Value: expected})
		}
		ips, err := m.opts.Lookup(ctx, network, host)
		if err != nil || len(ips) == 0 {
			state := "unknown"
			if expected != "" {
				state = "fail"
			}
			detail := "未查询到记录；不等于该地址族可达"
			if err != nil {
				detail += "：" + err.Error()
			}
			add("DNS "+kind, state, detail)
			continue
		}
		values := []string{}
		matched := expected == ""
		safe := true
		for _, ip := range ips {
			values = append(values, ip.String())
			if !m.opts.allowPrivate && !publicIP(ip) {
				safe = false
			}
			if ip.String() == expected {
				matched = true
			}
			r.Addresses = append(r.Addresses, ip.String())
		}
		state := "pass"
		detail := strings.Join(values, ", ")
		if !safe {
			state = "fail"
			detail += "；不允许向非公网地址发起部署检查"
		} else if !matched {
			state = "fail"
			detail += "；与已确认入口不同，检查旧记录或代理 DNS"
			if in.ProxyDNS && in.Mode == "proxy" {
				state = "unknown"
				detail += "；已声明代理 DNS，源站地址需在代理控制台另行核对"
			}
		} else if expected == "" {
			state = "unknown"
			detail += "；未填写已确认入口，无法判断是否直连本机"
		}
		add("DNS "+kind, state, detail)
	}
	if len(r.Addresses) == 0 {
		add("地址解析", "fail", "没有可连接地址")
	}
	if in.Mode == "auto" {
		if in.IPv4 == "" && in.IPv6 == "" {
			add("公网入口", "fail", "请填写运营商提供且已确认的公网 IPv4 或 IPv6；网卡地址不代表公网入口")
		}
		for _, p := range []struct {
			name        string
			value, want int
		}{{"HTTP-01 公网端口", in.HTTPPort, 80}, {"HTTPS 公网端口", in.HTTPSPort, 443}} {
			state, detail := "pass", "用户填写的端口满足要求；公网实际连通仍待验证"
			if p.value == 0 {
				state = "unknown"
				detail = "映射未知，不能开始自动证书申请"
				r.Ready = false
			} else if p.value != p.want {
				state = "fail"
				detail = fmt.Sprintf("公网端口必须为 %d；高位端口映射到内网 %d 仍不满足条件", p.want, p.want)
			}
			add(p.name, state, detail)
		}
		m.mu.Lock()
		started := len(m.listeners) > 0
		handlerReady := m.handler != nil
		m.mu.Unlock()
		if !handlerReady {
			add("本机监听", "fail", "证书监听接口尚未接入运行中的服务")
		} else if started {
			add("本机监听", "pass", "本应用已持有挑战与 TLS 监听；不代表公网可达")
		} else {
			for _, addr := range []string{m.opts.ChallengeListen, m.opts.TLSListen} {
				ln, err := net.Listen("tcp", addr)
				if err != nil {
					add("本机监听 "+addr, "fail", err.Error())
				} else {
					_ = ln.Close()
					add("本机监听 "+addr, "pass", "当前可绑定；正式申请时会再次绑定，不更改其他服务")
				}
			}
		}
	}
	add("外部可达性", "unknown", "服务端自检无法证明独立公网路径；请从另一网络访问。HTTP-01 通过只证明 CA 能访问挑战端口")
	if in.ProxyDNS {
		add("代理 DNS", "unknown", "已声明代理 DNS；返回的可能是代理地址，TLS 与站点身份仍需实际验证")
	}
	r.Guide = guide(in, m.opts.Listen)
	return r
}

func guide(in Input, listen string) string {
	host := hostname(in.URL)
	text := "完整 DNS 名称：" + host + "\n请在管理该完整名称的 DNS 控制台添加记录。托管区边界未确认时，不猜测主机记录；按提供商要求填写完整名称或相对名称。\nA 仅填写已确认公网 IPv4；AAAA 仅填写已确认且可达的公网 IPv6。没有 IPv6 时不要添加 AAAA。代理 DNS 请使用已有反代模式，或关闭代理后再做直连检查。\n"
	if net.ParseIP(host) != nil {
		text = "当前使用 IP 入口：" + host + "，无需添加 DNS 记录。若要绑定域名，请改填域名完整访问地址，再根据已确认公网入口生成 DNS 指导。\n"
	}
	if in.Mode == "auto" {
		text += "直连自动 HTTPS：公网 TCP 80 必须转发到挑战监听，公网 TCP 443 必须转发到 TLS 监听。45002→80 之类的高位映射不满足 HTTP-01。无需让管理进程以 root 运行：可由部署阶段授予低端口能力，或将公网 80/443 映射到应用的高位监听（启动参数 --challenge-listen / --tls-listen）。\n"
	} else {
		text += "NAT：请填写外部实际可用的完整地址，例如 http://域名:映射端口；只映射高位端口时保留该端口。已有公网反代可在标准端口终止 TLS，再回源面板。不自动提供隧道。\n"
	}
	if listen == "" {
		listen = "127.0.0.1:8080"
	}
	if h, p, e := net.SplitHostPort(listen); e == nil && (h == "" || h == "0.0.0.0" || h == "::") {
		listen = net.JoinHostPort("127.0.0.1", p)
	}
	// Both domain and listen are validated as plain network addresses before
	// inclusion. This is a copyable sample, never executed by the application.
	if _, _, e := net.SplitHostPort(listen); e == nil && net.ParseIP(host) == nil {
		text += "\n已有反代示例（仅供复制，需按实际路径调整，不会自动修改服务）：\nCaddy:\n" + host + " {\n  reverse_proxy " + listen + "\n}\n\nNginx（放入已配置证书的 HTTPS server 块）：\nlocation / {\n  proxy_pass http://" + listen + ";\n  proxy_set_header Host $host;\n  proxy_set_header X-Forwarded-For $remote_addr;\n  proxy_set_header X-Forwarded-Proto $scheme;\n}\n"
	}
	return text + "\n可信代理：在“数据 → 用量与保护”中只添加实际代理源 IP/CIDR，并确保代理覆盖来访者提供的转发头。未信任来源的 X-Forwarded-* 不生效；主地址始终使用已验证配置。\n更换主域名会改变新分享地址；旧域名继续可用需要保留对应 DNS、证书及跳转。\n每次修改 DNS/NAT 后重新检查；超过 5 分钟的结果属于过期检查。"
}

// probe checks a random nonce over the requested entry, with full hostname and
// CA verification for HTTPS. Redirects and environment proxies are forbidden.
func (m *Manager) probe(ctx context.Context, raw string, addresses []string, expected *tls.Certificate) error {
	if len(addresses) == 0 {
		return errors.New("无可连接地址")
	}
	token := randomToken()
	m.mu.Lock()
	m.proof = token
	m.mu.Unlock()
	defer func() { m.mu.Lock(); m.proof = ""; m.mu.Unlock() }()
	u, _ := url.Parse(raw)
	port := u.Port()
	if port == "" {
		port = "80"
		if u.Scheme == "https" {
			port = "443"
		}
	}
	// Verify every published address, so a broken AAAA or stale extra A does
	// not become a silently intermittent canonical URL.
	for _, address := range addresses {
		if !m.opts.allowPrivate && !publicIP(net.ParseIP(address)) {
			return errors.New("非公网地址被拒绝")
		}
		tr := &http.Transport{TLSClientConfig: &tls.Config{MinVersion: tls.VersionTLS12, RootCAs: m.opts.Roots, ServerName: u.Hostname()},
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				if m.opts.probeDial != nil {
					return m.opts.probeDial(ctx, network, net.JoinHostPort(address, port))
				}
				return (&net.Dialer{Timeout: 8 * time.Second}).DialContext(ctx, network, net.JoinHostPort(address, port))
			}}
		client := &http.Client{Transport: tr, Timeout: 12 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("部署验证不接受重定向") }}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, raw+"/.well-known/filepanel/"+token, nil)
		res, err := client.Do(req)
		if err != nil {
			tr.CloseIdleConnections()
			return fmt.Errorf("%s: %w", address, err)
		}
		body, readErr := io.ReadAll(io.LimitReader(res.Body, 256))
		_ = res.Body.Close()
		tr.CloseIdleConnections()
		if readErr != nil || res.StatusCode != 200 || string(body) != token {
			return fmt.Errorf("%s 未返回当前面板的部署凭证", address)
		}
		if expected != nil && (res.TLS == nil || len(res.TLS.PeerCertificates) == 0 || sha256.Sum256(res.TLS.PeerCertificates[0].Raw) != sha256.Sum256(expected.Certificate[0])) {
			return errors.New("TLS 端点尚未部署本次证书")
		}
	}
	return nil
}

func (m *Manager) ensureListenersLocked() error {
	if len(m.listeners) > 0 {
		return nil
	}
	if m.handler == nil {
		return errors.New("服务监听接口未就绪")
	}
	httpLn, err := net.Listen("tcp", m.opts.ChallengeListen)
	if err != nil {
		return fmt.Errorf("挑战端口绑定失败（检查权限或其他服务）：%w", err)
	}
	tlsLn, err := net.Listen("tcp", m.opts.TLSListen)
	if err != nil {
		_ = httpLn.Close()
		return fmt.Errorf("TLS 端口绑定失败（检查权限或其他服务）：%w", err)
	}
	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12, GetCertificate: m.GetCertificate}
	challenge := &http.Server{Handler: http.HandlerFunc(m.challenge), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 30 * time.Second, MaxHeaderBytes: 16 << 10}
	https := &http.Server{Handler: m.handler, TLSConfig: tlsConfig, ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 1 << 20}
	m.listeners = []net.Listener{httpLn, tlsLn}
	m.servers = []*http.Server{challenge, https}
	m.listenerError = ""
	for i, ln := range m.listeners {
		server := m.servers[i]
		m.wg.Add(1)
		go func(i int, ln net.Listener) {
			defer m.wg.Done()
			var e error
			if i == 1 {
				e = server.Serve(tls.NewListener(ln, tlsConfig))
			} else {
				e = server.Serve(ln)
			}
			if e != nil && !errors.Is(e, http.ErrServerClosed) {
				m.mu.Lock()
				m.listenerError = "监听异常：" + e.Error()
				m.mu.Unlock()
			}
		}(i, ln)
	}
	return nil
}

func (m *Manager) challenge(w http.ResponseWriter, r *http.Request) {
	if r.Method != "GET" {
		http.NotFound(w, r)
		return
	}
	m.mu.Lock()
	value := m.challenges[hostname("http://"+r.Host)+r.URL.Path]
	m.mu.Unlock()
	if value == "" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/plain")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(value))
}
