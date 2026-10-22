package profile

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

type rgb struct{ r, g, b float64 }

var (
	white    = rgb{255, 255, 255}
	black    = rgb{0, 0, 0}
	darkCard = rgb{0x22, 0x29, 0x25} // 与 publicCSS 暗色模式的 --card 保持一致
)

func parseHex(s string) (rgb, bool) {
	if len(s) != 7 || s[0] != '#' {
		return rgb{}, false
	}
	v, err := strconv.ParseUint(s[1:], 16, 32)
	if err != nil {
		return rgb{}, false
	}
	return rgb{float64(v >> 16 & 0xff), float64(v >> 8 & 0xff), float64(v & 0xff)}, true
}

func (c rgb) hex() string {
	return fmt.Sprintf("#%02x%02x%02x", int(math.Round(c.r)), int(math.Round(c.g)), int(math.Round(c.b)))
}

func channel(v float64) float64 {
	v /= 255
	if v <= 0.03928 {
		return v / 12.92
	}
	return math.Pow((v+0.055)/1.055, 2.4)
}

func luminance(c rgb) float64 {
	return 0.2126*channel(c.r) + 0.7152*channel(c.g) + 0.0722*channel(c.b)
}

func contrast(a, b rgb) float64 {
	la, lb := luminance(a), luminance(b)
	if la < lb {
		la, lb = lb, la
	}
	return (la + 0.05) / (lb + 0.05)
}

// mix 返回 a*(1-t)+b*t。
func mix(a, b rgb, t float64) rgb {
	return rgb{a.r*(1-t) + b.r*t, a.g*(1-t) + b.g*t, a.b*(1-t) + b.b*t}
}

// lightSoft / darkSoft 是标签、次要按钮使用的“着色底”。强调色文字会同时出现在卡片和着色底上，
// 所以对比度必须对两者都达标。
func lightSoft(c rgb) rgb { return mix(c, white, 0.95) }
func darkSoft(c rgb) rgb  { return mix(darkCard, c, 0.15) }

// forDark 把强调色逐步提亮，直到在暗色卡片和暗色着色底上的文字对比度都达到 4.5。
func forDark(c, soft rgb) rgb {
	for t := 0.0; t < 0.95; t += 0.05 {
		if m := mix(c, white, t); contrast(m, darkCard) >= minAccentContrast && contrast(m, soft) >= minAccentContrast {
			return m
		}
	}
	return white
}

var allowedURLPrefixes = []string{"/site/", "data:image/png;base64,", "data:image/jpeg;base64,", "data:image/webp;base64,"}

// safeURL 只放行服务端自己生成的站内路径或三种位图的 base64 data URL，其他一律返回空串。
// 页面模板把结果当作可信 URL 使用，所以这里必须是白名单而不是黑名单。
func safeURL(u string) string {
	allowed := false
	for _, p := range allowedURLPrefixes {
		if strings.HasPrefix(u, p) {
			allowed = true
		}
	}
	if !allowed {
		return ""
	}
	for _, r := range u {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune("/+=:;,?._-", r):
		default:
			return ""
		}
	}
	return u
}

// themeCSS 生成追加在官方样式之后的覆盖规则。只输出校验过的颜色和服务端生成的 URL，
// 不接受任何自由 CSS。
func themeCSS(accent string, urls map[Role]string) string {
	var b strings.Builder
	if c, ok := parseHex(accent); ok {
		deep := mix(c, black, 0.2)
		fmt.Fprintf(&b, ":root{--button-ink:#fff;--mark-ink:#fff;--hero-ink:#fff;--blue:%s;--soft:%s;--btn1:%s;--btn2:%s;--mark1:%s;--mark2:%s;--hero1:%s;--hero2:%s;--hero3:%s}",
			c.hex(), lightSoft(c).hex(), c.hex(), deep.hex(), c.hex(), deep.hex(),
			c.hex(), mix(c, black, 0.15).hex(), mix(c, black, 0.3).hex())
		fmt.Fprintf(&b, "@media(prefers-color-scheme:dark){:root{--button-ink:#fff;--mark-ink:#fff;--hero-ink:#fff;--blue:%s;--soft:%s}}", forDark(c, darkSoft(c)).hex(), darkSoft(c).hex())
	}
	if u := safeURL(urls[Banner]); u != "" {
		fmt.Fprintf(&b, `.hero{background-image:linear-gradient(rgba(10,16,40,.55),rgba(10,16,40,.55)),url("%s");background-size:cover;background-position:center}`, u)
	}
	if u := safeURL(urls[Background]); u != "" {
		fmt.Fprintf(&b, `body::before{content:"";position:fixed;inset:0;z-index:-1;background:url("%s") center/cover no-repeat}body::after{content:"";position:fixed;inset:0;z-index:-1;background:var(--bg);opacity:.88}`, u)
	}
	return b.String()
}

// View 是公开页面渲染所需的、已套用默认值的品牌信息。
type View struct {
	SiteName, Mark, Intro, Footer, ButtonText string
	LogoURL                                   string
	CSS                                       string
	Revision                                  int64
}

// NewView 套用默认值并生成主题 CSS。urls 把素材角色映射到页面中使用的 URL
// （正式页面是 /site/<role>?v=<revision>，预览是 data URL）。
func NewView(f Fields, urls map[Role]string) View {
	v := View{SiteName: f.SiteName, Intro: f.Intro, Footer: f.Footer, ButtonText: f.ButtonText}
	if v.SiteName == "" {
		v.SiteName = DefaultSiteName
	}
	if v.Intro == "" {
		v.Intro = DefaultIntro
	}
	if v.Footer == "" {
		v.Footer = DefaultFooter
	}
	if v.ButtonText == "" {
		v.ButtonText = DefaultButtonText
	}
	v.Mark = strings.ToUpper(string([]rune(v.SiteName)[:1]))
	v.LogoURL = safeURL(urls[Logo])
	v.CSS = themeCSS(f.Accent, urls)
	return v
}
