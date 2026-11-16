'use strict';
// Real Chromium DOM checks with test-only native fixtures; no real login, downloads or launches.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const fs = require('node:fs/promises');
const path = require('node:path');
const os = require('node:os');
const { pathToFileURL } = require('node:url');
const delay = ms => new Promise(r => setTimeout(r, ms));
const chrome = process.env.CHROME_BIN;
test('live UI with fake native bindings: preview, actions, secrets, auth cadence, EULA and operation guards', { skip: !chrome, timeout: 90000 }, async t => {
  const profile = await fs.mkdtemp(path.join(process.env.PI_SCRATCH_DIR || os.tmpdir(), 'neo-live-browser-'));
  const child = spawn(chrome, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--remote-debugging-port=0', `--user-data-dir=${profile}`, 'about:blank'], { stdio: 'ignore' });
  let socket;
  t.after(async () => { socket?.close(); child.kill(); await delay(700); await fs.rm(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }).catch(() => {}); });
  let port;
  for (let i = 0; i < 100; i++) { try { port = (await fs.readFile(path.join(profile, 'DevToolsActivePort'), 'utf8')).split('\n')[0]; break; } catch { await delay(100); } }
  assert.ok(port);
  const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  socket = new WebSocket(targets.find(t => t.type === 'page').webSocketDebuggerUrl);
  await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject; });
  const pending = new Map(), exceptions = []; let sequence = 0;
  socket.onmessage = event => { const reply = JSON.parse(event.data); if (reply.method === 'Runtime.exceptionThrown') exceptions.push(reply.params.exceptionDetails.exception?.description || reply.params.exceptionDetails.text); const p = pending.get(reply.id); if (p) { pending.delete(reply.id); clearTimeout(p.timer); reply.error ? p.reject(new Error(reply.error.message)) : p.resolve(reply.result); } };
  function cdp(method, params = {}) { return new Promise((resolve, reject) => { const id = ++sequence; const timer = setTimeout(() => reject(new Error('CDP timeout ' + method)), 5000); pending.set(id, { resolve, reject, timer }); socket.send(JSON.stringify({ id, method, params })); }); }
  async function ev(expression) { const r = await cdp('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true }); if (r.exceptionDetails) throw new Error(r.exceptionDetails.exception?.description || r.exceptionDetails.text); return r.result.value; }
  const el = id => `document.getElementById(${JSON.stringify(id)})`;
  const query = selector => `document.querySelector(${JSON.stringify(selector)})`;
  const click = id => ev(`${el(id)}.click()`);
  const qclick = selector => ev(`${query(selector)}.click()`);
  const input = (id, value) => ev(`${el(id)}.value=${JSON.stringify(value)}; ${el(id)}.dispatchEvent(new Event('change',{bubbles:true}))`);
  const view = name => qclick(`[data-view="${name}"]`);
  async function until(expression) { for (let i = 0; i < 150; i++) { if (await ev(expression)) return; await delay(40); } assert.fail('Timed out: ' + expression + '\n' + await ev(`document.body.innerText`)); }
  async function key(key, modifiers = 0) { for (const type of ['keyDown', 'keyUp']) await cdp('Input.dispatchKeyEvent', { type, key, code: key, windowsVirtualKeyCode: { Escape: 27, Tab: 9, Enter: 13 }[key], modifiers }); }
  const closed = () => until(`!${el('action-dialog')}.open && ${el('dialog-body')}.childElementCount === 0`);
  const idle = () => until(`!${el('add-server')}.disabled`);
  const act = (action, id) => `[data-action="${action}"][data-id="${id}"]`;
  async function layout(width) { await cdp('Emulation.setDeviceMetricsOverride', { width, height: 900, deviceScaleFactor: 1, mobile: false }); await delay(260); const sizes = await ev(`({body:document.documentElement.scrollWidth,viewport:innerWidth,main:${el('main-content')}.scrollWidth,client:${el('main-content')}.clientWidth})`); assert.ok(sizes.body <= sizes.viewport && sizes.main <= sizes.client + 1, JSON.stringify(sizes)); }
  await cdp('Page.enable'); await cdp('Runtime.enable');
  const url = pathToFileURL(path.join(__dirname, '../internal/app/frontend/index.html')).href;
  await cdp('Page.navigate', { url });
  await until(`${el('enter-preview')} && !${el('enter-preview')}.hidden`);
  assert.equal(await ev(`${el('browser-banner')}.hidden`), true);
  await click('enter-preview'); await until(`!${el('browser-banner')}.hidden`);
  for (const id of ['sign-in', 'add-local-account', 'add-instance', 'add-server', 'save-settings', 'search-content', 'save-key']) assert.equal(await ev(`${el(id)}.disabled`), true, id);
  for (const width of [1280, 900, 680, 390, 320]) for (const name of ['play', 'servers', 'content', 'settings']) { await view(name); await layout(width); }
  await cdp('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-reduced-motion', value: 'reduce' }] });
  await view('play'); assert.equal(await ev(`getComputedStyle(${query('.water-flow')}).animationName`), 'none');
  await cdp('Emulation.setEmulatedMedia', { features: [] });
  assert.equal(await ev(`getComputedStyle(${query('.water-flow')}).animationName`), 'water-fall');
  await layout(1280);
  // Test-only bindings: fixture calls are not an application browser fallback.
  await cdp('Page.addScriptToEvaluateOnNewDocument', { source: `
    window.__calls=[]; window.__authStatus='pending'; window.__authInterval=1; window.__fail='';
    const copy=x=>JSON.parse(JSON.stringify(x));
    const p=(id,installed,loader='fabric')=>({id,name:id==='client'?'Test <img src=x> world':id,version:id==='legacy'?'':'1.21.4',loader,directory:'C:/test/'+id,installed,port:25565,memory_mb:2048});
    window.__state={schema_version:1,data_dir:'C:/test/'+'long-path-'.repeat(40),settings:{java_path:'',client_memory_mb:4096,curseforge_enabled:false},curseforge_configured:false,instances:[p('client',true),p('fresh',false),p('legacy',false)],servers:[p('server',true)],accounts:[],selected_account_id:'',operations:[],processes:[],next_server_port:25566};
    __state.local_accounts=[]; __state.selected_account_kind='microsoft'; __state.selected_local_account_id='';
    const app={}; const record=(m,a)=>{__calls.push({m,a:m==='SetCurseForgeKey'?['masked']:a,time:Date.now()}); if(__fail===m){__fail='';throw new Error('Test failure '+m+(m==='SetCurseForgeKey'?' SECRET-FIXTURE':''));}};
    app.GetState=async()=>{record('GetState',[]);return copy(__state)};
    app.GetMinecraftVersions=async()=>{record('GetMinecraftVersions',[]);return [{id:'1.21.4',type:'release'}]};
    for(const m of ['CreateInstance','CreateServer']) app[m]=async input=>{record(m,[input]);__state[m==='CreateServer'?'servers':'instances'].push({...p('created-'+__calls.length,false,input.loader),...input});return copy(__state)};
    app.SetProfileVersion=async(kind,id,version,loader)=>{record('SetProfileVersion',[kind,id,version,loader]);Object.assign(__state[kind==='server'?'servers':'instances'].find(p=>p.id===id),{version,loader});return copy(__state)};
    app.InstallProfile=async(kind,id)=>{record('InstallProfile',[kind,id]);const op={id:'op-'+id,target_id:id,kind,label:'Install '+id,state:'running',progress:{stage:'Downloading'}};__state.operations.push(op);return copy(op)};
    app.CancelOperation=async id=>{record('CancelOperation',[id]);__state.operations.find(o=>o.id===id).state='cancelled'};
    for(const [m,kind,status] of [['StartServer','server','running'],['StopServer','server','stopped'],['StartClient','client','running'],['StopClient','client','stopped']])app[m]=async(...args)=>{record(m,args);__state.processes=__state.processes.filter(p=>p.id!==args[0]);__state.processes.push({id:args[0],kind,status,exit_code:0});return copy(__state)};
    app.BeginMicrosoftLogin=async()=>{record('BeginMicrosoftLogin',[]);return {flow_id:'flow',user_code:'DEMO-CODE',verification_uri:'https://www.microsoft.com/link',expires_at:new Date(Date.now()+60000).toISOString(),poll_interval_sec:__authInterval}};
    app.PollMicrosoftLogin=async id=>{record('PollMicrosoftLogin',[id]);if(__authStatus==='complete'){__state.accounts=[{id:'account',name:'Test player',uuid:'uuid'}];__state.selected_account_id='account';__state.selected_account_kind='microsoft'}return {status:__authStatus,poll_interval_sec:__authStatus==='slow_down'?2:__authInterval}};
    app.CancelMicrosoftLogin=async id=>record('CancelMicrosoftLogin',[id]);
    app.SelectAccount=async id=>{record('SelectAccount',[id]);__state.selected_account_id=id;__state.selected_account_kind='microsoft';return copy(__state)};
    app.RemoveAccount=async id=>{record('RemoveAccount',[id]);__state.accounts=[];__state.selected_account_id='';return copy(__state)};
    app.CreateLocalAccount=async name=>{record('CreateLocalAccount',[name]);__state.local_accounts.push({id:'local',name,uuid:'local-uuid'});__state.selected_local_account_id='local';__state.selected_account_kind='local';return copy(__state)};
    app.SelectLocalAccount=async id=>{record('SelectLocalAccount',[id]);__state.selected_local_account_id=id;__state.selected_account_kind='local';return copy(__state)};
    app.RemoveLocalAccount=async id=>{record('RemoveLocalAccount',[id]);__state.local_accounts=__state.local_accounts.filter(a=>a.id!==id);if(__state.selected_local_account_id===id)__state.selected_local_account_id='';return copy(__state)};
    app.SaveSettings=async input=>{record('SaveSettings',[input]);__state.settings=input;return copy(__state)};
    app.SetCurseForgeKey=async key=>{record('SetCurseForgeKey',[key]);__state.curseforge_configured=!!key;if(!key)__state.settings.curseforge_enabled=false;return copy(__state)};
    app.ChooseJava=async()=>{record('ChooseJava',[]);__state.settings.java_path='C:/Java/bin/java.exe';return copy(__state)};
    for(const m of ['OpenDataFolder','OpenInstanceFolder','OpenServerFolder','OpenExternal','SendServerCommand'])app[m]=async(...a)=>record(m,a);
    app.GetLogs=async(...a)=>{record('GetLogs',a);return ['Test log <img src=x onerror=alert(1)>','x'.repeat(600)]};
    app.SearchContent=async input=>{record('SearchContent',[input]);return [{provider:input.provider,id:'project',name:'Test <img src=x> pack',description:'Description '+ 'long'.repeat(100),kind:input.kind,icon_url:'https://evil.test/icon.png'}]};
    app.GetContentVersions=async input=>{record('GetContentVersions',[input]);return [{id:'release',name:'Compatible test release'}]};
    app.InstallContent=async input=>{record('InstallContent',[input]);const op={id:'content-'+__calls.length,target_id:input.target_id||'new-pack',kind:input.kind,label:'Install content',state:'running',progress:{}};__state.operations.push(op);return copy(op)};
    window.go={app:{App:app}};
  ` });
  await cdp('Page.navigate', { url }); await idle();
  assert.equal(await ev(`${el('browser-banner')}.hidden`), true);
  assert.equal(await ev(`${query(act('start', 'client'))}.disabled`), true, 'no account means no Play');
  assert.equal(await ev(`${query(act('install', 'legacy'))}.disabled`), true, 'old blank versions need repair');
  assert.equal(await ev(`${el('instance-list')}.querySelectorAll('img').length`), 0);
  // No idle state polling.
  const count = await ev(`__calls.filter(c=>c.m==='GetState').length`); await delay(1650); assert.equal(await ev(`__calls.filter(c=>c.m==='GetState').length`), count);
  // Local play needs no Microsoft account or authentication calls (all launches remain fixtures).
  await click('add-local-account'); await until(`!!${el('local-account-name')}`);
  assert.equal(await ev(`document.activeElement.id`), 'local-account-name');
  assert.match(await ev(`${el('dialog-body')}.textContent`), /not a game profile/);
  for (const name of ['', 'bad name', 'é', 'a'.repeat(17)]) { await input('local-account-name', name); assert.equal(await ev(`${query('[data-submit]')}.disabled`), true); }
  assert.equal(await ev(`__calls.filter(c=>c.m==='CreateLocalAccount').length`), 0);
  await input('local-account-name', 'Sky_Builder'); await qclick('[data-submit]'); await closed(); await idle();
  assert.deepEqual(await ev(`__calls.find(c=>c.m==='CreateLocalAccount').a`), ['Sky_Builder']);
  assert.equal(await ev(`${query(act('start', 'client'))}.disabled`), false);
  assert.match(await ev(`${query('.account-row.selected')}.textContent`), /Local · Offline/);
  await qclick(act('start', 'client')); await until(`!!${query(act('stop', 'client'))}`);
  assert.equal(await ev(`__calls.filter(c=>/MicrosoftLogin/.test(c.m)).length`), 0);
  assert.equal(await ev(`${query(act('stop', 'client'))}.querySelector('use').getAttribute('href')`), '#i-stop');
  await qclick(act('stop', 'client')); await qclick('[data-submit]'); await closed(); await idle();
  for (const width of [680, 320]) await layout(width); await layout(1280);
  await click('sign-in'); await until(`!!${query('.device-code')}`); assert.equal(await ev(`${query('.device-code')}.textContent`), 'DEMO-CODE');
  await delay(400); assert.equal(await ev(`__calls.filter(c=>c.m==='PollMicrosoftLogin').length`), 0);
  await ev(`__authStatus='slow_down'`); await until(`__calls.some(c=>c.m==='PollMicrosoftLogin')`);
  await delay(1100); assert.equal(await ev(`__calls.filter(c=>c.m==='PollMicrosoftLogin').length`), 1, 'slow-down interval honored');
  await key('Escape'); await closed(); await until(`__calls.some(c=>c.m==='CancelMicrosoftLogin')`);
  const polls = await ev(`__calls.filter(c=>c.m==='PollMicrosoftLogin').length`); await delay(1100); assert.equal(await ev(`__calls.filter(c=>c.m==='PollMicrosoftLogin').length`), polls);
  await ev(`__authStatus='denied'`); await click('sign-in'); await until(`!!${el('auth-status')} && ${el('auth-status')}.textContent.includes('denied')`);
  assert.equal(await ev(`${el('account-list')}.textContent.includes('Test player')`), false, 'denied fixture flow cannot create an account');
  await key('Escape'); await closed();
  await ev(`__authStatus='complete'`); await click('sign-in'); await until(`${el('action-dialog')}.open`); await closed(); // simulated completion, not real Microsoft authentication
  await until(`${el('account-list')}.textContent.includes('Test player')`);
  await qclick(act('start', 'client')); await until(`!!${query(act('stop', 'client'))}`);
  await qclick(act('stop', 'client')); await until(`${el('action-dialog')}.open`);
  assert.match(await ev(`${el('dialog-body')}.textContent`), /unsaved progress/); assert.equal(await ev(`__calls.filter(c=>c.m==='StopClient').length`), 1);
  await qclick('[data-submit]'); await closed(); await idle();
  // Same lists coexist; only the explicit kind is selected. Microsoft failure never chooses local.
  assert.equal(await ev(`${query('.account-row.selected .account-kind')}.textContent`), 'Microsoft');
  await ev(`__fail='StartClient'`); await qclick(act('start', 'client')); await idle();
  assert.match(await ev(`${el('global-error')}.textContent`), /Test failure StartClient/);
  assert.equal(await ev(`__state.selected_account_kind`), 'microsoft');
  assert.equal(await ev(`__calls.filter(c=>c.m==='SelectLocalAccount').length`), 0);
  assert.equal(await ev(`${query(act('stop', 'client'))} === null`), true);
  await qclick(act('select-local-account', 'local')); await idle();
  assert.equal(await ev(`__state.selected_account_kind`), 'local');
  assert.match(await ev(`${query('.account-row.selected')}.textContent`), /Sky_Builder/);
  await qclick(act('select-account', 'account')); await idle();
  assert.equal(await ev(`__state.selected_account_kind`), 'microsoft');
  // Safe native errors are visible as text; retries are always explicit.
  await ev(`__fail='BeginMicrosoftLogin'`); await click('sign-in'); await until(`!${el('dialog-error')}.hidden`);
  assert.match(await ev(`${el('dialog-error')}.textContent`), /Test failure BeginMicrosoftLogin/);
  for (const hint of ['fresh code', 'personal Microsoft account', 'private browser window', 'try later']) assert.ok((await ev(`${el('dialog-body')}.textContent`)).includes(hint));
  const begins = await ev(`__calls.filter(c=>c.m==='BeginMicrosoftLogin').length`); await delay(1100);
  assert.equal(await ev(`__calls.filter(c=>c.m==='BeginMicrosoftLogin').length`), begins);
  await key('Escape'); await closed();
  await ev(`__authStatus='pending';__fail='PollMicrosoftLogin'`); await click('sign-in'); await until(`!${el('dialog-error')}.hidden`);
  assert.match(await ev(`${el('dialog-error')}.textContent`), /Test failure PollMicrosoftLogin/);
  const failedPolls = await ev(`__calls.filter(c=>c.m==='PollMicrosoftLogin').length`); await delay(1100);
  assert.equal(await ev(`__calls.filter(c=>c.m==='PollMicrosoftLogin').length`), failedPolls);
  assert.equal(await ev(`__state.selected_account_kind`), 'microsoft'); await key('Escape'); await closed();
  await qclick(act('select-local-account', 'local')); await idle();
  await qclick(act('remove-local-account', 'local')); await until(`${el('action-dialog')}.open`);
  assert.match(await ev(`${el('dialog-body')}.textContent`), /worlds and installed game files stay intact/);
  await key('Escape'); await closed(); assert.equal(await ev(`__calls.filter(c=>c.m==='RemoveLocalAccount').length`), 0);
  await qclick(act('remove-local-account', 'local')); await qclick('[data-submit]'); await closed(); await idle();
  assert.equal(await ev(`__state.selected_account_kind`), 'local'); assert.equal(await ev(`__state.selected_local_account_id`), '');
  assert.equal(await ev(`${query(act('start', 'client'))}.disabled`), true, 'selected local deletion never falls back to saved Microsoft');
  assert.equal(await ev(`document.querySelectorAll('.account-row.selected').length`), 0);
  await qclick(act('select-account', 'account')); await idle();
  assert.equal(await ev(`${query(act('start', 'client'))}.disabled`), false);
  // Stale Microsoft ID cannot fall back either, even with an available local player.
  await ev(`__state.selected_account_id='missing';__state.local_accounts=[{id:'other',name:'Local_2',uuid:'u'}];__state.selected_local_account_id='other'`); await click('refresh-state'); await idle();
  assert.equal(await ev(`${query(act('start', 'client'))}.disabled`), true);
  await qclick(act('select-account', 'account')); await idle();
  assert.equal(await ev(`Array.from(document.querySelectorAll('button svg')).every(s=>s.getAttribute('aria-hidden')==='true' && s.querySelector('use')?.getAttribute('href').startsWith('#i-'))`), true);
  for (const action of ['start', 'logs', 'folder']) assert.ok(await ev(`${query(act(action, 'client'))}.querySelector('svg') && ${query(act(action, 'client'))}.textContent.trim().length > 0`));
  await qclick(act('version', 'legacy')); await until(`${el('profile-version')}.options.length===2`); await input('profile-version', '1.21.4'); await qclick('[data-submit]'); await closed(); await idle();
  assert.ok(await ev(`__calls.some(c=>c.m==='SetProfileVersion'&&c.a[1]==='legacy')`));
  await click('add-instance'); await until(`${el('profile-version')}.options.length===2`);
  assert.equal(await ev(`document.activeElement.id`), 'profile-name'); await key('Tab'); assert.equal(await ev(`document.activeElement.id`), 'profile-version'); assert.equal(await ev(`getComputedStyle(document.activeElement).outlineStyle`), 'solid');
  await input('profile-name', 'New world'); await input('profile-version', '1.21.4'); await input('profile-loader', 'fabric'); await qclick('[data-submit]'); await closed(); await idle();
  await qclick(act('install', 'fresh')); await until(`${query(act('install', 'fresh'))}.disabled && !${el('operations-panel')}.hidden`);
  assert.equal(await ev(`${query(act('version', 'fresh'))}.disabled`), true); assert.doesNotMatch(await ev(`${el('operation-list')}.textContent`), /%/);
  await ev(`__state.operations.find(o=>o.target_id==='fresh').state='complete';__state.instances.find(p=>p.id==='fresh').installed=true`);
  await until(`!!${query(act('start', 'fresh'))}`); // polling observes installation, not optimistic Play
  await view('servers'); await qclick(act('start', 'server')); await until(`${el('action-dialog')}.open`);
  assert.equal(await ev(`${el('accept-eula')}.checked`), false); assert.equal(await ev(`${query('[data-submit]')}.disabled`), true);
  await qclick('[data-submit]'); assert.equal(await ev(`__calls.filter(c=>c.m==='StartServer').length`), 0);
  await click('accept-eula'); await qclick('[data-submit]'); await closed(); await idle();
  assert.deepEqual(await ev(`__calls.find(c=>c.m==='StartServer').a`), ['server', true]);
  await qclick(act('logs', 'server')); await until(`${el('logs')}.textContent.includes('Test log')`);
  assert.equal(await ev(`${el('logs')}.querySelectorAll('img').length`), 0); await input('server-command', 'list'); await click('send-command'); await until(`__calls.some(c=>c.m==='SendServerCommand')`);
  await layout(320); assert.ok(await ev(`${el('action-dialog')}.scrollWidth <= ${el('action-dialog')}.clientWidth+1`));
  await key('Escape'); await closed(); const logs = await ev(`__calls.filter(c=>c.m==='GetLogs').length`); await delay(1150); assert.equal(await ev(`__calls.filter(c=>c.m==='GetLogs').length`), logs);
  await qclick(act('stop', 'server')); await idle(); await layout(1280);
  await view('settings'); assert.equal(await ev(`${el('cf-enabled')}.disabled`), true);
  await input('cf-key', 'SECRET-FIXTURE'); assert.equal(await ev(`${el('cf-key')}.type`), 'password'); await ev(`__fail='SetCurseForgeKey'`); await click('save-key'); await idle();
  assert.equal(await ev(`${el('cf-key')}.value`), ''); assert.doesNotMatch(await ev(`document.body.innerText`), /SECRET-FIXTURE/);
  await input('cf-key', 'SECRET-FIXTURE'); await click('save-key'); await idle(); assert.equal(await ev(`${el('cf-option')}.disabled`), true);
  await click('cf-enabled'); await click('save-settings'); await idle(); assert.equal(await ev(`${el('cf-option')}.disabled`), false);
  await input('client-memory', '8192'); await click('browse-java'); await idle(); assert.equal(await ev(`${el('client-memory')}.value`), '8192');
  await click('remove-key'); await idle(); assert.equal(await ev(`${el('cf-enabled')}.disabled`), true); assert.equal(await ev(`${el('cf-enabled')}.checked`), false);
  await view('content'); await input('provider', 'curseforge'); const searches = await ev(`__calls.filter(c=>c.m==='SearchContent').length`); await click('search-content'); await idle(); assert.equal(await ev(`__calls.filter(c=>c.m==='SearchContent').length`), searches, 'disabled provider blocked even if DOM selection manipulated');
  await input('provider', 'modrinth'); await click('search-content'); await until(`!!${query('[data-action="content-install"]')}`); await idle();
  assert.equal(await ev(`${el('content-results')}.querySelectorAll('img').length`), 0, 'remote icons omitted');
  await qclick('[data-action="content-install"]'); await until(`${el('content-release')}.value==='release'`);
  assert.equal(await ev(`${el('target-id')}.value`), 'client'); await qclick('[data-submit]'); await closed(); await idle();
  assert.deepEqual(await ev(`__calls.find(c=>c.m==='InstallContent').a[0].target_id`), 'client');
  // A running operation blocks that target in the next mod picker.
  await qclick('[data-action="content-install"]'); await until(`!!${el('target-id')}`); assert.ok(!(await ev(`Array.from(${el('target-id')}.options).map(o=>o.value)`)).includes('client')); await key('Escape'); await closed();
  await input('content-kind', 'modpack'); await input('content-side', 'server'); await click('search-content'); await idle(); await qclick('[data-action="content-install"]'); await until(`${el('content-release')}.value==='release'`);
  assert.match(await ev(`${el('dialog-body')}.textContent`), /NEW profile/); await input('pack-name', 'A new server pack'); await click('include-optional'); await qclick('[data-submit]'); await closed(); await idle();
  const pack = await ev(`__calls.filter(c=>c.m==='InstallContent').at(-1).a[0]`); assert.equal(pack.target_kind, 'server'); assert.equal(pack.target_id, ''); assert.equal(pack.port, 25566); assert.equal(pack.memory_mb, 2048); assert.equal(pack.include_optional, true);
  // Client packs never validate hidden server resources, even after changing targets.
  await qclick('[data-action="content-install"]'); await until(`${el('content-release')}.value==='release'`);
  await input('profile-port', ''); await input('target-kind', 'client'); await until(`${el('content-release')}.value==='release'`);
  assert.equal(await ev(`${el('profile-port')}.disabled`), true);
  await input('pack-name', 'A new client pack'); await qclick('[data-submit]'); await closed(); await idle();
  const clientPack = await ev(`__calls.filter(c=>c.m==='InstallContent').at(-1).a[0]`); assert.equal(clientPack.target_kind, 'client'); assert.equal(clientPack.target_id, ''); assert.equal(clientPack.port, 0);
  const operationId = await ev(`__state.operations.filter(o=>o.state==='running').at(-1).id`);
  await qclick(act('cancel-operation', operationId)); await idle(); assert.equal(await ev(`__state.operations.find(o=>o.id===${JSON.stringify(operationId)}).state`), 'cancelled');
  await view('servers'); await click('add-server'); await until(`${el('profile-version')}.options.length===2`); await input('profile-name', 'Created server'); await input('profile-version', '1.21.4');
  await ev(`__fail='GetState'`); await qclick('[data-submit]'); await closed(); await idle();
  assert.match(await ev(`${el('global-error')}.textContent`), /Request accepted/);
  assert.equal(await ev(`__calls.filter(c=>c.m==='CreateServer').length`), 1, 'refresh failure must not invite duplicate creation');
  await click('refresh-state'); await idle();
  for (const [name,id] of [['OpenDataFolder','open-data']]) { await view('settings'); await click(id); await idle(); assert.ok(await ev(`__calls.some(c=>c.m===${JSON.stringify(name)})`)); }
  await view('play'); await qclick(act('folder', 'client')); await idle();
  await qclick(act('remove-account', 'account')); await until(`${el('action-dialog')}.open`); await qclick('[data-submit]'); await closed(); await idle(); assert.ok(await ev(`__calls.some(c=>c.m==='RemoveAccount')`));
  for (const width of [1280, 680, 390, 320]) for (const name of ['play', 'servers', 'content', 'settings']) { await view(name); await layout(width); }
  assert.deepEqual(exceptions, [], 'no uncaught runtime exceptions');
});
