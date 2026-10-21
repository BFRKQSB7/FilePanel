(() => {
'use strict';
const $ = s => document.querySelector(s);
let csrf = '', items = [], filesBusy = false, dataBusy = false, uploading = false;
const queue = [];
const format = n => { if (n < 1024) return n + ' B'; const u = ['KiB','MiB','GiB','TiB']; let i = -1; do { n /= 1024; i++; } while (n >= 1024 && i < u.length - 1); return n.toFixed(1) + ' ' + u[i]; };
const date = t => new Date(t * 1000).toLocaleString('zh-CN');
const node = (tag, text, cls) => { const e = document.createElement(tag); if (text) e.textContent = text; if (cls) e.className = cls; return e; };
function toast(text) { const e = node('div', text, 'toast'); e.setAttribute('role','status'); if(activeDialog){activeDialog.querySelector('.toast')?.remove();activeDialog.append(e);}else $('#toast-root').replaceChildren(e);setTimeout(() => e.remove(), 6500); }
function message(text, error = true) {
 const e = activeDialog ? (activeDialog.querySelector('.dialog-message') || activeDialog.appendChild(node('div','','dialog-message'))) : $('#message');
 e.textContent = text;e.setAttribute('role','alert'); e.className = (activeDialog?'dialog-message ':'')+'message show ' + (error ? 'error' : 'ok'); e.append(action('关闭提示', () => e.classList.remove('show')));
}
function action(label, fn, cls = 'ghost-btn') { const b = node('button', label, cls); b.type = 'button'; b.addEventListener('click', fn); return b; }
async function api(url, options = {}) {
 const session = csrf;
 const headers = new Headers(options.headers || {});
 if (csrf && options.method && options.method !== 'GET') headers.set('X-CSRF-Token', csrf);
 let res;
 try { res = await fetch(url, {credentials:'same-origin', ...options, headers}); }
 catch { throw new Error('连接失败，请检查网络后重试；已提交的操作请先刷新确认结果。'); }
 if(session && csrf!==session)throw new Error('登录状态已变化，请重新加载。');
 const body = (res.headers.get('content-type') || '').includes('json') ? await res.json() : null;
 if (!res.ok) { if (res.status === 401 && url !== '/api/login') showAuth(); throw new Error(body?.error || '请求失败（' + res.status + '），请重试'); }
 return body;
}
// Drafts stay in the DOM when switching sections; never persist passwords or files to browser storage.
function dirty(root) { root.dataset.dirty = 'true'; root.dataset.revision=String(Number(root.dataset.revision||0)+1); }
function clean(root) { delete root.dataset.dirty; root.dataset.revision=String(Number(root.dataset.revision||0)+1); }
function watchDraft(root) { root.addEventListener('input', () => dirty(root)); root.addEventListener('change', () => dirty(root)); }
const hasDraft = () => !!document.querySelector('[data-dirty="true"]');
window.addEventListener('beforeunload', e => { if (hasDraft() || uploading) { e.preventDefault(); e.returnValue = ''; } });
let activeDialog;
function openDialog(dialog, {canClose = () => true, onClose = () => {}} = {}) {
 if (activeDialog) return false;
 const trigger = document.activeElement;
 const close = () => {
  if (!canClose()) return;
  if (dialog.dataset.dirty && !confirm('有未保存的修改。放弃这些修改并关闭？')) return;
  dialog.close(); dialog.remove(); activeDialog = null; onClose();
  if (trigger?.isConnected) trigger.focus();
  else if (trigger?.dataset.focusKey) document.querySelector('[data-focus-key="'+CSS.escape(trigger.dataset.focusKey)+'"]')?.focus();
 };
 dialog.addEventListener('cancel', e => { e.preventDefault(); close(); });
 // Keep keyboard cycling within the visible controls, including dynamic fields.
 dialog.addEventListener('keydown', e => {
  if (e.key !== 'Tab') return;
  const controls = [...dialog.querySelectorAll('button, a[href], input, select, textarea, summary, iframe, [tabindex]')]
   .filter(el => el.tabIndex >= 0 && !el.disabled && !el.closest('[inert]') && el.getClientRects().length && getComputedStyle(el).visibility !== 'hidden');
  const first = controls[0], last = controls[controls.length - 1];
  if (!first) { e.preventDefault(); return; }
  if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last.focus(); }
  else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first.focus(); }
 });
 dialog.addEventListener('click', e => { if (e.target === dialog) { const r = dialog.getBoundingClientRect(); if (e.clientX < r.left || e.clientX > r.right || e.clientY < r.top || e.clientY > r.bottom) close(); } });
 document.body.append(dialog); activeDialog = dialog; dialog.showModal();
 return close;
}
async function copy(text, input) {
 try { await navigator.clipboard.writeText(text); toast('链接已复制'); }
 catch { if (input) { input.focus(); input.select(); } message('浏览器未允许自动复制，请选中链接后复制。', false); }
}
function navigate(tab, section) {
 if (!['files','data','tutorial','settings'].includes(tab)) tab = 'files';
 for (const b of document.querySelectorAll('.nav button')) { const selected = b.dataset.tab === tab; b.classList.toggle('active', selected); b.setAttribute('aria-current', selected ? 'page' : 'false'); }
 for (const id of ['files','data','tutorial','settings']) $('#page-' + id).classList.toggle('hidden', id !== tab);
 $('#crumb-current').textContent = {files:'文件',data:'用量与状态',tutorial:'使用帮助',settings:'设置'}[tab];
 if (tab === 'settings') {
  section = section || 'appearance';
  if (!['appearance','access','protection','nodes','account','maintenance'].includes(section)) section = 'appearance';
  for (const e of document.querySelectorAll('.settings-pane')) e.classList.toggle('hidden', e.id !== 'settings-' + section);
  for (const b of document.querySelectorAll('[data-settings]')) { const selected = b.dataset.settings === section; b.classList.toggle('active', selected); b.setAttribute('aria-current', selected ? 'page' : 'false'); }
 }
 const hash = '#' + tab + (tab === 'settings' ? '-' + section : '');
 if (location.hash !== hash) history.pushState(null, '', hash);
 document.dispatchEvent(new CustomEvent('fp:navigate', {detail:{tab, section}}));
 if (tab === 'data') refreshData();
}
function route() { const [tab, section] = location.hash.slice(1).split('-'); navigate(tab || 'files', section); }
window.addEventListener('popstate', route);
document.querySelectorAll('.nav button').forEach(b => b.addEventListener('click', () => navigate(b.dataset.tab)));
document.querySelectorAll('[data-settings]').forEach(b => b.addEventListener('click', () => navigate('settings', b.dataset.settings)));
document.querySelectorAll('[data-go]').forEach(b => b.addEventListener('click', () => { const [tab, section] = b.dataset.go.split('-'); navigate(tab, section); }));
function showAuth() {
 csrf = ''; items = []; queue.length = 0;
 if (activeDialog) { activeDialog.close(); activeDialog.remove(); activeDialog = null; }
 document.querySelectorAll('[data-dirty]').forEach(clean);
 document.querySelectorAll('input[type=password]').forEach(e=>e.value='');$('#upload-results').replaceChildren();
 $('#app').classList.add('hidden'); $('#auth-screen').classList.remove('hidden');
 $('#login-view').classList.remove('hidden'); $('#init-view').classList.add('hidden');
 document.dispatchEvent(new Event('fp:logout'));
}
async function showApp() { $('#auth-screen').classList.add('hidden'); $('#app').classList.remove('hidden'); route(); await Promise.all([refreshFiles(), refreshData()]); }
async function boot() {
 try { const state = await api('/api/status');
  if (!state.initialized) { $('#auth-screen').classList.remove('hidden'); $('#init-view').classList.remove('hidden'); }
  else if (!state.authenticated) showAuth();
  else { csrf = state.csrf || ''; await showApp(); }
 } catch (e) { message(e.message); $('#boot-retry').classList.remove('hidden'); }
}
async function refreshFiles() {
 if (filesBusy || !csrf) return; filesBusy = true;
 $('#file-status').textContent = '正在刷新文件…';
 try {
  items = await api('/api/files'); renderRows();
  $('#stat-count').textContent = items.length; $('#stat-size').textContent = format(items.reduce((s,x) => s + x.size, 0));
  const shared = items.filter(x => x.shared).length;
  $('#stat-access').textContent = shared ? shared + ' 个分享 · ' + (items.length - shared) + ' 个未分享' : '全部未分享';
  $('#file-status').textContent = '更新于 ' + new Date().toLocaleTimeString();
  drawStart(items.some(x => x.shared));
 } catch (e) { $('#file-status').textContent = '刷新失败；当前列表可能已过期。'; message(e.message); }
 finally { filesBusy = false; }
}
function drawStart(published) {
 let dismissed = false; try { dismissed = localStorage.getItem('fp-start-dismissed') === '1'; } catch {}
 $('#getting-started').classList.toggle('hidden', dismissed || published);
 $('#start-upload').textContent = items.length ? '✓ 已上传文件' : '1 · 上传第一个文件';
 $('#start-share').disabled = !items.length;
}
$('#dismiss-start').addEventListener('click', () => { try { localStorage.setItem('fp-start-dismissed','1'); } catch {} $('#getting-started').classList.add('hidden'); });
$('#show-start').addEventListener('click', () => { try { localStorage.removeItem('fp-start-dismissed'); } catch {} navigate('files'); $('#getting-started').classList.remove('hidden'); });
$('#start-upload').addEventListener('click', () => $('#file-picker').click());
$('#start-share').addEventListener('click', () => { if (items.length) window.FP.openShare(items[0]); });
function renderRows() {
 const q = $('#search').value.trim().toLocaleLowerCase(), filtered = items.filter(x => x.name.toLocaleLowerCase().includes(q));
 $('#file-rows').replaceChildren(); $('#empty').classList.toggle('hidden', filtered.length !== 0);
 $('#empty-title').textContent = q ? '没有找到匹配的文件' : '这里还没有文件';
 $('#empty-hint').textContent = q ? '试试其他关键词，或清除搜索查看全部文件。' : '上传后默认只有你能访问，需要时再开启分享。';
 $('#clear-search').classList.toggle('hidden', !q);
 for (const item of filtered) {
  const tr = node('tr'), td = node('td'), cell = node('div', '', 'file-cell'), meta = node('div');
  const name = node('div', item.name, 'file-name');
  const info = node('div', item.storage_node ? '远端存储' : '本机存储', 'file-meta');
  info.append(node('span', item.featured ? '公开分享 · 主页展示' : item.shared ? '私密分享 · 仅链接' : '未分享', 'badge' + (item.shared ? ' shared' : '')));
  meta.append(name, info); cell.append(node('div','▤','file-icon'), meta); td.append(cell);
  const size = node('td', format(item.size), 'file-size'), created = node('td',date(item.created_at),'hide-mobile');
  const actions = node('td'), wrap = node('div','','row-actions'); actions.className = 'file-actions';
  const download = node('a','下载','ghost-btn'); download.href = '/api/files/' + encodeURIComponent(item.id); download.download = item.name;
  const shareButton=action('分享',()=>window.FP.openShare(item));shareButton.dataset.focusKey='share-'+item.id;wrap.append(download,shareButton);
  if(item.shared)wrap.append(action('取消分享',()=>window.FP.cancelShare(item),'danger-btn'));
  const more = node('details'), summary = node('summary','更多'); summary.setAttribute('aria-label', '更多操作：' + item.name); more.append(summary);
  more.addEventListener('keydown', e => { if (e.key === 'Escape') { more.open = false; summary.focus(); e.stopPropagation(); } });
  const list = node('div','','more-actions');
  if (item.storage_node || window.FP.hasStorageNodes) list.append(action('移动', () => { more.open = false; window.FP.openMove(item); }));
  list.append(action('重命名', () => rename(item)), action('删除', () => remove(item),'danger-btn'));
  const hash = node('small','文件校验码（SHA-256，用于核对完整性）：' + item.sha256); list.append(hash); more.append(list); wrap.append(more); actions.append(wrap); tr.append(td,size,created,actions); $('#file-rows').append(tr);
 }
}
async function refreshData() {
 if (dataBusy || !csrf) return; dataBusy = true; $('#data-status').textContent = '正在读取磁盘信息…';
 try { const d = await api('/api/data');
  for (const k of ['total','used','free']) $('#data-' + k).textContent = format(d[k]);
  const pct = d.total ? Math.min(100,d.used / d.total * 100) : 0;
  $('#storage-percent').textContent = pct.toFixed(1) + '%'; $('#storage-meter').style.width = pct + '%';
  $('#storage-foot').textContent = format(d.used) + ' 已用 · ' + format(d.free) + ' 可用';
  $('#data-status').textContent = '更新于 ' + new Date().toLocaleTimeString() + '；这是所在磁盘的总用量，包含其他程序。';
 } catch { $('#data-status').textContent = '磁盘读取失败，旧数据可能已过期，请刷新重试。'; $('#storage-foot').textContent = '磁盘信息暂不可用'; }
 finally { dataBusy = false; }
}
async function rename(item) {
 const name = prompt('输入新的文件名（不会改变文件内容和分享链接）',item.name); if (name === null || name === item.name) return;
 try { await api('/api/files/' + encodeURIComponent(item.id), {method:'PATCH',headers:{'Content-Type':'application/json'},body:JSON.stringify({name})}); toast('文件名已更新'); await refreshFiles(); } catch(e) { message(e.message); }
}
async function remove(item) {
 if (!confirm('确定永久删除“' + item.name + '”？文件无法从面板恢复；公开分享链接、介绍页和相关图片也将失效。')) return;
 try { await api('/api/files/' + encodeURIComponent(item.id),{method:'DELETE'}); toast('文件已删除'); await Promise.all([refreshFiles(),refreshData()]); } catch(e) { message(e.message); }
}
function enqueue(files) {
 $('#upload-results').closest('details').open = true;
 for (const file of files) {
  const row = node('li',file.name + ' · 等待上传'); $('#upload-results').append(row);
  queue.push({file,row,target:window.FP.uploadTarget?.() || ''});
 }
 if (!uploading) drainQueue();
}
async function drainQueue() {
 if (uploading || !queue.length) return; uploading = true;
 $('#progress').classList.remove('hidden');
 try {
  while (queue.length && csrf) {
   const task = queue.shift(), {file,row,target} = task;
   $('#upload-status').textContent = '正在上传 ' + file.name + ' · 等待 ' + queue.length + ' 个'; row.textContent = file.name + ' · 上传中';
   const bar = $('#progress span'); bar.style.width = '0%';
   try {
    if (file.size > 10 * 1024 ** 3) throw new Error('超过单文件 10 GiB 上限');
    await new Promise((resolve,reject) => {
     const form = new FormData(); form.append('file',file,file.name);
     const xhr = new XMLHttpRequest(); xhr.open('POST','/api/files?storage=' + encodeURIComponent(target)); xhr.withCredentials = true; xhr.setRequestHeader('X-CSRF-Token',csrf);
     xhr.upload.onprogress = e => { if (e.lengthComputable) { bar.style.width = (e.loaded / e.total * 100) + '%'; if (e.loaded === e.total) { row.textContent = file.name + ' · 上传已发送，正在校验和保存…'; $('#upload-status').textContent = row.textContent; } } };
     xhr.onload = () => { let body = {}; try { body = JSON.parse(xhr.responseText); } catch {} if (xhr.status >= 200 && xhr.status < 300) resolve(); else reject(new Error(body.error || '上传失败（' + xhr.status + '）')); };
     xhr.onerror = () => reject(new Error('连接中断，请刷新确认文件是否已保存，再决定重试')); xhr.onabort = () => reject(new Error('上传已中断')); xhr.send(form);
    }); row.textContent = file.name + ' · 已完成';
   } catch(e) { row.textContent = file.name + ' · ' + e.message; row.append(action('重试此文件', e => { e.currentTarget.remove(); row.textContent = file.name + ' · 等待重试'; queue.push(task); if (!uploading) drainQueue(); })); }
  }
 } finally { uploading = false; $('#progress').classList.add('hidden'); $('#upload-status').textContent = '队列处理完毕，请查看每个文件的结果。'; await Promise.all([refreshFiles(),refreshData()]); }
}
$('#clear-search').addEventListener('click', () => { $('#search').value = ''; renderRows(); $('#search').focus(); });
$('#search').addEventListener('input',renderRows);
for (const id of ['choose-upload','top-upload']) $('#' + id).addEventListener('click', () => $('#file-picker').click());
$('#file-picker').addEventListener('change',e => { enqueue([...e.target.files]); e.target.value = ''; });
const drop = $('#drop-zone');
for (const ev of ['dragenter','dragover']) drop.addEventListener(ev,e => { e.preventDefault(); drop.classList.add('drag'); });
for (const ev of ['dragleave','drop']) drop.addEventListener(ev,e => { e.preventDefault(); drop.classList.remove('drag'); });
drop.addEventListener('drop',e => enqueue([...e.dataTransfer.files]));
$('#refresh-btn').addEventListener('click', async e => { const b = e.currentTarget; b.disabled = true; b.setAttribute('aria-busy','true'); try { await Promise.all([refreshFiles(),refreshData()]); } finally { b.disabled = false; b.removeAttribute('aria-busy'); } });
$('#data-refresh').addEventListener('click',refreshData);
$('#boot-retry').addEventListener('click',boot);
function formSubmit(selector, fn) { $(selector).addEventListener('submit',async e => { e.preventDefault(); const b = e.currentTarget.querySelector('button[type=submit]'); if (b.disabled) return; b.disabled = true; b.setAttribute('aria-busy','true'); try { await fn(); } catch(err) { message(err.message); } finally { b.disabled = false; b.removeAttribute('aria-busy'); } }); }
const json = body => ({method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)});
formSubmit('#init-form',async () => { await api('/api/init',json({token:$('#init-token').value,password:$('#init-password').value})); $('#init-form').reset(); showAuth(); message('管理员已初始化，请使用刚设置的密码登录。',false); });
formSubmit('#login-form',async () => { const r = await api('/api/login',json({password:$('#login-password').value})); csrf = r.csrf; $('#login-form').reset(); $('#message').classList.remove('show'); await showApp(); });
$('#logout-btn').addEventListener('click',async () => { if ((hasDraft() || uploading) && !confirm('有未保存的修改或上传任务，仍要退出登录？')) return; try { await api('/api/logout',{method:'POST'}); showAuth(); } catch(e) { message(e.message); } });
formSubmit('#password-form',async () => { await api('/api/password',json({current:$('#current-password').value,next:$('#next-password').value})); $('#password-form').reset(); showAuth(); message('密码已更新，请使用新密码登录。',false); });
watchDraft($('#password-form'));
window.FP = {api,toast,message,format,date,refreshFiles,refreshData,renderRows,navigate,dirty,clean,watchDraft,hasDraft,openDialog,copy,csrf:() => csrf};
document.addEventListener('DOMContentLoaded',boot,{once:true});
})();
