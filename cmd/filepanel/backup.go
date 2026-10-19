package main

import (
	"encoding/json"
	"errors"
	"filepanel/internal/backup"
	"filepanel/internal/store"
	"flag"
	"fmt"
	"os"
)

func backupCommand(args []string) error {
	if len(args) == 0 {
		return errors.New("用法: filepanel backup create/verify/restore --path PATH [--data-dir PATH]")
	}
	f := flag.NewFlagSet("backup "+args[0], flag.ContinueOnError)
	dir := f.String("data-dir", "./data", "已停止服务的数据目录；恢复时须为新目录")
	path := f.String("path", "", "私有备份目录")
	reserve := f.Uint64("reserve-bytes", 16<<20, "至少 16MiB 且保留磁盘 5%")
	if e := f.Parse(args[1:]); e != nil {
		return e
	}
	if f.NArg() != 0 || *path == "" || *reserve < 16<<20 {
		return errors.New("备份参数无效")
	}
	var m backup.Manifest
	var e error
	switch args[0] {
	case "create":
		lock, err := store.LockDataDir(*dir)
		if err != nil {
			return err
		}
		defer lock.Close()
		m, e = backup.Create(*dir, *path, *reserve)
	case "verify":
		m, e = backup.Verify(*path)
	case "restore":
		m, e = backup.RestoreNew(*path, *dir, *reserve)
	default:
		return errors.New("未知备份动作")
	}
	if e != nil {
		return e
	}
	b, _ := json.Marshal(m)
	fmt.Fprintln(os.Stdout, string(b))
	fmt.Println("备份含本机数据与身份密钥，请限制权限并加密离线保管；远端正式文件仅列清单，不在包内。")
	return nil
}
