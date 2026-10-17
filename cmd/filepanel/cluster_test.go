package main

import (
	"bytes"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
	"testing"
	"time"

	"filepanel/internal/auth"
	"filepanel/internal/download"
	"filepanel/internal/files"
	"filepanel/internal/nodes"
	"filepanel/internal/share"
	"filepanel/internal/store"
	"filepanel/internal/usage"
)

// Opt-in real-process smoke; the hold mode exposes only an isolated local
// fixture for interactive browser checks, and tears down its own children.
func TestThreeProcessCluster(t *testing.T) {
	if os.Getenv("FILEPANEL_CLUSTER_SMOKE") != "1" {
		t.Skip("opt-in three-process smoke")
	}
	check := func(e error) {
		t.Helper()
		if e != nil {
			t.Fatal(e)
		}
	}
	root := t.TempDir()
	exe := filepath.Join(root, "filepanel")
	if runtime.GOOS == "windows" {
		exe += ".exe"
	}
	goexe := filepath.Join(runtime.GOROOT(), "bin", "go")
	if runtime.GOOS == "windows" {
		goexe += ".exe"
	}
	build := exec.Command(goexe, "build", "-buildvcs=false", "-o", exe, ".")
	if out, e := build.CombinedOutput(); e != nil {
		t.Fatalf("build: %s %v", out, e)
	}
	address := func() string {
		l, e := net.Listen("tcp", "127.0.0.1:0")
		check(e)
		a := l.Addr().String()
		l.Close()
		return a
	}
	panelAddr := address()
	panelURL := "http://" + panelAddr
	target, e := url.Parse(panelURL)
	check(e)
	proxy := httptest.NewTLSServer(httputil.NewSingleHostReverseProxy(target))
	defer proxy.Close()
	ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: proxy.Certificate().Raw})
	keyDER, e := x509.MarshalPKCS8PrivateKey(proxy.TLS.Certificates[0].PrivateKey)
	check(e)
	certPath, keyPath := filepath.Join(root, "cert.pem"), filepath.Join(root, "key.pem")
	check(os.WriteFile(certPath, ca, 0600))
	check(os.WriteFile(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0600))
	dir := filepath.Join(root, "panel")
	db, e := store.Open(dir)
	check(e)
	f, e := files.New(db, dir, files.DefaultMaxUpload, 0)
	check(e)
	a := auth.New(db)
	token, e := a.CreateInitToken()
	check(e)
	check(a.Initialize(token, "cluster-test-password"))
	session, csrf, e := a.Login(httptest.NewRequest("POST", panelURL+"/api/login", nil), "cluster-test-password")
	check(e)
	sh, e := share.New(db, f)
	check(e)
	m, e := usage.New(db, nil)
	check(e)
	d, e := download.New(db, f, m)
	check(e)
	p, e := nodes.NewPanel(db, f, sh, a, m, d)
	check(e)
	check(p.ConfigureEndpoint(proxy.URL, string(ca)))
	panelID := p.ID()
	tickets := []string{}
	for i := 0; i < 2; i++ {
		v, e := p.NewTicket()
		check(e)
		tickets = append(tickets, v)
	}
	check(db.Close())
	start := func(args ...string) *exec.Cmd {
		cmd := exec.Command(exe, args...)
		log, e := os.OpenFile(filepath.Join(root, "process.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0600)
		check(e)
		cmd.Stdout, cmd.Stderr = log, log
		check(cmd.Start())
		t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait(); log.Close() })
		return cmd
	}
	panel := start("serve", "--data-dir", dir, "--listen", panelAddr)
	_ = panel
	client, e := nodes.Client(string(ca))
	check(e)
	defer client.CloseIdleConnections()
	client.Timeout = 10 * time.Second
	visitor := &http.Client{Transport: client.Transport, Timeout: 10 * time.Second}
	wait := func(path string, condition func([]byte) bool) {
		t.Helper()
		until := time.Now().Add(15 * time.Second)
		for time.Now().Before(until) {
			req, e := http.NewRequest("GET", panelURL+path, nil)
			check(e)
			req.AddCookie(&http.Cookie{Name: "fp_session", Value: session})
			res, e := client.Do(req)
			if e == nil {
				b, _ := io.ReadAll(res.Body)
				res.Body.Close()
				if res.StatusCode == 200 && condition(b) {
					return
				}
			}
			time.Sleep(100 * time.Millisecond)
		}
		t.Fatalf("timeout waiting for %s", path)
	}
	wait("/healthz", func([]byte) bool { return true })
	var peerArgs [][]string
	var peerCmds []*exec.Cmd
	for i := 0; i < 2; i++ {
		addr := address()
		nodeDir := filepath.Join(root, "node"+string(rune('a'+i)))
		join := exec.Command(exe, "node", "join", "--data-dir", nodeDir, "--panel", proxy.URL, "--panel-id", panelID, "--ticket", tickets[i], "--endpoint", "https://"+addr, "--ca", certPath, "--node-ca", certPath)
		if out, e := join.CombinedOutput(); e != nil {
			t.Fatalf("join failed: %s %v", out, e)
		}
		args := []string{"node", "serve", "--data-dir", nodeDir, "--listen", addr, "--tls-cert", certPath, "--tls-key", keyPath}
		peerArgs = append(peerArgs, args)
		peerCmds = append(peerCmds, start(args...))
	}
	var nodeList struct {
		Nodes []nodes.Node `json:"nodes"`
	}
	wait("/api/nodes", func(b []byte) bool {
		_ = json.Unmarshal(b, &nodeList)
		return len(nodeList.Nodes) == 2 && nodeList.Nodes[0].Online && nodeList.Nodes[1].Online
	})
	src, relay := nodeList.Nodes[0], nodeList.Nodes[1]
	api := func(method, path string, body io.Reader, contentType string) ([]byte, int) {
		t.Helper()
		req, e := http.NewRequest(method, panelURL+path, body)
		check(e)
		req.AddCookie(&http.Cookie{Name: "fp_session", Value: session})
		req.Header.Set("X-CSRF-Token", csrf)
		if contentType != "" {
			req.Header.Set("Content-Type", contentType)
		}
		res, e := client.Do(req)
		check(e)
		defer res.Body.Close()
		b, e := io.ReadAll(res.Body)
		check(e)
		return b, res.StatusCode
	}
	requestJSON := func(method, path string, v any) []byte {
		b, _ := json.Marshal(v)
		out, status := api(method, path, bytes.NewReader(b), "application/json")
		if status >= 400 {
			t.Fatalf("%s: %d %s", path, status, out)
		}
		return out
	}
	src.Storage = true
	src.ManualRegion = true
	src.Region = "本机测试存储"
	src.Note = "三进程存储"
	requestJSON("PUT", "/api/nodes/"+src.ID, src)
	relay.ManualRegion = true
	relay.Region = "本机测试出口"
	relay.Note = "三进程出口"
	requestJSON("PUT", "/api/nodes/"+relay.ID, relay)
	requestJSON("POST", "/api/nodes/"+src.ID+"/probe", nil)
	requestJSON("POST", "/api/nodes/"+relay.ID+"/probe", nil)
	payload := bytes.Repeat([]byte("three-process-stream\n"), 2048)
	upload := func(storage string) files.Item {
		var body bytes.Buffer
		mw := multipart.NewWriter(&body)
		part, e := mw.CreateFormFile("file", "cluster.bin")
		check(e)
		_, e = part.Write(payload)
		check(e)
		check(mw.Close())
		out, status := api("POST", "/api/files?storage="+storage, &body, mw.FormDataContentType())
		if status != 201 {
			t.Fatalf("upload %d %s", status, out)
		}
		var v files.Item
		check(json.Unmarshal(out, &v))
		return v
	}
	local := upload("")
	remote := upload(src.ID)
	remoteSlug := ""
	for _, v := range []files.Item{local, remote} {
		out := requestJSON("PUT", "/api/files/"+v.ID+"/share", share.Input{Enabled: true, Featured: true})
		var view struct {
			Slug string `json:"slug"`
		}
		check(json.Unmarshal(out, &view))
		if v.ID == remote.ID {
			remoteSlug = view.Slug
		}
		for _, line := range []string{"local", relay.ID} {
			res, e := visitor.Get(panelURL + "/s/" + view.Slug + "/get?relay=" + line)
			check(e)
			b, e := io.ReadAll(res.Body)
			res.Body.Close()
			check(e)
			if res.StatusCode != 200 || !bytes.Equal(b, payload) {
				t.Fatalf("real process transfer status=%d bytes=%d", res.StatusCode, len(b))
			}
		}
	}
	indexFor := func(id string) int {
		for _, n := range nodeList.Nodes {
			if n.ID == id {
				for i, args := range peerArgs {
					if "https://"+args[5] == n.Endpoint {
						return i
					}
				}
			}
		}
		t.Fatal("missing peer process")
		return -1
	}
	stopPeer := func(id string) { i := indexFor(id); check(peerCmds[i].Process.Kill()); _ = peerCmds[i].Wait() }
	startPeer := func(id string) { i := indexFor(id); peerCmds[i] = start(peerArgs[i]...) }
	requestJSON("PUT", "/api/nodes/settings", nodes.Settings{DefaultRelay: relay.ID, Fallback: false})
	stopPeer(relay.ID)
	res, e := visitor.Get(panelURL + "/s/" + remoteSlug + "/get")
	check(e)
	res.Body.Close()
	if res.StatusCode < 400 {
		t.Fatal("failed relay ignored disabled fallback")
	}
	requestJSON("PUT", "/api/nodes/settings", nodes.Settings{DefaultRelay: relay.ID, Fallback: true})
	res, e = visitor.Get(panelURL + "/s/" + remoteSlug + "/get")
	check(e)
	b, e := io.ReadAll(res.Body)
	res.Body.Close()
	check(e)
	if res.StatusCode != 200 || !bytes.Equal(b, payload) {
		t.Fatal("remote source did not fall back through panel")
	}
	stopPeer(src.ID)
	res, e = visitor.Get(panelURL + "/s/" + remoteSlug + "/get")
	check(e)
	res.Body.Close()
	if res.StatusCode < 400 {
		t.Fatal("offline unique source falsely recovered")
	}
	startPeer(src.ID)
	startPeer(relay.ID)
	wait("/healthz", func([]byte) bool {
		res, e := visitor.Get(panelURL + "/s/" + remoteSlug + "/get?relay=local")
		if e != nil {
			return false
		}
		b, e := io.ReadAll(res.Body)
		res.Body.Close()
		return e == nil && res.StatusCode == 200 && bytes.Equal(b, payload)
	})
	requestJSON("PUT", "/api/nodes/settings", nodes.Settings{Fallback: true})
	t.Log("three independent processes: four storage/relay combinations, fallback, unique-source outage and restart hashes verified")
	if os.Getenv("FILEPANEL_CLUSTER_HOLD") == "1" {
		done := make(chan struct{})
		var finish sync.Once
		var processMu sync.Mutex
		control := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != "POST" {
				http.Error(w, "method", 405)
				return
			}
			if r.URL.Path == "/finish" {
				finish.Do(func() { close(done) })
				return
			}
			processMu.Lock()
			defer processMu.Unlock()
			for i, n := range nodeList.Nodes {
				index := i
				for k, args := range peerArgs {
					if "https://"+args[5] == n.Endpoint {
						index = k
					}
				}
				if r.URL.Path == "/stop/"+n.ID {
					_ = peerCmds[index].Process.Kill()
				} else if r.URL.Path == "/start/"+n.ID {
					peerCmds[index] = start(peerArgs[index]...)
				}
			}
		}))
		defer control.Close()
		manifest := map[string]any{"panel": panelURL, "control": control.URL, "source": src.ID, "relay": relay.ID, "sample_path": filepath.Join(root, "browser-sample.bin")}
		check(os.WriteFile(filepath.Join(root, "browser-sample.bin"), payload, 0600))
		manifestPath := os.Getenv("FILEPANEL_CLUSTER_MANIFEST")
		if manifestPath == "" {
			t.Fatal("manifest path required for hold mode")
		}
		blob, _ := json.Marshal(manifest)
		check(os.WriteFile(manifestPath, blob, 0600))
		t.Log("browser fixture ready at " + panelURL)
		select {
		case <-done:
		case <-time.After(45 * time.Minute):
			t.Fatal("interactive fixture expired")
		}
	}
}
