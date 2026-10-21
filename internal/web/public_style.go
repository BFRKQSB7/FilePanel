package web

// publicCSS 是唯一官方公开页模板的样式。不引入外部资源，不允许内容自定义 CSS。
const publicCSS = `
:root{--bg:#f4f5f7;--card:#fff;--ink:#202936;--muted:#526071;--line:#dce0e6;--blue:#315f9e;--soft:#e8eff8;--warn-bg:#fff6df;--warn-ink:#805c16;--err-bg:#fceeee;--err-ink:#b13535;--btn1:#315f9e;--mark1:#315f9e;--hero1:#315f9e;--radius-sm:4px;--radius-md:6px;--radius-lg:8px;color-scheme:light dark}
@media(prefers-color-scheme:dark){:root{--bg:#171c24;--card:#202731;--ink:#edf0f4;--muted:#adb8c7;--line:#394452;--blue:#91b8ee;--soft:#293a50;--warn-bg:#3a2e12;--warn-ink:#f1c872;--err-bg:#3a1c1c;--err-ink:#ff9d97}}
*{box-sizing:border-box}html{-webkit-text-size-adjust:100%}
body{margin:0;background:var(--bg);color:var(--ink);font:15px/1.7 -apple-system,BlinkMacSystemFont,"Segoe UI","PingFang SC","Microsoft YaHei",sans-serif;overflow-wrap:anywhere}
a{color:var(--blue)}
.top{background:var(--card);border-bottom:1px solid var(--line)}
.top-in,.wrap{max-width:1120px;margin:0 auto;padding:0 24px}
.top-in{display:flex;align-items:center;justify-content:space-between;gap:16px;min-height:64px;flex-wrap:wrap}
.brand{display:flex;align-items:center;gap:10px;font-weight:750;color:var(--ink);text-decoration:none;font-size:17px}
.mark{width:34px;height:34px;border-radius:var(--radius-md);background:var(--mark1);color:#fff;display:grid;place-items:center;font-weight:800}
.logo{display:block;height:34px;width:auto;max-width:160px;object-fit:contain;border-radius:var(--radius-lg)}
.search{display:flex;gap:8px}
.search input{height:44px;width:min(260px,60vw);border:1px solid var(--line);background:var(--bg);color:var(--ink);border-radius:var(--radius-sm);padding:0 12px;font:inherit;font-size:14px}
.btn{display:inline-flex;align-items:center;justify-content:center;border:0;border-radius:var(--radius-sm);padding:0 18px;height:44px;font:inherit;font-size:14px;font-weight:650;color:#fff;background:var(--btn1);text-decoration:none;cursor:pointer}
.btn.ghost{background:var(--soft);color:var(--blue)}
.btn.big{height:46px;padding:0 28px;font-size:16px}
.btn[aria-disabled=true],.btn:disabled{opacity:.6;pointer-events:none}
.btn:hover{filter:brightness(.9)}
.btn:active{filter:brightness(.8)}
:focus-visible{outline:2px solid var(--blue);outline-offset:3px}
.btn,input,select{transition:background-color .14s,border-color .14s}
select{appearance:none;max-width:100%;min-height:44px;padding:9px 40px 9px 12px;border:1px solid var(--line);border-radius:var(--radius-sm);color:var(--ink);background:var(--card) url("data:image/svg+xml,%3Csvg xmlns='http://www.w3.org/2000/svg' width='16' height='16'%3E%3Cpath d='m4 6 4 4 4-4' stroke='%23778394' stroke-width='1.5' fill='none'/%3E%3C/svg%3E") no-repeat right 12px center;font:inherit}
option{background:var(--card);color:var(--ink)}
select:hover,.search input:hover{border-color:var(--blue)}
select:disabled{opacity:.6;cursor:not-allowed}
.hero{margin:28px 0;padding:44px 40px;border-radius:var(--radius-md);color:#fff;background:var(--hero1)}
.hero h1{margin:0 0 8px;font-size:clamp(24px,4vw,36px);line-height:1.25;letter-spacing:-.5px}
.hero p{margin:0;max-width:620px;opacity:.9}
.notice{margin:0 0 20px;padding:12px 16px;border-radius:var(--radius-md);background:var(--warn-bg);color:var(--warn-ink);font-size:14px}
.notice.err{background:var(--err-bg);color:var(--err-ink)}
.grid{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:20px}
.card{background:var(--card);border:1px solid var(--line);border-radius:var(--radius-md);overflow:hidden;display:flex;flex-direction:column;text-decoration:none;color:inherit;transition:border-color .14s,background-color .14s}
.card:hover{border-color:var(--blue);background:var(--bg)}
.cover{aspect-ratio:16/9;background:var(--soft);display:grid;place-items:center;color:var(--blue);font-size:40px;overflow:hidden}
.cover img{width:100%;height:100%;object-fit:cover;display:block}
.card-body{padding:18px 20px 20px;display:flex;flex-direction:column;gap:6px;flex:1}
.card h2{margin:0;font-size:18px;line-height:1.4}
.card p{margin:0;color:var(--muted);font-size:14px;display:-webkit-box;-webkit-line-clamp:3;-webkit-box-orient:vertical;overflow:hidden}
.meta{margin-top:auto;padding-top:8px;display:flex;gap:10px;flex-wrap:wrap;font-size:12px;color:var(--muted)}
.tag{background:var(--soft);color:var(--blue);border-radius:var(--radius-md);padding:1px 10px;font-weight:650}
.pager{display:flex;justify-content:center;align-items:center;gap:14px;margin:30px 0;color:var(--muted);font-size:14px}
.empty{text-align:center;padding:60px 20px;color:var(--muted);background:var(--card);border:1px dashed var(--line);border-radius:var(--radius-md)}
.empty strong{display:block;color:var(--ink);font-size:18px;margin-bottom:6px}
.article{background:var(--card);border:1px solid var(--line);border-radius:var(--radius-md);padding:36px 40px;margin:28px 0}
.article>h1{margin:0 0 6px;font-size:clamp(24px,4vw,34px);line-height:1.3}
.article .sub{color:var(--muted);margin:0 0 20px}
.article .banner{border-radius:var(--radius-md);overflow:hidden;margin:0 0 22px}
.article .banner img{display:block;width:100%;height:auto}
.dl{display:flex;gap:14px;align-items:center;flex-wrap:wrap;padding:18px 20px;border-radius:var(--radius-md);background:var(--soft);margin:0 0 26px}
.dl small{color:var(--muted);display:block}
.md h2,.md h3,.md h4{margin:1.4em 0 .5em;line-height:1.35}
.md p,.md ul,.md ol,.md blockquote,.md pre{margin:0 0 1em}
.md blockquote{border-left:4px solid var(--blue);padding:2px 16px;color:var(--muted);background:var(--bg);border-radius:0 var(--radius-sm) var(--radius-sm) 0}
.md code{background:var(--bg);padding:1px 6px;border-radius:var(--radius-sm);font-size:.9em}
.md pre{background:var(--bg);padding:14px 16px;border-radius:var(--radius-md);overflow:auto}
.md pre code{padding:0;background:none}
.md img{max-width:100%;height:auto;border-radius:var(--radius-md)}
.md hr{border:0;border-top:1px solid var(--line);margin:1.6em 0}
.shots{display:grid;grid-template-columns:repeat(2,minmax(0,1fr));gap:14px;margin:0 0 24px}
.shots img{width:100%;height:auto;border-radius:var(--radius-md);border:1px solid var(--line);display:block}
.facts{display:grid;grid-template-columns:auto 1fr;gap:6px 18px;margin:0;font-size:13px;color:var(--muted)}
.facts dt{font-weight:650}.facts dd{margin:0;font-family:ui-monospace,Consolas,monospace}
.foot{padding:30px 0 50px;text-align:center;color:var(--muted);font-size:13px}
@media(max-width:760px){.grid,.shots{grid-template-columns:1fr}.hero{padding:30px 22px;border-radius:var(--radius-md)}.article{padding:22px 18px;border-radius:var(--radius-md)}.top-in,.wrap{padding:0 16px}.search{width:100%}.search input{flex:1;width:auto}}
@media(prefers-reduced-motion:reduce){.btn,input,select,.card{transition:none}}
@media(forced-colors:active){select{appearance:auto;background-image:none}}
`
