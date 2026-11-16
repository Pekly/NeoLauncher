'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const ui = require('../internal/app/frontend/app.js');
const html = fs.readFileSync(path.join(__dirname, '../internal/app/frontend/index.html'), 'utf8');
const css = fs.readFileSync(path.join(__dirname, '../internal/app/frontend/styles.css'), 'utf8');
const js = fs.readFileSync(path.join(__dirname, '../internal/app/frontend/app.js'), 'utf8');
const base = { name: 'Weekend world', version: '1.21.4', loader: 'fabric', port: 25565, memory_mb: 2048 };

test('typed profile payloads require versions and supported loaders', () => {
  assert.deepEqual(ui.validateServer({ ...base, name: ' My world ', port: '25565', memory_mb: '2048' }).value, { ...base, name: 'My world' });
  for (const loader of ['forge', 'neoforge', 'quilt', '../fabric']) assert.equal(ui.validateServer({ ...base, loader }).valid, false);
  for (const version of ['', '../1.21', '<script>', '1;rm', 'a'.repeat(65)]) assert.equal(ui.validateServer({ ...base, version }).valid, false);
  for (const version of ['a'.repeat(64), '3D Shareware v1.34', '1.14 Pre-Release 1']) assert.equal(ui.validateServer({ ...base, version }).valid, true, version);
  assert.equal(ui.validateServer({ ...base, version: ' 3D Shareware v1.34 ' }).value.version, '3D Shareware v1.34');
  assert.equal(ui.validateProfile({ ...base, version: '' }, 'server', false).valid, true, 'packs derive version from native manifest');
  for (const name of ['', '   ', 'a'.repeat(61), 'x\n', 'a\u2028b']) assert.equal(ui.validateServer({ ...base, name }).valid, false);
  for (const name of ['🌲'.repeat(60), '<img src=x>', '世界']) assert.equal(ui.validateServer({ ...base, name }).valid, true);
});
test('numeric validation rejects fractional, exponential and out of range values', () => {
  for (const port of [1024, 65535]) assert.equal(ui.validateServer({ ...base, port }).valid, true);
  for (const port of [1023, 65536, '', '2e4', '0x6400', 25565.5]) assert.equal(ui.validateServer({ ...base, port }).valid, false);
  for (const memory_mb of [511, 32769, '', '2e3', 2048.5]) assert.equal(ui.validateServer({ ...base, memory_mb }).valid, false);
  assert.deepEqual(ui.validateSettings({ java_path: ' C:/Java ', client_memory_mb: '4096' }).value, { java_path: 'C:/Java', client_memory_mb: 4096, curseforge_enabled: false });
});
test('state boundary preserves runtime fields, tolerates null slices, never keeps keys or tokens', async () => {
  const state = await ui.createPreviewAdapter().GetState();
  const normalized = ui.normalizeState({ ...state, servers: null, curseforge_key: 'secret', accounts: [{ id: 'a', name: 'User', uuid: 'u', token: 'secret' }], settings: { ...state.settings, curseforge_enabled: true, key: 'secret' } });
  assert.deepEqual(normalized.servers, []); assert.equal(normalized.settings.curseforge_enabled, false);
  assert.doesNotMatch(JSON.stringify(normalized), /secret|token|curseforge_key/);
  for (const invalid of [null, {}, { ...state, schema_version: 2 }, { ...state, servers: {} }, { ...state, servers: [{}] }]) assert.throws(() => ui.normalizeState(invalid));
});
test('provider requires both configured key and saved opt-in', () => {
  for (const configured of [false, true]) for (const enabled of [false, true]) {
    const state = { curseforge_configured: configured, settings: { curseforge_enabled: enabled } };
    assert.equal(ui.providerAllowed(state, 'curseforge'), configured && enabled);
    assert.equal(ui.providerAllowed(state, 'modrinth'), true);
    assert.equal(ui.providerAllowed(state, 'unknown'), false);
  }
});
test('busy guards distinguish stopped, running and operations', () => {
  const state = { processes: [], operations: [] };
  assert.equal(ui.profileBusy(state, 'client', '1'), false);
  for (const status of ['starting', 'running', 'stopping']) assert.equal(ui.profileBusy({ ...state, processes: [{ id: '1', kind: 'client', status }] }, 'client', '1'), true);
  assert.equal(ui.profileBusy({ ...state, processes: [{ id: '1', kind: 'server', status: 'running' }] }, 'client', '1'), false);
  assert.equal(ui.profileBusy({ ...state, operations: [{ id: 'op', target_id: '1', state: 'running' }] }, 'client', '1'), true);
  for (const status of ['complete', 'failed', 'cancelled']) assert.equal(ui.profileBusy({ ...state, operations: [{ target_id: '1', state: status }] }, 'client', '1'), false);
});
test('progress never invents percentages or unknown totals', () => {
  assert.equal(ui.progressText({ stage: 'Downloading' }), 'Downloading');
  assert.equal(ui.progressText({ done: 2, total: 5 }), '2 / 5 items');
  assert.match(ui.progressText({ bytes: 1024 }), /1,024 bytes/);
  assert.doesNotMatch(ui.progressText({}), /%|100|complete/i);
});
test('official verification URLs and server poll intervals', () => {
  assert.equal(ui.pollDelay(7), 7000); assert.equal(ui.pollDelay(12), 12000); assert.equal(ui.pollDelay(undefined), 5000);
  assert.equal(ui.officialVerificationURL('https://www.microsoft.com/link'), 'https://www.microsoft.com/link');
  for (const url of ['javascript:alert(1)', 'http://microsoft.com/link', 'https://microsoft.com.evil.test', 'https://user:pass@microsoft.com', 'https://evil.test']) assert.equal(ui.officialVerificationURL(url), '');
});
test('native startup waits and never silently becomes browser preview', async () => {
  assert.equal(ui.hasNativeEnvironment({ runtime: {} }), true);
  assert.equal(ui.hasNativeEnvironment({ location: { protocol: 'file:' } }), false);
  const host = {}; const binding = { GetState() {} };
  setTimeout(() => { host.go = { app: { App: binding } }; }, 10);
  assert.equal(await ui.waitForBindings(host, 100, 5), binding);
  await assert.rejects(ui.waitForBindings({}, 10, 5), /desktop connection/);
});
test('honest explicit preview cannot authenticate, create files or emulate running/download state', async () => {
  const adapter = ui.createPreviewAdapter();
  for (const key of ['instances', 'servers', 'accounts', 'operations', 'processes']) assert.deepEqual((await adapter.GetState())[key], []);
  for (const [method, fn] of Object.entries(adapter)) if (method !== 'GetState') await assert.rejects(fn(), /Only available in the desktop app/);
  assert.match(html, /Read-only UI preview/); assert.match(html, /id="sign-in"[^>]*disabled/);
});
test('safe text, EULA, pack isolation, masked key and motion contracts', () => {
  assert.equal(ui.escapeHTML(`<b a="'">&`), '&lt;b a=&quot;&#39;&quot;&gt;&amp;');
  assert.match(html, /id="cf-key" type="password"/);
  assert.match(js, /\$\('cf-key'\)\.value = ''/);
  assert.match(js, /id="accept-eula" type="checkbox"/);
  assert.doesNotMatch(js, /id="accept-eula"[^<]*checked/);
  assert.match(js, /target_id: pack \? ''/);
  assert.match(html, /Forge, NeoForge, Quilt and other pack loaders are unsupported/);
  assert.doesNotMatch(js, /\bfetch\(|XMLHttpRequest|localStorage|console\.log/);
  assert.match(html, /id="floating-island"/); assert.match(html, /id="fall-clip"/);
  assert.match(css, /focus-visible/); assert.match(css, /prefers-reduced-motion: reduce/);
  assert.doesNotMatch(css, /transition:\s*all|overflow:\s*hidden/);
});
