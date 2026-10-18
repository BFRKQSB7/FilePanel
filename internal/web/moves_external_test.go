package web

// This opt-in smoke runs release nodes on an explicitly configured SSH host.
// It uses loopback TLS over SSH forwards; it does not claim public NAT ingress.
import (
	"bytes"
	"context"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"filepanel/internal/download"
	"filepanel/internal/files"
	"filepanel/internal/nodes"
	"filepanel/internal/store"
	"filepanel/internal/usage"
	"net/http"
)

func TestMoveExternalNodes(t *testing.T) {
	host := os.Getenv("FILEPANEL_SMOKE_SSH_HOST")
	if host == "" {
		t.Skip("external SSH host not configured")
	}
	key, known, binary := os.Getenv("FILEPANEL_SMOKE_SSH_KEY"), os.Getenv("FILEPANEL_SMOKE_KNOWN_HOSTS"), os.Getenv("FILEPANEL_SMOKE_NODE_BINARY")
	root := os.Getenv("FILEPANEL_SMOKE_REMOTE_ROOT")
	if !regexp.MustCompile(`^/[a-zA-Z0-9/_-]+/filepanel-t06$`).MatchString(root) {
		t.Fatal("explicit isolated remote root required")
	}
	if strings.HasPrefix(host, "-") || !regexp.MustCompile(`^[a-zA-Z0-9@._-]+$`).MatchString(host) {
		t.Fatal("invalid SSH target")
	}
	for _, path := range []string{key, known, binary} {
		if _, e := os.Stat(path); e != nil {
			t.Fatal("missing smoke input", e)
		}
	}
	args := []string{"-i", key, "-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=yes", "-o", "UserKnownHostsFile=" + known, "-o", "ConnectTimeout=10"}
	ssh := func(command string) string {
		t.Helper()
		out, e := exec.Command("ssh", append(append([]string{}, args...), host, command)...).CombinedOutput()
		if e != nil {
			t.Fatalf("SSH smoke operation: %v %s", e, out)
		}
		return strings.TrimSpace(string(out))
	}
	remote := root + "/smoke-" + strconv.FormatInt(time.Now().UnixNano(), 10)
	ssh("umask 077; mkdir -p '" + remote + "'")
	e := newEnv(t)
	e.deps.Download.Now = time.Now
	panel := httptest.NewTLSServer(e.handler)
	defer panel.Close()
	cert := panel.TLS.Certificates[0]
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Certificate[0]})
	pk, err := x509.MarshalPKCS8PrivateKey(cert.PrivateKey)
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	certPath, keyPath := filepath.Join(tmp, "tls.pem"), filepath.Join(tmp, "tls.key")
	if err = os.WriteFile(certPath, ca, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), 0600); err != nil {
		t.Fatal(err)
	}
	scpArgs := append(append([]string{}, args...), binary, certPath, keyPath, host+":"+remote+"/")
	if out, err := exec.Command("scp", scpArgs...).CombinedOutput(); err != nil {
		t.Fatalf("SCP: %v %s", err, out)
	}
	bin := remote + "/" + filepath.Base(binary)
	ssh("chmod 700 '" + bin + "'; uname -m; cat /etc/os-release | head -2")
	panelPort := strings.Split(panel.Listener.Addr().String(), ":")[1]
	ports := []string{"19181", "19182"}
	tunnelArgs := append([]string{}, args...)
	tunnelArgs = append(tunnelArgs, "-N", "-o", "ExitOnForwardFailure=yes", "-o", "ServerAliveInterval=15")
	for _, port := range ports {
		l, err := net.Listen("tcp", "127.0.0.1:"+port)
		if err != nil {
			t.Fatal(err)
		}
		l.Close()
		tunnelArgs = append(tunnelArgs, "-L", "127.0.0.1:"+port+":127.0.0.1:"+port)
	}
	tunnelArgs = append(tunnelArgs, "-R", "127.0.0.1:"+panelPort+":127.0.0.1:"+panelPort, host)
	tunnel := exec.Command("ssh", tunnelArgs...)
	var tunnelLog bytes.Buffer
	tunnel.Stderr = &tunnelLog
	if err = tunnel.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tunnel.Process.Kill(); _ = tunnel.Wait() }()
	time.Sleep(time.Second)
	if err = e.deps.Nodes.ConfigureEndpoint(panel.URL, string(ca)); err != nil {
		t.Fatal(err)
	}
	var ids []string
	defer func() {
		// The generated path is a child of the validated project root. No shared
		// service, firewall, SSH configuration or other project directory is touched.
		command := "for p in '" + remote + "'/node-*.pid; do [ ! -f \"$p\" ] || kill \"$(cat \"$p\")\" 2>/dev/null || true; done; rm -rf -- '" + remote + "'"
		out, err := exec.Command("ssh", append(append([]string{}, args...), host, command)...).CombinedOutput()
		if err != nil {
			t.Errorf("isolated smoke cleanup: %v %s", err, out)
		}
	}()
	for i, port := range ports {
		ticket, err := e.deps.Nodes.NewTicket()
		if err != nil {
			t.Fatal(err)
		}
		endpoint := "https://127.0.0.1:" + port
		dir := fmt.Sprintf("%s/node-%d", remote, i)
		command := fmt.Sprintf("'%s' node join --panel '%s' --panel-id '%s' --ticket '%s' --endpoint '%s' --ca '%s/tls.pem' --node-ca '%s/tls.pem' --data-dir '%s'", bin, panel.URL, e.deps.Nodes.ID(), ticket, endpoint, remote, remote, dir)
		ssh(command)
		ssh(fmt.Sprintf("nohup '%s' node serve --listen '127.0.0.1:%s' --tls-cert '%s/tls.pem' --tls-key '%s/tls.key' --data-dir '%s' > '%s/node-%d.log' 2>&1 < /dev/null & echo $! > '%s/node-%d.pid'", bin, port, remote, remote, dir, remote, i, remote, i))
		deadline := time.Now().Add(15 * time.Second)
		var found nodes.Node
		for time.Now().Before(deadline) {
			list, err := e.deps.Nodes.List()
			if err != nil {
				t.Fatal(err)
			}
			for _, n := range list {
				if n.Endpoint == endpoint && n.Online {
					found = n
				}
			}
			if found.ID != "" {
				break
			}
			time.Sleep(200 * time.Millisecond)
		}
		if found.ID == "" {
			t.Fatal("remote heartbeat failed", tunnelLog.String())
		}
		found.Storage = true
		if err = e.deps.Nodes.Update(found.ID, found); err != nil {
			t.Fatal(err)
		}
		if err = e.deps.Nodes.Probe(context.Background(), found.ID); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, found.ID)
	}
	payload := content(1<<20, 37)
	item := e.upload("T06-跨主机测试.bin", payload)
	share := e.publish(item.ID, "T06 原分享", true)
	originalURL := "/s/" + share.Slug
	for _, target := range []string{ids[0], ids[1], ""} {
		req := requestJSON(t, e.handler, "POST", "/api/files/"+item.ID+"/move", map[string]string{"request_id": fmt.Sprintf("external-%d-%s", time.Now().UnixNano(), target), "target": target}, e.cookie, e.csrf)
		if req.Code != 202 {
			t.Fatalf("move API %d %s", req.Code, req.Body.String())
		}
		var task nodes.Move
		if err = json.Unmarshal(req.Body.Bytes(), &task); err != nil {
			t.Fatal(err)
		}
		e.deps.Nodes.ReconcileMoves(context.Background())
		task, err = e.deps.Nodes.Move(task.ID)
		if err != nil {
			t.Fatal(err)
		}
		if task.State == "cleaning" {
			time.Sleep(36 * time.Second)
			e.deps.Nodes.ReconcileMoves(context.Background())
			task, err = e.deps.Nodes.Move(task.ID)
		}
		if err != nil || task.State != "done" {
			t.Fatalf("remote task %+v %v", task, err)
		}
		page := requestJSON(t, e.handler, "GET", originalURL, nil, "", "")
		if page.Code != 200 {
			t.Fatalf("original share %d", page.Code)
		}
		current, err := e.deps.Shares.Get(item.ID)
		if err != nil {
			t.Fatal(err)
		}
		if current.Slug != share.Slug || current.AuthVersion != share.AuthVersion {
			t.Fatal("share identity changed")
		}
		tok := e.token(current, "127.0.0.1")
		res := requestJSON(t, e.handler, "GET", "/d/"+item.ID+"?t="+tok, nil, "", "")
		if res.Code != 200 || !bytes.Equal(res.Body.Bytes(), payload) {
			t.Fatalf("public hash mismatch %d bytes=%d", res.Code, res.Body.Len())
		}
		t.Logf("cross-host target=%q route=%s original_share=%s sha256=%s", target, task.Route, originalURL, sum(res.Body.Bytes()))
	}
	if wait, _ := strconv.Atoi(os.Getenv("FILEPANEL_SMOKE_UI_SECONDS")); wait > 0 {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go e.deps.Nodes.Run(ctx)
		t.Logf("BROWSER_URL=%s/admin", panel.URL)
		// A plain loopback UI avoids the test-only TLS certificate interstitial.
		ui := httptest.NewServer(e.handler)
		defer ui.Close()
		t.Logf("BROWSER_HTTP_URL=%s/admin", ui.URL)
		time.Sleep(time.Duration(wait) * time.Second)
	}
}

func TestMoveBrowserFixture(t *testing.T) {
	wait, _ := strconv.Atoi(os.Getenv("FILEPANEL_MOVE_BROWSER_SECONDS"))
	if wait <= 0 {
		t.Skip("interactive browser fixture not requested")
	}
	e := newEnv(t)
	e.deps.Download.Now = time.Now
	panel := httptest.NewTLSServer(e.handler)
	defer panel.Close()
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: panel.Certificate().Raw}))
	if err := e.deps.Nodes.ConfigureEndpoint(panel.URL, ca); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	for i := 0; i < 2; i++ {
		dir := t.TempDir()
		db, err := store.Open(dir)
		if err != nil {
			t.Fatal(err)
		}
		defer db.Close()
		f, err := files.New(db, dir, 8<<20, 1)
		if err != nil {
			t.Fatal(err)
		}
		m, err := usage.New(db, nil)
		if err != nil {
			t.Fatal(err)
		}
		d, err := download.New(db, f, m)
		if err != nil {
			t.Fatal(err)
		}
		var handler atomic.Value
		server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { handler.Load().(http.Handler).ServeHTTP(w, r) }))
		defer server.Close()
		ticket, err := e.deps.Nodes.NewTicket()
		if err != nil {
			t.Fatal(err)
		}
		if err = nodes.Enroll(ctx, db, nodes.Identity{Panel: panel.URL, PanelID: e.deps.Nodes.ID(), CA: ca}, ticket, server.URL, ca); err != nil {
			t.Fatal(err)
		}
		daemon, err := nodes.NewDaemon(db, f, m, d)
		if err != nil {
			t.Fatal(err)
		}
		handler.Store(daemon.Handler())
		if err = daemon.Heartbeat(ctx); err != nil {
			t.Fatal(err)
		}
		node, err := e.deps.Nodes.Get(daemon.Identity.ID)
		if err != nil {
			t.Fatal(err)
		}
		node.Storage = true
		node.Note = fmt.Sprintf("T06 测试节点 %d", i+1)
		if err = e.deps.Nodes.Update(node.ID, node); err != nil {
			t.Fatal(err)
		}
		if err = daemon.Heartbeat(ctx); err != nil {
			t.Fatal(err)
		}
		if err = e.deps.Nodes.Probe(ctx, node.ID); err != nil {
			t.Fatal(err)
		}
		go daemon.Run(ctx)
	}
	item := e.upload("T06-浏览器验收.bin", content(256<<10, 7))
	share := e.publish(item.ID, "移动后仍可下载", true)
	ui := httptest.NewServer(e.handler)
	defer ui.Close()
	go e.deps.Nodes.Run(ctx)
	t.Logf("BROWSER_URL=%s/admin SHARE_URL=%s/s/%s SHA256=%s", ui.URL, ui.URL, share.Slug, item.SHA256)
	time.Sleep(time.Duration(wait) * time.Second)
}
