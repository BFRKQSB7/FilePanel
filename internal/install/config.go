// Package install owns the local instance lifecycle. It never contacts peers.
package install

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"filepanel/internal/release"
	"fmt"
	"io"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
)

var instanceName = regexp.MustCompile(`^[a-z][a-z0-9-]{0,19}$`)

type Config struct {
	Instance  string `json:"instance"`
	Role      string `json:"role"`
	Data      string `json:"data"`
	Listen    string `json:"listen"`
	LowPorts  bool   `json:"low_ports"`
	Reserve   uint64 `json:"reserve_bytes"`
	UpdateKey string `json:"update_key,omitempty"`
	// Join arguments are read from a private input file, never stored in the manifest.
	JoinFile string `json:"-"`
}

type Artifact struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Release struct {
	Version   string              `json:"version"`
	Artifacts map[string]Artifact `json:"artifacts"`
}

func validInstance(s string) error {
	if !instanceName.MatchString(s) {
		return errors.New("实例名须为小写字母开头的 1-20 位字母、数字或短横线")
	}
	return nil
}

func Validate(c Config) error {
	if c.UpdateKey != "" {
		k, e := base64.StdEncoding.DecodeString(c.UpdateKey)
		if e != nil || len(k) != 32 {
			return errors.New("更新公钥须为 base64 Ed25519 公钥")
		}
	}
	if e := validInstance(c.Instance); e != nil {
		return e
	}
	if c.Role != "panel" && c.Role != "node" {
		return errors.New("role 只允许 panel/node")
	}
	host, port, e := net.SplitHostPort(c.Listen)
	if e != nil || net.ParseIP(host) == nil {
		return errors.New("listen 须为明确 IP:端口")
	}
	n, e := strconv.Atoi(port)
	if e != nil || n < 1 || n > 65535 || (n < 1024 && !c.LowPorts) {
		return errors.New("无效端口；低端口须显式启用 --low-ports")
	}
	if c.Role == "node" && !net.ParseIP(host).IsLoopback() {
		return errors.New("节点安装使用回环监听与管理员已有 HTTPS 反代")
	}
	if c.Reserve < 16<<20 {
		return errors.New("安全预留不得少于 16MiB；仍叠加磁盘 5% 保护")
	}
	return nil
}

func decodeFile(path string, v any) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1<<20))
	d.DisallowUnknownFields()
	if e = d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("JSON 尾部有多余内容")
	}
	return nil
}

func digestFile(path string) (string, int64, error) {
	f, e := os.Open(path)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, e
}

// VerifyRelease pins the manifest itself to an independently trusted digest.
func VerifyRelease(path, pin string) (string, Artifact, error) {
	sum, _, e := digestFile(path)
	if e != nil {
		return "", Artifact{}, e
	}
	if len(pin) != 64 || sum != strings.ToLower(pin) {
		return "", Artifact{}, errors.New("发布清单 SHA256 不匹配")
	}
	var r Release
	if e = decodeFile(path, &r); e != nil {
		// The independently pinned bootstrap may also use the signed T08 format.
		var env release.Envelope
		if e = release.Read(path, &env, 1<<20); e != nil {
			return "", Artifact{}, e
		}
		var m release.Manifest
		if e = release.Decode(env.Manifest, &m); e != nil || m.Product != "FilePanel" {
			return "", Artifact{}, errors.New("发布清单无效")
		}
		r.Version = m.Version
		r.Artifacts = map[string]Artifact{}
		for k, v := range m.Artifacts {
			r.Artifacts[k] = Artifact{v.File, v.SHA256, v.Size}
		}
	}
	a, ok := r.Artifacts["linux-"+runtime.GOARCH]
	if !ok || r.Version == "" || a.Size <= 0 || len(a.SHA256) != 64 || filepath.Base(a.File) != a.File || a.File == "." || strings.ContainsAny(a.File, "/\\") {
		return "", a, errors.New("发布清单无匹配的 Linux 产物")
	}
	src := filepath.Join(filepath.Dir(path), a.File)
	if i, e := os.Lstat(src); e != nil || !i.Mode().IsRegular() {
		return "", a, errors.New("产物须为普通文件")
	}
	sum, n, e := digestFile(src)
	if e != nil {
		return "", a, e
	}
	if sum != a.SHA256 || n != a.Size {
		return "", a, errors.New("产物大小或 SHA256 不匹配")
	}
	return src, a, nil
}

func Args(c Config) []string {
	args := []string{"serve", "--data-dir", c.Data, "--listen", c.Listen, "--reserve-bytes", fmt.Sprint(c.Reserve)}
	if c.Role == "node" {
		args = append([]string{"node"}, args...)
	} else {
		tls, ch := "127.0.0.1:8443", "127.0.0.1:8082"
		if c.LowPorts {
			tls, ch = ":443", ":80"
		}
		args = append(args, "--tls-listen", tls, "--challenge-listen", ch)
	}
	return args
}

func ReadConfig(path string) (Config, error) {
	var c Config
	e := decodeFile(path, &c)
	if e == nil {
		e = Validate(c)
	}
	return c, e
}
