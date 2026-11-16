'use strict';
// Optional real-Chromium checks, no npm dependencies. Node 22+.
// Set CHROME_BIN to a Chrome/Edge executable. Temporary profiles use PI_SCRATCH_DIR or OS temp.
const { test } = require('node:test');
const assert = require('node:assert/strict');
const { spawn } = require('node:child_process');
const fs = require('node:fs/promises');
const path = require('node:path');
const os = require('node:os');
const { pathToFileURL } = require('node:url');
const delay = ms => new Promise(resolve => setTimeout(resolve, ms));
const chrome = process.env.CHROME_BIN;

test('Chromium: preview, responsive layout, keyboard, reduced motion and native binding states', { skip: !chrome, timeout: 60000 }, async t => {
  const profile = await fs.mkdtemp(path.join(process.env.PI_SCRATCH_DIR || os.tmpdir(), 'neolauncher-browser-'));
  const child = spawn(chrome, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--remote-debugging-port=0', `--user-data-dir=${profile}`, 'about:blank'], { stdio: 'ignore' });
  let socket;
  t.after(async () => {
    socket?.close();
    child.kill();
    await delay(700);
    await fs.rm(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }).catch(() => {});
  });
  let port;
  for (let attempt = 0; attempt < 100; attempt++) {
    try { port = (await fs.readFile(path.join(profile, 'DevToolsActivePort'), 'utf8')).split('\n')[0]; break; } catch { await delay(100); }
  }
  assert.ok(port, 'Chromium exposes a debugging port');
  const targets = await (await fetch(`http://127.0.0.1:${port}/json/list`)).json();
  socket = new WebSocket(targets.find(target => target.type === 'page').webSocketDebuggerUrl);
  await new Promise((resolve, reject) => { socket.onopen = resolve; socket.onerror = reject; });
  let sequence = 0;
  const pending = new Map();
  const exceptions = [];
  socket.onmessage = event => {
    const reply = JSON.parse(event.data);
    if (reply.method === 'Runtime.exceptionThrown') exceptions.push(reply.params.exceptionDetails.text);
    const request = pending.get(reply.id);
    if (request) { pending.delete(reply.id); clearTimeout(request.timer); reply.error ? request.reject(new Error(reply.error.message)) : request.resolve(reply.result); }
  };
  function cdp(method, params = {}) {
    return new Promise((resolve, reject) => {
      const id = ++sequence;
      const timer = setTimeout(() => { pending.delete(id); reject(new Error(`CDP timeout: ${method}`)); }, 5000);
      pending.set(id, { resolve, reject, timer });
      socket.send(JSON.stringify({ id, method, params }));
    });
  }
  async function evaluate(expression, userGesture = false) {
    const response = await cdp('Runtime.evaluate', { expression, userGesture, returnByValue: true, awaitPromise: true });
    if (response.exceptionDetails) throw new Error(response.exceptionDetails.exception?.description || response.exceptionDetails.text);
    return response.result.value;
  }
  async function until(expression) {
    for (let i = 0; i < 100; i++) { if (await evaluate(expression)) return; await delay(40); }
    assert.fail(`Timed out: ${expression}`);
  }
  const el = id => `document.getElementById(${JSON.stringify(id)})`;
  // Dialog close watchers require activation when opening to make Escape cancelable.
  const click = id => evaluate(`${el(id)}.focus(); ${el(id)}.click()`, true);
  const input = (id, value) => evaluate(`${el(id)}.value=${JSON.stringify(value)}; ${el(id)}.dispatchEvent(new Event('input',{bubbles:true})); ${el(id)}.dispatchEvent(new Event('change',{bubbles:true}))`);
  const submit = () => evaluate(`document.querySelector('[data-submit]').click()`, true);
  async function key(key, code = key, modifiers = 0) {
    const vk = { Tab: 9, Escape: 27, Enter: 13 }[key];
    await cdp('Input.dispatchKeyEvent', { type: 'keyDown', key, code, windowsVirtualKeyCode: vk, modifiers, ...(key === 'Enter' ? { text: '\r' } : {}) });
    await cdp('Input.dispatchKeyEvent', { type: 'keyUp', key, code, windowsVirtualKeyCode: vk, modifiers });
  }
  const navigateView = name => evaluate(`document.querySelector('[data-view="${name}"]').click()`);
  async function width(value) {
    await cdp('Emulation.setDeviceMetricsOverride', { width: value, height: 900, deviceScaleFactor: 1, mobile: false });
    await delay(260);
  }
  async function assertNoOverflow(label) {
    const sizes = await evaluate(`({body:document.documentElement.scrollWidth, viewport:innerWidth, main:${el('main-content')}.scrollWidth, mainClient:${el('main-content')}.clientWidth})`);
    assert.ok(sizes.body <= sizes.viewport && sizes.main <= sizes.mainClient + 1, `${label}: ${JSON.stringify(sizes)}`);
  }
  await cdp('Page.enable');
  await cdp('Runtime.enable');
  const url = pathToFileURL(path.join(__dirname, '../internal/app/frontend/index.html')).href;
  await width(1280);
  await cdp('Page.navigate', { url });
  await until(`${el('enter-preview')} && !${el('enter-preview')}.hidden`);
  assert.equal(await evaluate(`${el('browser-banner')}.hidden`), true, 'browser adapter never starts automatically');
  await click('enter-preview');
  await until(`!${el('browser-banner')}.hidden`);
  for (const id of ['sign-in', 'add-local-account', 'add-instance', 'add-server', 'save-settings', 'open-data', 'search-content']) {
    assert.equal(await evaluate(`${el(id)}.disabled`), true, `${id} requires native bindings`);
  }
  await click('add-server');
  assert.equal(await evaluate(`${el('action-dialog')}.open`), false, 'read-only preview cannot create profiles');
  await click('add-local-account');
  assert.equal(await evaluate(`${el('action-dialog')}.open`), false, 'read-only preview cannot create local players');
  for (const id of ['sign-in', 'add-local-account', 'search-content', 'save-settings', 'open-data']) {
    assert.equal(await evaluate(`${el(id)}.querySelector('svg').getAttribute('aria-hidden')`), 'true');
    assert.ok(await evaluate(`${el(id)}.textContent.trim().length > 0`), 'icons retain visible button names');
  }
  assert.equal(await evaluate(`Array.from(document.querySelectorAll('use')).every(u=>u.getAttribute('href').startsWith('#') && !!document.querySelector(u.getAttribute('href')))`), true, 'all icons resolve to local sprite symbols');
  assert.equal(await evaluate(`document.querySelectorAll('img, image').length`), 0, 'no raster or remote icon images');

  await t.test('responsive Play/Servers/Content/Settings grids at 1280, 900, 680, 390 and 320px', async () => {
    for (const value of [1280, 900, 680, 390, 320]) {
      await width(value);
      for (const view of ['play', 'servers', 'content', 'settings']) {
        await navigateView(view);
        await assertNoOverflow(`${view} ${value}px`);
      }
    }
  });
  await width(900);
  await evaluate(`document.querySelector('[data-view="servers"]').focus()`);
  await key('Enter');
  assert.equal(await evaluate('document.activeElement.id'), 'servers-title');
  assert.equal(await evaluate(`document.querySelector('[data-view="servers"]').getAttribute('aria-current')`), 'page');
  await cdp('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-reduced-motion', value: 'reduce' }] });
  await navigateView('play');
  assert.equal(await evaluate(`getComputedStyle(${el('view-play')}).animationName`), 'none');
  assert.equal(await evaluate(`getComputedStyle(${el('sign-in')}).transitionDuration`), '0s');

  await t.test('delayed Wails binding, retry, native errors/success, Java and folder cleanup', async () => {
    // These are explicitly test-only native fixtures, not application/demo data.
    await cdp('Page.addScriptToEvaluateOnNewDocument', { source: `
      window.__calls = [];
      window.runtime = Object.fromEntries(['WindowMinimise','WindowToggleMaximise','Quit'].map(name => [name, async () => { window.__calls.push(name); }]));
      window.__state = {schema_version:1,data_dir:'C:/Data/'+'long-path-'.repeat(40),settings:{java_path:'',client_memory_mb:4096,curseforge_enabled:false},instances:[],servers:[],accounts:[],operations:[],processes:[],next_server_port:25565};
      window.__fail = 'GetState';
      const call = async (name, payload) => {
        window.__calls.push(name);
        if (name === 'CreateServer' && window.__holdCreate) await new Promise(resolve => { window.__releaseCreate = resolve; });
        await new Promise(resolve => setTimeout(resolve, name === 'CreateServer' ? 400 : 160));
        if (window.__fail === name) { window.__fail = ''; throw new Error('Test failure: '+name); }
        if (name === 'GetMinecraftVersions') return [{id:'1.21.4',type:'release'}, {id:'3D Shareware v1.34',type:'snapshot'}, {id:'v'.repeat(64),type:'release'}];
        if (name === 'SaveSettings') window.__state.settings = payload;
        if (name === 'ChooseJava') window.__state.settings.java_path = 'C:/Java/bin/java.exe';
        if (name === 'CreateServer') window.__state.servers.push({...payload,id:'native-test-'+window.__state.servers.length,directory:'C:/Data/test',installed:false});
        return JSON.parse(JSON.stringify(window.__state));
      };
      setTimeout(() => { window.go = {app:{App:Object.fromEntries(['GetState','GetMinecraftVersions','SaveSettings','CreateServer','ChooseJava','OpenDataFolder','OpenServerFolder'].map(name => [name,payload => call(name,payload)]))}}; }, 1500);
    ` });
    await cdp('Page.navigate', { url });
    await until(`${el('enter-preview')} && !!window.runtime`);
    await delay(1250);
    assert.equal(await evaluate(`${el('enter-preview')}.hidden`), true, 'native startup does not offer browser fallback');
    assert.equal(await evaluate(`${el('browser-banner')}.hidden`), true);
    await until(`!${el('retry')}.hidden`);
    assert.match(await evaluate(`${el('connection-message')}.textContent`), /Test failure: GetState/);
    await click('retry');
    await until(`!${el('add-server')}.disabled`);
    assert.equal(await evaluate(`${el('browser-banner')}.hidden`), true);
    await navigateView('settings');
    await input('client-memory', '511');
    await click('save-settings');
    assert.equal(await evaluate(`${el('client-memory')}.validity.rangeUnderflow`), true);
    assert.equal(await evaluate(`__calls.filter(c=>c==='SaveSettings').length`), 0, 'invalid resources never reach native');
    await input('java-path', 'C:/Unsaved/java.exe');
    await input('client-memory', '6144');
    await evaluate("window.__fail='SaveSettings'");
    await click('save-settings');
    assert.equal(await evaluate(`${el('save-settings')}.disabled`), true);
    assert.equal(await evaluate(`${el('save-settings')}.textContent`), 'Saving…');
    await until(`!${el('global-error')}.hidden`);
    assert.equal(await evaluate(`${el('save-settings')}.disabled`), false);
    assert.equal(await evaluate(`${el('client-memory')}.value`), '6144');
    await click('save-settings');
    await until(`${el('notice')}.textContent === 'Settings saved on this device.'`);
    assert.equal(await evaluate(`${el('save-settings')}.querySelectorAll('svg').length`), 1, 'settings icon survives busy label changes');
    await input('client-memory', '8192');
    await click('browse-java');
    await until(`${el('java-path')}.value === 'C:/Java/bin/java.exe' && !${el('open-data')}.disabled`);
    assert.equal(await evaluate(`${el('client-memory')}.value`), '8192', 'Java browse preserves unsaved memory');
    await evaluate("window.__fail='OpenDataFolder'");
    await click('open-data');
    await until(`${el('global-error')}.textContent.includes('OpenDataFolder')`);
    assert.equal(await evaluate(`${el('open-data')}.disabled`), false);
    assert.match(await evaluate(`${el('open-data')}.textContent`), /Open data folder/);
    await click('open-data');
    await until(`${el('notice')}.textContent.includes('request sent')`);
    await navigateView('servers');
    await click('add-server');
    await until(`${el('profile-version')}.options.length === 4`);
    assert.equal(await evaluate('document.activeElement.id'), 'profile-name');
    await key('Tab');
    assert.equal(await evaluate('document.activeElement.id'), 'profile-version');
    assert.equal(await evaluate('getComputedStyle(document.activeElement).outlineStyle'), 'solid');
    await key('Tab', 'Tab', 8);
    assert.equal(await evaluate('document.activeElement.id'), 'profile-name');
    await input('profile-version', '1.21.4');
    await evaluate(`document.querySelector('[data-submit]').focus()`);
    await key('Tab');
    assert.equal(await evaluate('document.activeElement.id'), 'dialog-close', 'dialog wraps forward Tab');
    await key('Tab', 'Tab', 8);
    assert.equal(await evaluate(`document.activeElement.matches('[data-submit]')`), true, 'dialog wraps reverse Tab');
    await key('Escape');
    await until(`!${el('action-dialog')}.open && document.activeElement.id === 'add-server'`);
    await evaluate("window.__fail='GetMinecraftVersions'");
    await click('add-server');
    await until(`!${el('retry-versions')}.hidden`);
    assert.match(await evaluate(`${el('dialog-error')}.textContent`), /GetMinecraftVersions/);
    assert.equal(await evaluate(`document.querySelector('[data-submit]').disabled`), true);
    await click('retry-versions');
    await until(`${el('profile-version')}.options.length === 4`);
    await input('profile-version', '3D Shareware v1.34');
    await submit();
    assert.equal(await evaluate(`${el('profile-name')}.validity.valueMissing`), true);
    assert.equal(await evaluate('document.activeElement.id'), 'profile-name');
    const hostileName = '<img src=x onerror=alert(1)>';
    await input('profile-name', hostileName);
    await evaluate("window.__fail='CreateServer'");
    await submit();
    await until(`${el('dialog-error')}.textContent.includes('CreateServer')`);
    assert.equal(await evaluate(`document.querySelector('[data-submit]').disabled`), false, 'failed create cleans up busy state');
    await evaluate('window.__holdCreate = true');
    await submit();
    await until(`typeof window.__releaseCreate === 'function' && ${el('action-dialog')}.getAttribute('aria-busy') === 'true'`);
    await key('Escape');
    assert.equal(await evaluate(`${el('action-dialog')}.open`), true, 'cannot dismiss a pending file-creation request');
    await evaluate('window.__holdCreate = false; window.__releaseCreate()');
    await until(`!${el('action-dialog')}.open`);
    assert.equal(await evaluate("document.querySelector('.server-card h2').textContent"), hostileName);
    assert.equal(await evaluate("document.querySelectorAll('.server-card img').length"), 0);
    assert.equal(await evaluate("document.querySelector('.server-card .status').textContent"), 'Not installed');
    assert.equal(await evaluate(`__state.servers[0].version`), '3D Shareware v1.34');
    await evaluate(`document.querySelector('[data-action="folder"][data-kind="server"]').click()`);
    await until(`${el('notice')}.textContent.includes('request sent')`);
    await click('add-server');
    await until(`${el('profile-version')}.options.length === 4`);
    await input('profile-name', 'N'.repeat(60));
    await input('profile-version', 'v'.repeat(64));
    await input('profile-port', '25566');
    await submit();
    await until(`!${el('action-dialog')}.open`);
    assert.equal(await evaluate(`__state.servers[1].version.length`), 64);
    for (const value of [1280, 680, 390, 320]) {
      await width(value);
      await assertNoOverflow(`long server name ${value}px`);
      await navigateView('settings');
      await assertNoOverflow(`long data path ${value}px`);
      await navigateView('servers');
    }
    await click('minimize'); await click('maximize'); await click('close-window');
    const calls = await evaluate('window.__calls');
    for (const name of ['WindowMinimise','WindowToggleMaximise','Quit','OpenServerFolder']) assert.ok(calls.includes(name), name);
  });
  assert.deepEqual(exceptions, [], 'no uncaught browser exceptions');
});
