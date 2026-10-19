// release-tool is a development-side offline signer, never installed on servers.
package main

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"filepanel/internal/release"
	"flag"
	"fmt"
	"os"
	"path/filepath"
)

func main() {
	if e := run(); e != nil {
		fmt.Fprintln(os.Stderr, e)
		os.Exit(1)
	}
}
func run() error {
	dir := flag.String("dir", "", "发布目录")
	keyPath := flag.String("key", "", "开发机离线签名私钥，base64")
	create := flag.Bool("create-key", false, "仅 fixture 创建一次新密钥")
	version := flag.String("version", "0.1.0", "版本")
	seq := flag.Uint64("sequence", 1, "严格递增序号")
	source := flag.String("source", "", "源码归档 SHA256")
	flag.Parse()
	if *dir == "" || *keyPath == "" {
		return fmt.Errorf("需要 dir/key")
	}
	if *create {
		_, key, e := ed25519.GenerateKey(rand.Reader)
		if e != nil {
			return e
		}
		f, e := os.OpenFile(*keyPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
		if e != nil {
			return e
		}
		_, e = f.WriteString(base64.StdEncoding.EncodeToString(key))
		f.Close()
		if e != nil {
			return e
		}
	}
	b, e := os.ReadFile(*keyPath)
	if e != nil {
		return e
	}
	key, e := base64.StdEncoding.DecodeString(string(b))
	if e != nil || len(key) != 64 {
		return fmt.Errorf("私钥格式无效")
	}
	m := release.Manifest{Product: "FilePanel", Version: *version, Sequence: *seq, Source: *source, SchemaMin: release.Schema, SchemaMax: release.Schema, ProtocolMin: release.Protocol, ProtocolMax: release.Protocol, Artifacts: map[string]release.Artifact{}}
	for _, arch := range []string{"amd64", "arm64"} {
		name := "filepanel-linux-" + arch
		h, n, e := release.Digest(filepath.Join(*dir, name))
		if e != nil {
			return e
		}
		m.Artifacts["linux-"+arch] = release.Artifact{File: name, SHA256: h, Size: n}
	}
	body, e := json.Marshal(m)
	if e != nil {
		return e
	}
	env := release.Envelope{Manifest: body, Signature: base64.StdEncoding.EncodeToString(ed25519.Sign(ed25519.PrivateKey(key), body))}
	b, e = json.Marshal(env)
	if e != nil {
		return e
	}
	pub := ed25519.PrivateKey(key).Public().(ed25519.PublicKey)
	if _, _, e = release.Verify(b, pub, 0, "amd64"); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(*dir, "release.json"), b, 0644); e != nil {
		return e
	}
	if e = os.WriteFile(filepath.Join(*dir, "trusted.pub"), []byte(base64.StdEncoding.EncodeToString(pub)), 0644); e != nil {
		return e
	}
	fmt.Println("签名发布清单已生成：", *version)
	return nil
}
