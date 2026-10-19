package access

import (
	"context"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"errors"
	"filepanel/internal/files"
	"fmt"
	"net"
	"net/http"
	"runtime"
	"strings"
	"sync"
	"time"

	"filepanel/internal/store"
)

type Space interface{ CheckSpace(uint64) error }
type Options struct {
	Listen, TLSListen, ChallengeListen, DirectoryURL string
	Roots                                            *x509.CertPool
	Now                                              func() time.Time
	Lookup                                           func(context.Context, string, string) ([]net.IP, error)
	// Only package tests can relax public-address checks or promote a test CA.
	allowPrivate, promoteTestCA bool
	probeDial                   func(context.Context, string, string) (net.Conn, error)
}
type persisted struct {
	Active       *Active    `json:"active"`
	Candidate    *Candidate `json:"candidate"`
	Bundle       string     `json:"bundle,omitempty"` // private; never returned by View
	RenewalError string     `json:"renewal_error"`
	LastRenewal  time.Time  `json:"last_renewal"`
}
type Manager struct {
	db            *store.Store
	space         Space
	opts          Options
	mu            sync.Mutex
	state         persisted
	busy          bool
	cert          *tls.Certificate
	pending       *tls.Certificate
	pendingHost   string
	challenges    map[string]string
	proof         string
	handler       http.Handler
	servers       []*http.Server
	listeners     []net.Listener
	listenerError string
	ctx           context.Context
	cancel        context.CancelFunc
	wg            sync.WaitGroup
}

func New(db *store.Store, space Space, opts Options) (*Manager, error) {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Lookup == nil {
		opts.Lookup = net.DefaultResolver.LookupIP
	}
	if opts.TLSListen == "" {
		opts.TLSListen = ":443"
	}
	if opts.ChallengeListen == "" {
		opts.ChallengeListen = ":80"
	}
	if opts.DirectoryURL == "" {
		opts.DirectoryURL = productionDirectory
	}
	ctx, cancel := context.WithCancel(context.Background())
	m := &Manager{db: db, space: space, opts: opts, challenges: map[string]string{}, ctx: ctx, cancel: cancel}
	raw, ok, err := db.Setting(settingKey)
	if err != nil {
		cancel()
		return nil, err
	}
	if ok {
		if err = json.Unmarshal([]byte(raw), &m.state); err != nil {
			cancel()
			return nil, fmt.Errorf("读取访问地址配置: %w", err)
		}
	}
	if m.state.Candidate != nil && m.state.Candidate.Stage == "working" {
		m.state.Candidate.Stage = "failed"
		m.state.Candidate.Error = "上次操作被重启中断；旧入口保留，请重新检查"
	}
	if m.state.Bundle != "" {
		c, err := parseBundle(m.state.Bundle)
		if err != nil {
			cancel()
			return nil, fmt.Errorf("读取当前 TLS 证书: %w", err)
		}
		m.cert = c
	}
	return m, nil
}

// Start attaches application handling before opening optional listeners. Failure
// of TLS never removes the original HTTP management listener.
func (m *Manager) Start(handler http.Handler) {
	m.mu.Lock()
	m.handler = handler
	if m.state.Bundle != "" {
		if err := m.ensureListenersLocked(); err != nil {
			m.listenerError = err.Error()
		}
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		ticker := time.NewTicker(time.Hour)
		defer ticker.Stop()
		m.Renew(m.ctx)
		for {
			select {
			case <-ticker.C:
				m.Renew(m.ctx)
			case <-m.ctx.Done():
				return
			}
		}
	}()
}

func (m *Manager) Close() {
	m.cancel()
	m.mu.Lock()
	for _, s := range m.servers {
		_ = s.Close()
	}
	m.mu.Unlock()
	m.wg.Wait()
}

func (m *Manager) saveLocked() error {
	raw, err := json.Marshal(m.state)
	if err != nil {
		return err
	}
	release, err := files.HoldSpace(m.space, uint64(len(raw)*2+65536))
	if err != nil {
		return err
	}
	defer release()
	return m.db.SetSetting(settingKey, string(raw))
}

func (m *Manager) BaseURL() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.state.Active != nil {
		return m.state.Active.URL
	}
	return ""
}
func (m *Manager) URL(path string) string { return m.BaseURL() + path }

func (m *Manager) View() View {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Detach the view from mutable state, and exclude all private material.
	raw, _ := json.Marshal(m.state)
	var state persisted
	_ = json.Unmarshal(raw, &state)
	v := View{Active: state.Active, Candidate: state.Candidate, Busy: m.busy, RenewalError: state.RenewalError, LastRenewal: state.LastRenewal,
		Staging: m.staging(), Environment: m.environmentLocked(), Warning: m.listenerError}
	if v.Candidate != nil {
		age := m.opts.Now().Sub(v.Candidate.Report.At)
		v.Stale = age > freshness || age < 0
	}
	if v.Active != nil && v.Active.Mode == "auto" && v.Active.Expires.Sub(m.opts.Now()) < 30*24*time.Hour {
		v.Warning += " 证书将在 30 天内到期或已经过期，请检查续期与公网 80 可达性。"
	}
	return v
}

func (m *Manager) environmentLocked() Environment {
	e := Environment{OS: runtime.GOOS, Arch: runtime.GOARCH, Listen: m.opts.Listen, TLSListen: m.opts.TLSListen, ChallengeListen: m.opts.ChallengeListen, IPv4: []string{}, IPv6: []string{}}
	addresses, _ := net.InterfaceAddrs()
	for _, a := range addresses {
		ip, _, err := net.ParseCIDR(a.String())
		if err != nil || ip.IsLoopback() {
			continue
		}
		if ip.To4() != nil {
			e.IPv4 = append(e.IPv4, ip.String())
		} else {
			e.IPv6 = append(e.IPv6, ip.String())
		}
	}
	return e
}

func (m *Manager) staging() bool {
	return m.opts.DirectoryURL != productionDirectory && !m.opts.promoteTestCA
}

func (m *Manager) Check(ctx context.Context, in Input) (View, error) {
	in, err := in.normalize()
	if err != nil {
		return View{}, err
	}
	if !m.begin() {
		return View{}, errors.New("已有检查、绑定或续期正在进行")
	}
	r := m.diagnose(ctx, in)
	m.mu.Lock()
	old := m.state.Candidate
	m.state.Candidate = &Candidate{Input: in, Stage: "checked", Report: r}
	err = m.saveLocked()
	if err != nil {
		m.state.Candidate = old
	}
	m.mu.Unlock()
	m.end()
	return m.View(), err
}

func (m *Manager) begin() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.busy || m.ctx.Err() != nil {
		return false
	}
	m.busy = true
	return true
}
func (m *Manager) end() { m.mu.Lock(); m.busy = false; m.mu.Unlock() }

// Apply acknowledges quickly; all network/CA work is bounded and observable via View.
func (m *Manager) Apply() error {
	if !m.begin() {
		return errors.New("已有检查、绑定或续期正在进行")
	}
	m.mu.Lock()
	if m.state.Candidate == nil {
		m.busy = false
		m.mu.Unlock()
		return errors.New("请先检查候选地址")
	}
	in := m.state.Candidate.Input
	m.state.Candidate.Stage = "working"
	m.state.Candidate.Error = ""
	if err := m.saveLocked(); err != nil {
		m.state.Candidate.Stage = "failed"
		m.busy = false
		m.mu.Unlock()
		return err
	}
	m.wg.Add(1)
	m.mu.Unlock()
	go func() {
		defer m.wg.Done()
		defer m.end()
		ctx, cancel := context.WithTimeout(m.ctx, 4*time.Minute)
		defer cancel()
		err := m.activate(ctx, in)
		m.mu.Lock()
		defer m.mu.Unlock()
		if err != nil {
			m.state.Candidate.Stage = "failed"
			m.state.Candidate.Error = err.Error()
		}
		if e := m.saveLocked(); e != nil {
			m.state.Candidate.Stage = "failed"
			m.state.Candidate.Error = "保存操作结果失败：" + e.Error()
		}
	}()
	return nil
}

func (m *Manager) activate(ctx context.Context, in Input) error {
	r := m.diagnose(ctx, in)
	m.mu.Lock()
	m.state.Candidate.Report = r
	m.mu.Unlock()
	if err := r.requireReady(); err != nil {
		return err
	}
	var cert *tls.Certificate
	var bundle string
	var err error
	if in.Mode == "auto" {
		if !in.AcceptTerms {
			return errors.New("请先同意证书颁发机构的服务条款")
		}
		m.mu.Lock()
		err = m.ensureListenersLocked()
		m.mu.Unlock()
		if err != nil {
			return err
		}
		bundle, err = m.issue(ctx, in)
		if err != nil {
			return fmt.Errorf("HTTP-01 证书申请失败：%w", err)
		}
		cert, err = parseBundle(bundle)
		if err != nil {
			return err
		}
		m.mu.Lock()
		m.pending = cert
		m.pendingHost = hostname(in.URL)
		m.mu.Unlock()
		defer func() { m.mu.Lock(); m.pending = nil; m.pendingHost = ""; m.mu.Unlock() }()
	}
	if err = m.probe(ctx, in.URL, r.Addresses, cert); err != nil {
		return fmt.Errorf("验证部署失败，旧入口保留：%w", err)
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.staging() && in.Mode == "auto" {
		m.state.Candidate.Stage = "staging_verified"
		return nil
	}
	old := m.state
	m.state.Active = &Active{Input: in, VerifiedAt: m.opts.Now()}
	if cert != nil {
		m.state.Active.Expires = cert.Leaf.NotAfter
	}
	if cert != nil {
		m.state.Bundle = bundle
	}
	m.state.RenewalError = ""
	m.state.Candidate.Stage = "enabled"
	if err = m.saveLocked(); err != nil {
		m.state = old
		return err
	}
	if cert != nil {
		m.cert = cert
	}
	return nil
}

func (m *Manager) Renew(ctx context.Context) {
	if !m.begin() {
		return
	}
	defer m.end()
	m.mu.Lock()
	a := m.state.Active
	if a == nil || a.Mode != "auto" || a.Expires.Sub(m.opts.Now()) > 30*24*time.Hour {
		m.mu.Unlock()
		return
	}
	in := a.Input
	m.mu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	bundle, err := m.issue(ctx, in)
	var cert *tls.Certificate
	if err == nil {
		cert, err = parseBundle(bundle)
	}
	if err == nil {
		m.mu.Lock()
		m.pending = cert
		m.pendingHost = hostname(in.URL)
		m.mu.Unlock()
		defer func() { m.mu.Lock(); m.pending = nil; m.pendingHost = ""; m.mu.Unlock() }()
		r := m.diagnose(ctx, in)
		err = r.requireReady()
		if err == nil {
			err = m.probe(ctx, in.URL, r.Addresses, cert)
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	old := m.state
	m.state.LastRenewal = m.opts.Now()
	if err != nil {
		m.state.RenewalError = err.Error()
		_ = m.saveLocked()
		return
	}
	next := *m.state.Active
	next.Expires = cert.Leaf.NotAfter
	next.VerifiedAt = m.opts.Now()
	m.state.Active = &next
	m.state.Bundle = bundle
	m.state.RenewalError = ""
	if err = m.saveLocked(); err != nil {
		m.state = old
		m.state.RenewalError = "保存续期证书失败：" + err.Error()
		return
	}
	m.cert = cert
}

func parseBundle(bundle string) (*tls.Certificate, error) {
	c, err := tls.X509KeyPair([]byte(bundle), []byte(bundle))
	if err != nil {
		return nil, err
	}
	c.Leaf, err = x509.ParseCertificate(c.Certificate[0])
	return &c, err
}
func randomToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b[:])
}

// Proof is public but exists only during a bounded deployment verification.
func (m *Manager) Proof(w http.ResponseWriter, r *http.Request) {
	m.mu.Lock()
	token := m.proof
	m.mu.Unlock()
	if token == "" || r.URL.Path != "/.well-known/filepanel/"+token {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(token))
}

func (m *Manager) GetCertificate(hello *tls.ClientHelloInfo) (*tls.Certificate, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.pending != nil && strings.EqualFold(hello.ServerName, m.pendingHost) {
		return m.pending, nil
	}
	if m.cert != nil && m.cert.Leaf.VerifyHostname(hello.ServerName) == nil {
		return m.cert, nil
	}
	return nil, errors.New("该域名尚无已验证证书")
}
