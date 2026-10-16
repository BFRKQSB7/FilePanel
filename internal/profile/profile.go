// Package profile 管理唯一官方模板上的站点品牌配置（站点名、Logo、简介、强调色、横幅/页面背景、
// 页脚、默认按钮文字）以及可迁移的个性化 ZIP 包。
//
// 数据边界：配置包只含 profile.json 与它引用的图片素材，不含文件、资源正文、账号、节点密钥、
// 证书或用量。应用采用“版本化素材目录 + 单条 UPDATE 切换”，失败或崩溃后旧配置或新配置必有其一完整。
package profile

import (
	"errors"
	"fmt"
	"math"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

const (
	FormatName    = "filepanel-profile"
	FormatVersion = 1

	DefaultSiteName   = "FilePanel"
	DefaultIntro      = "公开发布的文件与版本说明，支持断点续传。点击卡片查看介绍后下载。"
	DefaultFooter     = "由 FilePanel 提供文件发布"
	DefaultButtonText = "立即下载"

	maxSiteName = 40
	maxIntro    = 200
	maxFooter   = 120
	maxButton   = 20
)

// Role 是素材的逻辑用途。包内和磁盘上的文件名都由 Role 与图片类型决定，从不使用外部提供的名字。
type Role string

const (
	Logo       Role = "logo"
	Banner     Role = "banner"
	Background Role = "background"
)

var Roles = []Role{Logo, Banner, Background}

func ParseRole(s string) (Role, bool) {
	for _, r := range Roles {
		if string(r) == s {
			return r, true
		}
	}
	return "", false
}

func (r Role) Label() string {
	switch r {
	case Logo:
		return "Logo"
	case Banner:
		return "横幅图片"
	default:
		return "页面背景"
	}
}

// MaxBytes 是单张素材的大小上限。
func (r Role) MaxBytes() int64 {
	if r == Logo {
		return 512 << 10
	}
	return 2 << 20
}

// ErrSpace 表示磁盘安全空间不足；调用方应保持原配置不变。
var ErrSpace = errors.New("可用空间不足，已保留系统安全空间")

// UserError 是可以直接展示给管理员的校验错误（输入或配置包的问题，不是服务端故障）。
type UserError struct{ Msg string }

func (e *UserError) Error() string { return e.Msg }

func userErr(format string, args ...any) error { return &UserError{Msg: fmt.Sprintf(format, args...)} }

// Fields 是全站文字与颜色配置。空串表示“使用内置默认”，默认值在渲染时才套用，
// 这样导出/导入不会把默认值固化成显式值，单资源的明确设置也永远优先。
type Fields struct {
	SiteName   string `json:"site_name"`
	Intro      string `json:"intro"`
	Accent     string `json:"accent"`
	Footer     string `json:"footer"`
	ButtonText string `json:"button_text"`
}

var accentRE = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

// minAccentContrast 是强调色与白色按钮文字之间要求的最小对比度（WCAG 普通文字 AA）。
const minAccentContrast = 4.5

func cleanText(s string, max int, name string) (string, error) {
	if !utf8.ValidString(s) {
		return "", userErr("%s包含无效字符", name)
	}
	var b strings.Builder
	for _, r := range s {
		switch {
		case r == '\n' || r == '\r' || r == '\t':
			b.WriteRune(' ')
		case unicode.IsControl(r):
		case r >= 0x202A && r <= 0x202E, r >= 0x2066 && r <= 0x2069: // 双向文本控制符，可用于伪装文字
		default:
			b.WriteRune(r)
		}
	}
	out := strings.TrimSpace(b.String())
	if utf8.RuneCountInString(out) > max {
		return "", userErr("%s不能超过 %d 个字符", name, max)
	}
	return out, nil
}

// Normalize 清理并校验文字与强调色，返回规范形式。
func (f Fields) Normalize() (Fields, error) {
	var out Fields
	var err error
	if out.SiteName, err = cleanText(f.SiteName, maxSiteName, "站点名"); err != nil {
		return Fields{}, err
	}
	if out.Intro, err = cleanText(f.Intro, maxIntro, "简介"); err != nil {
		return Fields{}, err
	}
	if out.Footer, err = cleanText(f.Footer, maxFooter, "页脚"); err != nil {
		return Fields{}, err
	}
	if out.ButtonText, err = cleanText(f.ButtonText, maxButton, "默认按钮文字"); err != nil {
		return Fields{}, err
	}
	accent := strings.TrimSpace(f.Accent)
	if accent != "" {
		if !accentRE.MatchString(accent) {
			return Fields{}, userErr("强调色必须是 #RRGGBB 格式，例如 #4263eb")
		}
		accent = strings.ToLower(accent)
		c, _ := parseHex(accent)
		// 白色按钮文字落在强调色上；强调色文字落在浅色着色底上。两处都要达到 AA。
		if ratio := math.Min(contrast(c, white), contrast(c, lightSoft(c))); ratio < minAccentContrast {
			return Fields{}, userErr("强调色 %s 太浅：与白色按钮文字/浅色底的对比度为 %.1f，至少需要 %.1f", accent, ratio, minAccentContrast)
		}
	}
	out.Accent = accent
	return out, nil
}

// Blob 是一张已通过内容校验的素材。
type Blob struct {
	Mime string
	Data []byte
}

// Candidate 是一份完整、已规范化、等待预览或应用的配置（文字 + 素材）。
type Candidate struct {
	Fields Fields
	Blobs  map[Role]Blob
}

func (c Candidate) TotalBytes() uint64 {
	var n uint64
	for _, b := range c.Blobs {
		n += uint64(len(b.Data))
	}
	return n
}

var mimeExt = map[string]string{"image/png": "png", "image/jpeg": "jpg", "image/webp": "webp"}

func fileName(role Role, mime string) string { return string(role) + "." + mimeExt[mime] }

var fileNameRE = regexp.MustCompile(`^(logo|banner|background)\.(png|jpg|webp)$`)
