package install

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"time"
)

func Updating() bool {
	p := os.Getenv("FILEPANEL_MAINTENANCE")
	if p == "" {
		return false
	}
	_, e := os.Lstat(p)
	return !os.IsNotExist(e)
}
func WaitForMaintenance(ctx context.Context) bool {
	t := time.NewTicker(200 * time.Millisecond)
	defer t.Stop()
	for Updating() {
		select {
		case <-ctx.Done():
			return false
		case <-t.C:
		}
	}
	return true
}
func MaintenanceGate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Updating() && r.URL.Path != "/healthz" {
			w.Header().Set("Retry-After", "3")
			w.Header().Set("Content-Type", "application/json")
			http.Error(w, `{"error":"维护中，请稍后重试"}`, 503)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// AgentCall accepts only fixed actions; the website supplies no path, shell,
// executable, version or URL to the privileged agent.
func AgentCall(ctx context.Context, apply bool) ([]byte, error) {
	id := os.Getenv("FILEPANEL_INSTANCE")
	if validInstance(id) != nil {
		return nil, errors.New("此实例未启用安装维护代理")
	}
	t := &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", "/run/filepanel-"+id+"/update.sock")
	}}
	defer t.CloseIdleConnections()
	c := http.Client{Transport: t, Timeout: 5 * time.Second}
	method, path := "GET", "/status"
	if apply {
		method, path = "POST", "/apply"
	}
	r, e := http.NewRequestWithContext(ctx, method, "http://local"+path, nil)
	if e != nil {
		return nil, e
	}
	res, e := c.Do(r)
	if e != nil {
		return nil, errors.New("维护代理未就绪；请由管理员启动 update-agent")
	}
	defer res.Body.Close()
	b, e := io.ReadAll(io.LimitReader(res.Body, 65536))
	if e != nil {
		return nil, e
	}
	if res.StatusCode >= 400 {
		return nil, errors.New(string(b))
	}
	return b, nil
}
