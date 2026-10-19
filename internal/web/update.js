(() => {
'use strict';
const FP = window.FP; if (!FP) return;
const root = document.createElement('section'); root.className = 'section-card'; root.style.marginTop = '24px';
const body = document.createElement('div'); body.className = 'setting-form'; root.append(body);
const title = document.createElement('h3'); title.textContent = '程序更新与恢复';
const note = document.createElement('p'); note.textContent = '管理员预置签名发布包后可从这里更新。更新期间暂停访问，失败自动回退；备份与恢复使用服务器上的受限维护命令。远端正式文件须在对应节点另行备份。';
const status = document.createElement('p'); status.setAttribute('role', 'status');
const refresh = document.createElement('button'); refresh.className = 'quiet-btn'; refresh.textContent = '检查预置更新'; refresh.type = 'button';
const apply = document.createElement('button'); apply.className = 'primary-btn'; apply.textContent = '更新本机面板'; apply.type = 'button'; apply.disabled = true;
body.append(title, note, status, refresh, apply); document.querySelector('#page-settings').append(root);
let busy = false;
async function load() {
 if (busy || !FP.csrf()) return; busy = true; refresh.disabled = true; apply.disabled = true; status.textContent = '正在读取更新状态…';
 try { const s = await FP.api('/api/update'); status.textContent = s.error || ('状态：' + s.phase + (s.version ? ' · 版本 ' + s.version : '') + ' · 检查时间 ' + new Date().toLocaleString()); apply.disabled = s.phase !== 'staged'; }
 catch (e) { status.textContent = e.message + '；可稍后重试。'; }
 finally { busy = false; refresh.disabled = false; }
}
refresh.addEventListener('click', load);
apply.addEventListener('click', async () => {
 if (busy || !confirm('更新将暂停访问，并保存可回退快照。继续更新本机面板？')) return;
 busy = true; apply.disabled = true; refresh.disabled = true; status.textContent = '正在提交更新…';
 try { await FP.api('/api/update', { method: 'POST' }); status.textContent = '已提交。服务恢复后点击检查预置更新查看结果。'; }
 catch (e) { status.textContent = e.message; }
 finally { busy = false; refresh.disabled = false; }
});
document.querySelector('[data-tab="settings"]').addEventListener('click', load);
})();
