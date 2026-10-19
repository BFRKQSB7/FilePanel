package main

import (
	"bytes"
	"crypto/sha256"
	"crypto/x509"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/cookiejar"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"testing"
	"time"

	"filepanel/internal/files"
	"filepanel/internal/nodes"
	"filepanel/internal/share"
)

// Opt-in, destructive only to explicitly named T07 isolated instances. Run this
// Linux test binary on the development machine, never on the 128MB test VPS.
// SSH forwards exercise real installed services without changing firewall/DNS.
func TestInstalledLifecycle(t *testing.T) {
	if os.Getenv("FILEPANEL_INSTALLED_SMOKE") != "1" {
		t.Skip("opt-in installed service lifecycle")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("run this smoke in the Debian subsystem")
	}
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	host := os.Getenv("FILEPANEL_INSTALL_SSH_HOST")
	panelRemote := os.Getenv("FILEPANEL_INSTALL_PANEL_REMOTE") == "1"
	var sshArgs []string
	if host != "" {
		sshArgs = []string{"-F", envOr("FILEPANEL_INSTALL_SSH_CONFIG", "/dev/null"), "-i", os.Getenv("FILEPANEL_INSTALL_SSH_KEY"), "-p", envOr("FILEPANEL_INSTALL_SSH_PORT", "22"), "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + os.Getenv("FILEPANEL_INSTALL_KNOWN_HOSTS")}
	}
	shell := func(remote bool, command string, input []byte) string {
		t.Helper()
		var c *exec.Cmd
		if remote {
			if !strings.HasPrefix(host, "root@") && os.Getenv("FILEPANEL_INSTALL_SSH_ROOT") != "1" {
				command = "sudo -n sh -c " + quote(command)
			}
			c = exec.Command("ssh", append(append([]string{}, sshArgs...), host, command)...)
		} else {
			c = exec.Command("sh", "-c", command)
		}
		c.Stdin = bytes.NewReader(input)
		out, e := c.CombinedOutput()
		if e != nil {
			t.Fatalf("isolated operation failed: %v\n%s", e, out)
		}
		return string(out)
	}
	localPort := func() string {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		if e != nil {
			t.Fatal(e)
		}
		a := strings.Split(l.Addr().String(), ":")[1]
		l.Close()
		return a
	}
	panelAddr := "127.0.0.1:19870"
	panelControlAddr := panelAddr
	nodeAddr := "127.0.0.1:19872"
	if panelRemote {
		panelAddr = "127.0.0.1:" + localPort()
		panelControlAddr = "127.0.0.1:" + localPort()
	}
	if host != "" {
		nodeAddr = "127.0.0.1:" + localPort()
	}
	proxy := func(addr string) *httptest.Server {
		u, _ := url.Parse("http://" + addr)
		p := httputil.NewSingleHostReverseProxy(u)
		p.ModifyResponse = func(r *http.Response) error {
			if r.StatusCode >= 400 {
				t.Logf("proxy %s %s %s status=%d", addr, r.Request.Method, r.Request.URL.Path, r.StatusCode)
			}
			return nil
		}
		return httptest.NewTLSServer(p)
	}
	panelTLS := proxy(panelControlAddr)
	defer panelTLS.Close()
	nodeTLS := proxy(nodeAddr)
	defer nodeTLS.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: panelTLS.Certificate().Raw}))
	nodeCA := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: nodeTLS.Certificate().Raw}))
	if host != "" {
		links := [][]string{{"-L", nodeAddr + ":127.0.0.1:19872"}}
		if panelRemote {
			links = append(links, []string{"-L", panelAddr + ":127.0.0.1:19870"}, []string{"-L", panelControlAddr + ":127.0.0.1:19870"})
		}
		for _, s := range []*httptest.Server{panelTLS, nodeTLS} {
			port := strings.Split(s.Listener.Addr().String(), ":")[1]
			links = append(links, []string{"-R", "127.0.0.1:" + port + ":127.0.0.1:" + port})
		}
		// Separate SSH transports keep bulk fixture traffic from starving the
		// 5-second authorization control requests through one TCP connection.
		for _, link := range links {
			args := append([]string{}, sshArgs...)
			args = append(args, "-N", "-o", "ExitOnForwardFailure=yes")
			args = append(args, link...)
			args = append(args, host)
			tunnel := exec.Command("ssh", args...)
			tunnel.WaitDelay = 2 * time.Second
			var log bytes.Buffer
			tunnel.Stderr = &log
			if e := tunnel.Start(); e != nil {
				t.Fatal(e)
			}
			defer func() { tunnel.Process.Kill(); tunnel.Wait() }()
		}
		time.Sleep(time.Second)
	}
	fixture := "/opt/filepanel-t07-fixture"
	arch := func(remote bool) string {
		if strings.Contains(shell(remote, "uname -m", nil), "aarch64") {
			return "arm64"
		}
		return "amd64"
	}
	binary := func(remote bool) string { return fixture + "/filepanel-linux-" + arch(remote) }
	pb, nb := binary(panelRemote), binary(host != "")
	install := func(remote bool, bin, id, extra string) {
		pin := strings.Fields(shell(remote, "sha256sum "+fixture+"/release.json", nil))[0]
		shell(remote, bin+" install --instance "+id+" --release "+fixture+"/release.json --manifest-sha256 "+pin+" "+extra, nil)
	}
	if !strings.Contains(shell(panelRemote, "test -e /etc/filepanel/t07-panel.json && echo exists || echo missing", nil), "exists") {
		install(panelRemote, pb, "t07-panel", "--listen 127.0.0.1:19870")
	}
	jar, _ := cookiejar.New(nil)
	client := &http.Client{Jar: jar, Timeout: 90 * time.Second}
	base := "http://" + panelAddr
	csrf := ""
	api := func(method, path string, v any, out any) {
		t.Helper()
		var body io.Reader
		if v != nil {
			b, _ := json.Marshal(v)
			body = bytes.NewReader(b)
		}
		r, _ := http.NewRequest(method, base+path, body)
		r.Header.Set("Content-Type", "application/json")
		r.Header.Set("X-CSRF-Token", csrf)
		res, e := client.Do(r)
		if e != nil {
			t.Fatal(e)
		}
		defer res.Body.Close()
		if res.StatusCode >= 300 {
			b, _ := io.ReadAll(res.Body)
			t.Fatalf("%s %s: %d %s", method, path, res.StatusCode, b)
		}
		if out != nil {
			if e = json.NewDecoder(res.Body).Decode(out); e != nil {
				t.Fatal(e)
			}
		}
	}
	var status struct {
		Initialized bool `json:"initialized"`
	}
	api("GET", "/api/status", nil, &status)
	if !status.Initialized {
		token := strings.Split(shell(panelRemote, pb+" maintain --instance t07-panel --action init-token", nil), "\n")[1]
		api("POST", "/api/init", map[string]string{"token": token, "password": "t07-test-password"}, nil)
	}
	var login struct {
		CSRF string `json:"csrf"`
	}
	api("POST", "/api/login", map[string]string{"password": "t07-test-password"}, &login)
	csrf = login.CSRF
	var ticket struct {
		Command string `json:"command"`
	}
	api("POST", "/api/nodes/ticket", map[string]string{"endpoint": panelTLS.URL, "ca": ca}, &ticket)
	parts := strings.Split(ticket.Command, "\n")
	start, end := -1, -1
	for i, s := range parts {
		if strings.Contains(s, "<<'FILEPANEL_JOIN'") {
			start = i + 1
		}
		if s == "FILEPANEL_JOIN" {
			end = i
		}
	}
	if start < 0 || end < start {
		t.Fatal("installer command does not contain private join JSON")
	}
	var input map[string]string
	if e := json.Unmarshal([]byte(strings.Join(parts[start:end], "\n")), &input); e != nil {
		t.Fatal(e)
	}
	input["endpoint"] = nodeTLS.URL
	input["node_ca"] = nodeCA
	b, _ := json.Marshal(input)
	joinPath := fixture + "/join.json"
	shell(host != "", "umask 077; cat > "+joinPath, b)
	install(host != "", nb, "t07-node", "--role node --listen 127.0.0.1:19872 --join-file "+joinPath)
	shell(host != "", "rm "+joinPath, nil)
	list := func() []nodes.Node {
		var r struct {
			Nodes []nodes.Node `json:"nodes"`
		}
		api("GET", "/api/nodes", nil, &r)
		return r.Nodes
	}
	var peer nodes.Node
	for i := 0; i < 70; i++ {
		for _, n := range list() {
			if n.Endpoint == nodeTLS.URL && n.Online {
				peer = n
			}
		}
		if peer.ID != "" {
			break
		}
		time.Sleep(250 * time.Millisecond)
	}
	if peer.ID == "" {
		t.Fatal("installed node did not heartbeat")
	}
	peer.Storage = true
	peer.Note = "T07 installed lifecycle"
	api("PUT", "/api/nodes/"+peer.ID, peer, nil)
	time.Sleep(11 * time.Second)
	api("POST", "/api/nodes/"+peer.ID+"/probe", nil, nil)
	// Transfer a real object to the installed node, then prove its disk hash survives
	// panel purge. No remote deletion endpoints are ever invoked by uninstall.
	payload := make([]byte, 16<<20)
	for i := range payload {
		payload[i] = byte(i*31) ^ byte(i>>8)
	}
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	w, _ := mw.CreateFormFile("file", "retained.bin")
	w.Write(payload)
	mw.Close()
	r, _ := http.NewRequest("POST", base+"/api/files?storage="+peer.ID, &body)
	r.Header.Set("Content-Type", mw.FormDataContentType())
	r.Header.Set("X-CSRF-Token", csrf)
	res, e := client.Do(r)
	if e != nil {
		t.Fatal(e)
	}
	var item files.Item
	if res.StatusCode != 201 {
		b, _ := io.ReadAll(res.Body)
		res.Body.Close()
		t.Logf("upload response %d; checking durable reconciliation: %s", res.StatusCode, b)
		for i := 0; i < 45; i++ {
			var items []files.Item
			api("GET", "/api/files", nil, &items)
			for _, v := range items {
				if v.Name == "retained.bin" && v.StorageNode == peer.ID && v.Size == int64(len(payload)) {
					item = v
				}
			}
			if item.ID != "" {
				break
			}
			time.Sleep(time.Second)
		}
		if item.ID == "" {
			t.Fatal("remote upload did not reconcile into the panel file list")
		}
	} else {
		if e = json.NewDecoder(res.Body).Decode(&item); e != nil {
			t.Fatal(e)
		}
		res.Body.Close()
	}
	api("PUT", "/api/files/"+item.ID+"/share", share.Input{Enabled: true, Title: "T07"}, nil)
	if os.Getenv("FILEPANEL_INSTALL_BOOT_ONLY") == "1" {
		t.Log("仅准备面板/节点自启测试实例，完整卸载断言未在本次运行执行")
		return
	}
	var sh share.Share
	api("GET", "/api/files/"+item.ID+"/share", nil, &sh)
	settings := nodes.Settings{DefaultRelay: peer.ID}
	api("PUT", "/api/nodes/settings", settings, nil)
	roots, _ := x509.SystemCertPool()
	if roots == nil {
		roots = x509.NewCertPool()
	}
	roots.AppendCertsFromPEM([]byte(nodeCA))
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.TLSClientConfig = panelTLS.Client().Transport.(*http.Transport).TLSClientConfig.Clone()
	transport.TLSClientConfig.RootCAs = roots
	visitor := &http.Client{Transport: transport, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }, Timeout: 65 * time.Second}
	defer transport.CloseIdleConnections()
	get := func(u string) *http.Response {
		t.Helper()
		r, e := visitor.Get(u)
		if e != nil {
			t.Fatal(e)
		}
		return r
	}
	redir := get(base + "/s/" + sh.Slug + "/get")
	u := redir.Header.Get("Location")
	redir.Body.Close()
	if strings.HasPrefix(u, "/") {
		u = base + u
	}
	redir = get(u)
	nodeURL := redir.Header.Get("Location")
	redir.Body.Close()
	if !strings.HasPrefix(nodeURL, nodeTLS.URL+"/d/") {
		t.Fatalf("expected node redirect: %q", nodeURL)
	}
	stream := get(nodeURL)
	if stream.StatusCode != 200 {
		t.Fatalf("node stream status %d", stream.StatusCode)
	}
	buf := make([]byte, 4096)
	if _, e = io.ReadFull(stream.Body, buf); e != nil {
		t.Fatal(e)
	}
	before := shell(host != "", "find /var/lib/filepanel-t07-node/objects/data -type f -exec sha256sum {} \\;", nil)
	wantHash := sha256.Sum256(payload)
	if !strings.Contains(before, hex.EncodeToString(wantHash[:])) {
		t.Fatal("installed node payload hash differs from source")
	}
	install(host != "", nb, "t07-node", "--role node --listen 127.0.0.1:19872")
	shell(host != "", nb+" maintain --instance t07-node --action restart", nil)
	// The restart closes this response, so use a fresh authorization for disconnect.
	stream.Body.Close()
	redir = get(base + "/s/" + sh.Slug + "/get")
	u = redir.Header.Get("Location")
	redir.Body.Close()
	if strings.HasPrefix(u, "/") {
		u = base + u
	}
	redir = get(u)
	nodeURL = redir.Header.Get("Location")
	redir.Body.Close()
	stream = get(nodeURL)
	if stream.StatusCode != 200 {
		t.Fatal("stream did not start after node restart")
	}
	io.ReadFull(stream.Body, buf)
	output := shell(panelRemote, pb+" uninstall --instance t07-panel --purge", []byte("yes\n"))
	if !strings.Contains(output, peer.Note) || !strings.Contains(output, "关联文件=1") || !strings.Contains(output, "远端程序和文件保留") {
		t.Fatal("remote impact summary missing: ", output)
	}
	// Give the documented 30s lease plus 5s skew time to expire. Waiting here is
	// inside a bounded test, while the root agent remains available for updates.
	time.Sleep(36 * time.Second)
	rejected := get(nodeURL)
	rejected.Body.Close()
	if rejected.StatusCode < 400 {
		t.Fatal("new node download accepted after panel uninstall")
	}
	n, readErr := io.Copy(io.Discard, stream.Body)
	stream.Body.Close()
	if ne, ok := readErr.(net.Error); ok && ne.Timeout() {
		t.Fatal("client timeout cannot prove server lease cancellation")
	}
	total := 4096 + int(n)
	if total >= len(payload) {
		t.Fatalf("active transfer completed after panel uninstall: %d/%d", total, len(payload))
	}
	shell(host != "", nb+" maintain --instance t07-node --action status", nil)
	after := shell(host != "", "find /var/lib/filepanel-t07-node/objects/data -type f -exec sha256sum {} \\;", nil)
	if before == "" || before != after {
		t.Fatal("panel uninstall changed node files")
	}
	shell(host != "", nb+" uninstall --instance t07-node", []byte("yes\n"))
	install(host != "", nb, "t07-node", "--role node --listen 127.0.0.1:19872")
	if got := shell(host != "", "find /var/lib/filepanel-t07-node/objects/data -type f -exec sha256sum {} \\;", nil); got != before {
		t.Fatal("node reinstall lost files/identity")
	}
	shell(host != "", nb+" uninstall --instance t07-node --purge", []byte("yes\n"))
	t.Logf("installed panel+node lifecycle passed; SSH=%t panelRemote=%t; remote hash retained; expired leases reject downloads", host != "", panelRemote)
	t.Logf("active stream cut at %d/%d bytes", total, len(payload))
}
