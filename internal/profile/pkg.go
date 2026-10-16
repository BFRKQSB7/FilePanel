package profile

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
	"time"
)

// 配置包限制。所有上限都以“实际解压字节”再次检查，不只相信 ZIP 头。
const (
	MaxPackageBytes = 6 << 20  // 整个 ZIP 文件
	MaxEditBytes    = 5 << 20  // 表单直接编辑时的 multipart 总量（素材合计上限 4.5 MiB）
	maxEntries      = 8        // profile.json + assets/ + 3 张素材，留少量余量
	maxProfileJSON  = 64 << 10 // profile.json
	maxUncompressed = 5 << 20  // 全部条目解压后合计
	maxRatio        = 100      // 单条目压缩比上限
	ratioFloor      = 64 << 10 // 小于该值的条目不检查压缩比
)

var entryRE = regexp.MustCompile(`^assets/(logo|banner|background)\.(png|jpg|webp)$`)

type doc struct {
	Format     string            `json:"format"`
	Version    int               `json:"version"`
	SiteName   string            `json:"site_name"`
	Intro      string            `json:"intro"`
	Accent     string            `json:"accent"`
	Footer     string            `json:"footer"`
	ButtonText string            `json:"button_text"`
	Assets     map[string]string `json:"assets"`
}

func pkgErr(format string, args ...any) error {
	return userErr("配置包无效："+format, args...)
}

var packageTime = time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)

// BuildPackage 生成确定性的个性化包：profile.json + assets/<role>.<ext>，别无他物。
func BuildPackage(f Fields, blobs map[Role]Blob) ([]byte, error) {
	d := doc{Format: FormatName, Version: FormatVersion, SiteName: f.SiteName, Intro: f.Intro, Accent: f.Accent,
		Footer: f.Footer, ButtonText: f.ButtonText, Assets: map[string]string{}}
	for _, role := range Roles {
		if b, ok := blobs[role]; ok {
			d.Assets[string(role)] = "assets/" + fileName(role, b.Mime)
		}
	}
	body, err := json.MarshalIndent(d, "", "  ")
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	add := func(name string, method uint16, data []byte) error {
		h := &zip.FileHeader{Name: name, Method: method, Modified: packageTime}
		h.SetMode(0o644)
		w, err := zw.CreateHeader(h)
		if err != nil {
			return err
		}
		_, err = w.Write(data)
		return err
	}
	if err := add("profile.json", zip.Deflate, append(body, '\n')); err != nil {
		return nil, err
	}
	for _, role := range Roles {
		if b, ok := blobs[role]; ok {
			if err := add("assets/"+fileName(role, b.Mime), zip.Store, b.Data); err != nil {
				return nil, err
			}
		}
	}
	if err := zw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// checkName 返回条目名的问题描述；空串表示名字形式安全且在白名单内。
func checkName(name string) string {
	if name == "" {
		return "条目名为空"
	}
	for _, r := range name {
		if r < 0x20 || r == 0x7f {
			return "条目名包含控制字符"
		}
	}
	if strings.Contains(name, `\`) {
		return "条目名包含反斜杠"
	}
	if strings.HasPrefix(name, "/") || (len(name) >= 2 && name[1] == ':') {
		return "条目使用了绝对路径"
	}
	trimmed := strings.TrimSuffix(name, "/")
	for _, seg := range strings.Split(trimmed, "/") {
		if seg == ".." {
			return "条目路径包含 .."
		}
		if seg == "." || seg == "" {
			return "条目路径未规范化"
		}
	}
	if name == "profile.json" || name == "assets/" || entryRE.MatchString(name) {
		return ""
	}
	return "不允许的文件"
}

func entryLimit(name string) int64 {
	if name == "profile.json" {
		return maxProfileJSON
	}
	if m := entryRE.FindStringSubmatch(name); m != nil {
		return Role(m[1]).MaxBytes()
	}
	return 0
}

func readEntry(f *zip.File, limit int64) ([]byte, error) {
	rc, err := f.Open()
	if err != nil {
		return nil, pkgErr("无法读取 %s", f.Name)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, limit+1))
	if err != nil {
		return nil, pkgErr("%s 已损坏或与声明的大小不符", f.Name)
	}
	if int64(len(data)) > limit {
		return nil, pkgErr("%s 解压后超过 %d KiB 上限", f.Name, limit>>10)
	}
	if uint64(len(data)) != f.UncompressedSize64 {
		return nil, pkgErr("%s 的实际解压大小与 ZIP 头声明不一致", f.Name)
	}
	return data, nil
}

// ParsePackage 在内存中完成结构、版本、素材的全部校验，不接触文件系统。
// 条目名只用于对照白名单，从不用来拼接任何磁盘路径。
func ParsePackage(data []byte) (Candidate, error) {
	if len(data) > MaxPackageBytes {
		return Candidate{}, pkgErr("文件超过 %d MiB", MaxPackageBytes>>20)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if errors.Is(err, zip.ErrInsecurePath) {
		return Candidate{}, pkgErr("包含不安全的路径")
	}
	if err != nil {
		return Candidate{}, pkgErr("不是有效的 ZIP 文件（可能已损坏）")
	}
	if len(zr.File) > maxEntries {
		return Candidate{}, pkgErr("条目过多（%d > %d）", len(zr.File), maxEntries)
	}
	byName := map[string]*zip.File{}
	seen := map[string]bool{}
	var total uint64
	for _, f := range zr.File {
		if why := checkName(f.Name); why != "" {
			return Candidate{}, pkgErr("%s：%q", why, f.Name)
		}
		key := strings.ToLower(f.Name)
		if seen[key] {
			return Candidate{}, pkgErr("重复的条目：%q", f.Name)
		}
		seen[key] = true
		mode := f.Mode()
		switch {
		case mode&os.ModeSymlink != 0:
			return Candidate{}, pkgErr("%q 是符号链接", f.Name)
		case f.Name == "assets/":
			if !mode.IsDir() || f.UncompressedSize64 != 0 {
				return Candidate{}, pkgErr("assets/ 必须是空目录")
			}
			continue
		case !mode.IsRegular():
			return Candidate{}, pkgErr("%q 不是普通文件", f.Name)
		}
		if f.Flags&0x1 != 0 {
			return Candidate{}, pkgErr("%q 已加密", f.Name)
		}
		if f.Method != zip.Store && f.Method != zip.Deflate {
			return Candidate{}, pkgErr("%q 使用了不支持的压缩方式", f.Name)
		}
		limit := entryLimit(f.Name)
		if f.UncompressedSize64 > uint64(limit) {
			return Candidate{}, pkgErr("%q 声明大小 %d 字节，超过 %d KiB 上限", f.Name, f.UncompressedSize64, limit>>10)
		}
		if f.UncompressedSize64 > ratioFloor && (f.CompressedSize64 == 0 || f.UncompressedSize64/f.CompressedSize64 > maxRatio) {
			return Candidate{}, pkgErr("%q 压缩比异常（超过 %d:1）", f.Name, maxRatio)
		}
		total += f.UncompressedSize64
		if total > maxUncompressed {
			return Candidate{}, pkgErr("解压后总大小超过 %d MiB", maxUncompressed>>20)
		}
		byName[f.Name] = f
	}
	pf := byName["profile.json"]
	if pf == nil {
		return Candidate{}, pkgErr("缺少 profile.json")
	}
	raw, err := readEntry(pf, maxProfileJSON)
	if err != nil {
		return Candidate{}, err
	}
	// 先只看格式标识与版本：未来版本可能有新字段，不能因“未知字段”给出误导性错误，也不猜测兼容。
	var head struct {
		Format  string `json:"format"`
		Version int    `json:"version"`
	}
	if err := json.Unmarshal(raw, &head); err != nil {
		return Candidate{}, pkgErr("profile.json 不是有效的 JSON")
	}
	if head.Format != FormatName {
		return Candidate{}, pkgErr("不是 FilePanel 个性化配置包")
	}
	if head.Version != FormatVersion {
		return Candidate{}, pkgErr("配置包版本 %d 与当前支持的版本 %d 不兼容", head.Version, FormatVersion)
	}
	var d doc
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil || dec.Decode(new(any)) != io.EOF {
		return Candidate{}, pkgErr("profile.json 含有未知或格式错误的字段")
	}
	fields, err := Fields{SiteName: d.SiteName, Intro: d.Intro, Accent: d.Accent, Footer: d.Footer, ButtonText: d.ButtonText}.Normalize()
	if err != nil {
		return Candidate{}, pkgErr("%s", err.Error())
	}
	cand := Candidate{Fields: fields, Blobs: map[Role]Blob{}}
	used := map[string]bool{"profile.json": true}
	for key, ref := range d.Assets {
		role, ok := ParseRole(key)
		if !ok {
			return Candidate{}, pkgErr("未知的素材用途 %q", key)
		}
		m := entryRE.FindStringSubmatch(ref)
		if m == nil || m[1] != string(role) {
			return Candidate{}, pkgErr("素材引用 %q 不是规范的 assets/%s.<png|jpg|webp>", ref, role)
		}
		f := byName[ref]
		if f == nil {
			return Candidate{}, pkgErr("素材引用 %q 在包中不存在", ref)
		}
		blob, err := readEntry(f, role.MaxBytes())
		if err != nil {
			return Candidate{}, err
		}
		mime, err := ValidateImage(role, blob)
		if err != nil {
			return Candidate{}, pkgErr("%s", err.Error())
		}
		if mimeExt[mime] != m[2] {
			return Candidate{}, pkgErr("%q 的扩展名与实际图片类型 %s 不符", ref, mime)
		}
		cand.Blobs[role] = Blob{Mime: mime, Data: blob}
		used[ref] = true
	}
	for name := range byName {
		if !used[name] {
			return Candidate{}, pkgErr("%q 没有被 profile.json 引用", name)
		}
	}
	return cand, nil
}
