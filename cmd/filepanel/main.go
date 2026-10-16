package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"filepanel/internal/access"
	"filepanel/internal/auth"
	"filepanel/internal/files"
	"filepanel/internal/store"
	"filepanel/internal/web"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("命令执行失败", "error", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		return errors.New("用法: filepanel serve [参数] | filepanel init-token [--data-dir PATH]")
	}
	switch args[0] {
	case "serve":
		return serve(args[1:])
	case "init-token":
		return createInitToken(args[1:])
	case "help", "-h", "--help":
		fmt.Println("用法: filepanel serve [--data-dir PATH] [--listen ADDRESS]\n      filepanel init-token [--data-dir PATH]")
		return nil
	default:
		return fmt.Errorf("未知命令 %q", args[0])
	}
}

func dataPath(value string) (string, error) {
	path, err := filepath.Abs(value)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(path, 0700); err != nil {
		return "", fmt.Errorf("创建数据目录失败: %w", err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", errors.New("数据目录必须是普通目录，不能是符号链接")
	}
	if err := os.Chmod(path, 0700); err != nil {
		return "", fmt.Errorf("设置数据目录权限失败: %w", err)
	}
	return filepath.EvalSymlinks(path)
}

func createInitToken(args []string) error {
	flags := flag.NewFlagSet("init-token", flag.ContinueOnError)
	dataDir := flags.String("data-dir", envOr("FILEPANEL_DATA", "./data"), "受控数据目录")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("init-token 不接受位置参数")
	}
	root, err := dataPath(*dataDir)
	if err != nil {
		return err
	}
	s, err := store.Open(root)
	if err != nil {
		return err
	}
	defer s.Close()
	token, err := auth.New(s).CreateInitToken()
	if err != nil {
		return err
	}
	fmt.Println("初始化令牌（仅显示本次；30 分钟有效）：")
	fmt.Println(token)
	fmt.Println("请在管理页面完成管理员初始化，不要将令牌写入日志或提交到代码库。")
	return nil
}

func serve(args []string) error {
	flags := flag.NewFlagSet("serve", flag.ContinueOnError)
	dataDir := flags.String("data-dir", envOr("FILEPANEL_DATA", "./data"), "受控数据目录")
	listen := flags.String("listen", envOr("FILEPANEL_LISTEN", "127.0.0.1:8080"), "HTTP 监听地址")
	secureCookie := flags.Bool("secure-cookie", strings.EqualFold(os.Getenv("FILEPANEL_SECURE_COOKIE"), "true"), "在 TLS 反向代理后强制 Secure Cookie")
	tlsListen := flags.String("tls-listen", envOr("FILEPANEL_TLS_LISTEN", ":443"), "自动证书 TLS 监听；仅启用自动 HTTPS 时绑定，可使用高位端口")
	challengeListen := flags.String("challenge-listen", envOr("FILEPANEL_CHALLENGE_LISTEN", ":80"), "HTTP-01 挑战监听；公网必须通过 80 转入")
	staging := flags.Bool("acme-staging", false, "使用 Let's Encrypt staging；只验证流程，不切换主入口")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return errors.New("serve 不接受位置参数")
	}
	root, err := dataPath(*dataDir)
	if err != nil {
		return err
	}
	lock, err := store.LockDataDir(root)
	if err != nil {
		return err
	}
	defer lock.Close()
	s, err := store.Open(root)
	if err != nil {
		return err
	}
	defer s.Close()
	a := auth.New(s)
	manager, err := files.New(s, root, files.DefaultMaxUpload, 0)
	if err != nil {
		return err
	}
	opts := access.Options{Listen: *listen, TLSListen: *tlsListen, ChallengeListen: *challengeListen}
	if *staging {
		opts.DirectoryURL = access.StagingDirectory
	}
	deps, err := web.Build(s, a, manager, opts)
	if err != nil {
		return err
	}
	defer deps.Access.Close()
	handler := web.NewServer(deps, *secureCookie)
	server := &http.Server{
		Addr:              *listen,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    1 << 20,
	}
	listener, err := net.Listen("tcp", *listen)
	if err != nil {
		return err
	}
	deps.Access.Start(handler)
	serverErrors := make(chan error, 1)
	go func() {
		slog.Info("FilePanel 正在监听", "address", *listen, "data_dir", root)
		serverErrors <- server.Serve(listener)
	}()
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	select {
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	case <-ctx.Done():
		shutdown, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := server.Shutdown(shutdown); err != nil {
			return fmt.Errorf("关闭 HTTP 服务失败: %w", err)
		}
		return nil
	}
}

func envOr(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
