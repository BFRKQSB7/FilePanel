(() => {
'use strict';
const FP = window.FP;
if (!FP) return;

// 所有可见文本都通过 textContent 写入；仅服务端已净化的 Markdown 预览使用 innerHTML。
function el(tag, props = {}, ...kids) {
  const node = document.createElement(tag);
  for (const [k, v] of Object.entries(props)) {
    if (k === 'class') node.className = v;
    else if (k === 'text') node.textContent = v;
    else if (k.startsWith('on')) node.addEventListener(k.slice(2), v);
    else if (v !== false && v !== undefined) node.setAttribute(k, v === true ? '' : v);
  }
  node.append(...kids.filter(Boolean));
  return node;
}
const json = (method, body) => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
let fieldID = 0;
const field = (label, input, hint) => { if (!input.id) input.id = 'field-' + (++fieldID); return el('div', { class: 'field' }, el('label', { text: label, for: input.id }), input, hint && el('div', { class: 'section-sub', text: hint })); };

let shareOpening = false;

async function openShare(item) {
  let closeModal=()=>{};
  if (shareOpening || document.querySelector("dialog[open]")) return;
  shareOpening = true;
  let view,nodeState;
  try { [view,nodeState] = await Promise.all([FP.api('/api/files/' + encodeURIComponent(item.id) + '/share'),FP.api('/api/nodes').catch(()=>null)]); }
  catch (e) { FP.message(e.message); return; }
  finally { shareOpening = false; }
  if(document.querySelector("dialog[open]"))return;
  const title = el('input', { maxlength: 80, value: view.title, placeholder: view.file_name });
  const version = el('input', { maxlength: 40, value: view.version, placeholder: '例如 1.2.0' });
  const summary = el('textarea', { maxlength: 300, rows: 2 }); summary.value = view.summary;
  const button = el('input', { maxlength: 20, value: view.button_text, placeholder: '立即下载' });
  const markdown = el('textarea', { rows: 8, maxlength: 20000, placeholder: '支持标题、列表、链接、代码和本分享的图片；不支持 HTML。' }); markdown.value = view.markdown;
  const mode = el('select');
  mode.append(new Option('不分享（仅管理员可访问）','private'),new Option('私密分享（仅凭链接访问）','link'),new Option('公开分享（在主页展示）','public'));
  mode.value = view.featured ? 'public' : view.enabled ? 'link' : 'private';
  const pageTemplate=el('select');pageTemplate.append(new Option('标准介绍','standard'),new Option('简洁下载','compact'),new Option('封面展示','showcase'));pageTemplate.value=view.page_template||'standard';
  const sample=el('div',{'aria-hidden':'true'},el('div',{class:'sample-cover'},FP.icon('file-text')),el('strong',{text:view.title||view.file_name}),el('span',{class:'sample-download',text:'立即下载'}));
  const drawTemplate=()=>{sample.className='template-sample template-'+pageTemplate.value;};pageTemplate.addEventListener('change',drawTemplate);drawTemplate();
  const relayMode=el('select');relayMode.append(new Option('使用所有已启用的下载出口','all'),new Option('手动指定下载 / 中转服务器','selected'));relayMode.value=view.relay_mode||'all';
  const relayChoices=el('div',{class:'relay-choices'}),relayChecks=new Map();
  const choices=[{id:'local',label:(nodeState?.settings?.local_region||'主面板')+' · 本机出口'},...(nodeState?.nodes||[]).filter(n=>n.relay&&!n.revoked).map(n=>({id:n.id,label:(n.region||'地区未知')+' · '+(n.note||n.public_ip||n.endpoint)+(n.online?'':'（离线）')}))];
  for(const id of view.relays||[])if(!choices.some(x=>x.id===id))choices.push({id,label:'已保存线路（当前未启用）：'+id});
  for(const choice of choices){const input=el('input',{type:'checkbox',checked:relayMode.value==='all'||(view.relays||[]).includes(choice.id)});relayChecks.set(choice.id,input);relayChoices.append(el('label',{},input,choice.label));}
  const drawRelays=()=>{relayChoices.hidden=relayMode.value!=='selected';};relayMode.addEventListener('change',drawRelays);drawRelays();
  const preview = el('div', { class: 'section-sub' });
  let busy = false;
  const status = el('p', { role:'status' });
  const assets = el('div', { class: 'section-sub' });
  let cover = view.cover_asset_id;

  function drawAssets() {
    assets.replaceChildren();
    if (!view.assets.length) { assets.append('还没有图片。最多 8 张，每张不超过 2 MiB（PNG/JPEG/GIF/WebP）。'); return; }
    for (const a of view.assets) {
      const img = el('img', { src: '/api/assets/' + encodeURIComponent(a.id), alt: '素材', style: 'width:84px;height:56px;object-fit:cover;border-radius:var(--radius-sm);border:1px solid var(--line)' });
      const isCover = cover === a.id;
      assets.append(el('div', { class: 'asset-row' }, img,
        el('button', { type: 'button', class: 'quiet-btn', text: isCover ? '封面 ✓' : '设为封面', onclick: () => { cover = isCover ? '' : a.id; FP.dirty(modal); drawAssets(); } }),
        el('code', { text: '![说明](asset:' + a.id + ')', style: 'font-size:12px;user-select:all' }),
        el('button', { type: 'button', class: 'danger-btn', text: '删除', onclick: async () => {
          if(busy)return;
          if (!confirm('立即删除这张图片？已发布页面也会受影响，关闭编辑框不会撤销。')) return;
          busy=true;save.disabled=true;
          try { await FP.api('/api/files/' + encodeURIComponent(item.id) + '/assets/' + encodeURIComponent(a.id), { method: 'DELETE' }); view.assets = view.assets.filter(x => x.id !== a.id); if (cover === a.id) cover = ''; drawAssets(); }
          catch (e) { FP.message(e.message); } finally {busy=false;save.disabled=false;}
        } })));
    }
  }
  drawAssets();

  const picker = el('input', { type: 'file', accept: 'image/png,image/jpeg,image/gif,image/webp', class: 'hidden', onchange: async (e) => {
    const f = e.target.files[0]; e.target.value = ''; if (!f || busy) return;busy=true;save.disabled=true;
    const form = new FormData(); form.append('image', f, f.name);
    try { const a = await FP.api('/api/files/' + encodeURIComponent(item.id) + '/assets', { method: 'POST', body: form }); view.assets.push(a); drawAssets(); }
    catch (err) { FP.message(err.message); } finally {busy=false;save.disabled=false;}
  } });

  const link = el('input', { readonly: true, value: view.url.startsWith('/') ? location.origin + view.url : view.url, 'aria-label': '稳定分享链接' });
  const save = el('button', { type: 'button', class: 'primary-btn', text: '保存分享设置' });
  const copy = el('button', {type:'button',class:'quiet-btn',text:'复制分享链接',onclick:()=>FP.copy(link.value,link)});
  const visit = el('a', {href:link.value,target:'_blank',rel:'noopener',class:'quiet-btn',text:'查看分享页面'});
  const cancel = el('button', {type:'button',class:'danger-btn',text:'取消分享',onclick:async()=>{
    if(busy)return;busy=true;modal.inert=true;
    let cancelled=false;
    try { cancelled=await FP.cancelShare(item); } finally {busy=false;modal.inert=false;}
    if(cancelled){FP.clean(modal);closeModal();}
  }});
  function savedState() { copy.disabled = !view.enabled; visit.hidden = !view.enabled; cancel.hidden = !view.enabled; status.textContent = view.featured ? '当前为公开分享：公开主页展示，访客也可通过链接访问。' : view.enabled ? '当前为私密分享：仅凭链接访问下载页面，不在公开主页展示。拥有链接的人无需登录。' : '当前未分享：只有管理员能访问。选择分享方式并保存后，链接才会生效。'; }
  savedState();
  const modal = el('dialog', { class: 'modal share-modal', role: 'dialog', 'aria-modal': 'true', 'aria-label': '分享设置' },
    el('h2', { text: '分享与展示 · ' + view.file_name }),
    el('p',{text:'选择分享方式并保存。标题、版本、介绍和图片全部可选。'}),
    field('分享方式', mode, '私密分享不在公开主页展示；公开分享会在主页展示。选择“不分享”并保存，也可取消现有分享。'),
    field('下载页面模板',pageTemplate,'仅改变这个分享页面的布局；文件、介绍与下载授权保留。'),sample,
    field('下载线路',relayMode,'下载按钮按实际出口地区显示。出口可以读取本机副本，也可以从存储服务器中转；未勾选的出口不能通过修改链接绕过。'),relayChoices,
    field('分享链接（改名、换存储后仍可用）', link),status,
    el('div',{class:'modal-actions'},copy,visit,cancel),
    el('details',{},el('summary',{text:'编辑介绍与图片（可选）'}),
    field('标题（留空使用文件名）', title), field('版本（可选）', version), field('简介', summary), field('下载按钮文字（可选）', button),
    field('文件介绍', markdown, '可以直接写普通文字。Markdown 示例：# 标题、- 列表项、**加粗文字**。'),
    el('div', { class: 'modal-actions', style: 'justify-content:flex-start;margin:6px 0' },
      el('button', { type: 'button', class: 'quiet-btn', text: '预览', onclick: async () => {
        try { const r = await FP.api('/api/markdown/preview', json('POST', { file_id: item.id, markdown: markdown.value })); preview.innerHTML = r.html || '（空）'; }
        catch (e) { preview.textContent = e.message; }
      } }),
      el('button', { type: 'button', class: 'quiet-btn', text: '上传图片', onclick: () => picker.click() }), picker),
    preview, assets,el('p',{text:'图片上传与删除会立即生效；其他编辑需点击保存。'})),
    el('div', { class: 'modal-actions' }, el('button', { type: 'button', class: 'quiet-btn', text: '关闭', onclick: () => closeModal() }), save));
  const body=el('div',{class:'share-dialog-body'}), footer=modal.lastElementChild;
  while(modal.firstChild!==footer)body.append(modal.firstChild);
  modal.prepend(body);
  save.addEventListener('click', async () => {
    if (busy) return; busy = true; save.disabled = true; save.setAttribute('aria-busy','true');
    const controls=[...modal.querySelectorAll('input,textarea,select,button')].filter(e=>e!==save);const disabled=controls.map(e=>e.disabled);controls.forEach(e=>e.disabled=true);
    try {
      view = await FP.api('/api/files/' + encodeURIComponent(item.id) + '/share', json('PUT', {
        title: title.value, version: version.value, summary: summary.value, markdown: markdown.value, button_text: button.value,
        cover_asset_id: cover, enabled: mode.value!=='private', featured: mode.value==='public',page_template:pageTemplate.value,relay_mode:relayMode.value,relays:[...relayChecks].filter(([,input])=>input.checked).map(([id])=>id) }));
      FP.clean(modal); savedState(); FP.toast(view.featured ? '已公开分享并在主页展示' : view.enabled ? '已保存为私密分享，可复制链接' : '已取消分享，访客无法访问'); await FP.refreshFiles();
    } catch (e) { status.textContent = e.message; } finally { busy = false; save.disabled = false; save.removeAttribute('aria-busy'); controls.forEach((e,i)=>e.disabled=disabled[i]);copy.disabled=!view.enabled; }
  });
  FP.watchDraft(modal);
  closeModal = FP.openDialog(modal,{canClose:()=>!busy});
  mode.focus();
}
FP.openShare = openShare;
FP.cancelShare = async item => {
  if(!confirm('取消“'+item.name+'”的分享？原分享页面与下载授权将失效，文件保留。未保存的分享设置不会提交。'))return false;
  try {
    await FP.api('/api/files/'+encodeURIComponent(item.id)+'/share',{method:'DELETE'});
    FP.toast('已取消分享，文件保留');await FP.refreshFiles();return true;
  } catch(e) {FP.message(e.message);return false;}
};
FP.el = el; FP.json = json; FP.field = field;

// ---- 用量与保护（挂载到“数据”页） ----
const GB = 1e9, TB = 1e12;
const dec = (n) => n >= TB ? (n / TB).toFixed(2) + ' TB' : n >= GB ? (n / GB).toFixed(2) + ' GB' : n >= 1e6 ? (n / 1e6).toFixed(1) + ' MB' : n >= 1e3 ? (n / 1e3).toFixed(1) + ' KB' : n + ' B';
const day = (t) => new Date(t * 1000).toLocaleDateString('zh-CN', { timeZone: 'UTC' });
const names = { public_download: '访客下载', admin_download: '管理员下载', asset: '图片素材', admin_upload: '管理员上传' };
const states = {
  ok: ['保护生效', '流量在保护线以内。'],
  disabled: ['保护已关闭', '当前不限制流量，仍会统计用量。建议开启保护。'],
  quota_unknown: ['总量保护未生效', '尚未填写本机流量额度：只统计、不限总量。请在设置中的“流量保护”填写套餐月流量。'],
  exhausted: ['已达保护线', '新下载和活动响应已被限制；管理与介绍页仍可访问。下个统计周期自动恢复，不会清空用量。'],
};
const root = el('div', { id: 'usage-panel' });
document.querySelector('#page-data').append(root);
const protectionRoot = el('div'); document.querySelector('#settings-protection').append(protectionRoot); FP.watchDraft(protectionRoot);
let usageBusy = false;

async function loadUsage() {
  if (usageBusy || !FP.csrf()) return; usageBusy = true;
  root.replaceChildren(el('section', { class: 'section-card', style: 'margin-top:16px' }, el('div', { class: 'empty', text: '正在读取用量…' })));
  const revision=protectionRoot.dataset.revision;
  let st;
  try { st = await FP.api('/api/usage'); }
  catch (e) {
    root.replaceChildren(el('section', { class: 'section-card', style: 'margin-top:16px' },
      el('div', { class: 'empty' }, el('strong', { text: '用量读取失败' }), e.message, el('div', { style: 'margin-top:12px' }, el('button', { class: 'primary-btn', type: 'button', text: '重试', onclick: loadUsage })))));
    if(!protectionRoot.children.length)protectionRoot.replaceChildren(el('p',{text:'保护设置读取失败：'+e.message}),el('button',{type:'button',class:'quiet-btn',text:'重新读取保护设置',onclick:loadUsage}));
    usageBusy = false; return;
  }
  usageBusy = false; drawUsage(st,protectionRoot.dataset.revision!==revision);
}

function drawUsage(st,preserveConfig=false) {
  const [label, hint] = states[st.state] || [st.state, ''];
  const pct = st.limit ? Math.min(100, st.used / st.limit * 100) : 0;
  const bar = el('div', { class: 'meter' }, el('span', { style: 'width:' + pct + '%' }));
  const rows = (st.categories || []).map(c => el('tr', {}, el('td', { text: names[c.category] || c.category }), el('td', { text: c.direction === 'sent' ? '发送' : '接收' }), el('td', { text: dec(c.bytes) })));
  const hist = (st.history || []).map(h => el('tr', {}, el('td', { text: day(h.start) + ' – ' + day(h.end - 1) }), el('td', { text: dec(h.sent) }), el('td', { text: dec(h.received) }), el('td', { text: h.downloads })));
  const table = (head, body, empty) => body.length ? el('table', {}, el('thead', {}, el('tr', {}, ...head.map(h => el('th', { text: h })))), el('tbody', {}, ...body)) : el('div', { class: 'empty', text: empty });
  const reconcile = st.orphaned > 0 && el('div', { class: 'message show error', style: 'display:block;margin:12px 19px' }, '有 ' + dec(st.orphaned) + ' 的未确认预留（可能来自上次异常退出），会继续占用额度。 ',
    el('button', { class: 'quiet-btn', type: 'button', text: '对账并释放', onclick: async () => {
      try { const r = await FP.api('/api/usage/reconcile', { method: 'POST' }); FP.toast('已释放 ' + dec(r.released)); loadUsage(); } catch (e) { FP.toast(e.message); }
    } }));
  const c = st.config;
  const quotaGB = el('input', { type: 'number', min: 0, step: 'any', value: c.quota_bytes ? c.quota_bytes / GB : '', placeholder: '0 或留空表示未知' });
  const protect = el('input', { type: 'checkbox', checked: c.protect });
  const basis = el('select', {}, el('option', { value: 'sent', text: '仅发送（默认）', selected: c.basis === 'sent' }), el('option', { value: 'total', text: '收发合计', selected: c.basis === 'total' }));
  const reset = el('input', { type: 'number', min: 1, max: 31, value: c.reset_day });
  const conc = el('input', { type: 'number', min: 1, max: 100, value: c.ip_concurrency });
  const ipPct = el('input', { type: 'number', min: 1, max: 100, value: c.ip_percent });
  const proxies = el('textarea', { rows: 2, placeholder: '每行一个 IP 或 CIDR；只有来自这些地址的请求才会采信 X-Forwarded-For' }); proxies.value = (c.trusted_proxies || []).join('\n');
  const form = el('form', { class: 'setting-form', onsubmit: async (e) => {
    e.preventDefault();
    if(protectionRoot.inert)return;protectionRoot.inert=true;
    const q = parseFloat(quotaGB.value);
    try {
      const next = await FP.api('/api/usage/config', FP.json('PUT', { protect: protect.checked, quota_bytes: Number.isFinite(q) && q > 0 ? Math.round(q * GB) : 0, basis: basis.value,
        reset_day: +reset.value, ip_concurrency: +conc.value, ip_percent: +ipPct.value, trusted_proxies: proxies.value.split('\n').map(s => s.trim()).filter(Boolean) }));
      FP.clean(protectionRoot); FP.toast('保护设置已保存'); drawUsage(next);
    } catch (err) { FP.message(err.message); } finally { protectionRoot.inert=false; }
  } },
    el('div', { class: 'field' }, el('label', {}, protect, ' 启用流量与来源保护（推荐）')),
    FP.field('本周期流量额度（十进制 GB；1 TB = 1000 GB）', quotaGB, '实际保护线 = 额度 × 95%，预留 5%。'),
    FP.field('服务商如何计算流量', basis, '仅发送：只统计从服务器传出的数据；收发合计：上传和下载都计入。请按套餐账单选择。'), FP.field('每月流量重置日', reset, '按 UTC 世界标准时间统计；29–31 日在较短月份取月底。'),
    el('details',{},el('summary',{text:'高级保护设置（通常无需修改）'}),
    FP.field('单 IP 活动连接上限', conc), FP.field('单 IP 可用流量占保护线的百分比', ipPct), FP.field('可信反向代理', proxies, '仅使用 Nginx、Caddy 等代理时填写代理服务器 IP 或网段（CIDR）；直接访问留空。用于识别真实访客 IP，不是填写访客地址。')),
    el('button', { class: 'primary-btn', type: 'submit', text: '保存保护设置' }));
  root.replaceChildren(
    el('section', { class: 'section-card', style: 'margin-top:16px' },
      el('div', { class: 'section-head' }, el('div', {}, el('div', { class: 'section-title', text: '流量保护 · ' + label }), el('div', { class: 'section-sub', text: hint })),
        el('button', { class: 'quiet-btn', type: 'button', text: '刷新', onclick: loadUsage })),
      el('div', { class: 'setting-form' }, bar,
        el('div', { class: 'storage-foot', text: '本周期 ' + day(st.period.start) + ' – ' + day(st.period.end - 1) + '（UTC） · 已用 ' + dec(st.used) + (st.limit ? ' / 保护线 ' + dec(st.limit) : ' / 额度未知') + ' · 其中未确认预留 ' + dec(st.pending) + ' · 活动下载 ' + st.active + ' · 逻辑下载数 ' + st.period.downloads }),
        el('div', { class: 'section-sub', text: '统计的是本应用内容流的字节，不是整机网卡流量，也不是服务商账单。' })),
      reconcile || null),
    el('section', { class: 'section-card', style: 'margin-top:16px' }, el('div', { class: 'section-head' }, el('div', { class: 'section-title', text: '本周期分类' })), table(['类别', '方向', '字节'], rows, '本周期还没有内容流。')),
    el('section', { class: 'section-card', style: 'margin-top:16px' }, el('div', { class: 'section-head' }, el('div', { class: 'section-title', text: '历史周期' })), table(['周期', '发送', '接收', '完整下载'], hist, '暂无历史。')),
    el('button',{type:'button',class:'quiet-btn',text:'设置流量保护',onclick:()=>FP.navigate('settings','protection')}));
  if (!preserveConfig && !protectionRoot.dataset.dirty) protectionRoot.replaceChildren(el('section',{class:'section-card'},el('div',{class:'section-head'},el('h3',{text:'流量保护'})),form));
}
document.addEventListener('fp:navigate',e=>{if(e.detail.tab==='data'||e.detail.section==='protection')loadUsage();});
document.querySelector('#data-refresh').addEventListener('click', loadUsage);
document.querySelector('#refresh-btn').addEventListener('click', () => { if (!document.querySelector('#page-data').classList.contains('hidden')) loadUsage(); });

// ---- 站点个性化（挂载到“设置”页） ----
const ROLES = ['logo', 'banner', 'background'];
const ROLE_LABEL = { logo: 'Logo（≤ 512 KiB）', banner: '横幅图片（首页顶部背景，≤ 2 MiB）', background: '页面背景（≤ 2 MiB）' };
const siteRoot = el('div', { id: 'profile-panel' });
document.querySelector('#settings-appearance').append(siteRoot); FP.watchDraft(siteRoot);
let profileBusy = false;
const card = (...kids) => el('section', { class: 'section-card' }, ...kids);
const kib = (n) => n >= 1048576 ? (n / 1048576).toFixed(1) + ' MiB' : Math.ceil(n / 1024) + ' KiB';
const readDataURL = (file) => new Promise((resolve, reject) => { const r = new FileReader(); r.onload = () => resolve(r.result); r.onerror = reject; r.readAsDataURL(file); });
const note = (box, text, ok) => { box.textContent = text; box.className = 'message show ' + (ok ? 'ok' : 'error'); box.style.display = text ? 'block' : 'none'; };
const previewFrame = () => el('iframe', { sandbox: '', title: '站点预览', class: 'preview-frame', style: 'display:none' });

async function loadProfile() {
  if (profileBusy || siteRoot.dataset.dirty || !FP.csrf()) return; profileBusy = true;
  const revision=siteRoot.dataset.revision;
  let p;
  try { p = await FP.api('/api/profile'); }
  catch (e) {
    if(!siteRoot.dataset.dirty && siteRoot.dataset.revision===revision) siteRoot.replaceChildren(card(el('div', { class: 'empty' }, el('strong', { text: '站点个性化读取失败' }), e.message,
      el('div', { style: 'margin-top:12px' }, el('button', { class: 'primary-btn', type: 'button', text: '重试', onclick: loadProfile })))));
    profileBusy = false; return;
  }
  profileBusy = false; if (!siteRoot.dataset.dirty && siteRoot.dataset.revision===revision) drawProfile(p);
}

function drawProfile(p) {
  const f = p.fields, d = p.defaults;
  const pending = {}; // role -> File | 'remove'
  const name = el('input', { maxlength: 40, value: f.site_name, placeholder: d.site_name, 'aria-label': '站点名' });
  const intro = el('textarea', { maxlength: 200, rows: 2, placeholder: d.intro }); intro.value = f.intro;
  const footer = el('input', { maxlength: 120, value: f.footer, placeholder: d.footer });
  const button = el('input', { maxlength: 20, value: f.button_text, placeholder: d.button_text });
  const useAccent = el('input', { type: 'checkbox', checked: !!f.accent });
  const accent = el('input', { type: 'color', value: f.accent || '#315f9e', 'aria-label': '强调色' });
  const status = el('div', { class: 'message', style: 'display:none;margin:12px 0 0' });
  const frame = previewFrame();

  const boxes = {};
  function drawAsset(role) {
    const box = boxes[role], cur = p.assets[role], pend = pending[role];
    const thumb = el('div', { class: 'asset-thumbnail' });
    let label = '未设置（使用默认外观）';
    if (pend instanceof File) { label = '待上传：' + pend.name + '（' + kib(pend.size) + '）'; readDataURL(pend).then(u => { thumb.style.backgroundImage = 'url("' + u + '")'; }); }
    else if (pend === 'remove') label = '保存后移除';
    else if (cur) { label = '当前：' + kib(cur.size); thumb.style.backgroundImage = 'url("' + cur.url + '")'; }
    const picker = el('input', { type: 'file', accept: 'image/png,image/jpeg,image/webp', class: 'hidden', onchange: (e) => {
      const file = e.target.files[0]; e.target.value = ''; if (!file) return; FP.dirty(siteRoot); pending[role] = file; drawAsset(role);
    } });
    box.replaceChildren(...[thumb,
      el('div', { style: 'flex:1;min-width:140px' }, el('div', { text: ROLE_LABEL[role], style: 'font-weight:650;font-size:12px' }), el('div', { class: 'section-sub', text: label })),
      el('button', { type: 'button', class: 'quiet-btn', text: '选择图片', onclick: () => picker.click() }), picker,
      pend !== undefined ? el('button', { type: 'button', class: 'quiet-btn', text: '撤销更改', onclick: () => { delete pending[role]; FP.dirty(siteRoot); drawAsset(role); } })
        : cur ? el('button', { type: 'button', class: 'danger-btn', text: '移除', onclick: () => { pending[role] = 'remove'; FP.dirty(siteRoot); drawAsset(role); } }) : null].filter(Boolean));
  }
  for (const role of ROLES) { boxes[role] = el('div', { style: 'display:flex;gap:12px;align-items:center;flex-wrap:wrap;margin:10px 0' }); drawAsset(role); }

  function buildForm() {
    const form = new FormData(), assets = {};
    for (const role of ROLES) {
      const c = pending[role];
      if (c === 'remove') assets[role] = 'remove';
      else if (c instanceof File) { assets[role] = 'replace'; form.append(role, c, c.name); }
      else assets[role] = 'keep';
    }
    form.append('config', JSON.stringify({ site_name: name.value, intro: intro.value, accent: useAccent.checked ? accent.value : '', footer: footer.value, button_text: button.value, assets }));
    return form;
  }

  const importPicker = el('input', { type: 'file', accept: '.zip,application/zip', class: 'hidden', onchange: async (e) => {
    const file = e.target.files[0]; e.target.value = ''; if (!file) return;
    if(siteRoot.inert)return;siteRoot.inert=true;
    const form = new FormData(); form.append('package', file, file.name);
    note(status, '正在校验配置包…', true);
    try { const r = await FP.api('/api/profile/import', { method: 'POST', body: form }); note(status, '', true); reviewImport(r); }
    catch (err) { note(status, '导入失败，当前配置未改变：' + err.message, false); }finally{siteRoot.inert=false;}
  } });

  const act = (text, cls, fn) => el('button', { type: 'button', class: cls, text, onclick: async (e) => {
    const b = e.currentTarget; b.disabled = true; b.setAttribute('aria-busy','true'); siteRoot.inert=true;
    try { await fn(); } catch (err) { note(status, err.message, false); } finally { b.disabled = false; b.removeAttribute('aria-busy'); siteRoot.inert=false; }
  } });

  siteRoot.replaceChildren(card(
    el('div', { class: 'section-head' }, el('div', {}, el('div', { class: 'section-title', text: '站点个性化' }),
      el('div', { class: 'section-sub', text: '只作用于唯一官方模板：站点名、Logo、简介、强调色、横幅与页面背景、页脚、默认按钮文字。留空即使用默认；单个资源自己设置的按钮文字始终优先。' }))),
    el('div', { class: 'setting-form' },
      FP.field('站点名', name), FP.field('简介（显示在首页横幅）', intro),
      el('div', { class: 'field' }, el('label', {}, useAccent, ' 使用自定义强调色'), accent,
        el('div', { class: 'section-sub', text: '需与白色按钮文字保持足够对比度（≥ 4.5），过浅的颜色会被拒绝。' })),
      FP.field('页脚文字', footer), FP.field('默认下载按钮文字', button),
      el('div', { class: 'section-sub', text: 'PNG / JPEG / WebP；不支持 SVG 与外链图片。' }),
      ...ROLES.map(r => boxes[r]),
      el('div', { class: 'modal-actions', style: 'justify-content:flex-start;flex-wrap:wrap' },
        act('预览', 'quiet-btn', async () => { const r = await FP.api('/api/profile/preview', { method: 'POST', body: buildForm() }); note(status, '', true); frame.srcdoc = r.html; frame.style.display = 'block'; }),
        act('保存并应用', 'primary-btn', async () => { const next = await FP.api('/api/profile', { method: 'POST', body: buildForm() }); FP.clean(siteRoot); FP.toast('已保存并应用到公开主页与详情页'); drawProfile(next); }),
        el('a', { class: 'quiet-btn', href: '/api/profile/export', download: true, text: '导出配置包', style: 'text-decoration:none' }),
        el('button', { type: 'button', class: 'quiet-btn', text: '导入配置包', onclick: () => importPicker.click() }), importPicker),
      el('button',{type:'button',class:'quiet-btn',text:'放弃未保存的外观修改',onclick:()=>{if(!siteRoot.dataset.dirty||confirm('放弃未保存的外观修改？')){FP.clean(siteRoot);loadProfile();}}}),status, frame,
      el('div', { class: 'section-sub', style: 'margin-top:10px', text: '配置包只含站点外观与它引用的图片，不含文件、账号、证书或用量。导入会先预览，确认后才替换当前配置。' }))));
}

function reviewImport(r) {
  let closeModal=()=>{};
  const f = r.fields;
  let importing=false,consumed=false;
  const finish = async (apply) => {
    if(importing)return;importing=true;modal.querySelectorAll('button').forEach(b=>b.disabled=true);
    try {
      if (apply) { const next = await FP.api('/api/profile/import/' + encodeURIComponent(r.token) + '/apply', { method: 'POST' }); FP.clean(siteRoot);consumed=true;importing=false;closeModal();FP.toast('已导入并应用');drawProfile(next); }
      else { await FP.api('/api/profile/import/' + encodeURIComponent(r.token), { method: 'DELETE' });consumed=true;importing=false;closeModal(); }
    } catch (e) { FP.message(e.message); }finally{importing=false;modal.querySelectorAll('button').forEach(b=>b.disabled=false);}
  };
  const line = (k, v) => el('div', {}, el('b', { text: k + '：' }), v);
  const frame = previewFrame(); frame.style.display = 'block'; frame.srcdoc = r.html;
  const modal = el('dialog', { class: 'modal', role: 'dialog', 'aria-modal': 'true', 'aria-label': '确认导入配置包' },
    el('h2', { text: '确认导入个性化配置包' }),
    el('div', { class: 'section-sub', style: 'margin:0 0 10px', text: '配置包已通过校验并暂存，当前站点尚未改变。确认后将整体替换站点个性化设置；文件、账号和单资源设置不受影响。' }),
    el('div', { style: 'font-size:14px;line-height:1.9;color:var(--text-secondary);margin-bottom:12px' },
      line('站点名', f.site_name || '（默认）'), line('简介', f.intro || '（默认）'), line('强调色', f.accent || '（默认）'),
      line('页脚', f.footer || '（默认）'), line('默认按钮文字', f.button_text || '（默认）'),
      line('图片素材', r.assets.length ? r.assets.map(a => a.label + ' ' + kib(a.size)).join('、') : '无')),
    frame,
    el('div', { class: 'modal-actions' }, el('button', { type: 'button', class: 'quiet-btn', text: '放弃导入', onclick: () => finish(false) }),
      el('button', { type: 'button', class: 'primary-btn', text: '确认并应用', onclick: () => finish(true) })));
  closeModal = FP.openDialog(modal,{canClose:()=>!importing,onClose:()=>{if(!consumed)FP.api('/api/profile/import/'+encodeURIComponent(r.token),{method:'DELETE'}).catch(()=>{});}});
}
document.addEventListener('fp:navigate',e=>{if(e.detail.section==='appearance')loadProfile();});
document.addEventListener('fp:logout',()=>{siteRoot.replaceChildren();protectionRoot.replaceChildren();root.replaceChildren();});
})();
