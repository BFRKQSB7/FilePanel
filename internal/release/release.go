// Package release verifies offline catalogs against an independently installed key.
package release

import (
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
)

var Version = "0.2.3"
var Sequence = "7"
var Source = "development"

const Schema = 5
const Protocol = 1

type Artifact struct {
	File   string `json:"file"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}
type Manifest struct {
	Product     string              `json:"product"`
	Version     string              `json:"version"`
	Sequence    uint64              `json:"sequence"`
	Source      string              `json:"source"`
	SchemaMin   int                 `json:"schema_min"`
	SchemaMax   int                 `json:"schema_max"`
	ProtocolMin int                 `json:"protocol_min"`
	ProtocolMax int                 `json:"protocol_max"`
	Artifacts   map[string]Artifact `json:"artifacts"`
}
type Envelope struct {
	Manifest  json.RawMessage `json:"manifest"`
	Signature string          `json:"signature"`
}

func Decode(b []byte, v any) error {
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if e := d.Decode(v); e != nil {
		return e
	}
	if d.Decode(new(any)) != io.EOF {
		return errors.New("JSON 尾部有多余内容")
	}
	return nil
}
func Read(path string, v any, max int64) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	b, e := io.ReadAll(io.LimitReader(f, max+1))
	if e != nil {
		return e
	}
	if int64(len(b)) > max {
		return errors.New("文件超限")
	}
	return Decode(b, v)
}
func Verify(b []byte, key ed25519.PublicKey, after uint64, arch string) (Manifest, Artifact, error) {
	var m Manifest
	var env Envelope
	var a Artifact
	if len(b) > 1<<20 || Decode(b, &env) != nil || len(key) != ed25519.PublicKeySize {
		return m, a, errors.New("无效签名清单")
	}
	sig, e := base64.StdEncoding.DecodeString(env.Signature)
	if e != nil || !ed25519.Verify(key, env.Manifest, sig) {
		return m, a, errors.New("未知或损坏的发布签名")
	}
	if e = Decode(env.Manifest, &m); e != nil {
		return m, a, e
	}
	if m.Product != "FilePanel" || m.Version == "" || len(m.Source) != 64 || m.Sequence <= after || m.SchemaMin > Schema || m.SchemaMax < Schema || m.ProtocolMin > Protocol || m.ProtocolMax < Protocol || m.SchemaMin < 1 || m.ProtocolMin < 1 {
		return m, a, errors.New("版本回滚或产品/schema/协议不兼容")
	}
	if _, e = hex.DecodeString(m.Source); e != nil {
		return m, a, errors.New("源码摘要无效")
	}
	if _, e = versionParts(m.Version); e != nil {
		return m, a, e
	}
	a, ok := m.Artifacts["linux-"+arch]
	if !ok || !SafeName(a.File) || a.Size <= 0 || a.Size > 128<<20 || len(a.SHA256) != 64 {
		return m, a, errors.New("产物平台或大小无效")
	}
	if _, e = hex.DecodeString(a.SHA256); e != nil {
		return m, a, e
	}
	return m, a, nil
}

func versionParts(s string) ([3]uint64, error) {
	var out [3]uint64
	p := strings.Split(s, ".")
	if len(p) != 3 {
		return out, errors.New("版本须为三段数字")
	}
	for i, v := range p {
		if v == "" || len(v) > 1 && v[0] == '0' {
			return out, errors.New("版本格式无效")
		}
		n, e := strconv.ParseUint(v, 10, 64)
		if e != nil {
			return out, e
		}
		out[i] = n
	}
	return out, nil
}
func Newer(next, current string) bool {
	a, e := versionParts(next)
	if e != nil {
		return false
	}
	b, e := versionParts(current)
	if e != nil {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return a[i] > b[i]
		}
	}
	return false
}
func SafeName(s string) bool {
	return s != "" && s != "." && s != ".." && filepath.Base(s) == s && !strings.ContainsAny(s, "/\\:\x00")
}
func Digest(path string) (string, int64, error) {
	i, e := os.Lstat(path)
	if e != nil {
		return "", 0, e
	}
	if !i.Mode().IsRegular() {
		return "", 0, errors.New("须为普通文件")
	}
	f, e := os.Open(path)
	if e != nil {
		return "", 0, e
	}
	defer f.Close()
	h := sha256.New()
	n, e := io.Copy(h, f)
	return hex.EncodeToString(h.Sum(nil)), n, e
}
func VerifyArtifact(path string, a Artifact) error {
	s, n, e := Digest(path)
	if e != nil {
		return e
	}
	if s != a.SHA256 || n != a.Size {
		return errors.New("产物大小或 SHA256 不匹配")
	}
	return nil
}
func Key(path string) (ed25519.PublicKey, error) {
	b, e := os.ReadFile(path)
	if e != nil {
		return nil, e
	}
	k, e := base64.StdEncoding.DecodeString(strings.TrimSpace(string(b)))
	if e != nil || len(k) != 32 {
		return nil, errors.New("可信公钥无效")
	}
	return ed25519.PublicKey(k), nil
}
func Info() map[string]any {
	return map[string]any{"product": "FilePanel", "version": Version, "sequence": Sequence, "source": Source, "schema": Schema, "protocol": Protocol, "platform": fmt.Sprintf("%s-%s", runtime.GOOS, runtime.GOARCH)}
}
