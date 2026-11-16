'use strict';
const { test } = require('node:test');
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const ui = require('../internal/app/frontend/app.js');
const base = { name: 'Weekend world', version: '', port: 25565, memory_mb: 2048 };
const html = fs.readFileSync(path.join(__dirname, '../internal/app/frontend/index.html'), 'utf8');
const css = fs.readFileSync(path.join(__dirname, '../internal/app/frontend/styles.css'), 'utf8');

test('server form trims names, allows blank versions, and emits typed Go payloads', () => {
  assert.deepEqual(ui.validateServer({ ...base, name: '  My world  ', port: '25565', memory_mb: '2048' }), {
    valid: true, errors: {}, value: { ...base, name: 'My world' }
  });
  for (const version of ['', '1.21.4', '26w01a', 'fabric_1.21+test-2', 'v'.repeat(48)]) assert.equal(ui.validateServer({ ...base, version }).valid, true);
});

test('server names enforce required, length, and newline guards', () => {
  for (const name of ['', '   ', 'a'.repeat(61), 'world\n', 'two\rworlds', 'a\u2028b', 'a\u2029b']) assert.ok(ui.validateServer({ ...base, name }).errors.name, JSON.stringify(name));
  for (const name of ['a', 'a'.repeat(60), '世界', '<img src=x>', '🌲'.repeat(60)]) assert.equal(ui.validateServer({ ...base, name }).valid, true);
});

test('version labels cannot be paths, markup, command flags, or overlong strings', () => {
  for (const version of ['v'.repeat(49), '../1.21', '1 21', '<script>', '1;rm', '1\n2', '1:21', 'é']) assert.ok(ui.validateServer({ ...base, version }).errors.version, version);
});

test('numeric ranges include boundaries and reject fractional, empty and nondecimal input', () => {
  for (const port of [1024, 65535]) assert.equal(ui.validateServer({ ...base, port }).valid, true);
  for (const port of [1023, 65536, 0, '', ' ', '2e4', '0x6400', '25565.5', NaN, Infinity]) assert.ok(ui.validateServer({ ...base, port }).errors.port, String(port));
  for (const memory_mb of [512, 32768]) assert.equal(ui.validateServer({ ...base, memory_mb }).valid, true);
  for (const memory_mb of [511, 32769, -1, '', '2e3', 2048.5, null, undefined]) assert.ok(ui.validateServer({ ...base, memory_mb }).errors.memory_mb, String(memory_mb));
});

test('settings allow blank Java path and enforce client memory range', () => {
  assert.deepEqual(ui.validateSettings({ java_path: '', client_memory_mb: '4096' }).value, { java_path: '', client_memory_mb: 4096 });
  assert.equal(ui.validateSettings({ java_path: ' C:\\Java\\bin\\java.exe ', client_memory_mb: 512 }).value.java_path, 'C:\\Java\\bin\\java.exe');
  assert.equal(ui.validateSettings({ java_path: '', client_memory_mb: 32768 }).valid, true);
  for (const client_memory_mb of [511, 32769, '', 3.5, '4e3']) assert.equal(ui.validateSettings({ java_path: '', client_memory_mb }).valid, false);
});

test('HTML escaping protects text and quoted attributes, including paths and IDs', () => {
  assert.equal(ui.escapeHTML(`<b a="'">&`), '&lt;b a=&quot;&#39;&quot;&gt;&amp;');
  const attack = '"><img src=x onerror="alert(1)">';
  const card = ui.serverCardHTML({ ...base, id: attack, name: attack, version: attack, directory: attack }, false, 0);
  assert.ok(!card.includes('<img'));
  assert.ok(!card.includes('=""><'));
  assert.ok(card.includes(ui.escapeHTML(attack)));
  assert.match(card, /Configured/);
  assert.doesNotMatch(card, /Online|Ready|Start server/);
});

test('empty version is explicitly not installed and browser folder action is disabled', () => {
  const card = ui.serverCardHTML({ ...base, id: '1', directory: '' }, true, 0);
  assert.match(card, /Not specified/);
  assert.match(card, /Not installed/);
  assert.match(card, / disabled/);
  assert.match(card, /no files written/);
});

test('state boundary accepts Go null server slices but rejects malformed state', async () => {
  const state = await ui.createPreviewAdapter().GetState();
  assert.deepEqual(ui.normalizeState({ ...state, servers: null }).servers, []);
  for (const invalid of [null, {}, { ...state, schema_version: 2 }, { ...state, servers: {} }, { ...state, settings: { java_path: '', client_memory_mb: 0 } }, { ...state, servers: [{}] }]) assert.throws(() => ui.normalizeState(invalid));
  assert.equal(ui.normalizeState({ ...state, next_server_port: 99999 }).next_server_port, 25565);
});

test('native signals prevent browser fallback before bindings are ready', () => {
  for (const host of [{ runtime: {} }, { chrome: { webview: {} } }, { _wails: {} }, { WailsInvoke() {} }, { go: { app: { App: {} } } }, { location: { hostname: 'wails.localhost' } }, { location: { protocol: 'wails:' } }]) assert.equal(ui.hasNativeEnvironment(host), true);
  assert.equal(ui.hasNativeEnvironment({ location: { protocol: 'file:' } }), false);
});

test('binding wait resolves late injection and times out without creating preview state', async () => {
  const host = {};
  const binding = { GetState: async () => ({}) };
  setTimeout(() => { host.go = { app: { App: binding } }; }, 20);
  assert.equal(await ui.waitForBindings(host, 200, 5), binding);
  await assert.rejects(ui.waitForBindings({}, 15, 5), /desktop connection/);
});

test('preview starts empty, is isolated and cannot write files or claim installation', async () => {
  const adapter = ui.createPreviewAdapter();
  const initial = await adapter.GetState();
  assert.deepEqual(initial.servers, []);
  assert.equal(initial.data_dir, '');
  assert.equal(initial.settings.client_memory_mb, 4096);
  const result = await adapter.CreateServer(base);
  assert.equal(result.servers.length, 1);
  assert.equal(result.servers[0].directory, '');
  assert.equal(result.next_server_port, 25566);
  result.servers[0].name = 'Changed outside adapter';
  assert.equal((await adapter.GetState()).servers[0].name, base.name);
  await assert.rejects(adapter.CreateServer(base), /port is already assigned/);
  await assert.rejects(adapter.CreateServer({ ...base, name: '' }), /name/);
  await adapter.SaveSettings({ java_path: '', client_memory_mb: 8192 });
  assert.equal((await adapter.GetState()).settings.client_memory_mb, 8192);
  await assert.rejects(adapter.SaveSettings({ java_path: '', client_memory_mb: 0 }));
  for (const method of ['ChooseJava', 'OpenDataFolder', 'OpenServerFolder']) await assert.rejects(adapter[method]('preview-1'), /only available in the desktop app/);
  assert.deepEqual((await ui.createPreviewAdapter().GetState()).servers, []);
});

test('scaffold has honest capability states and no account or game-launch action', () => {
  assert.ok(Object.values(ui.CAPABILITIES).every(value => value === false));
  assert.match(html, /id="play-button"[^>]*disabled/);
  assert.match(html, /Browser preview — no files written/);
  assert.match(html, /Server downloads and launching are not available yet/);
  assert.equal((html.match(/COMING NEXT/g) || []).length, 3);
  assert.doesNotMatch(html, /<script[^>]+https?:|<link[^>]+https?:/);
});

test('inputs have native labels; dialog, focus and motion affordances exist', () => {
  for (const [, id] of html.matchAll(/<input id="([^"]+)"/g)) assert.ok(html.includes(`for="${id}"`), id);
  assert.match(html, /<dialog id="server-dialog" aria-labelledby="dialog-title"/);
  assert.match(css, /focus-visible/);
  assert.match(css, /prefers-reduced-motion: reduce/);
  assert.doesNotMatch(css, /transition:\s*all|overflow:\s*hidden/);
});
