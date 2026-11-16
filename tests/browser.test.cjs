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
  async function evaluate(expression) {
    const response = await cdp('Runtime.evaluate', { expression, returnByValue: true, awaitPromise: true });
    if (response.exceptionDetails) throw new Error(response.exceptionDetails.exception?.description || response.exceptionDetails.text);
    return response.result.value;
  }
  async function until(expression) {
    for (let i = 0; i < 100; i++) { if (await evaluate(expression)) return; await delay(40); }
    assert.fail(`Timed out: ${expression}`);
  }
  const el = id => `document.getElementById(${JSON.stringify(id)})`;
  const click = id => evaluate(`${el(id)}.click()`);
  const input = (id, value) => evaluate(`${el(id)}.value=${JSON.stringify(value)}; ${el(id)}.dispatchEvent(new Event('input',{bubbles:true}))`);
  async function key(key, code = key, modifiers = 0) {
    const vk = { Tab: 9, Escape: 27, Enter: 13 }[key];
    await cdp('Input.dispatchKeyEvent', { type: 'keyDown', key, code, windowsVirtualKeyCode: vk, modifiers });
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
  await until(`!${el('browser-banner')}.hidden && !${el('add-server')}.disabled`);
  assert.equal(await evaluate(`${el('play-button')}.disabled`), true);
  assert.equal(await evaluate(`${el('open-data')}.disabled`), true);

  await t.test('responsive Play/Servers/Settings grids at 1280, 900, 680, 390 and 320px', async () => {
    for (const value of [1280, 900, 680, 390, 320]) {
      await width(value);
      for (const view of ['play', 'servers', 'settings']) {
        await navigateView(view);
        await assertNoOverflow(`${view} ${value}px`);
      }
    }
  });
  await width(900);
  await navigateView('servers');
  await click('empty-add-server');
  await until(`${el('server-dialog')}.open`);
  assert.equal(await evaluate('document.activeElement.id'), 'server-name');
  await key('Tab');
  assert.equal(await evaluate('document.activeElement.id'), 'server-version');
  assert.equal(await evaluate('getComputedStyle(document.activeElement).outlineStyle'), 'solid');
  await key('Tab', 'Tab', 8);
  assert.equal(await evaluate('document.activeElement.id'), 'server-name');
  await evaluate(`${el('create-server')}.focus()`);
  await key('Tab');
  assert.equal(await evaluate(`${el('server-dialog')}.contains(document.activeElement)`), true, 'native dialog traps Tab focus');
  await key('Escape');
  await until(`!${el('server-dialog')}.open && document.activeElement.id === 'empty-add-server'`);
  await click('empty-add-server');
  await click('create-server');
  assert.equal(await evaluate(`${el('server-name')}.getAttribute('aria-invalid')`), 'true');
  assert.equal(await evaluate('document.activeElement.id'), 'server-name');
  const hostileName = '<img src=x onerror=alert(1)>';
  await input('server-name', hostileName);
  await click('create-server');
  await until(`!${el('server-dialog')}.open`);
  assert.equal(await evaluate("document.querySelector('.server-card h2').textContent"), hostileName);
  assert.equal(await evaluate("document.querySelectorAll('.server-card img').length"), 0);
  assert.match(await evaluate(`${el('notice')}.textContent`), /No files written/);
  assert.equal(await evaluate("document.querySelector('.open-server-folder').disabled"), true);
  await click('add-server');
  await input('server-name', 'Second profile');
  await input('server-port', '25565');
  await click('create-server');
  await until(`!${el('dialog-error')}.hidden`);
  assert.equal(await evaluate(`${el('create-server')}.disabled`), false, 'failed create cleans up busy state');
  await key('Escape');
  await navigateView('settings');
  await input('client-memory', '511');
  await click('save-settings');
  assert.equal(await evaluate(`${el('client-memory')}.getAttribute('aria-invalid')`), 'true');
  await input('client-memory', '8192');
  await click('save-settings');
  await until(`${el('notice')}.textContent.includes('Preview settings updated')`);
  await cdp('Emulation.setEmulatedMedia', { features: [{ name: 'prefers-reduced-motion', value: 'reduce' }] });
  await navigateView('play');
  assert.equal(await evaluate(`getComputedStyle(${el('view-play')}).animationName`), 'none');
  assert.equal(await evaluate(`getComputedStyle(${el('play-button')}).transitionDuration`), '0s');

  await t.test('delayed Wails binding, retry, native errors/success, Java and folder cleanup', async () => {
    // These are explicitly test-only native fixtures, not application/demo data.
    await cdp('Page.addScriptToEvaluateOnNewDocument', { source: `
      window.__calls = [];
      window.runtime = Object.fromEntries(['WindowMinimise','WindowToggleMaximise','Quit'].map(name => [name, async () => { window.__calls.push(name); }]));
      window.__state = {schema_version:1,data_dir:'C:/Data/'+'long-path-'.repeat(40),settings:{java_path:'',client_memory_mb:4096},servers:[],next_server_port:25565};
      window.__fail = 'GetState';
      const call = async (name, payload) => {
        window.__calls.push(name);
        await new Promise(resolve => setTimeout(resolve, 160));
        if (window.__fail === name) { window.__fail = ''; throw new Error('Test failure: '+name); }
        if (name === 'SaveSettings') window.__state.settings = payload;
        if (name === 'ChooseJava') window.__state.settings.java_path = 'C:/Java/bin/java.exe';
        if (name === 'CreateServer') window.__state.servers.push({...payload,id:'native-test',directory:'C:/Data/test',created_at:''});
        return JSON.parse(JSON.stringify(window.__state));
      };
      setTimeout(() => { window.go = {app:{App:Object.fromEntries(['GetState','SaveSettings','CreateServer','ChooseJava','OpenDataFolder','OpenServerFolder'].map(name => [name,payload => call(name,payload)]))}}; }, 1500);
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
    await input('client-memory', '8192');
    await click('browse-java');
    await until(`${el('java-path')}.value === 'C:/Java/bin/java.exe'`);
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
    await input('server-name', 'N'.repeat(60));
    await input('server-version', 'v'.repeat(48));
    await click('create-server');
    await key('Escape');
    assert.equal(await evaluate(`${el('server-dialog')}.open`), true, 'cannot dismiss a pending file-creation request');
    await until(`!${el('server-dialog')}.open`);
    assert.equal(await evaluate("document.querySelector('.configured').textContent"), 'Configured');
    await evaluate("document.querySelector('.open-server-folder').click()");
    await until(`${el('notice')}.textContent.includes('request sent')`);
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
