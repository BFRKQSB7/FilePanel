// Run against an isolated, freshly initialized instance, never a live server.
// Usage: node scripts/smoke-admin.cjs /path/to/filepanel
// Requires Playwright (NODE_PATH may point to an existing installation).
const { chromium } = require('playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');
const net = require('node:net');
const { spawn, spawnSync } = require('node:child_process');
const binary = path.resolve(process.argv[2] || 'filepanel.exe');
const sleep = ms => new Promise(r => setTimeout(r, ms));
async function main() {
 const temp = fs.mkdtempSync(path.join(os.tmpdir(), 'filepanel-admin-test-'));
 const data = path.join(temp,'data');
 const init = spawnSync(binary,['init-token','--data-dir',data],{encoding:'utf8',windowsHide:true});
 assert.equal(init.status,0,init.stderr);
 const token = init.stdout.match(/\n([A-Za-z0-9_-]{30,})\r?\n/)[1];
 const port = await new Promise(resolve => { const s = net.createServer(); s.listen(0,'127.0.0.1',()=>{const p=s.address().port;s.close(()=>resolve(p));}); });
 const base = 'http://127.0.0.1:' + port;
 const server = spawn(binary,['serve','--data-dir',data,'--listen','127.0.0.1:'+port],{windowsHide:true,stdio:'ignore'});
 let browser; let checks = 0;
 const check = (ok, message) => { assert.ok(ok,message); checks++; console.log('PASS ' + message); };
 try {
  let ready=false; for(let i=0;i<60;i++){try{if((await fetch(base+'/api/status')).ok){ready=true;break;}}catch{}await sleep(100);} assert.ok(ready,'server startup');
  browser = await chromium.launch({headless:true,...(process.platform==='win32'?{channel:'msedge'}:{})});
  const context = await browser.newContext({viewport:{width:1440,height:900}});
  const page = await context.newPage(); const errors=[];page.on('pageerror',e=>errors.push(e.message));
  page.setDefaultTimeout(8000);
  await page.goto(base+'/admin');
  for(const [asset,type] of [['/icons.svg','image/svg+xml'],['/components.css','text/css']]) {
   const response=await context.request.get(base+asset);
   check(response.ok()&&response.headers()['content-type'].includes(type),'embedded asset served: '+asset);
  }
  await page.locator('#init-form').waitFor({state:'visible'});
  await page.screenshot({path:path.join(temp,'initialization.png'),fullPage:true,animations:'disabled'});
  for(const width of [375,768,1440,2560]) {
   await page.setViewportSize({width,height:900});
   check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'initialization fits '+width);
  }
  await page.locator('#init-token').fill(token); await page.locator('#init-password').fill('Local-UX-Regression-123'); await page.locator('#init-form button').click();
  await page.locator('#login-password').waitFor({state:'visible'});
  for(const width of [375,768,1440,2560]) {
   await page.setViewportSize({width,height:900});
   check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'login fits '+width);
   await page.screenshot({path:path.join(temp,'login-'+width+'.png'),fullPage:true,animations:'disabled'});
  }
  await page.setViewportSize({width:1440,height:900});
  await page.locator('#login-password').fill('Local-UX-Regression-123');await page.locator('#login-form button').click();await page.locator('#app').waitFor({state:'visible'});
  check(await page.locator('#getting-started').isVisible(),'new instance shows onboarding');
  await page.waitForFunction(()=>document.querySelector('#stat-count').textContent==='0');
  await page.waitForFunction(()=>document.querySelector('#upload-storage')?.closest('.field').hidden);
  check(await page.locator('#upload-storage').isHidden(),'single-server upload hides node selector');
  const files=['one.txt','two.txt'].map(name=>({name,mimeType:'text/plain',buffer:Buffer.from(name)}));
  await page.locator('#file-picker').setInputFiles(files);
  await page.waitForFunction(()=>document.querySelector('#stat-count').textContent==='2');
  check((await page.locator('#upload-results').textContent()).match(/已完成/g)?.length===2,'multi-select uploads all files');
  await page.evaluate(()=>{const dt=new DataTransfer();for(const name of ['drop-one.txt','drop-two.txt'])dt.items.add(new File(['drop'],name));document.querySelector('#drop-zone').dispatchEvent(new DragEvent('drop',{bubbles:true,dataTransfer:dt}));});
  await page.waitForFunction(()=>document.querySelector('#stat-count').textContent==='4');
  check(true,'multi-file drop uploads all files');
  await page.locator('#search').fill('no-match');
  check((await page.locator('#empty-title').textContent()).includes('没有找到匹配'),'search empty state differs from empty storage');
  await page.locator('#clear-search').click();check(await page.locator('#file-rows tr').count()===4,'clear search restores files');
  await page.getByRole('button',{name:'网格视图',exact:true}).click();
  check(await page.locator('#file-view').evaluate(e=>e.classList.contains('is-grid')),'grid view activated');
  await page.reload();await page.locator('#app').waitFor({state:'visible'});await page.waitForFunction(()=>document.querySelectorAll('#file-rows tr').length===4);
  check(await page.getByRole('button',{name:'网格视图',exact:true}).getAttribute('aria-pressed')==='true','view preference survives reload');
  for(const width of [375,768,1440,2560]) {
   await page.setViewportSize({width,height:900});
   check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'grid fits '+width);
  }
  await page.setViewportSize({width:1440,height:900});
  await page.locator('#file-rows tr').first().locator('.file-name').click();await page.keyboard.press('ArrowRight');
  check(await page.locator('#file-rows tr').nth(1).getAttribute('aria-selected')==='true','grid right arrow selects adjacent file');
  await page.keyboard.press('Home');await page.keyboard.press('ArrowDown');
  const columns=await page.locator('#file-rows').evaluate(e=>getComputedStyle(e).gridTemplateColumns.split(' ').length);
  check(await page.locator('#file-rows tr').nth(Math.min(3,columns)).getAttribute('aria-selected')==='true','grid down arrow moves one visual row');
  await page.keyboard.press('/');check(await page.locator('#search').evaluate(e=>e===document.activeElement),'slash focuses file search');
  await page.getByRole('button',{name:'文档',exact:true}).click();check(await page.locator('#file-rows tr').count()===4,'document filter keeps text files');
  await page.locator('#search').fill('drop-');check(await page.locator('#file-rows tr').count()===2,'file type combines with name search');
  await page.getByLabel('分享状态',{exact:true}).selectOption('shared');check(await page.locator('#empty').isVisible(),'share state combines with type and search');
  await page.locator('#clear-search').click();
  check(await page.locator('#file-rows tr').count()===4&&await page.getByRole('button',{name:'全部文件',exact:true}).getAttribute('aria-pressed')==='true','clear filters resets all dimensions');
  await page.getByRole('button',{name:'图片',exact:true}).click();check(await page.locator('#empty').isVisible(),'empty category has recovery action');
  await page.locator('#clear-search').click();await page.getByRole('button',{name:'列表视图',exact:true}).click();
  const rows=page.locator('#file-rows tr');
  await rows.nth(0).locator('.file-name').click();
  check(await page.locator('#file-rows tr.selected').count()===1,'click selects unshared files');
  await rows.nth(2).locator('.file-name').click({modifiers:['Shift']});
  check(await page.locator('#file-rows tr.selected').count()===3,'shift selects contiguous rows');
  await rows.nth(1).locator('.file-name').click({modifiers:['Control']});
  check(await page.locator('#file-rows tr.selected').count()===2,'ctrl toggles selection');
  await rows.nth(0).locator('.file-name').dblclick();
  await page.getByRole('dialog',{name:'文件属性'}).waitFor({state:'visible'});await page.keyboard.press('Escape');
  check(await rows.nth(0).evaluate(e=>e===document.activeElement),'properties closes back to focused file');
  await page.keyboard.press('F2');const renameInput=page.getByRole('textbox',{name:'重命名文件'});await renameInput.waitFor({state:'visible'});
  const oldName=await renameInput.inputValue();await renameInput.fill('cancelled-rename.txt');await renameInput.press('Escape');
  check(await rows.nth(0).locator('.file-name').innerText()===oldName,'Escape cancels inline rename');
  await page.keyboard.press('F2');await renameInput.fill('Explorer-renamed.txt');await renameInput.press('Enter');
  await page.getByText('Explorer-renamed.txt',{exact:true}).waitFor({state:'visible'});
  check(true,'F2 Enter saves inline rename');
  await rows.nth(0).locator('.file-name').click({button:'right'});
  await page.getByRole('menu',{name:'文件操作'}).waitFor({state:'visible'});await page.keyboard.press('ArrowDown');await page.keyboard.press('Escape');
  check(await page.getByRole('menu').count()===0,'context menu supports arrows and Escape');
  await page.keyboard.press('Control+a');check(await page.locator('#file-rows tr.selected').count()===4,'Ctrl+A selects current file list');
  await page.keyboard.press('Escape');check(await page.locator('#file-rows tr.selected').count()===0,'Escape clears selection');
  await page.getByRole('button',{name:'文件名',exact:true}).click();
  check(await page.locator('th[aria-sort="ascending"] [data-sort="name"]').count()===1,'column header sorts ascending');
  await page.getByRole('button',{name:'上传时间',exact:true}).click();await page.getByRole('button',{name:'上传时间',exact:true}).click();
  const sharedBytes=await (await context.request.get(base+await page.locator('#file-rows tr').first().getByRole('link',{name:'下载',exact:true}).getAttribute('href'))).body();
  await page.getByRole('button',{name:'分享',exact:true}).first().click();
  const dialog = page.getByRole('dialog',{name:'分享设置'});
  check(await dialog.locator('details').getAttribute('open')===null,'optional share fields start collapsed');
  check(await page.getByRole('button',{name:'复制分享链接',exact:true}).isDisabled(),'private link cannot be copied as a published link');
  await page.getByRole('combobox',{name:'分享方式',exact:true}).selectOption('link');
  await page.getByRole('button',{name:'保存分享设置'}).click();
  await page.waitForFunction(()=>document.querySelector('#stat-access').textContent.includes('1 个分享'));
  const linkOnlyURL=await page.getByRole('textbox',{name:'稳定分享链接'}).inputValue();
  check((await fetch(linkOnlyURL)).status===200,'link-only share page is accessible anonymously');
  const privateVisitor=await context.newPage();await privateVisitor.goto(linkOnlyURL);
  check(await privateVisitor.locator('form[role=search], a.brand, a[href="/"]').count()===0,'private share has no search or homepage navigation');
  await privateVisitor.screenshot({path:path.join(temp,'private-share.png'),fullPage:true});await privateVisitor.close();
  check(!(await (await fetch(base+'/')).text()).includes(new URL(linkOnlyURL).pathname),'link-only share is absent from homepage');
  const credential=(await fetch(linkOnlyURL+'/get',{redirect:'manual'})).headers.get('location');
  check(!!credential,'link-only share issues a download credential');
  check(Buffer.from(await (await fetch(new URL(credential,base))).arrayBuffer()).equals(sharedBytes),'link-only public download matches file');
  page.once('dialog',d=>d.dismiss());await dialog.getByRole('button',{name:'取消分享',exact:true}).click();
  check(await dialog.isVisible()&&await dialog.getByRole('button',{name:'复制分享链接',exact:true}).isEnabled(),'dismissing cancellation leaves sharing active');
  page.once('dialog',d=>d.accept());await dialog.getByRole('button',{name:'取消分享',exact:true}).click();await dialog.waitFor({state:'hidden'});
  check((await fetch(linkOnlyURL)).status===404,'cancel button closes share page');
  check((await fetch(new URL(credential,base))).status===410,'cancel button revokes previously issued download credential');
  check(await page.locator('#file-rows tr').count()===4,'cancelling sharing preserves files');
  await page.getByRole('button',{name:'分享',exact:true}).first().click();await dialog.waitFor({state:'visible'});
  check(await page.getByRole('combobox',{name:'分享方式',exact:true}).inputValue()==='private','reopened cancelled share shows not shared');
  await page.getByRole('combobox',{name:'分享方式',exact:true}).selectOption('public');
  await page.getByRole('button',{name:'保存分享设置'}).click();
  await page.waitForFunction(()=>document.querySelector('#stat-access').textContent.includes('1 个分享'));
  check((await page.locator('#stat-access').innerText()).includes('3 个未分享'),'overview reflects shared and unshared counts');
  check((await (await fetch(base+'/')).text()).includes(new URL(linkOnlyURL).pathname),'public share appears on homepage');
  check(await page.locator('#file-rows tr').first().getByRole('button',{name:'取消分享',exact:true}).isVisible(),'shared file row exposes cancellation directly');
  check(await page.getByRole('button',{name:'复制分享链接',exact:true}).isEnabled(),'publishing leaves copy action ready');
  const shareURL = await page.getByRole('textbox',{name:'稳定分享链接'}).inputValue();
  await dialog.locator('summary').click();await page.getByLabel('标题（留空使用文件名）').fill('unsaved share title');
  page.once('dialog',d=>d.dismiss());await page.keyboard.press('Escape');
  check(await dialog.isVisible(),'dirty dialog cancellation preserves edits');
  page.once('dialog',d=>d.accept());await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});
  check(true,'Escape confirms and closes dirty dialog');
  await page.getByRole('button',{name:'分享',exact:true}).first().click();await dialog.waitFor({state:'visible'});
  await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});
  check(true,'Escape closes clean dialog');
  const visitor = await context.newPage();await visitor.goto(shareURL);
  check((await visitor.locator('body').innerText()).includes('分享页不因此过期'),'visitor understands temporary download URL lifetime');
  const downloadEvent=visitor.waitForEvent('download');await visitor.getByRole('link',{name:'立即下载'}).click();const download=await downloadEvent;
  check(!await download.failure(),'public download still works');await visitor.close();
  const second=page.locator('#file-rows tr').nth(1);
  await second.getByRole('button',{name:'分享',exact:true}).click();await dialog.waitFor({state:'visible'});
  await page.getByRole('combobox',{name:'分享方式',exact:true}).selectOption('link');await page.getByRole('button',{name:'保存分享设置'}).click();
  await page.waitForFunction(()=>document.querySelector('#stat-access').textContent.includes('2 个分享'));
  const secondURL=await page.getByRole('textbox',{name:'稳定分享链接'}).inputValue();await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});
  page.once('dialog',d=>d.accept());await second.getByRole('button',{name:'取消分享',exact:true}).click();
  await page.waitForFunction(()=>document.querySelector('#stat-access').textContent.includes('1 个分享'));
  check((await fetch(secondURL)).status===404,'file row cancellation closes the selected share');
  await second.getByRole('button',{name:'分享',exact:true}).click();await dialog.waitFor({state:'visible'});
  await page.getByRole('combobox',{name:'分享方式',exact:true}).selectOption('link');await page.getByRole('button',{name:'保存分享设置'}).click();
  await page.waitForFunction(()=>document.querySelector('#stat-access').textContent.includes('2 个分享')&&!document.querySelector('dialog .primary-btn').disabled);await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});
  await page.getByLabel('分享状态',{exact:true}).selectOption('shared');check(await page.locator('#file-rows tr').count()===2,'shared filter excludes unshared files');
  for(const [id,label] of [['batch-link','私密分享'],['batch-public','公开分享']]){
   await page.locator('#select-shared').check();page.once('dialog',d=>d.accept());await page.locator('#'+id).click();
   await page.waitForFunction(label=>document.querySelectorAll('#file-rows tr').length===2&&[...document.querySelectorAll('#file-rows .badge')].every(x=>x.textContent===label)&&!document.querySelector('#file-rows').inert,label);
   check(true,'batch mode: '+label);
  }
  await page.locator('#file-rows tr').first().getByRole('button',{name:'分享',exact:true}).click();await dialog.waitFor({state:'visible'});
  const templateVisitor=await context.newPage();
  for(const layout of ['standard','compact','showcase']){
   await dialog.getByRole('combobox',{name:'下载页面模板',exact:true}).selectOption(layout);await dialog.getByRole('button',{name:'保存分享设置'}).click();
   await page.waitForFunction(()=>!document.querySelector('dialog .primary-btn').disabled&&!document.querySelector('dialog').dataset.dirty);
   await templateVisitor.goto(shareURL);check(await templateVisitor.locator('.layout-'+layout).count()===1,'saved download template: '+layout);
   for(const width of [375,1440]){await templateVisitor.setViewportSize({width,height:900});check(await templateVisitor.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'template '+layout+' fits '+width);await templateVisitor.screenshot({path:path.join(temp,'template-'+layout+'-'+width+'.png'),fullPage:true});}
  }
  await templateVisitor.close();await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});
  await page.locator('#select-shared').check();page.once('dialog',d=>d.accept());await page.locator('#batch-cancel').click();
  await page.waitForFunction(()=>document.querySelector('#stat-access').textContent==='全部未分享'&&!document.querySelector('#file-rows').inert);
  check((await fetch(shareURL)).status===404&&(await fetch(secondURL)).status===404,'batch cancellation revokes all selected pages');
  await page.getByLabel('分享状态',{exact:true}).selectOption('all');check(await page.locator('#file-rows tr').count()===4,'batch cancellation retains all files');
  await page.locator('#file-rows tr').first().getByRole('button',{name:'分享',exact:true}).click();await dialog.waitFor({state:'visible'});
  await dialog.getByRole('combobox',{name:'下载页面模板',exact:true}).selectOption('standard');await dialog.getByRole('combobox',{name:'分享方式',exact:true}).selectOption('public');await dialog.getByRole('button',{name:'保存分享设置'}).click();await page.waitForFunction(()=>!document.querySelector('dialog .primary-btn').disabled&&!document.querySelector('dialog').dataset.dirty);await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});
  const storageRow=page.locator('#file-rows tr').first();await storageRow.locator('summary').click();await storageRow.getByRole('button',{name:'存储与副本',exact:true}).click();
  const storageDialog=page.getByRole('dialog',{name:'存储与副本',exact:true});await storageDialog.waitFor({state:'visible'});
  check((await storageDialog.innerText()).includes('主位置'),'storage dialog distinguishes primary location');
  check(await storageDialog.getByRole('button',{name:'复制并保留原件',exact:true}).isDisabled(),'single-location instance cannot create a duplicate on the same server');
  for(const width of [375,1440]){await page.setViewportSize({width,height:900});check(await storageDialog.evaluate(e=>e.scrollWidth<=e.clientWidth),'storage dialog fits '+width);}
  await page.keyboard.press('Escape');await storageDialog.waitFor({state:'hidden'});
  await page.locator('[data-tab=settings]').click();
  const siteName=page.getByRole('textbox',{name:'站点名',exact:true});await siteName.fill('Draft site');
  await page.locator('[data-tab=files]').click();await page.locator('[data-tab=settings]').click();
  check(await siteName.inputValue()==='Draft site','appearance draft survives tab switch');
  await page.getByRole('button',{name:'保存并应用',exact:true}).click();
  await page.waitForFunction(()=>!document.querySelector('#profile-panel').dataset.dirty);
  const exported=page.waitForEvent('download');await page.getByRole('link',{name:'导出配置包',exact:true}).click();
  const exportFile=await exported;const bytes=fs.readFileSync(await exportFile.path());
  await page.locator('#profile-panel input[accept=".zip,application/zip"]').setInputFiles({name:'profile.zip',mimeType:'application/zip',buffer:bytes});
  const importDialog=page.getByRole('dialog',{name:'确认导入配置包'});await importDialog.waitFor({state:'visible'});
  await page.keyboard.press('Escape');await importDialog.waitFor({state:'hidden'});
  check(await siteName.inputValue()==='Draft site','cancelling import preserves saved appearance');
  await page.locator('#profile-panel input[accept=".zip,application/zip"]').setInputFiles({name:'profile.zip',mimeType:'application/zip',buffer:bytes});
  await importDialog.waitFor({state:'visible'});await page.getByRole('button',{name:'确认并应用',exact:true}).click();await importDialog.waitFor({state:'hidden'});
  check(await siteName.inputValue()==='Draft site','native import dialog applies real exported package');
  const png=Buffer.from('iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAQAAAC1HAwCAAAAC0lEQVR42mP8/x8AAwMCAO+jRZkAAAAASUVORK5CYII=','base64');
  await page.locator('#profile-panel input[type=file]').first().setInputFiles({name:'draft.png',mimeType:'image/png',buffer:png});
  await page.locator('[data-tab=files]').click();await page.locator('[data-tab=settings]').click();
  check((await page.locator('#profile-panel').innerText()).includes('待上传：draft.png'),'selected appearance image survives tab change');
  page.once('dialog',d=>d.accept());await page.getByRole('button',{name:'放弃未保存的外观修改'}).click();
  await page.waitForFunction(()=>!document.querySelector('#profile-panel').innerText.includes('待上传：'));
  await page.locator('[data-settings=access]').click();
  check(await page.locator('#access-form').isVisible(),'address form lives in access settings');
  check(await page.locator('#access-email').isHidden(),'manual address hides automatic certificate options');
  await page.locator('#access-mode').selectOption('auto');check(await page.locator('#access-email').isVisible(),'automatic HTTPS reveals certificate options');
  await page.locator('#access-url').fill('https://draft.example.com');
  await page.locator('[data-settings=appearance]').click();await page.locator('[data-settings=access]').click();
  check(await page.locator('#access-url').inputValue()==='https://draft.example.com','address draft survives section switch');
  await page.locator('[data-settings=protection]').click();
  const quota=page.getByLabel('本周期流量额度（十进制 GB；1 TB = 1000 GB）');await quota.fill('123');
  await page.locator('[data-tab=data]').click();await page.getByRole('button',{name:'设置流量保护',exact:true}).click();
  check(await quota.inputValue()==='123','traffic settings draft survives statistics refresh');
  await page.getByRole('button',{name:'保存保护设置',exact:true}).click();
  await page.waitForFunction(()=>!document.querySelector('#settings-protection [data-dirty]'));
  await page.locator('[data-settings=nodes]').click();
  await page.getByText('添加额外服务器（高级）',{exact:true}).click();
  const endpoint=page.getByLabel('节点连接的面板 HTTPS 地址');await endpoint.fill('https://panel.example.com');
  await page.locator('[data-tab=data]').click();await page.locator('[data-tab=settings]').click();await page.locator('[data-settings=nodes]').click();
  check(await endpoint.inputValue()==='https://panel.example.com','node draft survives data-page refresh');
  await page.locator('[data-settings=maintenance]').click();
  check(await page.getByRole('button',{name:'更新本机面板',exact:true}).isDisabled(),'unavailable maintenance keeps update disabled');
  await page.locator('[data-tab=tutorial]').click();check(await page.locator('#access-form').isHidden(),'help no longer contains active address configuration');
  // Preserve unsaved edits even when a delayed refresh finishes after typing.
  await page.locator('[data-tab=settings]').click();
  let release;const gate=new Promise(r=>release=r);
  await page.route('**/api/profile',async route=>{if(route.request().method()==='GET'){await gate;}await route.continue();});
  await page.locator('[data-tab=files]').click();await page.locator('[data-tab=settings]').click();await siteName.fill('Typed during refresh');release();
  await page.waitForResponse(r=>r.url().endsWith('/api/profile')&&r.request().method()==='GET');
  check(await siteName.inputValue()==='Typed during refresh','late profile response does not overwrite newer input');await page.unroute('**/api/profile');
  await page.getByRole('button',{name:'保存并应用',exact:true}).click();await page.waitForFunction(()=>!document.querySelector('#profile-panel').dataset.dirty);
  let releaseOld;let captured;const oldGate=new Promise(r=>releaseOld=r),capturedGate=new Promise(r=>captured=r);
  await page.route('**/api/profile',async route=>{if(route.request().method()==='GET'){const response=await route.fetch();captured();await oldGate;await route.fulfill({response});}else await route.continue();});
  await page.locator('[data-tab=files]').click();await page.locator('[data-tab=settings]').click();await capturedGate;
  await siteName.fill('Draft site');await page.getByRole('button',{name:'保存并应用',exact:true}).click();await page.waitForFunction(()=>!document.querySelector('#profile-panel').dataset.dirty);
  const oldResponse=page.waitForResponse(r=>r.url().endsWith('/api/profile')&&r.request().method()==='GET');releaseOld();await oldResponse;
  await page.waitForFunction(()=>document.querySelector('#profile-panel input[aria-label="站点名"]').value==='Draft site');
  check(await siteName.inputValue()==='Draft site','late pre-save snapshot cannot undo successful save');await page.unroute('**/api/profile');
  await page.locator('[data-tab=files]').click();
  // One failed upload must not discard the following queued file; retry is explicit.
  let failOnce=true;
  await page.route('**/api/files?storage=*',route=>{if(failOnce){failOnce=false;return route.fulfill({status:503,contentType:'application/json',body:JSON.stringify({error:'模拟上传失败'})});}return route.continue();});
  await page.locator('#file-picker').setInputFiles(['retry.txt','after-failure.txt'].map(name=>({name,mimeType:'text/plain',buffer:Buffer.from(name)})));
  await page.waitForFunction(()=>document.querySelector('#stat-count').textContent==='5');
  check((await page.locator('#upload-results').textContent()).includes('模拟上传失败'),'failed upload remains visible while queue continues');
  await page.getByRole('button',{name:'重试此文件'}).click();await page.waitForFunction(()=>document.querySelector('#stat-count').textContent==='6');
  check(true,'explicit retry uploads only the failed file');await page.unroute('**/api/files?storage=*');
  await page.getByRole('button',{name:'分享',exact:true}).first().click();await dialog.waitFor({state:'visible'});await dialog.locator('summary').click();
  await dialog.locator('input[type=file]').setInputFiles({name:'invalid.png',mimeType:'image/png',buffer:Buffer.from('invalid image')});
  await dialog.locator('[role=alert]').waitFor({state:'visible'});
  check(await page.getByRole('button',{name:'保存分享设置'}).isEnabled(),'image failure is visible inside modal and releases save button');
  page.once('dialog',d=>d.accept());await page.keyboard.press('Escape');await dialog.waitFor({state:'hidden'});
  for(const width of [375,768,1440,2560]) {
   await page.setViewportSize({width,height:900});
   check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'no page overflow at '+width);
   check(await page.locator('[data-tab=data]').innerText().then(s=>s.includes('用量与状态')),'navigation labels remain visible at '+width);
   check(await page.locator('.table-wrap').evaluate(e=>e.scrollWidth<=e.clientWidth+1),'file actions need no horizontal scroll at '+width);
  }
  await page.setViewportSize({width:375,height:812});
  await page.screenshot({path:path.join(temp,'mobile.png'),fullPage:true,animations:'disabled'});
  await page.getByRole('button',{name:'分享',exact:true}).first().click();await dialog.waitFor({state:'visible'});await page.keyboard.press('Tab');
  check(await page.evaluate(()=>!!document.activeElement.closest('dialog')),'native modal focuses its own controls');
  await page.keyboard.press('Escape');
  for(const section of ['appearance','access','protection','nodes','account','maintenance']){
   await page.locator('[data-tab=settings]').click();await page.locator('[data-settings='+section+']').click();
   check(await page.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'settings fit phone width: '+section);
  }
  // Fresh navigation (not a tab click) must initialize the selected section.
  page.on('dialog',d=>d.accept());await page.goto(base+'/admin#settings-appearance');await page.reload();
  await page.getByRole('textbox',{name:'站点名',exact:true}).waitFor({state:'visible'});
  check(await page.getByRole('textbox',{name:'站点名',exact:true}).inputValue()==='Draft site','deep link reload loads saved section');

  // Visual contract: real browser geometry, native keyboard interaction, and screenshots.
  // Node data below is a presentation fixture; it does not exercise remote transfers.
  await page.route('**/api/nodes', async route => {
   const response = await route.fetch(); const state = await response.json();
   state.nodes = [{id:'visual-node',endpoint:'https://[2001:db8:1234:5678:abcd:ef01:2345:6789]:8443',public_ip:'2001:db8:1234:5678:abcd:ef01:2345:6789',note:'长备注用于验证断行与布局。'.repeat(12),region:'测试地区',storage:true,relay:true,online:true,data_ready:true,policy:{quota_bytes:100e9,protect:true,basis:'sent'},report:null}];
   await route.fulfill({response,json:state});
  });
  await page.locator('[data-tab=files]').click(); await page.locator('#refresh-btn').click();
  await page.locator('#upload-storage').waitFor({state:'visible'});
  const longName='长期归档-'.repeat(18)+'2026.zip';
  await page.locator('#upload-storage').selectOption('');
  await page.locator('#file-picker').setInputFiles({name:longName,mimeType:'application/zip',buffer:Buffer.from('visual test')});
  await page.waitForFunction(name=>[...document.querySelectorAll('.file-name')].some(e=>e.textContent===name),longName);
  // Native select retains Arrow key behavior, no custom listbox or event emulation.
  await page.locator('#upload-storage').focus(); await page.keyboard.press('ArrowDown'); await page.keyboard.press('Enter');
  check(await page.locator('#upload-storage').inputValue()==='visual-node','native select supports keyboard selection');
  await page.locator('#upload-storage').selectOption('');
  await page.getByRole('button',{name:'分享',exact:true}).first().click(); await dialog.waitFor({state:'visible'});
  for(let i=0;i<24;i++) { await page.keyboard.press('Tab'); assert.ok(await page.evaluate(()=>document.activeElement.closest('dialog')),'dialog traps Tab'); }
  for(let i=0;i<24;i++) { await page.keyboard.press('Shift+Tab'); assert.ok(await page.evaluate(()=>document.activeElement.closest('dialog')),'dialog traps Shift+Tab'); }
  check(true,'dialog traps keyboard focus in both directions');
  await page.keyboard.press('Escape');
  check(await page.evaluate(()=>document.activeElement.dataset.focusKey?.startsWith('share-')),'Escape restores focus to originating share action');
  const more=page.locator('#file-rows summary').first();await more.focus();await page.keyboard.press('Enter');
  check(await more.evaluate(e=>e.parentElement.open),'more actions open with Enter');
  await page.keyboard.press('Tab');await page.keyboard.press('Escape');
  check(await more.evaluate(e=>!e.parentElement.open && e===document.activeElement),'more actions close with Escape and restore focus');

  const inspect = async name => {
   const problems=await page.evaluate(()=>{
    const visible=e=>e.getClientRects().length && getComputedStyle(e).visibility!=='hidden';
    const bad=[];
    for(const row of document.querySelectorAll('#file-rows tr'))if(visible(row)){
     const actions=[...row.querySelectorAll('.row-actions > a,.row-actions > button,.row-actions > details > summary')].slice(0,3);
     const boxes=actions.map(e=>e.getBoundingClientRect()),texts=actions.map(e=>{const r=document.createRange();r.selectNodeContents(e);return r.getBoundingClientRect();});
     if(actions.length!==3||Math.max(...boxes.map(r=>r.top))-Math.min(...boxes.map(r=>r.top))>1||Math.max(...texts.map(r=>r.top))-Math.min(...texts.map(r=>r.top))>1)bad.push('file action text alignment');
     if(actions.map(e=>e.textContent).join('|')!=='下载|分享|更多')bad.push('inconsistent primary action order');
    }
    if(document.documentElement.scrollWidth>innerWidth)bad.push('page overflow');
    for(const e of document.querySelectorAll('[hidden],.hidden'))if(visible(e))bad.push('hidden control is visible: '+(e.id||e.tagName));
    for(const e of document.querySelectorAll('select'))if(visible(e)){
     const s=getComputedStyle(e);
     if(e.getBoundingClientRect().height<44 || parseFloat(s.paddingRight)<36 || s.backgroundImage==='none')bad.push('unstyled select: '+(e.id||e.getAttribute('aria-label')));
     if(!e.labels.length && !e.getAttribute('aria-label'))bad.push('unlabelled select');
    }
    for(const e of document.querySelectorAll('button,a.quiet-btn,a.ghost-btn,input:not([type=checkbox]):not([type=radio]),select,summary'))if(visible(e) && e.getBoundingClientRect().height<43)bad.push('small target: '+(e.id||e.textContent.slice(0,30)));
    return bad;
   });
   check(problems.length===0,name+': '+(problems.join('; ')||'controls, labels and layout'));
  };
  for(const width of [375,768,1440,2560]) {
   await page.setViewportSize({width,height:1000});
   for(const tab of ['files','data','tutorial']) {
    await page.locator('[data-tab='+tab+']').click();
    if(tab==='data')await page.getByRole('heading',{name:'节点磁盘与独立用量'}).waitFor();
    await inspect(tab+' '+width);
    if(tab==='files')await page.locator('.upload-log summary').evaluate(e=>e.parentElement.open=false);
    await page.evaluate(()=>window.scrollTo(0,0));
    await page.screenshot({path:path.join(temp,tab+'-'+width+'.png'),fullPage:true,animations:'disabled'});
   }
   await page.locator('[data-tab=settings]').click();
   for(const section of ['appearance','access','protection','nodes','account','maintenance']) {
    await page.locator('[data-settings='+section+']').click();
    await page.locator('#settings-'+section+' input:visible, #settings-'+section+' button:visible').first().waitFor();
    await inspect(section+' '+width);
    await page.evaluate(()=>window.scrollTo(0,0));
    await page.screenshot({path:path.join(temp,section+'-'+width+'.png'),fullPage:true,animations:'disabled'});
   }
   await page.locator('[data-tab=files]').click();
   await page.getByRole('button',{name:'分享',exact:true}).first().click();await dialog.waitFor({state:'visible'});
   await dialog.locator('summary').click();await inspect('share dialog '+width);
   check(await page.getByRole('button',{name:'保存分享设置'}).evaluate(e=>{const r=e.getBoundingClientRect(),d=e.closest('dialog').getBoundingClientRect();return r.top>=d.top&&r.bottom<=d.bottom&&r.bottom<=innerHeight;}),'share save remains visible '+width);
   await page.screenshot({path:path.join(temp,'dialog-'+width+'.png'),fullPage:false,animations:'disabled'});await page.keyboard.press('Escape');
   await more.click();await page.getByRole('button',{name:'移动',exact:true}).first().click();
   await page.getByRole('dialog',{name:'移动文件'}).waitFor();await inspect('move dialog '+width);await page.keyboard.press('Escape');
  }
  await page.emulateMedia({reducedMotion:'reduce'});
  await page.locator('#refresh-btn').focus(); await page.keyboard.press('Tab'); await page.keyboard.press('Shift+Tab');
  check(await page.locator('#refresh-btn').evaluate(e=>getComputedStyle(e).transitionDuration.split(',').every(t=>parseFloat(t)===0) && getComputedStyle(e).outlineStyle!=='none'),'reduced motion and keyboard focus remain visible');
  await page.emulateMedia({reducedMotion:'no-preference'});
  await page.locator('[data-tab=files]').click();await page.setViewportSize({width:1440,height:1000});
  await page.emulateMedia({colorScheme:'dark'});
  check(await page.evaluate(()=>getComputedStyle(document.documentElement).colorScheme==='dark'),'admin follows dark color scheme');
  await page.screenshot({path:path.join(temp,'files-dark-1440.png'),fullPage:true,animations:'disabled'});
  await page.locator('#file-rows tr').first().locator('.file-name').click();
  await page.keyboard.press('Shift+F10');await page.getByRole('menu',{name:'文件操作'}).waitFor({state:'visible'});
  check(await page.getByRole('menu').evaluate(e=>{const r=e.getBoundingClientRect();return r.left>=0&&r.right<=innerWidth&&r.top>=0&&r.bottom<=innerHeight;}),'keyboard context menu stays inside viewport');
  await page.screenshot({path:path.join(temp,'explorer-menu.png'),fullPage:false,animations:'disabled'});await page.keyboard.press('Escape');await page.keyboard.press('Escape');
  await page.emulateMedia({colorScheme:'light'});
  const publicPage=await context.newPage();
  for(const scheme of ['light','dark']) {
   await publicPage.emulateMedia({colorScheme:scheme});
   for(const width of [375,768,1440,2560]) {
    await publicPage.setViewportSize({width,height:900});
    for(const [name,url] of [['public',base+'/'],['detail',shareURL]]) {
     await publicPage.goto(url);
     check(await publicPage.evaluate(()=>document.documentElement.scrollWidth<=innerWidth),'public '+name+' '+scheme+' '+width+' fits');
     if(width===375||width===1440)await publicPage.screenshot({path:path.join(temp,name+'-'+scheme+'-'+width+'.png'),fullPage:true,animations:'disabled'});
    }
   }
  }
  await publicPage.close();

  await page.locator('[data-tab=files]').click();await page.getByLabel('分享状态',{exact:true}).selectOption('all');
  const beforeTypes=await page.locator('#file-rows tr').count();
  const typeNames=['preview-photo.PNG','preview-archive.tar.gz','preview-audio.mp3','png','preview.blob'];
  await page.locator('#file-picker').setInputFiles(typeNames.map(name=>({name,mimeType:'application/octet-stream',buffer:Buffer.from('Extension classification fixture')})));
  await page.waitForFunction(count=>Number(document.querySelector('#stat-count').textContent)===count,beforeTypes+typeNames.length);
  for(const [kind,name] of [['图片','preview-photo.PNG'],['压缩包','preview-archive.tar.gz'],['影音','preview-audio.mp3']]) {
   await page.getByRole('button',{name:kind,exact:true}).click();
   const matching=await page.locator('#file-rows .file-name').allTextContents();
   check(matching.length===(kind==='压缩包'?2:1)&&matching.includes(name),'extension filter: '+kind);
  }
  await page.getByRole('button',{name:'其他',exact:true}).click();
  const others=await page.locator('#file-rows .file-name').allTextContents();
  check(others.includes('png')&&others.includes('preview.blob'),'extensionless and unknown files remain in other');
  await page.getByRole('button',{name:'全部文件',exact:true}).click();await page.getByRole('button',{name:'网格视图',exact:true}).click();
  for(const scheme of ['light','dark']) {
   await page.emulateMedia({colorScheme:scheme});
   for(const width of [375,1440]) {
    await page.setViewportSize({width,height:1000});await inspect('grid '+scheme+' '+width);
    await page.screenshot({path:path.join(temp,'grid-'+scheme+'-'+width+'.png'),fullPage:true,animations:'disabled'});
   }
  }

  await page.locator('#file-rows tr').first().getByRole('button',{name:'分享',exact:true}).click();await dialog.waitFor({state:'visible'});
  await dialog.getByText('编辑介绍与图片（可选）',{exact:true}).click();
  check(await dialog.getByText('图片上传后立即保存并占用空间，关闭窗口或取消分享不会删除；取消分享后仅管理员可访问。可在下方图片列表中删除素材。其他编辑需点击保存。',{exact:true}).isVisible(),'image storage behavior explained next to upload');
  await page.keyboard.press('Escape');
  // Historical and no-transfer operations must not look unfinished or claim bytes sent.
  const taskFixtures=[
   {id:'reuse',name:'route-reuse-fixture',kind:'move_existing',state:'done',route:'',size:610,progress:610},
   {id:'remove',name:'route-remove-fixture',kind:'remove',state:'done',route:'',size:1024,progress:0},
   {id:'direct',name:'route-direct-fixture',kind:'copy',state:'done',route:'direct',size:610,progress:610},
   {id:'panel',name:'route-panel-fixture',kind:'move',state:'transferring',route:'panel',size:610,progress:128},
   {id:'pending',name:'route-pending-fixture',kind:'move',state:'preflight',route:'',size:610,progress:0},
   {id:'history',name:'route-history-fixture',kind:'move',state:'done',route:'',size:610,progress:610},
   {id:'cancelled',name:'route-cancelled-fixture',kind:'move',state:'cancelled',route:'',size:610,progress:0}
  ];
  await page.route('**/api/moves',route=>route.fulfill({json:taskFixtures}));await page.locator('#refresh-btn').click();
  await page.getByText('route-reuse-fixture · 移动',{exact:true}).waitFor({state:'visible'});
  for(const [name,expected] of [['reuse','完成 · 复用已有副本（无需传输） · 文件大小 610 B'],['remove','完成 · 副本清理（无需传输） · 文件大小 1.0 KiB'],['direct','完成 · 直传 · 610 B / 610 B'],['panel','传输 · 面板中转 · 128 B / 610 B'],['pending','预检与预留 · 尚未确定传输方式'],['history','完成 · 传输方式未记录'],['cancelled','已取消 · 传输方式未记录']]){
   const text=await page.locator('.setting-form').filter({hasText:'route-'+name+'-fixture'}).innerText();
   check(text.includes(expected)&&!text.includes('待检查'),'storage task label: '+name);
  }
  await page.unroute('**/api/moves');
  check(errors.length===0,'no browser runtime errors: '+errors.join('; '));
  console.log(JSON.stringify({checks,artifacts:temp}));
 } finally { if(browser)await browser.close();server.kill();await new Promise(r=>{if(server.exitCode!==null)r();else server.once('exit',r);}); }
}
main().catch(e=>{console.error(e);process.exitCode=1;});
