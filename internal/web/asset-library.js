(() => {
'use strict';
const FP=window.FP;if(!FP)return;
const {el,field}=FP, root=document.querySelector('#page-assets');
let page=1,request=0,busy=false,timer,items=[],selection=new Set();
const search=el('input',{type:'search',placeholder:'搜索图片或所属文件','aria-label':'搜索素材'});
const filter=el('select',{'aria-label':'素材使用状态'},new Option('全部素材','all'),new Option('使用中','used'),new Option('未使用','unused'));
const owner=el('select',{'aria-label':'按所属文件筛选'},new Option('全部文件',''));
const summary=el('p',{class:'section-sub',role:'status'}), status=el('p',{role:'status'});
const grid=el('div',{class:'asset-grid'}),paging=el('div',{class:'asset-paging'});
const count=el('span',{class:'section-sub',text:'已选择 0 张'});
const selectAll=el('input',{type:'checkbox','aria-label':'选择本页未使用图片',onchange:()=>{selection.clear();if(selectAll.checked)for(const a of items)if(!a.uses.length)selection.add(a.id);draw();}});
const remove=el('button',{type:'button',class:'danger-btn',text:'删除所选未使用图片',disabled:true,onclick:async()=>{
  if(busy||!selection.size)return;
  const targets=items.filter(a=>selection.has(a.id)&&!a.uses.length);
  if(!confirm('永久删除这 '+targets.length+' 张未使用图片并释放空间？此操作无法撤销。'))return;
  busy=true;toolbar.inert=true;grid.inert=true;paging.inert=true;let deleted=0,failures=[];
  try{
    for(const a of targets){try{await FP.api('/api/files/'+encodeURIComponent(a.file_id)+'/assets/'+encodeURIComponent(a.id),{method:'DELETE'});deleted++;}catch(e){failures.push((a.name||'图片')+'：'+e.message);}}
    selection.clear();await load();await FP.refreshData();
    status.textContent='已删除 '+deleted+' 张图片。'+(failures.length?'未删除 '+failures.length+' 张：'+failures.join('；'):'');
  }finally{busy=false;toolbar.inert=false;grid.inert=false;paging.inert=false;}
}});
const toolbar=el('div',{class:'asset-toolbar'},search,filter,owner,el('button',{type:'button',class:'quiet-btn',text:'刷新素材',onclick:()=>load()}));
root.append(el('div',{class:'heading'},el('div',{},el('div',{class:'eyebrow',text:'YOUR CREATIVE LIBRARY'}),el('h1',{text:'素材库'}),el('p',{text:'让每一张图片，都有清楚的去处。'})),el('button',{type:'button',class:'primary-btn',text:'上传素材',onclick:upload})),
  el('section',{class:'section-card asset-library'},el('div',{class:'section-head'},el('div',{},el('div',{class:'section-title',text:'分享页面图片'}),summary)),toolbar,
  el('p',{class:'asset-help section-sub',text:'使用中包含已保存的封面、正文和截图，即使分享已关闭也会保留引用。图片存于主面板；删除所属文件会一并删除其素材。'}),
  el('div',{class:'asset-selection'},el('label',{class:'asset-check'},selectAll,'选择本页未使用图片'),count,remove),status,grid,paging));

function draw(){
  grid.replaceChildren();
  const unused=items.filter(a=>!a.uses.length);
  selectAll.disabled=!unused.length;selectAll.checked=unused.length>0&&unused.every(a=>selection.has(a.id));selectAll.indeterminate=selection.size>0&&!selectAll.checked;
  count.textContent='已选择 '+selection.size+' 张';remove.disabled=!selection.size;
  for(const a of items){
    const used=a.uses.length>0;
    const checkbox=el('input',{type:'checkbox',checked:selection.has(a.id),disabled:used,'aria-label':'选择 '+(a.name||'图片'),onchange:()=>{checkbox.checked?selection.add(a.id):selection.delete(a.id);draw();}});
    grid.append(el('article',{class:'asset-card'+(selection.has(a.id)?' selected':''),'data-asset-id':a.id},
      el('div',{class:'asset-image'},el('img',{src:'/api/assets/'+encodeURIComponent(a.id),alt:a.name||'图片素材',loading:'lazy'}),checkbox,el('span',{class:'asset-badge'+(used?' used':''),text:used?'使用中':'未使用'})),
      el('div',{class:'asset-card-body'},el('strong',{text:a.name||'图片素材',title:a.name||'图片素材'}),
      el('p',{class:'section-sub',text:FP.format(a.size)+' · '+FP.date(a.created_at)}),
      el('p',{class:'asset-owner',text:'所属文件：'+a.file_name,title:a.file_name}),
      el('p',{class:'section-sub',text:used?a.uses.join('、')+(a.enabled?' · 分享已开启':' · 分享已关闭，引用保留'):'尚未用于分享页面'}),
      el('button',{type:'button',class:'quiet-btn',text:used?'编辑使用位置':'用于分享页面',onclick:()=>FP.openShare({id:a.file_id,name:a.file_name},true)}))));
  }
}
async function load(){
  const ticket=++request;grid.setAttribute('aria-busy','true');status.textContent='正在读取素材…';
  try{
    const q=new URLSearchParams({q:search.value,status:filter.value,file_id:owner.value,page:String(page)});
    const data=await FP.api('/api/assets?'+q);
    if(ticket!==request)return;
    if(page>1&&!data.items.length){page=Math.max(1,Math.ceil(data.matched/24));return load();}
    items=data.items;selection.clear();draw();
    summary.textContent=data.total+' 张图片 · '+FP.format(data.bytes)+' · 使用中 '+data.used+' 张 · 未使用 '+(data.total-data.used)+' 张';
    status.textContent=!data.total?'还没有素材。上传图片后，在分享编辑中选择用途。':!data.matched?'没有匹配的素材，请调整搜索或筛选。':'找到 '+data.matched+' 张素材';
    paging.replaceChildren();
    if(data.matched>24)paging.append(el('button',{class:'quiet-btn',text:'上一页',disabled:page<=1,onclick:()=>{page--;load();}}),el('span',{text:page+' / '+Math.ceil(data.matched/24)}),el('button',{class:'quiet-btn',text:'下一页',disabled:page*24>=data.matched,onclick:()=>{page++;load();}}));
  }catch(e){if(ticket===request){items=[];selection.clear();draw();paging.replaceChildren();status.textContent='读取失败：'+e.message+'。请点击刷新素材重试。';}}
  finally{if(ticket===request)grid.removeAttribute('aria-busy');}
}
async function owners(){
  try{const files=await FP.api('/api/files'),current=owner.value;owner.replaceChildren(new Option('全部文件',''));for(const f of files)owner.add(new Option(f.name,f.id));if(files.some(f=>f.id===current))owner.value=current;}
  catch(e){FP.message(e.message);}
}
async function upload(){
  if(document.querySelector('dialog[open]'))return;
  let files;try{files=await FP.api('/api/files');}catch(e){FP.message(e.message);return;}
  if(!files.length){FP.message('请先在“我的文件”上传一个要分享的文件，再为它添加图片。',false);return;}
  const target=el('select',{'aria-label':'素材所属文件'});for(const f of files)target.add(new Option(f.name,f.id));if(owner.value)target.value=owner.value;
  const picker=el('input',{type:'file',multiple:true,accept:'image/png,image/jpeg,image/gif,image/webp','aria-label':'选择素材图片'});
  const result=el('p',{role:'status'});let uploading=false,close;
  const submit=el('button',{type:'button',class:'primary-btn',text:'上传到素材库',onclick:async()=>{
    if(uploading||!picker.files.length)return;
    const batch=[...picker.files],fileID=target.value;uploading=true;target.disabled=picker.disabled=submit.disabled=true;
    let done=0,failed=[];
    try{for(const f of batch){result.textContent='正在上传 '+f.name;const form=new FormData();form.append('image',f,f.name);try{await FP.api('/api/files/'+encodeURIComponent(fileID)+'/assets',{method:'POST',body:form});done++;}catch(e){failed.push(f.name+'：'+e.message);}}
      result.textContent='已上传 '+done+' 张，尚未用于分享页面。'+failed.join('；');picker.value='';await load();await FP.refreshData();
    }finally{uploading=false;target.disabled=picker.disabled=submit.disabled=false;}
  }});
  const dialog=el('dialog',{class:'modal',role:'dialog','aria-label':'上传素材'},el('h2',{text:'上传素材'}),el('p',{text:'选择图片所属的文件。每个文件最多 8 张，每张不超过 2 MiB；上传成功立即占用空间。'}),field('所属文件',target),field('图片',picker),result,el('div',{class:'modal-actions'},el('button',{class:'quiet-btn',text:'关闭',onclick:()=>close()}),submit));
  close=FP.openDialog(dialog,{canClose:()=>!uploading});target.focus();
}
search.addEventListener('input',()=>{clearTimeout(timer);timer=setTimeout(()=>{page=1;load();},200);});
for(const input of [filter,owner])input.addEventListener('change',()=>{page=1;load();});
document.addEventListener('fp:navigate',e=>{if(e.detail.tab==='assets'){owners().then(load);}});
document.addEventListener('fp:assets',()=>{if(!root.classList.contains('hidden'))load();});
document.querySelector('#refresh-btn').addEventListener('click',()=>{if(!root.classList.contains('hidden'))owners().then(load);});
document.addEventListener('fp:logout',()=>{request++;clearTimeout(timer);items=[];selection.clear();grid.replaceChildren();summary.textContent='';status.textContent='';});
if(!root.classList.contains('hidden'))owners().then(load);
})();
