package main

import (
	"context"
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
	"filepanel/internal/nodes"
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
	f, e := files.New(db, root, files.DefaultMaxUpload, 0)
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
	go n.Run(ctx)
	server := &http.Server{Addr: *listen, Handler: n.Handler(), ReadHeaderTimeout: 10 * time.Second, IdleTimeout: 90 * time.Second, MaxHeaderBytes: 32 << 10}
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
