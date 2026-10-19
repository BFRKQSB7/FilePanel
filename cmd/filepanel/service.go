package main

import (
	"errors"
	"filepanel/internal/install"
	"flag"
	"log/slog"
	"path/filepath"
)

func serviceRun(args []string) error {
	f := flag.NewFlagSet("service-run", flag.ContinueOnError)
	path := f.String("config", "", "安装器管理的配置文件")
	if e := f.Parse(args); e != nil {
		return e
	}
	if f.NArg() != 0 {
		return errors.New("service-run 不接受位置参数")
	}
	c, e := install.ReadConfig(*path)
	if e != nil {
		return e
	}
	slog.SetDefault(slog.New(slog.NewJSONHandler(&install.Log{Path: filepath.Join(filepath.Dir(*path), "logs", "service.log")}, nil)))
	e = run(install.Args(c))
	if e != nil {
		slog.Error("服务退出", "error", e)
	}
	return e
}
