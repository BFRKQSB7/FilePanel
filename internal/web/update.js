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
body.append(title, note, status, refresh, apply); document.querySelector('#settings-maintenance').append(root);
const guide=document.createElement('details');const heading=document.createElement('summary');heading.textContent='更新和备份前需要做什么？';const text=document.createElement('p');text.textContent='网页更新需由安装器启用维护服务，并由服务器管理员预先放入经过签名校验的发布包。直接运行开发程序时可能无法网页更新，这不影响文件管理。请让管理员按项目《更新与备份恢复》说明先备份数据，再准备更新；外观配置包不包含文件，不能代替整站备份。';guide.append(heading,text);body.append(guide);
const phases={idle:'暂无预置更新',staged:'更新包已就绪',preflight:'正在检查更新条件',backup:'正在备份',backing_up:'正在备份',applying:'正在安装',restarting:'正在重启',complete:'更新完成',failed:'更新失败',rolled_back:'已回退',rollback:'正在回退',recovering:'正在恢复'};
let busy = false;
async function load() {
 if (busy || !FP.csrf()) return; busy = true; refresh.setAttribute('aria-busy','true'); refresh.disabled = true; apply.disabled = true; status.textContent = '正在读取更新状态…';
 try { const s = await FP.api('/api/update'); status.textContent = s.error || ('状态：' + (phases[s.phase]||s.phase||'尚无更新记录') + (s.version ? ' · 版本 ' + s.version : '') + ' · 检查时间 ' + new Date().toLocaleString()); apply.disabled = s.phase !== 'staged'; }
 catch (e) { status.textContent = e.message + '；可稍后重试。'; }
 finally { busy = false; refresh.removeAttribute('aria-busy'); apply.removeAttribute('aria-busy'); refresh.disabled = false; }
}
refresh.addEventListener('click', load);
apply.addEventListener('click', async () => {
 if (busy || !confirm('更新将暂停访问，并保存可回退快照。继续更新本机面板？')) return;
 busy = true; apply.setAttribute('aria-busy','true'); apply.disabled = true; refresh.disabled = true; status.textContent = '正在提交更新…';
 try { await FP.api('/api/update', { method: 'POST' }); status.textContent = '已提交。服务恢复后点击检查预置更新查看结果。'; }
 catch (e) { status.textContent = e.message; }
 finally { busy = false; refresh.removeAttribute('aria-busy'); apply.removeAttribute('aria-busy'); refresh.disabled = false; }
});
document.addEventListener('fp:navigate',e=>{if(e.detail.section==='maintenance')load();});
})();
