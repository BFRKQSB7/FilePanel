(() => {
'use strict';
const FP = window.FP;
if (!FP) return;
const el = (tag, text) => { const n = document.createElement(tag); if (text) n.textContent = text; return n; };
const box = el('section'); box.className = 'section-card'; box.style.marginTop = '24px'; box.hidden=true; document.querySelector('#page-files').append(box);
const stages = { preflight: '预检与预留', transferring: '传输', verifying: '校验', switching: '提交存储位置', cleaning: '正在清理指定源对象', done: '完成', cancelling: '正在清理临时目标', cancelled: '已取消' };
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
   el('p', '源文件在目标完整校验并切换前持续可用，分享地址和版本保持。优先直传，无法连接时通过面板流式中转；每跳都受用量和空间保护。'), FP.field('目标存储位置', select));
  const preview = el('p'); form.append(preview);
  const describe = () => { const n = state.nodes.find(n => n.id === select.value); preview.textContent = n ? '目标磁盘最后报告：' + (n.report ? FP.format(n.report.free) + ' 可用' : '未知') + '；执行时检查实时安全预留和用量预算。' : '目标为本机；执行时检查实时安全预留和用量预算。'; };
  select.addEventListener('change', describe); describe();
  const requestID = crypto.randomUUID();
  const start = el('button', '确认移动'); start.type = 'button'; start.className = 'primary-btn'; start.disabled = !select.options.length;
  start.addEventListener('click', async () => { start.disabled = true; start.setAttribute('aria-busy','true'); try { await FP.api('/api/files/' + encodeURIComponent(item.id) + '/move', json({ request_id: requestID, target: select.value })); closing(); FP.toast('移动任务已建立，后台预检后执行'); await load(); } catch(e) { FP.toast(e.message); start.disabled = false; start.removeAttribute('aria-busy'); } });
  let closing; const close = el('button', '关闭'); close.type = 'button'; close.className = 'quiet-btn'; close.addEventListener('click', () => closing()); const actions = el('div'); actions.className = 'modal-actions'; actions.append(close, start); form.append(actions);
  closing=FP.openDialog(dialog);
 } catch(e) { FP.toast(e.message); }
};
FP.openStorage=async item=>{
 try{
 const [state,locations]=await Promise.all([FP.api('/api/nodes'),FP.api('/api/files/'+encodeURIComponent(item.id)+'/placements')]);
 if(document.querySelector('dialog[open]'))return;
 const modal=el('dialog');modal.className='modal';modal.setAttribute('aria-label','存储与副本');let busy=false,closing;
 const form=el('div');form.className='setting-form';modal.append(form);
 const label=id=>id?(state.nodes.find(n=>n.id===id)?nodeLabel(state.nodes.find(n=>n.id===id)):'离线或已撤销节点'):'主面板本机存储';
 form.append(el('h3','存储与副本 · '+item.name),el('p','复制会保留原件；新副本完整校验后才可下载。只保留一处时，移除其他副本即可；主位置始终保留。如需换主位置，使用“移动”。下载 / 中转出口在分享设置中独立指定。'));
 for(const loc of locations){const row=el('div');row.className='placement-row';row.append(el('strong',label(loc.node)),el('p',(loc.primary?'主位置':loc.state==='removing'?'正在移除副本':'已校验副本')+' · '+FP.format(loc.size)));
 if(!loc.primary&&loc.state==='active'){const remove=el('button','移除这个副本'),requestID=crypto.randomUUID();remove.type='button';remove.className='danger-btn';remove.onclick=async()=>{if(busy||!confirm('移除“'+label(loc.node)+'”上的副本？主位置和其他副本保留，已有下载完成后清理。'))return;busy=true;remove.disabled=true;try{await FP.api('/api/files/'+encodeURIComponent(item.id)+'/replicas/remove',json({request_id:requestID,target:loc.node}));busy=false;closing();FP.toast('副本清理已安排，进度见存储任务');await load();}catch(e){FP.message(e.message);busy=false;remove.disabled=false;}};row.append(remove);}form.append(row);}
 const select=el('select');select.setAttribute('aria-label','新增副本位置');if(!locations.some(x=>x.node===''))select.add(new Option('主面板本机存储',''));
 for(const n of state.nodes)if(n.storage&&n.online&&!n.revoked&&!locations.some(x=>x.node===n.id))select.add(new Option(nodeLabel(n),n.id));
 form.append(FP.field('新增副本位置',select));let requestID=crypto.randomUUID();select.onchange=()=>{requestID=crypto.randomUUID();};
 const copy=el('button','复制并保留原件');copy.type='button';copy.className='primary-btn';copy.disabled=!select.options.length;
 copy.onclick=async()=>{if(busy)return;busy=true;copy.disabled=true;select.disabled=true;try{await FP.api('/api/files/'+encodeURIComponent(item.id)+'/copy',json({request_id:requestID,target:select.value}));busy=false;closing();FP.toast('复制任务已建立，原件保留');await load();}catch(e){FP.message(e.message);busy=false;copy.disabled=false;select.disabled=false;}};
 const close=el('button','关闭');close.type='button';close.className='quiet-btn';close.onclick=()=>closing();const actions=el('div');actions.className='modal-actions';actions.append(close,copy);form.append(actions);closing=FP.openDialog(modal,{canClose:()=>!busy});
 }catch(e){FP.message(e.message);}
};
async function load() {
 if (!FP.csrf() || polling) return; polling = true;
 try {
  const tasks = await FP.api('/api/moves'); const next = JSON.stringify(tasks); if (signature === next) return; signature = next;
  box.hidden=!tasks.length; box.replaceChildren(el('h3', '文件存储任务'));
  if (!tasks.length) box.append(el('p', '在文件操作中选择“移动”，可在这里查看阶段、路径与重试。'));
  for (const task of tasks) {
   const row = el('div'); row.className = 'setting-form';
   const noTransfer = task.kind === 'move_existing' || task.kind === 'remove';
   const route = task.kind === 'move_existing' ? '复用已有副本（无需传输）' : task.kind === 'remove' ? '副本清理（无需传输）' : task.route === 'direct' ? '直传' : task.route === 'panel' ? '面板中转' : ['done','cancelled'].includes(task.state) ? '传输方式未记录' : '尚未确定传输方式';
   const progress = noTransfer ? '文件大小 ' + FP.format(task.size) : task.route === 'direct' && task.state === 'transferring' ? '传输中，等待目标确认字节数' : FP.format(task.progress) + ' / ' + FP.format(task.size);
   const kind=task.kind==='copy'?'复制':task.kind==='remove'?'移除副本':'移动';
   row.append(el('strong', task.name+' · '+kind), el('p', (stages[task.state] || task.state) + ' · ' + route + ' · ' + progress));
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
