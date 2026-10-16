package access

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"filepanel/internal/store"
)

type testSpace struct{ fail bool }

func (s *testSpace) CheckSpace(uint64) error {
	if s.fail {
		return errors.New("injected low space")
	}
	return nil
}

// A local ACME v2 authority exercises real JWS, HTTP-01, CSR signing and TLS.
// It is deliberately not a public CA and never uses the user's domain.
type testCA struct {
	mu                         sync.Mutex
	server                     *httptest.Server
	root                       *x509.Certificate
	key                        *ecdsa.PrivateKey
	account                    *ecdsa.PublicKey
	validated, finalized, fail bool
	cert                       []byte
	serial                     int64
	challengeAddr              func() string
	now                        func() time.Time
	requests, challenges       int
}

func newCA(t *testing.T, now func() time.Time) *testCA {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "FilePanel local test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(365 * 24 * time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	root, _ := x509.ParseCertificate(der)
	ca := &testCA{root: root, key: key, now: now, serial: 1}
	ca.server = httptest.NewServer(http.HandlerFunc(ca.serve))
	t.Cleanup(ca.server.Close)
	return ca
}

func (ca *testCA) serve(w http.ResponseWriter, r *http.Request) {
	ca.mu.Lock()
	defer ca.mu.Unlock()
	ca.requests++
	base := ca.server.URL
	w.Header().Set("Replay-Nonce", randomToken())
	w.Header().Set("Content-Type", "application/json")
	write := func(code int, v any) { w.WriteHeader(code); _ = json.NewEncoder(w).Encode(v) }
	if r.URL.Path == "/dir" {
		write(200, map[string]any{"newNonce": base + "/nonce", "newAccount": base + "/account", "newOrder": base + "/new-order", "meta": map[string]string{"termsOfService": base + "/tos"}})
		return
	}
	if r.URL.Path == "/nonce" {
		w.WriteHeader(200)
		return
	}
	if ca.fail {
		write(400, map[string]string{"type": "urn:ietf:params:acme:error:connection", "detail": "injected CA failure"})
		return
	}
	var jws struct{ Protected, Payload, Signature string }
	if json.NewDecoder(r.Body).Decode(&jws) != nil {
		write(400, map[string]string{"detail": "invalid JWS"})
		return
	}
	protected, err := base64.RawURLEncoding.DecodeString(jws.Protected)
	if err != nil {
		http.Error(w, "header", 400)
		return
	}
	var header struct {
		URL, Nonce, Alg, Kid string
		Jwk                  struct{ X, Y string }
	}
	if json.Unmarshal(protected, &header) != nil || header.URL != base+r.URL.Path || header.Nonce == "" || header.Alg != "ES256" {
		http.Error(w, "bad protected header", 400)
		return
	}
	if header.Jwk.X != "" {
		x, _ := base64.RawURLEncoding.DecodeString(header.Jwk.X)
		y, _ := base64.RawURLEncoding.DecodeString(header.Jwk.Y)
		pub := &ecdsa.PublicKey{Curve: elliptic.P256(), X: new(big.Int).SetBytes(x), Y: new(big.Int).SetBytes(y)}
		if !pub.Curve.IsOnCurve(pub.X, pub.Y) {
			http.Error(w, "bad JWK", 400)
			return
		}
		if ca.account != nil && !ca.account.Equal(pub) {
			http.Error(w, "account key changed", 400)
			return
		}
		ca.account = pub
	}
	sig, _ := base64.RawURLEncoding.DecodeString(jws.Signature)
	sum := sha256.Sum256([]byte(jws.Protected + "." + jws.Payload))
	if ca.account == nil || len(sig) != 64 || !ecdsa.Verify(ca.account, sum[:], new(big.Int).SetBytes(sig[:32]), new(big.Int).SetBytes(sig[32:])) {
		http.Error(w, "bad signature", 400)
		return
	}
	payload, _ := base64.RawURLEncoding.DecodeString(jws.Payload)
	order := func() map[string]any {
		status := "pending"
		if ca.validated {
			status = "ready"
		}
		if ca.finalized {
			status = "valid"
		}
		return map[string]any{"status": status, "identifiers": []map[string]string{{"type": "dns", "value": "panel.example"}}, "authorizations": []string{base + "/auth"}, "finalize": base + "/finalize", "certificate": base + "/cert"}
	}
	switch r.URL.Path {
	case "/account":
		w.Header().Set("Location", base+"/account/1")
		write(201, map[string]string{"status": "valid"})
	case "/new-order":
		ca.validated = false
		ca.finalized = false
		ca.cert = nil
		w.Header().Set("Location", base+"/order")
		write(201, order())
	case "/auth":
		status := "pending"
		if ca.validated {
			status = "valid"
		}
		write(200, map[string]any{"status": status, "identifier": map[string]string{"type": "dns", "value": "panel.example"}, "challenges": []map[string]string{{"type": "http-01", "url": base + "/challenge", "status": status, "token": "local-token"}}})
	case "/challenge":
		if ca.challengeAddr == nil {
			http.Error(w, "no challenge listener", 500)
			return
		}
		req, _ := http.NewRequest("GET", "http://"+ca.challengeAddr()+"/.well-known/acme-challenge/local-token", nil)
		req.Host = "panel.example"
		resp, err := (&http.Client{Timeout: 3 * time.Second}).Do(req)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		body, _ := io.ReadAll(resp.Body)
		_ = resp.Body.Close()
		// RFC 7638 JWK thumbprint in the key authorization.
		b64 := func(v *big.Int) string { return base64.RawURLEncoding.EncodeToString(v.FillBytes(make([]byte, 32))) }
		thumb := sha256.Sum256([]byte(`{"crv":"P-256","kty":"EC","x":"` + b64(ca.account.X) + `","y":"` + b64(ca.account.Y) + `"}`))
		if string(body) != "local-token."+base64.RawURLEncoding.EncodeToString(thumb[:]) || resp.StatusCode != 200 {
			http.Error(w, "HTTP-01 proof mismatch", 400)
			return
		}
		ca.validated = true
		ca.challenges++
		write(200, map[string]string{"type": "http-01", "status": "valid", "url": base + "/challenge", "token": "local-token"})
	case "/order":
		write(200, order())
	case "/finalize":
		if !ca.validated {
			http.Error(w, "unauthorized", 400)
			return
		}
		var in struct{ CSR string }
		_ = json.Unmarshal(payload, &in)
		csrBytes, _ := base64.RawURLEncoding.DecodeString(in.CSR)
		csr, err := x509.ParseCertificateRequest(csrBytes)
		if err != nil || csr.CheckSignature() != nil || strings.Join(csr.DNSNames, ",") != "panel.example" {
			http.Error(w, "bad CSR", 400)
			return
		}
		ca.serial++
		tmpl := &x509.Certificate{SerialNumber: big.NewInt(ca.serial), DNSNames: csr.DNSNames, NotBefore: time.Now().Add(-time.Hour), NotAfter: ca.now().Add(90 * 24 * time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca.root, csr.PublicKey, ca.key)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		ca.cert = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
		ca.cert = append(ca.cert, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: ca.root.Raw})...)
		ca.finalized = true
		w.Header().Set("Location", base+"/order")
		write(200, order())
	case "/cert":
		w.Header().Set("Content-Type", "application/pem-certificate-chain")
		_, _ = w.Write(ca.cert)
	default:
		http.NotFound(w, r)
	}
}

func setupAccess(t *testing.T, promote bool) (*Manager, *testCA, *testSpace, *time.Time) {
	t.Helper()
	now := time.Now()
	ca := newCA(t, func() time.Time { return now })
	root := t.TempDir()
	db, err := store.Open(root)
	if err != nil {
		t.Fatal(err)
	}
	space := &testSpace{}
	roots := x509.NewCertPool()
	roots.AddCert(ca.root)
	opts := Options{Listen: "127.0.0.1:8080", TLSListen: "127.0.0.1:0", ChallengeListen: "127.0.0.1:0", DirectoryURL: ca.server.URL + "/dir", Roots: roots, Now: func() time.Time { return now }, allowPrivate: true, promoteTestCA: promote,
		Lookup: func(ctx context.Context, network, host string) ([]net.IP, error) {
			if network == "ip4" {
				return []net.IP{net.ParseIP("127.0.0.1")}, nil
			}
			return nil, nil
		}}
	m, err := New(db, space, opts)
	if err != nil {
		t.Fatal(err)
	}
	m.opts.probeDial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		m.mu.Lock()
		addr := m.listeners[1].Addr().String()
		m.mu.Unlock()
		return (&net.Dialer{}).DialContext(ctx, network, addr)
	}
	ca.challengeAddr = func() string { m.mu.Lock(); defer m.mu.Unlock(); return m.listeners[0].Addr().String() }
	mux := http.NewServeMux()
	mux.HandleFunc("GET /.well-known/filepanel/{token}", m.Proof)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("ok")) })
	m.Start(mux)
	t.Cleanup(func() { m.Close(); _ = db.Close() })
	return m, ca, space, &now
}

func autoInput() Input {
	return Input{Mode: "auto", URL: "https://panel.example", IPv4: "127.0.0.1", HTTPPort: 80, HTTPSPort: 443, AcceptTerms: true}
}
func applyWait(t *testing.T, m *Manager) {
	t.Helper()
	if err := m.Apply(); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(8 * time.Second)
	for m.View().Busy {
		if time.Now().After(deadline) {
			t.Fatal("operation timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
}
func checkInput(t *testing.T, m *Manager, in Input) View {
	t.Helper()
	v, err := m.Check(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	return v
}
func peerCert(t *testing.T, m *Manager) *x509.Certificate {
	t.Helper()
	m.mu.Lock()
	addr := m.listeners[1].Addr().String()
	m.mu.Unlock()
	conn, err := tls.Dial("tcp", addr, &tls.Config{ServerName: "panel.example", RootCAs: m.opts.Roots})
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	return conn.ConnectionState().PeerCertificates[0]
}

func TestACMEActivationRenewalAndRecovery(t *testing.T) {
	m, ca, space, now := setupAccess(t, true)
	in := autoInput()
	checkInput(t, m, in)
	applyWait(t, m)
	v := m.View()
	if v.Active == nil || v.Active.URL != in.URL || v.Candidate.Stage != "enabled" {
		t.Fatalf("activation: %+v", v)
	}
	first := peerCert(t, m)
	m.mu.Lock()
	firstBundle := m.state.Bundle
	m.mu.Unlock()
	jsonView, _ := json.Marshal(v)
	if strings.Contains(string(jsonView), "PRIVATE KEY") || strings.Contains(string(jsonView), "bundle") {
		t.Fatal("private key exposed")
	}
	// Restart reads the same durable canonical entry and TLS material.
	restarted, err := New(m.db, space, m.opts)
	if err != nil {
		t.Fatal(err)
	}
	if restarted.BaseURL() != in.URL || restarted.cert.Leaf.SerialNumber.Cmp(first.SerialNumber) != 0 {
		t.Fatal("restart lost active entry")
	}
	restarted.Close()
	// No premature renewal, then a clock-driven successful renewal.
	ca.mu.Lock()
	count := ca.requests
	ca.mu.Unlock()
	m.Renew(context.Background())
	ca.mu.Lock()
	same := count == ca.requests
	ca.mu.Unlock()
	if !same {
		t.Fatal("renewed too early")
	}
	*now = now.Add(65 * 24 * time.Hour)
	m.Renew(context.Background())
	second := peerCert(t, m)
	if second.SerialNumber.Cmp(first.SerialNumber) == 0 || !second.NotAfter.After(first.NotAfter) {
		t.Fatalf("did not renew: %s", m.View().RenewalError)
	}
	m.mu.Lock()
	renewedBundle := m.state.Bundle
	m.mu.Unlock()
	if renewedBundle == firstBundle {
		t.Fatal("renewal not persisted")
	}
	// A failing CA never replaces a still-serving certificate or canonical URL.
	*now = now.Add(65 * 24 * time.Hour)
	ca.mu.Lock()
	ca.fail = true
	ca.mu.Unlock()
	m.Renew(context.Background())
	if m.View().RenewalError == "" || m.View().Warning == "" {
		t.Fatal("renewal failure not warned")
	}
	if peerCert(t, m).SerialNumber.Cmp(second.SerialNumber) != 0 {
		t.Fatal("failure replaced certificate")
	}
	m.mu.Lock()
	same = renewedBundle == m.state.Bundle
	m.mu.Unlock()
	if !same {
		t.Fatal("failed renewal changed durable key")
	}
	// A later candidate failure preserves the old entry.
	checkInput(t, m, in)
	applyWait(t, m)
	if m.View().Candidate.Stage != "failed" || m.BaseURL() != in.URL {
		t.Fatal("failed candidate changed entry")
	}
	ca.mu.Lock()
	ca.fail = false
	challenges := ca.challenges
	ca.mu.Unlock()
	if challenges != 2 {
		t.Fatalf("actual HTTP-01 validations: %d", challenges)
	}
	// TLS is still live and its certificate/key never appear in an API view.
	_ = peerCert(t, m)
}

func TestDeploymentMismatchAndCommitFailurePreserveActiveCertificate(t *testing.T) {
	m, _, space, _ := setupAccess(t, true)
	in := autoInput()
	checkInput(t, m, in)
	applyWait(t, m)
	first := peerCert(t, m)
	originalDial := m.opts.probeDial
	// The CA issues successfully, but the requested entry still serves the old
	// certificate. Merely obtaining a certificate must never promote a candidate.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	m.mu.Lock()
	oldCert := m.cert
	oldBundle := m.state.Bundle
	m.mu.Unlock()
	server := &http.Server{Handler: http.HandlerFunc(m.Proof)}
	go func() { _ = server.Serve(tls.NewListener(ln, &tls.Config{Certificates: []tls.Certificate{*oldCert}})) }()
	defer server.Close()
	m.opts.probeDial = func(ctx context.Context, network, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, network, ln.Addr().String())
	}
	checkInput(t, m, in)
	applyWait(t, m)
	if m.View().Candidate.Stage != "failed" || !strings.Contains(m.View().Candidate.Error, "本次证书") {
		t.Fatalf("mismatch not rejected: %+v", m.View())
	}
	if peerCert(t, m).SerialNumber.Cmp(first.SerialNumber) != 0 || m.BaseURL() != in.URL {
		t.Fatal("deployment mismatch replaced entry")
	}
	m.opts.probeDial = func(ctx context.Context, network, address string) (net.Conn, error) {
		conn, err := originalDial(ctx, network, address)
		if err == nil {
			space.fail = true
		}
		return conn, err
	}
	checkInput(t, m, in)
	applyWait(t, m)
	space.fail = false
	m.mu.Lock()
	same := m.state.Bundle == oldBundle
	m.mu.Unlock()
	if !same || peerCert(t, m).SerialNumber.Cmp(first.SerialNumber) != 0 || m.View().Candidate.Stage != "failed" {
		t.Fatal("failed commit replaced certificate")
	}
}

func TestStagingDoesNotPromoteAndUnknownNATDoesNotIssue(t *testing.T) {
	m, ca, _, _ := setupAccess(t, false)
	in := autoInput()
	in.HTTPPort = 45002
	in.HTTPSPort = 45001
	v := checkInput(t, m, in)
	if v.Candidate.Report.Ready {
		t.Fatal("high NAT ports accepted")
	}
	applyWait(t, m)
	if m.BaseURL() != "" {
		t.Fatal("NAT failure promoted candidate")
	}
	ca.mu.Lock()
	requests := ca.requests
	ca.mu.Unlock()
	if requests != 0 {
		t.Fatal("contacted CA before NAT checks")
	}
	in.HTTPPort = 0
	v = checkInput(t, m, in)
	if v.Candidate.Report.Ready {
		t.Fatal("unknown NAT accepted")
	}
	in = autoInput()
	checkInput(t, m, in)
	applyWait(t, m)
	if m.View().Candidate.Stage != "staging_verified" || m.BaseURL() != "" {
		t.Fatalf("staging promoted or failed: %+v", m.View())
	}
}

func TestDNSPortConflictStalenessAndLowSpace(t *testing.T) {
	m, _, space, now := setupAccess(t, true)
	in := autoInput()
	m.opts.Lookup = func(context.Context, string, string) ([]net.IP, error) {
		return nil, errors.New("injected DNS failure")
	}
	if checkInput(t, m, in).Candidate.Report.Ready {
		t.Fatal("DNS error accepted")
	}
	applyWait(t, m)
	if m.BaseURL() != "" {
		t.Fatal("DNS failure promoted")
	}
	m.opts.Lookup = func(ctx context.Context, network, host string) ([]net.IP, error) {
		if network == "ip4" {
			return []net.IP{net.ParseIP("127.0.0.1")}, nil
		}
		return nil, nil
	}
	conflict, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer conflict.Close()
	m.opts.ChallengeListen = conflict.Addr().String()
	if checkInput(t, m, in).Candidate.Report.Ready {
		t.Fatal("occupied port accepted")
	}
	m.opts.ChallengeListen = "127.0.0.1:0"
	checkInput(t, m, in)
	*now = now.Add(6 * time.Minute)
	if !m.View().Stale {
		t.Fatal("stale check reported fresh")
	}
	space.fail = true
	in.URL = "https://other.example"
	if _, err = m.Check(context.Background(), in); err == nil {
		t.Fatal("low space accepted")
	}
	if m.View().Candidate.URL == in.URL {
		t.Fatal("failed persistence replaced candidate")
	}
}

func TestProxyDNSAndManualHTTPSUseVerifiedEntry(t *testing.T) {
	m, _, _, _ := setupAccess(t, true)
	in := autoInput()
	checkInput(t, m, in)
	applyWait(t, m)
	in.Mode = "proxy"
	in.ProxyDNS = true
	in.IPv4 = "104.251.231.123"
	v := checkInput(t, m, in)
	if !v.Candidate.Report.Ready {
		t.Fatalf("proxy DNS incorrectly treated as origin mismatch: %+v", v)
	}
	applyWait(t, m)
	if m.View().Active.Mode != "proxy" || m.View().Candidate.Stage != "enabled" {
		t.Fatalf("proxy entry not verified: %+v", m.View())
	}
	_ = peerCert(t, m)
	in.Mode = "manual"
	in.ProxyDNS = false
	in.IPv4 = "127.0.0.1"
	checkInput(t, m, in)
	applyWait(t, m)
	if m.View().Active.Mode != "manual" {
		t.Fatal("manual HTTPS not verified")
	}
	_ = peerCert(t, m)
}

func TestPrivateTargetsAndUnsafeURLsRejected(t *testing.T) {
	for _, raw := range []string{"https://user:pass@example.com", "https://example.com/path", "https://example.com/?secret=x", "file:///etc/passwd", "https://bad_domain.example", "https://example.com:70000", "https://example.com/#fragment"} {
		if _, err := (Input{Mode: "manual", URL: raw}).normalize(); err == nil {
			t.Errorf("unsafe URL accepted: %s", raw)
		}
	}
	for _, value := range []string{"127.0.0.1", "169.254.169.254", "10.0.0.1", "198.18.0.1", "100.64.0.1", "::1", "fc00::1", "2001:db8::1", "::ffff:192.168.1.1"} {
		if publicIP(net.ParseIP(value)) {
			t.Errorf("non-public destination accepted: %s", value)
		}
	}
	m, _, _, _ := setupAccess(t, true)
	m.opts.allowPrivate = false
	v := checkInput(t, m, Input{Mode: "manual", URL: "http://127.0.0.1:8080"})
	if v.Candidate.Report.Ready {
		t.Fatal("private manual probe allowed")
	}
}
