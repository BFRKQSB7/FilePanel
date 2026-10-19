package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"filepanel/internal/download"
	"filepanel/internal/files"
	"filepanel/internal/install"
	"filepanel/internal/nodes"
	"filepanel/internal/release"
	"filepanel/internal/store"
	"filepanel/internal/usage"
)

func loopbackListen(address string) bool {
	host, _, e := net.SplitHostPort(address)
	if e != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func nodeCommand(args []string) error {
	if len(args) == 0 || (args[0] != "join" && args[0] != "serve") {
		return errors.New("用法: filepanel node join [参数] | filepanel node serve [参数]")
	}
	flags := flag.NewFlagSet("node "+args[0], flag.ContinueOnError)
	dir := flags.String("data-dir", "./node-data", "节点专用数据目录")
	panel := flags.String("panel", "", "可信 HTTPS 面板源地址")
	panelID := flags.String("panel-id", "", "设置页给出的固定面板身份")
	ticket := flags.String("ticket", "", "5 分钟内一次性入网票据")
	joinFile := flags.String("join-file", "", "私有 JSON 入网文件，票据不进入进程参数")
	reserve := flags.Uint64("reserve-bytes", 0, "非零磁盘预留；另保留磁盘 5%")
	endpoint := flags.String("endpoint", "", "节点公开 HTTPS 源地址，NAT 需包含映射端口")
	ca := flags.String("ca", "", "面板私有 CA PEM 文件（可选）")
	nodeCA := flags.String("node-ca", "", "节点私有 CA PEM 文件（可选）")
	listen := flags.String("listen", "127.0.0.1:8081", "监听地址；未配置 TLS 时只允许回环反代")
	cert := flags.String("tls-cert", "", "TLS 完整证书链文件")
	key := flags.String("tls-key", "", "TLS 私钥文件")
	if e := flags.Parse(args[1:]); e != nil {
		return e
	}
	if flags.NArg() != 0 {
		return errors.New("node 不接受位置参数")
	}
	if *reserve > 0 && *reserve < 16<<20 {
		return errors.New("安全预留不得少于 16MiB")
	}
	var inlineCA, inlineNodeCA string
	if args[0] == "join" && *joinFile != "" {
		b, e := os.ReadFile(*joinFile)
		if e != nil {
			return e
		}
		var input struct {
			Panel    string `json:"panel"`
			PanelID  string `json:"panel_id"`
			Ticket   string `json:"ticket"`
			Endpoint string `json:"endpoint"`
			CA       string `json:"ca"`
			NodeCA   string `json:"node_ca"`
		}
		if e = json.Unmarshal(b, &input); e != nil {
			return errors.New("入网文件格式无效")
		}
		*panel, *panelID, *ticket, *endpoint = input.Panel, input.PanelID, input.Ticket, input.Endpoint
		inlineCA, inlineNodeCA = input.CA, input.NodeCA
	}
	root, e := dataPath(*dir)
	if e != nil {
		return e
	}
	lock, e := store.LockDataDir(root)
	if e != nil {
		return e
	}
	defer lock.Close()
	db, e := store.Open(root)
	if e != nil {
		return e
	}
	defer db.Close()
	if args[0] == "join" {
		if *panelID == "" || *ticket == "" {
			return errors.New("缺少面板身份或入网票据")
		}
		readCA := func(path string) (string, error) {
			if path == "" {
				return "", nil
			}
			b, e := os.ReadFile(path)
			return string(b), e
		}
		panelCA, e := readCA(*ca)
		if e != nil {
			return e
		}
		peerCA, e := readCA(*nodeCA)
		if e != nil {
			return e
		}
		if *joinFile != "" {
			panelCA, peerCA = inlineCA, inlineNodeCA
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if e = nodes.Enroll(ctx, db, nodes.Identity{Panel: *panel, PanelID: *panelID, CA: panelCA}, *ticket, *endpoint, peerCA); e != nil {
			return e
		}
		fmt.Println("节点已绑定。请使用 node serve 启动；Storage 默认关闭，Relay 默认开启。")
		return nil
	}
	if (*cert == "") != (*key == "") {
		return errors.New("TLS 证书与私钥必须同时提供")
	}
	if *cert == "" && !loopbackListen(*listen) {
		return errors.New("明文节点监听仅允许回环地址；公开端点须使用 TLS 或可信 HTTPS 反代")
	}
	f, e := files.New(db, root, files.DefaultMaxUpload, *reserve)
	if e != nil {
		return e
	}
	m, e := usage.New(db, nil)
	if e != nil {
		return e
	}
	d, e := download.New(db, f, m)
	if e != nil {
		return e
	}
	n, e := nodes.NewDaemon(db, f, m, d)
	if e != nil {
		return e
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	go func() {
		if install.WaitForMaintenance(ctx) {
			n.Run(ctx)
		}
	}()
	mux := http.NewServeMux()
	mux.Handle("/", n.Handler())
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		if e := db.DB.PingContext(r.Context()); e != nil {
			http.Error(w, "database unavailable", 503)
			return
		}
		json.NewEncoder(w).Encode(release.Info())
	})
	server := &http.Server{Addr: *listen, Handler: install.MaintenanceGate(mux), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
	errs := make(chan error, 1)
	go func() {
		if *cert != "" {
			errs <- server.ListenAndServeTLS(*cert, *key)
		} else {
			errs <- server.ListenAndServe()
		}
	}()
	select {
	case e := <-errs:
		if errors.Is(e, http.ErrServerClosed) {
			return nil
		}
		return e
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdown)
	}
}
