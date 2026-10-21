(() => {
'use strict';
const FP = window.FP;
if (!FP) return;
function el(tag, props = {}, ...children) {
  const n = document.createElement(tag);
  for (const [k,v] of Object.entries(props)) {
    if (k === 'text') n.textContent = v;
    else if (k.startsWith('on')) n.addEventListener(k.slice(2),v);
    else if (v !== false && v !== undefined) n.setAttribute(k,v === true ? '' : v);
  }
  n.append(...children.filter(Boolean)); return n;
}
const json = body => ({method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
const heading = text => el('h3',{text});
const hint = text => el('p',{text,style:'line-height:1.8'});
const button = (text,click,primary=false) => el('button',{type:'button',class:primary?'primary-btn':'quiet-btn',text,onclick:click});
const field = (label,node,help) => {
  const caption = el('label',{for:node.id,text:label});
  if (node.type === 'checkbox') caption.prepend(node);
  return el('div',{class:'field'},caption,node.type !== 'checkbox' && node,help && hint(help));
};
const input = (id,placeholder,type='text') => el('input',{id,type,placeholder,autocomplete:'off'});
const root = el('section',{class:'section-card'});
const settings = el('section',{class:'section-card settings',style:'margin-top:20px'});
document.querySelector('#settings-access').append(root);
root.prepend(settings);
let current, timer, refreshing=false, applying=false;
const summary = el('div',{class:'setting-form'}), details = el('div',{class:'setting-form'});
const mode = el('select',{id:'access-mode','aria-label':'访问模式'},
  el('option',{value:'manual',text:'使用现有 IP 或端口地址'}),el('option',{value:'proxy',text:'已有 Nginx / Caddy 等 HTTPS 代理'}),el('option',{value:'auto',text:'绑定域名，自动申请 HTTPS 证书'}));
const url = input('access-url','例如 https://files.example.com 或 http://域名:映射端口','url');
const ipv4 = input('access-ipv4','已确认公网 IPv4；不要直接照抄内网网卡地址');
const ipv6 = input('access-ipv6','已确认公网 IPv6；没有则留空');
const httpPort = input('access-http-port','未知请留空','number');
const httpsPort = input('access-https-port','未知请留空','number');
for (const n of [httpPort,httpsPort]) {n.min=1;n.max=65535;}
const proxyDNS = input('access-proxy-dns','','checkbox');

const email = input('access-email','可选：证书通知邮箱','email');
const terms = input('access-terms','','checkbox');
const checkButton = button('检查候选地址',check,true), applyButton = button('验证并启用',apply,true);
const form = el('form',{id:'access-form',onsubmit:e=>{e.preventDefault();check();}},
  heading('1 · 填写要使用的访问地址'),hint('当前管理连接会保留。检查与申请失败时，主地址和现有证书保持可用。'),
  field('我想怎样访问',mode,'不确定时先保留现有地址。自动申请证书要求公网 80 和 443 端口可达；额外服务器不是必需的。'),field('完整访问地址',url),field('公网 IPv4 地址',ipv4,'从服务器服务商控制台查找，用于生成域名解析建议；不知道时先留空，检查结果会提示缺项。'),field('公网 IPv6 地址（可选）',ipv6,'没有 IPv6 可留空。不要填写 fe80 开头的本地地址。'),
  el('div',{style:'display:grid;grid-template-columns:repeat(auto-fit,minmax(170px,1fr));gap:12px'},field('外部 HTTP 端口',httpPort,'服务商映射给你的未加密访问端口；未知留空。'),field('外部 HTTPS 端口',httpsPort,'服务商映射给你的加密访问端口；未知留空。')),
  field('域名使用 Cloudflare 等代理',proxyDNS,'仅在 DNS 控制台开启橙云等代理时勾选；不知道可先不选，根据检查结果处理。'),
  field('证书通知邮箱',email),
  el('label',{},terms,el('span',{text:'自动证书：我同意证书机构的'}),el('a',{href:'https://letsencrypt.org/repository/',target:'_blank',rel:'noopener noreferrer',text:'服务条款'})),
  el('div',{style:'display:flex;flex-wrap:wrap;gap:10px;margin-top:18px'},checkButton,applyButton,button('刷新诊断 / 证书状态',()=>refresh(false))));
root.append(el('div',{class:'section-head'},el('div',{},el('div',{class:'section-title',text:'访问地址与 HTTPS'}),el('div',{class:'section-sub',text:'先检查，再验证部署，最后切换主地址'}))),el('div',{class:'setting-form'},form),details);
settings.append(el('div',{class:'section-head'},el('div',{class:'section-title',text:'访问地址与证书'})),summary);

function read() {
  return {mode:mode.value,url:url.value.trim(),ipv4:ipv4.value.trim(),ipv6:ipv6.value.trim(),http_port:Number(httpPort.value)||0,https_port:Number(httpsPort.value)||0,proxy_dns:proxyDNS.checked,email:email.value.trim(),accept_terms:terms.checked};
}
function fill(v) {
  const c = v.candidate || v.active;
  if (!c) return;
  mode.value=c.mode;url.value=c.url;ipv4.value=c.ipv4;ipv6.value=c.ipv6;httpPort.value=c.http_port||'';httpsPort.value=c.https_port||'';proxyDNS.checked=c.proxy_dns;email.value=c.email;terms.checked=c.accept_terms;
}
const stamp = value => value && !value.startsWith('0001-') ? new Date(value).toLocaleString('zh-CN') : '尚无记录';
const stages = {checked:'已检查，尚未启用',working:'正在检查、申请或验证部署',failed:'操作失败，旧入口保留',enabled:'部署验证通过，已启用',staging_verified:'测试 CA 流程验证通过；主入口未切换'};
function draw(v) {
  current=v;clearTimeout(timer);summary.replaceChildren();details.replaceChildren();
  const a=v.active;
  const active = () => a ? el('div',{},hint('已验证主地址：'+a.url),el('a',{href:a.url+'/admin',text:'打开主地址后台',rel:'noreferrer'}),hint('验证时间：'+stamp(a.verified_at)),a.mode==='auto'&&hint('证书到期：'+stamp(a.expires))) : hint('尚未配置主地址，继续使用当前访问入口。');
  summary.append(active(),button('查看域名与端口帮助',()=>FP.navigate('tutorial')));
  details.append(heading('2 · 当前状态'),active());
  if (v.staging) details.append(hint('当前使用测试 CA：验证成功也不会切换正式主地址。正式启用需去掉 --acme-staging 后重新检查和申请。'));
  for (const [name,value] of [['监听 / 到期告警',v.warning],['续期失败',v.renewal_error]]) if(value) {const p=hint(name+'：'+value);p.className='error-text';summary.append(p.cloneNode(true));details.append(p);}
  if (a?.mode==='auto') details.append(hint('最近续期尝试：'+stamp(v.last_renewal)+'。服务每小时检查，进入到期前 30 天自动续期；失败保留原证书。'));
  if (a?.mode==='proxy') details.append(hint('该入口由已有代理终止 TLS，证书申请与续期由代理维护。'));
  const e=v.environment;
  details.append(el('details',{},el('summary',{text:'详细运行环境（排错时查看）'}),hint(e.os+' / '+e.arch+' · HTTP '+(e.listen||'未提供')+' · TLS '+e.tls_listen+' · 挑战 '+e.challenge_listen),hint('本机网卡 IPv4：'+(e.ipv4||[]).join('、')+'；IPv6：'+(e.ipv6||[]).join('、')+'。公网 IP / NAT 映射无法从网卡推断，请按运营商控制台填写。')));
  const c=v.candidate;
  if(c) {
    details.append(heading(stages[c.stage]||c.stage),hint('候选：'+c.url+' · 检查时间：'+stamp(c.report.at)));
    if(c.error) {const p=hint(c.error);p.className='error-text';details.append(p);}
    if(v.stale) details.append(hint('检查已超过 5 分钟，请重新检查；启用时也会强制重新检测。'));
    const list=el('div',{'aria-label':'诊断结果',style:'display:grid;gap:9px;font-size:14px;line-height:1.7'});
    for(const x of c.report.checks||[]) list.append(el('div',{},el('b',{text:({pass:'通过',fail:'失败',unknown:'待验证'}[x.state]||x.state)+' · '+x.name+'：'}),el('span',{text:x.detail})));
    details.append(list,heading('3 · DNS 与反代配置指导'));
    const records=(c.report.records||[]).map(x=>x.name+'  '+x.type+'  '+x.value).join('\n');
    const content=(records ? '建议记录（请确认托管区填写规则）：\n'+records+'\n\n':'未生成 DNS 记录；IP 入口无需解析，域名记录需先确认公网地址。\n\n')+c.report.guide;
    const copybox=el('textarea',{id:'access-guide',rows:16,readonly:true,'aria-label':'可复制配置指导',style:'width:100%;box-sizing:border-box;white-space:pre;font-size:14px;padding:12px'});copybox.value=content;
    details.append(copybox,button('复制配置指导',async()=>{try{await navigator.clipboard.writeText(copybox.value);FP.toast('配置指导已复制');}catch{copybox.focus();copybox.select();FP.toast('请按 Ctrl+C 复制选中的配置指导');}}));
  }
  checkButton.disabled=v.busy;applyButton.disabled=applying||v.busy||!c||!c.report.ready||!!form.dataset.dirty;
  if(v.busy) timer=setTimeout(()=>refresh(false),1500);
}
async function refresh(refill=true) {if(refreshing||!FP.csrf())return;refreshing=true;const revision=form.dataset.revision;try{const v=await FP.api('/api/access');if(refill&&!form.dataset.dirty&&form.dataset.revision===revision){fill(v);modeFields();}draw(v);}catch(e){FP.message('访问状态读取失败：'+e.message);}finally{refreshing=false;}}
async function check() {if(checkButton.disabled)return;const submitted=JSON.stringify(read());checkButton.disabled=true;checkButton.setAttribute('aria-busy','true');applyButton.disabled=true;try{const v=await FP.api('/api/access/check',json(read()));if(JSON.stringify(read())===submitted){FP.clean(form);fill(v);modeFields();}draw(v);FP.toast('候选检查完成，查看下方诊断');}catch(e){FP.toast(e.message);}finally{checkButton.removeAttribute('aria-busy');checkButton.disabled=!!current?.busy;}}
async function apply() {
  if(applying||!current?.candidate) return;
  if(JSON.stringify(read())!==JSON.stringify(Object.fromEntries(Object.keys(read()).map(k=>[k,current.candidate[k]])))) {FP.toast('表单已修改，请先重新检查候选地址');return;}
  applying=true;applyButton.disabled=true;applyButton.setAttribute('aria-busy','true');
  try{draw(await FP.api('/api/access/apply',{method:'POST'}));FP.toast('已开始验证；当前入口继续可用');}catch(e){FP.message(e.message);}finally{applying=false;applyButton.removeAttribute('aria-busy');applyButton.disabled=!!current?.busy||!!form.dataset.dirty||!current?.candidate?.report.ready;}
}
FP.watchDraft(form);
function modeFields(){const auto=mode.value==='auto';for(const n of [email])n.closest('.field').hidden=!auto;terms.closest('label').hidden=!auto;}
mode.addEventListener('change',modeFields);modeFields();
form.addEventListener('input',()=>{applyButton.disabled=true;});
form.addEventListener('change',()=>{applyButton.disabled=true;});
document.addEventListener('fp:navigate',e=>{if(e.detail.section==='access')refresh();});
document.addEventListener('fp:logout',()=>{clearTimeout(timer);form.reset();FP.clean(form);current=null;summary.replaceChildren();details.replaceChildren();});
document.querySelector('#refresh-btn').addEventListener('click',()=>{if(!document.querySelector('#page-tutorial').classList.contains('hidden')||!document.querySelector('#page-settings').classList.contains('hidden'))refresh(false);});
})();
