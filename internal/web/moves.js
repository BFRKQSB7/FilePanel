(() => {
'use strict';
const FP = window.FP;
if (!FP) return;
const el = (tag, text) => { const n = document.createElement(tag); if (text) n.textContent = text; return n; };
const box = el('section'); box.className = 'section-card'; box.style.marginTop = '24px'; box.hidden=true; document.querySelector('#page-files').append(box);
const stages = { preflight: '预检与预留', transferring: '传输', verifying: '校验', switching: '主位置切换', cleaning: '目标已切换，源清理待重试', done: '完成', cancelling: '正在清理临时目标', cancelled: '已取消' };
const json = body => ({ method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
let dialog, polling = false, signature = '';
const nodeLabel = n => { const endpoint = new URL(n.endpoint); return (n.public_ip || endpoint.hostname) + (endpoint.port ? ':' + endpoint.port : '') + (n.note ? ' · ' + n.note : ''); };
FP.openMove = async item => {
 try {
  const state = await FP.api('/api/nodes');
  if(document.querySelector('dialog[open]'))return; dialog = el('dialog');dialog.className='modal'; dialog.setAttribute('aria-label', '移动文件');
  const form = el('div'); form.className = 'setting-form'; dialog.append(form);
  const select = el('select'); select.setAttribute('aria-label', '目标存储位置');
  if (item.storage_node) select.add(new Option('本机存储', ''));
  for (const n of state.nodes) if (n.storage && n.online && !n.revoked && n.id !== item.storage_node) select.add(new Option(nodeLabel(n), n.id));
  const source = item.storage_node ? state.nodes.find(n => n.id === item.storage_node) : null;
  form.append(el('h3', '移动“' + item.name + '”'), el('p', '源：' + (source ? nodeLabel(source) : (item.storage_node ? '离线节点' : '本机')) + ' · ' + FP.format(item.size)),
   el('p', '源文件在目标完整校验并切换前持续可用，分享地址和版本保持。优先直传，无法连接时通过面板流式中转；每跳都受用量和空间保护。'), select);
  const preview = el('p'); form.append(preview);
  const describe = () => { const n = state.nodes.find(n => n.id === select.value); preview.textContent = n ? '目标磁盘最后报告：' + (n.report ? FP.format(n.report.free) + ' 可用' : '未知') + '；执行时检查实时安全预留和用量预算。' : '目标为本机；执行时检查实时安全预留和用量预算。'; };
  select.addEventListener('change', describe); describe();
  const requestID = crypto.randomUUID();
  const start = el('button', '确认移动'); start.type = 'button'; start.disabled = !select.options.length;
  start.addEventListener('click', async () => { start.disabled = true; try { await FP.api('/api/files/' + encodeURIComponent(item.id) + '/move', json({ request_id: requestID, target: select.value })); closing(); FP.toast('移动任务已建立，后台预检后执行'); await load(); } catch(e) { FP.toast(e.message); start.disabled = false; } });
  let closing; const close = el('button', '关闭'); close.type = 'button'; close.addEventListener('click', () => closing()); form.append(start, close);
  closing=FP.openDialog(dialog);
 } catch(e) { FP.toast(e.message); }
};
async function load() {
 if (!FP.csrf() || polling) return; polling = true;
 try {
  const tasks = await FP.api('/api/moves'); const next = JSON.stringify(tasks); if (signature === next) return; signature = next;
  box.hidden=!tasks.length; box.replaceChildren(el('h3', '文件移动任务'));
  if (!tasks.length) box.append(el('p', '在文件操作中选择“移动”，可在这里查看阶段、路径与重试。'));
  for (const task of tasks) {
   const row = el('div'); row.className = 'setting-form';
   const route = task.route === 'direct' ? '直传' : task.route === 'panel' ? '面板中转' : '待检查';
   const progress = task.route === 'direct' && task.state === 'transferring' ? '传输中，等待目标确认字节数' : FP.format(task.progress) + ' / ' + FP.format(task.size);
   row.append(el('strong', task.name), el('p', (stages[task.state] || task.state) + ' · ' + route + ' · ' + progress));
   if (task.error) row.append(el('p', task.error));
   if (!['done', 'cancelled'].includes(task.state)) {
    for (const action of ['retry', ...(['cleaning', 'cancelling'].includes(task.state) ? [] : ['cancel'])]) {
     const b = el('button', action === 'retry' ? '重试 / 继续清理' : '取消移动'); b.type = 'button'; b.className = 'quiet-btn';
     b.addEventListener('click', async () => { b.disabled = true; try { await FP.api('/api/moves/' + task.id + '/' + action, { method: 'POST' }); await load(); } catch(e) { FP.toast(e.message); } finally { b.disabled = false; } }); row.append(b);
    }
   }
   box.append(row);
  }
  if (tasks.some(t => t.state === 'done' || t.state === 'cleaning')) await FP.refreshFiles();
 } catch(e) { FP.toast(e.message); } finally { polling = false; }
}
document.addEventListener('fp:logout',()=>{signature='';box.replaceChildren();box.hidden=true;});
setInterval(() => { if (!document.querySelector('#page-files').classList.contains('hidden')) load(); }, 4000);
document.querySelector('#refresh-btn').addEventListener('click', load);
new MutationObserver(load).observe(document.querySelector('#app'), { attributes: true, attributeFilter: ['class'] });
})();
