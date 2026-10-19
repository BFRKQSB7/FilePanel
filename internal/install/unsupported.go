//go:build !linux

package install

import (
	"errors"
	"io"
)

func Command(args []string, in io.Reader, out io.Writer) error {
	return errors.New("系统安装/卸载仅支持 Alpine/OpenRC、Debian/systemd 和 Ubuntu 24.04 LTS/systemd")
}
