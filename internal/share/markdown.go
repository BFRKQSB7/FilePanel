package share

import (
	"html"
	"net/url"
	"regexp"
	"strings"
)

var assetRef = regexp.MustCompile(`^asset:([A-Za-z0-9_-]{16,64})$`)

// RenderMarkdown 把受限 Markdown 转为 HTML。所有文本一律转义，不接受原始 HTML；
// 链接只允许 http/https/mailto，图片只能引用本分享自己的素材对象（asset:ID）。
// assetOK 为 nil 时一律不渲染图片。
func RenderMarkdown(src string, assetOK func(id string) bool) string {
	var out strings.Builder
	lines := strings.Split(strings.ReplaceAll(strings.ReplaceAll(src, "\r\n", "\n"), "\r", "\n"), "\n")
	var para []string
	list := ""
	flushPara := func() {
		if len(para) > 0 {
			out.WriteString("<p>" + inline(strings.Join(para, " "), assetOK) + "</p>\n")
			para = nil
		}
	}
	closeList := func() {
		if list != "" {
			out.WriteString("</" + list + ">\n")
			list = ""
		}
	}
	for i := 0; i < len(lines); i++ {
		line := lines[i]
		trimmed := strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(trimmed, "```"):
			flushPara()
			closeList()
			var code []string
			for i++; i < len(lines) && !strings.HasPrefix(strings.TrimSpace(lines[i]), "```"); i++ {
				code = append(code, lines[i])
			}
			out.WriteString("<pre><code>" + html.EscapeString(strings.Join(code, "\n")) + "</code></pre>\n")
		case trimmed == "":
			flushPara()
			closeList()
		case strings.HasPrefix(trimmed, "#"):
			flushPara()
			closeList()
			level := len(trimmed) - len(strings.TrimLeft(trimmed, "#"))
			if level > 3 || level >= len(trimmed) || trimmed[level] != ' ' {
				para = append(para, trimmed)
				continue
			}
			level++ // 页面标题已占 h1，正文标题从 h2 开始
			out.WriteString("<h" + string(rune('0'+level)) + ">" + inline(strings.TrimSpace(trimmed[level-1:]), assetOK) + "</h" + string(rune('0'+level)) + ">\n")
		case trimmed == "---" || trimmed == "***":
			flushPara()
			closeList()
			out.WriteString("<hr>\n")
		case strings.HasPrefix(trimmed, "> "):
			flushPara()
			closeList()
			out.WriteString("<blockquote>" + inline(strings.TrimPrefix(trimmed, "> "), assetOK) + "</blockquote>\n")
		case isBullet(trimmed) || isOrdered(trimmed):
			flushPara()
			kind, text := "ul", trimmed[2:]
			if isOrdered(trimmed) {
				kind, text = "ol", trimmed[strings.Index(trimmed, ".")+1:]
			}
			if list != kind {
				closeList()
				out.WriteString("<" + kind + ">\n")
				list = kind
			}
			out.WriteString("<li>" + inline(strings.TrimSpace(text), assetOK) + "</li>\n")
		default:
			closeList()
			para = append(para, trimmed)
		}
	}
	flushPara()
	closeList()
	return out.String()
}

func isBullet(s string) bool {
	return len(s) > 2 && (s[0] == '-' || s[0] == '*' || s[0] == '+') && s[1] == ' '
}

func isOrdered(s string) bool {
	dot := strings.Index(s, ".")
	if dot < 1 || dot > 3 || dot+1 >= len(s) || s[dot+1] != ' ' {
		return false
	}
	for _, c := range s[:dot] {
		if c < '0' || c > '9' {
			return false
		}
	}
	return true
}

func inline(s string, assetOK func(string) bool) string {
	var out strings.Builder
	for i := 0; i < len(s); {
		rest := s[i:]
		switch {
		case rest[0] == '`':
			if end := strings.IndexByte(rest[1:], '`'); end >= 0 {
				out.WriteString("<code>" + html.EscapeString(rest[1:1+end]) + "</code>")
				i += end + 2
				continue
			}
		case strings.HasPrefix(rest, "**"):
			if end := strings.Index(rest[2:], "**"); end > 0 {
				out.WriteString("<strong>" + inline(rest[2:2+end], assetOK) + "</strong>")
				i += end + 4
				continue
			}
		case rest[0] == '*':
			if end := strings.IndexByte(rest[1:], '*'); end > 0 {
				out.WriteString("<em>" + inline(rest[1:1+end], assetOK) + "</em>")
				i += end + 2
				continue
			}
		case rest[0] == '!' || rest[0] == '[':
			if text, target, n, ok := linkAt(rest); ok {
				if rest[0] == '!' {
					if m := assetRef.FindStringSubmatch(target); m != nil && assetOK != nil && assetOK(m[1]) {
						out.WriteString(`<img src="/a/` + m[1] + `" alt="` + html.EscapeString(text) + `" loading="lazy">`)
						i += n
						continue
					}
					out.WriteString(html.EscapeString(text))
					i += n
					continue
				}
				if safe, ok := safeURL(target); ok {
					out.WriteString(`<a href="` + html.EscapeString(safe) + `" rel="nofollow noopener noreferrer ugc" target="_blank">` + html.EscapeString(text) + `</a>`)
					i += n
					continue
				}
				out.WriteString(html.EscapeString(text))
				i += n
				continue
			}
		}
		// 逐个 rune 写入转义后的文本。
		r := []rune(rest)[0]
		out.WriteString(html.EscapeString(string(r)))
		i += len(string(r))
	}
	return out.String()
}

// linkAt 解析 [text](target) 或 ![text](target)，返回消耗的字节数。
func linkAt(s string) (text, target string, n int, ok bool) {
	start := 0
	if s[0] == '!' {
		start = 1
	}
	if start >= len(s) || s[start] != '[' {
		return
	}
	closeText := strings.Index(s[start:], "](")
	if closeText < 0 {
		return
	}
	text = s[start+1 : start+closeText]
	if strings.ContainsAny(text, "[]\n") {
		return "", "", 0, false
	}
	targetStart := start + closeText + 2
	end := strings.IndexByte(s[targetStart:], ')')
	if end < 0 {
		return "", "", 0, false
	}
	return text, strings.TrimSpace(s[targetStart : targetStart+end]), targetStart + end + 1, true
}

func safeURL(raw string) (string, bool) {
	if raw == "" || len(raw) > 2000 || strings.ContainsAny(raw, " \t\r\n\x00") {
		return "", false
	}
	for _, c := range raw {
		if c < 0x20 || c == 0x7f {
			return "", false
		}
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", false
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https":
		if u.Host == "" {
			return "", false
		}
	case "mailto":
	default:
		return "", false
	}
	return u.String(), true
}
