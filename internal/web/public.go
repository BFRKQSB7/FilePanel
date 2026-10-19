package web

import (
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"filepanel/internal/download"
	"filepanel/internal/profile"
	"filepanel/internal/share"
	"filepanel/internal/usage"
)

var publicTemplates = template.Must(template.New("public").Funcs(template.FuncMap{
	"size": formatSize,
	"add":  func(a, b int) int { return a + b },
}).Parse(`
{{define "head"}}<!doctype html><html lang="zh-CN"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><meta name="color-scheme" content="light dark"><meta name="referrer" content="no-referrer"><title>{{.Title}}</title>{{if .Canonical}}<link rel="canonical" href="{{.Canonical}}">{{end}}<style>{{.CSS}}</style></head><body>
<header class="top"><div class="top-in"><a class="brand" href="{{.Base}}/">{{if .Logo}}<img class="logo" src="{{.Logo}}" alt="">{{else}}<span class="mark">{{.Mark}}</span>{{end}}{{.Brand}}</a><form class="search" action="{{.Base}}/" method="get" role="search"><input name="q" value="{{.Query}}" placeholder="搜索资源" aria-label="搜索资源" maxlength="80"><button class="btn" type="submit">搜索</button></form></div></header><main class="wrap">{{end}}
{{define "foot"}}</main><footer class="foot">{{.Footer}}</footer></body></html>{{end}}
{{define "home"}}{{template "head" .}}
<section class="hero"><h1>资源下载</h1><p>{{.Intro}}</p></section>
{{if .Notice}}<div class="notice" role="status">{{.Notice}}</div>{{end}}
{{if .Items}}<div class="grid">{{range .Items}}<a class="card" href="{{$.Base}}/s/{{.Slug}}"><div class="cover">{{if .Cover}}<img src="/a/{{.Cover}}" alt="" loading="lazy">{{else}}▤{{end}}</div><div class="card-body"><h2>{{.Name}}</h2>{{if .Summary}}<p>{{.Summary}}</p>{{end}}<div class="meta">{{if .Version}}<span class="tag">{{.Version}}</span>{{end}}<span>{{size .Size}}</span></div></div></a>{{end}}</div>
{{if or .Prev .Next}}<nav class="pager" aria-label="分页">{{if .Prev}}<a class="btn ghost" href="{{.Prev}}">上一页</a>{{end}}<span>第 {{.Page}} / {{.Pages}} 页</span>{{if .Next}}<a class="btn ghost" href="{{.Next}}">下一页</a>{{end}}</nav>{{end}}
{{else if .Query}}<div class="empty"><strong>没有找到匹配的资源</strong>换个关键词试试，或 <a href="{{.Base}}/">查看全部资源</a>。</div>
{{else}}<div class="empty"><strong>这里还没有公开资源</strong>管理员发布资源后会显示在这里。</div>{{end}}
{{template "foot" .}}{{end}}
{{define "share"}}{{template "head" .}}
<article class="article"><h1>{{.Name}}</h1><p class="sub">{{if .Version}}版本 {{.Version}} · {{end}}{{size .Size}}</p>
{{if .Summary}}<p>{{.Summary}}</p>{{end}}
{{if .Cover}}<div class="banner"><img src="/a/{{.Cover}}" alt="{{.Name}} 封面"></div>{{end}}
{{if .Notice}}<div class="notice {{if .NoticeErr}}err{{end}}" role="status">{{.Notice}}</div>{{end}}
<div class="dl"><a class="btn big" href="{{.Base}}/s/{{.Slug}}/get" {{if not .CanDownload}}aria-disabled="true" tabindex="-1"{{end}}>{{.Button}}</a><div>{{.FileName}}<small>支持断点续传 · 下载链接 5 分钟内有效</small></div></div>
{{if .Lines}}<form action="{{.Base}}/s/{{.Slug}}/get" method="get"><label>下载线路 <select name="relay"><option value="">自动（管理员默认出口）</option><option value="local">本机出口</option>{{range .Lines}}<option value="{{.ID}}">{{.Label}}</option>{{end}}</select></label> <button class="btn ghost" type="submit">使用所选线路</button></form>{{end}}
{{if .Shots}}<div class="shots">{{range .Shots}}<img src="/a/{{.}}" alt="截图" loading="lazy">{{end}}</div>{{end}}
{{if .Body}}<div class="md">{{.Body}}</div>{{end}}
<dl class="facts"><dt>文件名</dt><dd>{{.FileName}}</dd><dt>大小</dt><dd>{{size .Size}}</dd><dt>SHA-256</dt><dd>{{.SHA}}</dd></dl></article>
{{template "foot" .}}{{end}}
{{define "preview"}}{{template "head" .}}
<section class="hero"><h1>资源下载</h1><p>{{.Intro}}</p></section>
<div class="grid"><a class="card" href="#"><div class="cover">▤</div><div class="card-body"><h2>示例资源</h2><p>这是预览用的示例卡片，不会出现在公开主页。</p><div class="meta"><span class="tag">1.0.0</span><span>12.3 MB</span></div></div></a></div>
<article class="article"><h1>示例资源</h1><p class="sub">版本 1.0.0 · 12.3 MB</p><div class="dl"><a class="btn big" href="#">{{.Button}}</a><div>example.bin<small>支持断点续传 · 下载链接 5 分钟内有效</small></div></div></article>
{{template "foot" .}}{{end}}
{{define "error"}}{{template "head" .}}
<div class="empty" role="alert"><strong>{{.Heading}}</strong>{{.Message}}<p>{{if .Retry}}<a class="btn" href="{{.Retry}}">{{.RetryText}}</a> {{end}}<a class="btn ghost" href="{{.Back}}">返回</a></p></div>
{{template "foot" .}}{{end}}
`))

type page struct {
	Base, Canonical                          string
	Title, Brand, Mark, Query, Intro, Footer string
	Logo                                     template.URL // 仅来自 profile.safeURL 白名单（/site/… 或位图 data URL）
	CSS                                      template.CSS
	Notice                                   string
	NoticeErr                                bool
}

// newPage 套用站点个性化：品牌、Logo、简介、页脚与主题 CSS 都来自已校验的配置。
func (s *Server) newPage(title, query string) page {
	p := pageFor(s.profile.View(), title, query)
	if s.access != nil {
		p.Base = s.access.BaseURL()
	}
	return p
}

func pageFor(v profile.View, title, query string) page {
	return page{Title: title + " · " + v.SiteName, CSS: template.CSS(publicCSS + v.CSS), Brand: v.SiteName, Mark: v.Mark,
		Intro: v.Intro, Footer: v.Footer, Logo: template.URL(v.LogoURL), Query: query}
}

func formatSize(n int64) string {
	if n < 1000 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB", "TB"}
	v, i := float64(n)/1000, 0
	for v >= 1000 && i < len(units)-1 {
		v /= 1000
		i++
	}
	return fmt.Sprintf("%.1f %s", v, units[i])
}

type card struct {
	Slug, Name, Summary, Version, Cover string
	Size                                int64
}

func displayName(s share.Share) string {
	if s.Title != "" {
		return s.Title
	}
	return s.FileName
}

func (s *Server) renderPublic(w http.ResponseWriter, status int, name string, data any) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	_ = publicTemplates.ExecuteTemplate(w, name, data)
}

func (s *Server) errorPage(w http.ResponseWriter, r *http.Request, status int, heading, message, retry, retryText, back string) {
	if back == "" {
		back = "/"
	}
	if retry != "" || status >= 500 {
		w.Header().Set("Retry-After", "60")
	}
	if !strings.Contains(r.Header.Get("Accept"), "text/html") && r.URL.Path != "/" {
		http.Error(w, heading+"："+message, status)
		return
	}
	data := struct {
		page
		Heading, Message, Retry, RetryText, Back string
	}{s.newPage(heading, ""), heading, message, retry, retryText, back}
	s.renderPublic(w, status, "error", data)
}

func (s *Server) clientIP(r *http.Request) string {
	cfg, err := s.meter.Config()
	if err != nil {
		cfg = usage.DefaultConfig()
	}
	return download.ClientIP(r, cfg)
}

// quotaNotice 返回访客可见的保护状态说明；空串表示可下载。
func (s *Server) quotaNotice(r *http.Request) (string, bool) {
	err := s.meter.Available(s.clientIP(r))
	switch {
	case err == nil:
		return "", true
	case errors.Is(err, usage.ErrQuota):
		return "本机流量已达保护线，下载暂时关闭，说明页仍可浏览。请在下个统计周期开始后重试。", false
	case errors.Is(err, usage.ErrIPLimit):
		return "当前网络的下载量已达本周期上限，请稍后再试。", false
	case errors.Is(err, usage.ErrBusy):
		return "当前网络同时进行的下载过多，请等待其他下载结束后重试。", false
	}
	return "暂时无法确认下载状态，请刷新页面重试。", false
}

func (s *Server) home(w http.ResponseWriter, r *http.Request) {
	query := strings.TrimSpace(r.URL.Query().Get("q"))
	if len([]rune(query)) > 80 {
		query = string([]rune(query)[:80])
	}
	pageNo, _ := strconv.Atoi(r.URL.Query().Get("page"))
	if pageNo < 1 {
		pageNo = 1
	}
	items, total, err := s.shares.Public(query, pageNo)
	if err != nil {
		s.errorPage(w, r, http.StatusInternalServerError, "资源列表暂时无法加载", "这是服务端的临时问题，不代表没有资源。", r.URL.RequestURI(), "重试", "/")
		return
	}
	pages := (total + share.PageSize - 1) / share.PageSize
	link := func(p int) string {
		v := url.Values{}
		if query != "" {
			v.Set("q", query)
		}
		v.Set("page", strconv.Itoa(p))
		return "/?" + v.Encode()
	}
	data := struct {
		page
		Items       []card
		Page, Pages int
		Prev, Next  string
	}{page: s.newPage("资源下载", query), Page: pageNo, Pages: pages}
	if data.Base != "" {
		data.Canonical = s.entryURL("/")
	}
	for _, it := range items {
		data.Items = append(data.Items, card{Slug: it.Slug, Name: displayName(it), Summary: it.Summary, Version: it.Version, Cover: it.CoverAsset, Size: it.Size})
	}
	if pageNo > 1 {
		data.Prev = link(pageNo - 1)
	}
	if pageNo < pages {
		data.Next = link(pageNo + 1)
	}
	if notice, ok := s.quotaNotice(r); !ok && strings.Contains(notice, "保护线") {
		data.Notice = notice
	}
	if len(items) == 0 && pageNo > 1 && total > 0 {
		http.Redirect(w, r, link(pages), http.StatusFound)
		return
	}
	s.renderPublic(w, http.StatusOK, "home", data)
}

func (s *Server) sharePage(w http.ResponseWriter, r *http.Request) {
	sh, err := s.shares.BySlug(r.PathValue("slug"))
	if err != nil {
		if errors.Is(err, share.ErrNotFound) {
			s.errorPage(w, r, http.StatusNotFound, "资源不存在或已下架", "链接可能已被关闭。", "", "", "/")
		} else {
			s.errorPage(w, r, http.StatusInternalServerError, "页面暂时无法加载", "请稍后重试。", r.URL.RequestURI(), "重试", "/")
		}
		return
	}
	owned := s.shares.AssetOwned(sh.FileID)
	assets, _ := s.shares.Assets(sh.FileID)
	data := struct {
		page
		Slug, Name, Version, Summary, Cover, Button, FileName, SHA string
		Size                                                       int64
		Body                                                       template.HTML
		Shots                                                      []string
		CanDownload                                                bool
		Lines                                                      []publicLine
	}{page: s.newPage(displayName(sh), ""), Slug: sh.Slug, Name: displayName(sh), Version: sh.Version, Summary: sh.Summary,
		Cover: sh.CoverAsset, Button: sh.ButtonText, FileName: sh.FileName, SHA: sh.SHA256, Size: sh.Size}
	if s.nodes != nil {
		if list, e := s.nodes.List(); e == nil {
			for _, n := range list {
				if n.Relay && n.DataReady {
					region := n.Region
					if region == "" {
						region = "地区未知"
					}
					data.Lines = append(data.Lines, publicLine{n.ID, fmt.Sprintf("线路 %d · %s", len(data.Lines)+1, region)})
				}
			}
		}
	}
	if data.Base != "" {
		data.Canonical = s.entryURL("/s/" + sh.Slug)
	}
	if data.Button == "" { // 单资源按钮文字优先；未设置时使用全站默认
		data.Button = s.profile.View().ButtonText
	}
	// 渲染器对全部文本转义，只产出白名单标签。
	data.Body = template.HTML(share.RenderMarkdown(sh.Markdown, owned))
	for _, a := range assets {
		if a.ID != sh.CoverAsset {
			data.Shots = append(data.Shots, a.ID)
		}
	}
	notice, ok := s.quotaNotice(r)
	if !s.needsLocalBudget(r, sh.FileID) {
		notice, ok = "", true
	}
	data.CanDownload, data.Notice, data.NoticeErr = ok, notice, !ok
	s.renderPublic(w, http.StatusOK, "share", data)
}

type publicLine struct{ ID, Label string }

// getLink 为访客签发初次凭证（300 秒）并跳转到受控内容入口。
func (s *Server) getLink(w http.ResponseWriter, r *http.Request) {
	sh, err := s.shares.BySlug(r.PathValue("slug"))
	if err != nil {
		s.errorPage(w, r, http.StatusNotFound, "资源不存在或已下架", "链接可能已被关闭。", "", "", "/")
		return
	}
	ip := s.clientIP(r)
	if err := s.meter.Available(ip); err != nil && s.needsLocalBudget(r, sh.FileID) {
		derr := downloadError(err)
		s.errorPage(w, r, derr.Status, derr.Message, "说明页仍可浏览，稍后可再次尝试下载。", "/s/"+sh.Slug+"/get", "重新尝试", "/s/"+sh.Slug)
		return
	}
	token, _ := s.dl.Issue(download.Claims{File: sh.FileID, SHA: sh.SHA256, AuthVersion: sh.AuthVersion, Scope: "public", IP: ip})
	query := url.Values{"t": {token}}
	if relay := r.URL.Query().Get("relay"); relay != "" {
		query.Set("relay", relay)
	}
	http.Redirect(w, r, s.entryURL("/d/"+url.PathEscape(sh.FileID)+"?"+query.Encode()), http.StatusFound)
}

func downloadError(err error) *download.Error {
	var de *download.Error
	if errors.As(err, &de) {
		return de
	}
	switch {
	case errors.Is(err, usage.ErrBusy), errors.Is(err, usage.ErrIPLimit):
		return &download.Error{Status: http.StatusTooManyRequests, Message: err.Error()}
	case errors.Is(err, usage.ErrQuota):
		return &download.Error{Status: http.StatusServiceUnavailable, Message: err.Error()}
	}
	return &download.Error{Status: http.StatusInternalServerError, Message: "暂时无法下载，请稍后重试"}
}

// content 是公开内容入口：凭证 → 分享授权版本 → 受控流（租约、Range、计量）。
func (s *Server) content(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	ip := s.clientIP(r)
	relink, relinkText, back := "", "", "/"
	if sh, err := s.shares.Get(id); err == nil && sh.Enabled {
		relink, relinkText, back = "/s/"+sh.Slug+"/get", "重新获取下载链接", "/s/"+sh.Slug
	}
	claims, err := s.dl.Verify(r.URL.Query().Get("t"), id, ip)
	if err != nil {
		status, heading := http.StatusForbidden, "下载链接无效"
		if errors.Is(err, download.ErrExpiredToken) {
			status, heading = http.StatusGone, "下载链接已过期"
		}
		s.errorPage(w, r, status, heading, "请回到资源介绍页重新获取。如果更换了网络，也需要重新获取。", relink, relinkText, back)
		return
	}
	if err := s.shares.Authorized(id, claims.AuthVersion); err != nil {
		s.errorPage(w, r, http.StatusGone, "该资源已停止分享", "管理员已关闭分享，旧链接已失效。", "", "", "/")
		return
	}
	if s.distribute(w, r, claims) {
		return
	}
	err = s.dl.Serve(w, r, download.Request{FileID: id, SHA: claims.SHA, Category: usage.CategoryPublic, IP: ip,
		Revalidate: func() error { return s.shares.Authorized(id, claims.AuthVersion) }})
	if err != nil {
		de := downloadError(err)
		if de.Retry > 0 {
			w.Header().Set("Retry-After", strconv.Itoa(int(de.Retry.Seconds())))
		}
		retry, text := relink, relinkText
		if de.Status == http.StatusTooManyRequests || de.Status == http.StatusServiceUnavailable {
			retry, text = r.URL.RequestURI(), "重试"
		}
		s.errorPage(w, r, de.Status, de.Message, "", retry, text, back)
	}
}

func (s *Server) publicAsset(w http.ResponseWriter, r *http.Request) {
	a, data, err := s.shares.OpenAsset(r.PathValue("id"), true)
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if err := s.dl.ServeBytes(w, r, s.clientIP(r), usage.CategoryAsset, a.Mime, data); err != nil {
		de := downloadError(err)
		http.Error(w, de.Message, de.Status)
	}
}
