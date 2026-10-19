(() => {
'use strict';
const FP = window.FP; if (!FP) return;
const el = (tag, text, cls) => { const n = document.createElement(tag); if (text) n.textContent = text; if (cls) n.className = cls; return n; };
const json = (method, body) => ({ method, headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
const button = (text, fn) => { const b = el('button', text, 'quiet-btn'); b.type = 'button'; b.addEventListener('click', async () => { b.disabled = true;root.inert=true; try { await fn(); } catch (e) { FP.toast(e.message); } finally { b.disabled = false;root.inert=false; } }); return b; };
let nextField = 0;
const field = (label, input) => { const d = el('div', '', 'field node-field'); const l = el('label', label); if (input.type === 'checkbox') { d.classList.add('node-check'); l.prepend(input); d.append(l); } else { if (!input.id) input.id = 'node-field-' + (++nextField); l.htmlFor = input.id; d.append(l, input); } return d; };
document.head.append(el('style', '.node-field input:not([type="checkbox"]),.node-field textarea,.node-field select{width:100%;max-width:640px;box-sizing:border-box}.node-field textarea,.node-field select{padding:10px;border:1px solid var(--line);border-radius:9px;background:var(--card,#fff);color:inherit;font:inherit}.node-check label{display:flex;gap:9px;align-items:center}.node-check input{height:auto!important;width:auto}.node-field input[type="file"]{padding:8px;height:auto}'));
const input = (type, value) => { const n = el('input'); n.type = type; if (type === 'checkbox') n.checked = !!value; else n.value = value || ''; return n; };
const root = el('section', '', 'section-card'); root.style.marginTop = '24px'; document.querySelector('#settings-nodes').append(root); FP.watchDraft(root);
const dataRoot = el('section', '', 'section-card'); dataRoot.style.marginTop = '24px'; document.querySelector('#page-data').append(dataRoot);
const target = el('select'); target.id = 'upload-storage'; target.setAttribute('aria-label', '上传存储位置');
const uploadField = field('上传存储位置', target); document.querySelector('#drop-zone').before(uploadField);
FP.uploadTarget = () => target.value;
let state, busy = false;
function syncDirty(){if(root.querySelector('[data-edited]'))FP.dirty(root);else FP.clean(root);}
for(const name of ['input','change'])root.addEventListener(name,e=>{e.target.dataset.edited='true';});
async function load() {
 if (busy || !FP.csrf()) return; busy = true;
 const revision=root.dataset.revision;
 try { state = await FP.api('/api/nodes'); draw(root.dataset.revision!==revision); } catch (e) { FP.message('节点状态刷新失败，当前显示可能已过期：'+e.message); } finally { busy = false; }
}
function draw(preserveConfig=false) {
 const selected = target.value; target.replaceChildren(new Option('本机存储', ''));
 for (const n of state.nodes) if (n.storage && n.data_ready) target.add(new Option(label(n), n.id));
 if ([...target.options].some(o => o.value === selected)) target.value = selected;
 uploadField.hidden=target.options.length<2; FP.hasStorageNodes=target.options.length>1; FP.renderRows();
 drawReport(); if(preserveConfig||root.dataset.dirty)return;
 root.replaceChildren(); const content = el('div', '', 'setting-form'); root.append(content);
 content.append(el('h3', '节点与下载线路（可选）'), el('p', '一台服务器即可正常上传和分享，不需要添加节点。节点是额外的服务器，可存放文件或提供下载线路。默认只提供下载线路，开启“存储文件”后才会出现在上传位置列表。'));
 const setup=el('details');setup.append(el('summary','添加额外服务器（高级）'));content.append(setup);
 const endpoint = input('url', state.endpoint); endpoint.placeholder = 'https://面板域名:映射端口';
 const panelCA = el('textarea'); panelCA.placeholder = '使用系统信任的证书时留空'; panelCA.rows = 3;
 panelCA.value = state.ca || '';
 const command = el('textarea'); command.readOnly = true; command.rows = 4; command.setAttribute('aria-label', '节点接入命令');
 setup.append(el('p','接入前需要：另一台服务器、可访问的 HTTPS 地址及已验证的程序发布包。依次完成安装准备、生成接入命令、在额外服务器执行、刷新检查连接。'),field('节点连接的面板 HTTPS 地址', endpoint), field('自签名证书的信任文件（CA / PEM，通常留空）', panelCA),
  button('生成一次性接入命令', async () => { const r = await FP.api('/api/nodes/ticket', json('POST', { endpoint: endpoint.value, ca: panelCA.value })); command.value = r.command; endpoint.removeAttribute('data-edited');panelCA.removeAttribute('data-edited');syncDirty();FP.toast(r.note); }), command,
  button('复制接入命令', async () => { if(!command.value){FP.toast('请先生成接入命令');return;}await FP.copy(command.value,command); }),
  el('p', '在节点上准备已校验的 filepanel 与 release.json，设置 FILEPANEL_MANIFEST_SHA256 后执行接入命令。先将 JSON 内节点地址换成实际 HTTPS 地址，私有节点证书在 node_ca 填入 PEM。安装器以专用用户运行并启用自启，监听 127.0.0.1:8081；管理员配置已有 HTTPS 反代，NAT 端口须逐跳可达。入网文件使用后删除。尚未配置在线发布源。'));
 const relay = el('select'); relay.add(new Option('本机出口', ''));
 for (const n of state.nodes) if (n.relay && !n.revoked) relay.add(new Option(label(n), n.id)); relay.value = state.settings.default_relay;
 const fallback = input('checkbox', state.settings.fallback);
 content.append(field('自动线路使用的默认出口', relay), field('出口失败时尝试本机（仍检查文件源与额度）', fallback),
  button('保存线路设置', async () => { await FP.api('/api/nodes/settings', json('PUT', { default_relay: relay.value, fallback: fallback.checked })); relay.removeAttribute('data-edited');fallback.removeAttribute('data-edited');syncDirty();FP.toast('线路设置已保存'); await load(); }), button('刷新节点', load));
 for (const n of state.nodes) {
  const card = el('div', '', 'setting-form'); card.style.borderTop = '1px solid var(--line)';
  card.append(el('h4', label(n)), el('p', n.revoked ? '已撤销；文件与离线记录保留' : (n.rebind_pending ? '迁移后等待验证新地址并改连 · ' : '') + (n.online ? '控制连接在线' : '控制连接离线') + ' · 面板→节点：' + (n.data_ready ? 'TLS 与身份检查通过' : n.probe_error || '待检查') + ' · 出口→存储：按下载请求实测 · 访客→出口：待外部验证'));
  const storage = input('checkbox', n.storage), out = input('checkbox', n.relay), note = input('text', n.note), region = input('text', n.region);
  const manual = input('checkbox', n.manual_region), quota = input('number', n.policy.quota_bytes / 1e9), protect = input('checkbox', n.policy.protect);
  const basis = el('select'); basis.add(new Option('仅发送', 'sent')); basis.add(new Option('收发合计', 'total')); basis.value = n.policy.basis;
  card.append(field('存储文件：允许上传新文件', storage), field('提供下载线路：允许访客从这里下载', out), field('备注', note), field('地区（自动失败显示未知）', region), field('手动覆盖地区', manual), field('启用用量保护', protect), field('周期额度（GB；0 为未知）', quota), field('计费口径', basis));
  card.append(button('保存节点设置', async () => { await FP.api('/api/nodes/' + n.id, json('PUT', { storage: storage.checked, relay: out.checked, note: note.value, region: region.value, manual_region: manual.checked, policy: { ...n.policy, protect: protect.checked, quota_bytes: Math.round(Number(quota.value) * 1e9), basis: basis.value } })); card.querySelectorAll('[data-edited]').forEach(e=>e.removeAttribute('data-edited'));syncDirty();FP.toast('已保存，将在下一次连接检查时同步'); await load(); }),
   button('检查面板到节点', async () => { await FP.api('/api/nodes/' + n.id + '/probe', { method: 'POST' }); await load(); }),
   button('检查节点预置更新', async () => { const s = await FP.api('/api/nodes/' + n.id + '/update'); FP.toast('节点更新：' + s.phase + (s.version ? ' · ' + s.version : '') + (s.error ? ' · ' + s.error : '')); }),
   button('更新此节点', async () => { if (!confirm('仅更新此节点，维护期间暂停该节点传输。继续？')) return; await FP.api('/api/nodes/' + n.id + '/update', { method: 'POST' }); FP.toast('此节点更新已提交，恢复后可检查结果'); }),
   button('撤销节点凭据', async () => { if (!confirm('撤销后该节点停止接受授权，已有文件仍保留。继续？')) return; await FP.api('/api/nodes/' + n.id + '/revoke', { method: 'POST' }); await load(); }),
   button('移除已撤销且无文件的记录', async () => { await FP.api('/api/nodes/' + n.id, { method: 'DELETE' }); await load(); })); content.append(card);
 }
 content.append(button('放弃未保存的节点修改',async()=>{if(!root.dataset.dirty||confirm('放弃未保存的节点与线路修改？')){FP.clean(root);draw();}}));
 const migration=el('details');migration.append(el('summary','迁移到另一台面板（高级）'));content.append(migration);drawMigration(migration);
}
function drawReport(){
 dataRoot.hidden=!state.nodes.length; dataRoot.replaceChildren(); const report = el('div', '', 'setting-form'); dataRoot.append(report);
 report.append(el('h3', '节点磁盘与独立用量'), el('p', '各跳用量独立累计，不能相加当作访客净下载量。离线时显示最后一次报告；未确认预算仍由节点保守保留。'));
 for (const n of state.nodes) { const r = n.report; report.append(el('h4', label(n)),el('p','最近连接：'+(n.last_seen?new Date(n.last_seen*1000).toLocaleString():'尚无记录')+(n.online?' · 在线':' · 离线，以下为最后报告')), el('p', r ? '周期 UTC ' + new Date(r.period.start * 1000).toISOString().slice(0, 10) + ' · 发送 ' + FP.format(r.period.sent) + ' · 接收 ' + FP.format(r.period.received) + ' · 今日 UTC 发送 ' + FP.format(r.today.sent) + ' / 接收 ' + FP.format(r.today.received) + ' · 完整出口下载 ' + r.period.downloads + ' · 磁盘可用 ' + FP.format(r.free) + ' / ' + FP.format(r.total) : '尚无报告')); }
}
function drawMigration(content) {
 const box = el('div', '', 'setting-form'); box.style.borderTop = '1px solid var(--line)';
 box.append(el('h3', '迁移到新面板'), el('p', '卸载前先导出加密迁移包并保管密码。停止旧面板后，在没有文件和节点记录的新面板导入，填写节点可访问的新面板 HTTPS 地址。此包保留节点身份、远端文件关联、分享路径和线路设置；不含本机文件、图片素材、站点外观和 HTTPS 私钥。'));
 const password = input('password', ''); password.autocomplete = 'new-password'; password.minLength = 12;
 const file = input('file', ''); file.accept = '.json';
 const endpoint = input('url', state.endpoint); endpoint.placeholder = 'https://新面板域名:端口';
 const ca = el('textarea'); ca.rows = 3; ca.placeholder = '新面板使用系统信任证书时留空';
 const preview = el('p'); let prepared = null;
 const apply = button('确认导入并恢复节点身份', async () => {
  if (!prepared) return;
  const result = await FP.api('/api/nodes/migration/apply', json('POST', prepared));
  prepared = null; password.value = ''; apply.disabled = true;
  FP.toast('已导入 ' + result.nodes + ' 个节点与 ' + result.files + ' 个远端文件；正在验证节点新地址');
  preview.textContent = '已导入 ' + result.nodes + ' 个节点与 ' + result.files + ' 个远端文件；后台将验证并通知节点改连。' + result.warning;
  await load(); await FP.refreshFiles();
 }); apply.disabled = true;
 for (const n of [password, file, endpoint, ca]) n.addEventListener('input', () => { prepared = null; apply.disabled = true; });
 box.append(field('迁移包加密密码（至少 12 字节）', password), button('导出加密节点迁移包', async () => {
  const res = await fetch('/api/nodes/migration/export', { ...json('POST', { password: password.value }), headers: { 'Content-Type': 'application/json', 'X-CSRF-Token': FP.csrf() }, credentials: 'same-origin' });
  if (!res.ok) throw new Error((await res.json()).error || '导出失败');
  const url = URL.createObjectURL(await res.blob()); const link = el('a'); link.href = url; link.download = 'filepanel-nodes.enc.json'; document.body.append(link); link.click(); link.remove(); setTimeout(() => URL.revokeObjectURL(url), 1000); password.value = ''; FP.toast('已导出；请先保存包与密码，再卸载旧面板');
 }), field('从旧面板导出的迁移包', file), field('新面板 HTTPS 地址', endpoint), field('新面板私有 CA（可选）', ca), button('校验并预览导入', async () => {
  if (!file.files[0] || file.files[0].size > 4 * 1024 * 1024) throw new Error('请选择 4 MiB 以内的迁移包');
  const bytes = new Uint8Array(await file.files[0].arrayBuffer()); let binary = ''; for (const b of bytes) binary += String.fromCharCode(b);
  const body = { package: btoa(binary), password: password.value, endpoint: endpoint.value, ca: ca.value };
  const result = await FP.api('/api/nodes/migration/preview', json('POST', body)); prepared = body; apply.disabled = false;
  preview.textContent = '将恢复 ' + result.nodes + ' 个节点与 ' + result.files + ' 个远端文件。' + result.warning;
 }), preview, apply);
 for (const n of state.nodes) if (!n.revoked) box.append(button('通知 ' + label(n) + ' 改连当前面板', async () => { await FP.api('/api/nodes/' + n.id + '/rebind', { method: 'POST' }); FP.toast('节点已验证并保存新面板地址'); await load(); }));
 content.append(box);
}
function label(n) { let host = n.public_ip || 'IP 未知'; try { const port = new URL(n.endpoint).port; if (port) host += ':' + port; } catch {} return host + ' · ' + (n.region || '地区未知') + (n.note ? ' · ' + n.note : ''); }
document.querySelectorAll('#refresh-btn,#data-refresh').forEach(n=>n.addEventListener('click',load));
document.addEventListener('fp:navigate',e=>{if(e.detail.tab==='data'||e.detail.section==='nodes')load();});
document.addEventListener('fp:logout',()=>{state=null;root.replaceChildren();dataRoot.replaceChildren();});
new MutationObserver(load).observe(document.querySelector('#app'), { attributes: true, attributeFilter: ['class'] });

FP.loadNodes = load; load();
})();
