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
const field = (label, input, hint) => el('div', { class: 'field' }, el('label', { text: label }), input, hint && el('div', { class: 'section-sub', text: hint }));

function closeModal() { document.querySelector('#modal-root').replaceChildren(); }

async function openShare(item) {
  let view;
  try { view = await FP.api('/api/files/' + encodeURIComponent(item.id) + '/share'); }
  catch (e) { FP.toast(e.message); return; }
  const title = el('input', { maxlength: 80, value: view.title, placeholder: view.file_name });
  const version = el('input', { maxlength: 40, value: view.version, placeholder: '例如 1.2.0' });
  const summary = el('textarea', { maxlength: 300, rows: 2 }); summary.value = view.summary;
  const button = el('input', { maxlength: 20, value: view.button_text, placeholder: '立即下载' });
  const markdown = el('textarea', { rows: 8, maxlength: 20000, placeholder: '支持标题、列表、链接、代码和本分享的图片；不支持 HTML。' }); markdown.value = view.markdown;
  const enabled = el('input', { type: 'checkbox', checked: view.enabled });
  const featured = el('input', { type: 'checkbox', checked: view.featured });
  const preview = el('div', { class: 'section-sub' });
  const assets = el('div', { class: 'section-sub' });
  let cover = view.cover_asset_id;
  enabled.addEventListener('change', () => { if (!enabled.checked) featured.checked = false; });
  featured.addEventListener('change', () => { if (featured.checked) enabled.checked = true; });

  function drawAssets() {
    assets.replaceChildren();
    if (!view.assets.length) { assets.append('还没有图片。最多 8 张，每张不超过 2 MiB（PNG/JPEG/GIF/WebP）。'); return; }
    for (const a of view.assets) {
      const img = el('img', { src: '/api/assets/' + encodeURIComponent(a.id), alt: '素材', style: 'width:84px;height:56px;object-fit:cover;border-radius:8px;border:1px solid var(--line)' });
      const isCover = cover === a.id;
      assets.append(el('div', { style: 'display:flex;gap:8px;align-items:center;margin:6px 0' }, img,
        el('button', { type: 'button', class: 'quiet-btn', text: isCover ? '封面 ✓' : '设为封面', onclick: () => { cover = isCover ? '' : a.id; drawAssets(); } }),
        el('code', { text: '![说明](asset:' + a.id + ')', style: 'font-size:9px;user-select:all' }),
        el('button', { type: 'button', class: 'danger-btn', text: '删除', onclick: async () => {
          try { await FP.api('/api/files/' + encodeURIComponent(item.id) + '/assets/' + encodeURIComponent(a.id), { method: 'DELETE' }); view.assets = view.assets.filter(x => x.id !== a.id); if (cover === a.id) cover = ''; drawAssets(); }
          catch (e) { FP.toast(e.message); }
        } })));
    }
  }
  drawAssets();

  const picker = el('input', { type: 'file', accept: 'image/png,image/jpeg,image/gif,image/webp', class: 'hidden', onchange: async (e) => {
    const f = e.target.files[0]; e.target.value = ''; if (!f) return;
    const form = new FormData(); form.append('image', f, f.name);
    try { const a = await FP.api('/api/files/' + encodeURIComponent(item.id) + '/assets', { method: 'POST', body: form }); view.assets.push(a); drawAssets(); }
    catch (err) { FP.toast(err.message); }
  } });

  const link = el('input', { readonly: true, value: view.url.startsWith('/') ? location.origin + view.url : view.url, 'aria-label': '稳定分享链接' });
  const save = el('button', { type: 'button', class: 'primary-btn', text: '保存' });
  const modal = el('div', { class: 'modal', style: 'width:min(640px,100%);max-height:92vh;overflow:auto', role: 'dialog', 'aria-modal': 'true', 'aria-label': '分享设置' },
    el('h2', { text: '分享与展示 · ' + view.file_name }),
    field('稳定链接（改名、换存储都不会变）', link),
    el('div', { style: 'display:flex;gap:18px;flex-wrap:wrap;margin:6px 0' },
      el('label', {}, enabled, ' 开启公开分享'), el('label', {}, featured, ' 在公开主页展示（会同时开启分享）')),
    field('标题', title), field('版本', version), field('简介', summary), field('下载按钮文字', button),
    field('正文（Markdown）', markdown),
    el('div', { class: 'modal-actions', style: 'justify-content:flex-start;margin:6px 0' },
      el('button', { type: 'button', class: 'quiet-btn', text: '预览', onclick: async () => {
        try { const r = await FP.api('/api/markdown/preview', json('POST', { file_id: item.id, markdown: markdown.value })); preview.innerHTML = r.html || '（空）'; }
        catch (e) { preview.textContent = e.message; }
      } }),
      el('button', { type: 'button', class: 'quiet-btn', text: '上传图片', onclick: () => picker.click() }), picker),
    preview, assets,
    el('div', { class: 'modal-actions' }, el('button', { type: 'button', class: 'quiet-btn', text: '关闭', onclick: closeModal }), save));
  save.addEventListener('click', async () => {
    save.disabled = true;
    try {
      await FP.api('/api/files/' + encodeURIComponent(item.id) + '/share', json('PUT', {
        title: title.value, version: version.value, summary: summary.value, markdown: markdown.value, button_text: button.value,
        cover_asset_id: cover, enabled: enabled.checked, featured: featured.checked }));
      FP.toast(enabled.checked ? '已保存并公开' : '已保存（私有）'); closeModal(); await FP.refreshFiles();
    } catch (e) { FP.toast(e.message); save.disabled = false; }
  });
  document.querySelector('#modal-root').replaceChildren(el('div', { class: 'modal-back', onclick: (e) => { if (e.target === e.currentTarget) closeModal(); } }, modal));
  title.focus();
}
FP.openShare = openShare;
FP.el = el; FP.json = json; FP.field = field;

// ---- 用量与保护（挂载到“数据”页） ----
const GB = 1e9, TB = 1e12;
const dec = (n) => n >= TB ? (n / TB).toFixed(2) + ' TB' : n >= GB ? (n / GB).toFixed(2) + ' GB' : n >= 1e6 ? (n / 1e6).toFixed(1) + ' MB' : n >= 1e3 ? (n / 1e3).toFixed(1) + ' KB' : n + ' B';
const day = (t) => new Date(t * 1000).toLocaleDateString('zh-CN', { timeZone: 'UTC' });
const names = { public_download: '访客下载', admin_download: '管理员下载', asset: '图片素材', admin_upload: '管理员上传' };
const states = {
  ok: ['保护生效', '流量在保护线以内。'],
  disabled: ['保护已关闭', '当前不限制流量，仍会统计用量。建议开启保护。'],
  quota_unknown: ['总量保护未生效', '尚未填写本机流量额度：只统计、不限总量。请在下方填写套餐的月流量。'],
  exhausted: ['已达保护线', '新下载和活动响应已被限制；管理与介绍页仍可访问。下个统计周期自动恢复，不会清空用量。'],
};
const root = el('div', { id: 'usage-panel' });
document.querySelector('#page-data').append(root);

async function loadUsage() {
  root.replaceChildren(el('section', { class: 'section-card', style: 'margin-top:16px' }, el('div', { class: 'empty', text: '正在读取用量…' })));
  let st;
  try { st = await FP.api('/api/usage'); }
  catch (e) {
    root.replaceChildren(el('section', { class: 'section-card', style: 'margin-top:16px' },
      el('div', { class: 'empty' }, el('strong', { text: '用量读取失败' }), e.message, el('div', { style: 'margin-top:12px' }, el('button', { class: 'primary-btn', type: 'button', text: '重试', onclick: loadUsage })))));
    return;
  }
  drawUsage(st);
}

function drawUsage(st) {
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
    const q = parseFloat(quotaGB.value);
    try {
      const next = await FP.api('/api/usage/config', FP.json('PUT', { protect: protect.checked, quota_bytes: Number.isFinite(q) && q > 0 ? Math.round(q * GB) : 0, basis: basis.value,
        reset_day: +reset.value, ip_concurrency: +conc.value, ip_percent: +ipPct.value, trusted_proxies: proxies.value.split('\n').map(s => s.trim()).filter(Boolean) }));
      FP.toast('保护设置已保存'); drawUsage(next);
    } catch (err) { FP.toast(err.message); }
  } },
    el('div', { class: 'field' }, el('label', {}, protect, ' 启用流量与来源保护（推荐）')),
    FP.field('本周期流量额度（十进制 GB；1 TB = 1000 GB）', quotaGB, '实际保护线 = 额度 × 95%，预留 5%。'),
    FP.field('额度口径', basis), FP.field('每月重置日（UTC，29–31 在短月取月底）', reset),
    FP.field('单 IP 活动连接上限', conc), FP.field('单 IP 可用流量占保护线的百分比', ipPct), FP.field('可信反向代理', proxies),
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
    el('section', { class: 'section-card', style: 'margin-top:16px' }, el('div', { class: 'section-head' }, el('div', { class: 'section-title', text: '保护设置' })), form));
}
document.querySelector('[data-tab="data"]').addEventListener('click', loadUsage);
document.querySelector('#data-refresh').addEventListener('click', loadUsage);
document.querySelector('#refresh-btn').addEventListener('click', () => { if (!document.querySelector('#page-data').classList.contains('hidden')) loadUsage(); });

// ---- 站点个性化（挂载到“设置”页） ----
const ROLES = ['logo', 'banner', 'background'];
const ROLE_LABEL = { logo: 'Logo（≤ 512 KiB）', banner: '横幅图片（首页顶部背景，≤ 2 MiB）', background: '页面背景（≤ 2 MiB）' };
const siteRoot = el('div', { id: 'profile-panel' });
document.querySelector('#page-settings').append(siteRoot);
const card = (...kids) => el('section', { class: 'section-card', style: 'margin-top:16px;max-width:820px' }, ...kids);
const kib = (n) => n >= 1048576 ? (n / 1048576).toFixed(1) + ' MiB' : Math.ceil(n / 1024) + ' KiB';
const readDataURL = (file) => new Promise((resolve, reject) => { const r = new FileReader(); r.onload = () => resolve(r.result); r.onerror = reject; r.readAsDataURL(file); });
const note = (box, text, ok) => { box.textContent = text; box.className = 'message show ' + (ok ? 'ok' : 'error'); box.style.display = text ? 'block' : 'none'; };
const previewFrame = () => el('iframe', { sandbox: '', title: '站点预览', style: 'display:none;width:100%;height:560px;border:1px solid var(--line);border-radius:10px;background:#fff' });

async function loadProfile() {
  let p;
  try { p = await FP.api('/api/profile'); }
  catch (e) {
    siteRoot.replaceChildren(card(el('div', { class: 'empty' }, el('strong', { text: '站点个性化读取失败' }), e.message,
      el('div', { style: 'margin-top:12px' }, el('button', { class: 'primary-btn', type: 'button', text: '重试', onclick: loadProfile })))));
    return;
  }
  drawProfile(p);
}

function drawProfile(p) {
  const f = p.fields, d = p.defaults;
  const pending = {}; // role -> File | 'remove'
  const name = el('input', { maxlength: 40, value: f.site_name, placeholder: d.site_name, 'aria-label': '站点名' });
  const intro = el('textarea', { maxlength: 200, rows: 2, placeholder: d.intro }); intro.value = f.intro;
  const footer = el('input', { maxlength: 120, value: f.footer, placeholder: d.footer });
  const button = el('input', { maxlength: 20, value: f.button_text, placeholder: d.button_text });
  const useAccent = el('input', { type: 'checkbox', checked: !!f.accent });
  const accent = el('input', { type: 'color', value: f.accent || '#4263eb', 'aria-label': '强调色', style: 'width:64px;padding:2px' });
  const status = el('div', { class: 'message', style: 'display:none;margin:12px 0 0' });
  const frame = previewFrame();

  const boxes = {};
  function drawAsset(role) {
    const box = boxes[role], cur = p.assets[role], pend = pending[role];
    const thumb = el('div', { style: 'width:96px;height:56px;border-radius:8px;border:1px solid var(--line);background:#f6f7fb center/contain no-repeat;flex-shrink:0' });
    let label = '未设置（使用默认外观）';
    if (pend instanceof File) { label = '待上传：' + pend.name + '（' + kib(pend.size) + '）'; readDataURL(pend).then(u => { thumb.style.backgroundImage = 'url("' + u + '")'; }); }
    else if (pend === 'remove') label = '保存后移除';
    else if (cur) { label = '当前：' + kib(cur.size); thumb.style.backgroundImage = 'url("' + cur.url + '")'; }
    const picker = el('input', { type: 'file', accept: 'image/png,image/jpeg,image/webp', class: 'hidden', onchange: (e) => {
      const file = e.target.files[0]; e.target.value = ''; if (!file) return; pending[role] = file; drawAsset(role);
    } });
    box.replaceChildren(thumb,
      el('div', { style: 'flex:1;min-width:140px' }, el('div', { text: ROLE_LABEL[role], style: 'font-weight:650;font-size:12px' }), el('div', { class: 'section-sub', text: label })),
      el('button', { type: 'button', class: 'quiet-btn', text: '选择图片', onclick: () => picker.click() }), picker,
      pend !== undefined ? el('button', { type: 'button', class: 'quiet-btn', text: '撤销更改', onclick: () => { delete pending[role]; drawAsset(role); } })
        : cur ? el('button', { type: 'button', class: 'danger-btn', text: '移除', onclick: () => { pending[role] = 'remove'; drawAsset(role); } }) : null);
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
    const form = new FormData(); form.append('package', file, file.name);
    note(status, '正在校验配置包…', true);
    try { const r = await FP.api('/api/profile/import', { method: 'POST', body: form }); note(status, '', true); reviewImport(r); }
    catch (err) { note(status, '导入失败，当前配置未改变：' + err.message, false); }
  } });

  const act = (text, cls, fn) => el('button', { type: 'button', class: cls, text, onclick: async (e) => {
    const b = e.currentTarget; b.disabled = true;
    try { await fn(); } catch (err) { note(status, err.message, false); } finally { b.disabled = false; }
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
        act('保存并应用', 'primary-btn', async () => { const next = await FP.api('/api/profile', { method: 'POST', body: buildForm() }); FP.toast('已保存并应用到公开主页与详情页'); drawProfile(next); }),
        el('a', { class: 'quiet-btn', href: '/api/profile/export', download: true, text: '导出配置包', style: 'text-decoration:none' }),
        el('button', { type: 'button', class: 'quiet-btn', text: '导入配置包', onclick: () => importPicker.click() }), importPicker),
      status, frame,
      el('div', { class: 'section-sub', style: 'margin-top:10px', text: '配置包只含站点外观与它引用的图片，不含文件、账号、证书或用量。导入会先预览，确认后才替换当前配置。' }))));
}

function reviewImport(r) {
  const f = r.fields;
  const finish = async (apply) => {
    try {
      if (apply) { const next = await FP.api('/api/profile/import/' + encodeURIComponent(r.token) + '/apply', { method: 'POST' }); FP.toast('已导入并应用'); closeModal(); drawProfile(next); }
      else { await FP.api('/api/profile/import/' + encodeURIComponent(r.token), { method: 'DELETE' }); closeModal(); }
    } catch (e) { FP.toast(e.message); }
  };
  const line = (k, v) => el('div', {}, el('b', { text: k + '：' }), v);
  const frame = previewFrame(); frame.style.display = 'block'; frame.srcdoc = r.html;
  const modal = el('div', { class: 'modal', style: 'width:min(820px,100%);max-height:92vh;overflow:auto', role: 'dialog', 'aria-modal': 'true', 'aria-label': '确认导入配置包' },
    el('h2', { text: '确认导入个性化配置包' }),
    el('div', { class: 'section-sub', style: 'margin:0 0 10px', text: '配置包已通过校验并暂存，当前站点尚未改变。确认后将整体替换站点个性化设置；文件、账号和单资源设置不受影响。' }),
    el('div', { style: 'font-size:11px;line-height:1.9;color:#4c5b73;margin-bottom:12px' },
      line('站点名', f.site_name || '（默认）'), line('简介', f.intro || '（默认）'), line('强调色', f.accent || '（默认）'),
      line('页脚', f.footer || '（默认）'), line('默认按钮文字', f.button_text || '（默认）'),
      line('图片素材', r.assets.length ? r.assets.map(a => a.label + ' ' + kib(a.size)).join('、') : '无')),
    frame,
    el('div', { class: 'modal-actions' }, el('button', { type: 'button', class: 'quiet-btn', text: '放弃导入', onclick: () => finish(false) }),
      el('button', { type: 'button', class: 'primary-btn', text: '确认并应用', onclick: () => finish(true) })));
  document.querySelector('#modal-root').replaceChildren(el('div', { class: 'modal-back', onclick: (e) => { if (e.target === e.currentTarget) finish(false); } }, modal));
}
document.querySelector('[data-tab="settings"]').addEventListener('click', loadProfile);
})();
