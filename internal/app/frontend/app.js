/* Native-only workflows. No credentials, network requests or persistence in the WebView. */
(function (root) {
  'use strict';
  const LIMITS = Object.freeze({ minMemory: 512, maxMemory: 32768, minPort: 1024, maxPort: 65535 });
  const text = v => typeof v === 'string' ? v : '';
  const escapeHTML = v => String(v ?? '').replace(/[&<>"']/g, c => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[c]);
  const integerInRange = (v, min, max) => /^\d+$/.test(String(v)) && Number.isSafeInteger(Number(v)) && Number(v) >= min && Number(v) <= max;
  const memoryValid = v => integerInRange(v, 512, 32768);
  const portValid = v => integerInRange(v, 1024, 65535);
  const loaderValid = v => ['vanilla', 'fabric'].includes(v);
  function validateProfile(input, kind = 'client', requireVersion = true) {
    const name = text(input.name).trim(), version = text(input.version).trim(), loader = input.loader || 'vanilla';
    const errors = {};
    if (!name || Array.from(name).length > 60 || /[\r\n\u2028\u2029]/.test(text(input.name))) errors.name = 'Enter a name of 1–60 characters without line breaks.';
    if ((requireVersion && !version) || version.length > 64 || (version && !/^[A-Za-z0-9][A-Za-z0-9 ._+\-]*$/.test(version))) errors.version = 'Choose a Minecraft version.';
    if (!loaderValid(loader)) errors.loader = 'Only Vanilla and Fabric are supported.';
    if (kind === 'server' && !portValid(input.port)) errors.port = 'Port must be a whole number from 1,024 to 65,535.';
    if (kind === 'server' && !memoryValid(input.memory_mb)) errors.memory_mb = 'Memory must be whole MiB from 512 to 32,768.';
    return { valid: !Object.keys(errors).length, errors, value: { name, version, loader, ...(kind === 'server' ? { port: Number(input.port), memory_mb: Number(input.memory_mb) } : {}) } };
  }
  const validateServer = input => validateProfile(input, 'server');
  function validateSettings(input) {
    const errors = memoryValid(input.client_memory_mb) ? {} : { client_memory_mb: 'Memory must be whole MiB from 512 to 32,768.' };
    return { valid: !Object.keys(errors).length, errors, value: { java_path: text(input.java_path).trim(), client_memory_mb: Number(input.client_memory_mb), curseforge_enabled: input.curseforge_enabled === true } };
  }
  function normalizeState(v) {
    if (!v || v.schema_version !== 1 || !v.settings || !memoryValid(v.settings.client_memory_mb) || typeof v.settings.java_path !== 'string' || typeof v.data_dir !== 'string') throw new Error('Unsupported workspace. Restart the desktop app and retry.');
    const list = key => { if (v[key] != null && !Array.isArray(v[key])) throw new Error('Unreadable workspace list: ' + key); return v[key] || []; };
    const profiles = key => list(key).map(p => {
      if (!p || !text(p.id) || typeof p.name !== 'string' || typeof p.version !== 'string' || typeof p.directory !== 'string' || (key === 'servers' && (!portValid(p.port) || !memoryValid(p.memory_mb)))) throw new Error('A profile could not be read. Check launcher data and retry.');
      return { id: p.id, name: p.name, version: p.version, directory: p.directory, loader: text(p.loader).toLowerCase() || 'vanilla', loader_version: text(p.loader_version), installed: p.installed === true, port: p.port, memory_mb: p.memory_mb };
    });
    // Whitelist fields: the frontend must never keep native tokens or API keys.
    return { schema_version: 1, data_dir: v.data_dir, settings: { java_path: v.settings.java_path, client_memory_mb: Number(v.settings.client_memory_mb), curseforge_enabled: v.curseforge_configured === true && v.settings.curseforge_enabled === true },
      curseforge_configured: v.curseforge_configured === true, instances: profiles('instances'), servers: profiles('servers'), accounts: list('accounts').map(a => ({ id: text(a.id), name: text(a.name), uuid: text(a.uuid) })), selected_account_id: text(v.selected_account_id), selected_instance_id: text(v.selected_instance_id), accounts_error: text(v.accounts_error), next_server_port: portValid(v.next_server_port) ? Number(v.next_server_port) : 25565,
      operations: list('operations').map(o => ({ id: text(o.id), target_id: text(o.target_id), kind: text(o.kind), label: text(o.label), state: text(o.state), progress: o.progress || {}, error: text(o.error) })), processes: list('processes').map(p => ({ id: text(p.id), kind: text(p.kind), status: text(p.status), exit_code: p.exit_code })) };
  }
  const processFor = (state, kind, id) => state.processes.find(p => p.id === id && p.kind === kind);
  const processActive = p => !!p && ['starting', 'running', 'stopping'].includes(p.status);
  const profileBusy = (state, kind, id) => processActive(processFor(state, kind, id)) || state.operations.some(o => o.target_id === id && o.state === 'running');
  const providerAllowed = (state, provider) => provider === 'modrinth' || (provider === 'curseforge' && state.curseforge_configured && state.settings.curseforge_enabled);
  const pollDelay = seconds => Number.isFinite(Number(seconds)) && Number(seconds) > 0 ? Math.max(1000, Number(seconds) * 1000) : 5000;
  function officialVerificationURL(value) {
    try { const u = new URL(value); return u.protocol === 'https:' && !u.username && !u.password && ['microsoft.com', 'www.microsoft.com', 'login.microsoftonline.com', 'login.live.com', 'microsoftonline.com'].includes(u.hostname) ? u.href : ''; } catch { return ''; }
  }
  function progressText(p = {}) {
    const parts = [text(p.stage), text(p.name)].filter(Boolean);
    if (Number(p.total) > 0 && Number(p.done) >= 0) parts.push(`${p.done} / ${p.total} items`);
    if (Number(p.total_bytes) > 0) parts.push(`${Number(p.bytes || 0).toLocaleString()} / ${Number(p.total_bytes).toLocaleString()} bytes`);
    else if (Number(p.bytes) > 0) parts.push(`${Number(p.bytes).toLocaleString()} bytes`);
    return parts.join(' · ') || 'Waiting for native progress';
  }
  function hasNativeEnvironment(host) { return !!(host.go?.app?.App || host.runtime || host._wails || host.WailsInvoke || host.chrome?.webview || host.location?.hostname === 'wails.localhost' || host.location?.protocol === 'wails:'); }
  function waitForBindings(host, timeout = 8000, interval = 75) {
    const start = Date.now();
    return new Promise((resolve, reject) => { function check() { if (typeof host.go?.app?.App?.GetState === 'function') return resolve(host.go.app.App); if (Date.now() - start >= timeout) return reject(new Error('The desktop connection is unavailable. Retry once the app has started.')); setTimeout(check, interval); } check(); });
  }
  function createPreviewAdapter() {
    const empty = { schema_version: 1, data_dir: '', settings: { java_path: '', client_memory_mb: 4096, curseforge_enabled: false }, servers: [], instances: [], accounts: [], operations: [], processes: [], next_server_port: 25565 };
    const adapter = { GetState: async () => structuredClone(empty) };
    for (const method of ['SaveSettings', 'ChooseJava', 'OpenDataFolder', 'OpenServerFolder', 'OpenInstanceFolder', 'GetMinecraftVersions', 'CreateServer', 'CreateInstance', 'SetProfileVersion', 'InstallProfile', 'StartServer', 'StopServer', 'SendServerCommand', 'StartClient', 'StopClient', 'GetLogs', 'CancelOperation', 'BeginMicrosoftLogin', 'PollMicrosoftLogin', 'CancelMicrosoftLogin', 'SelectAccount', 'RemoveAccount', 'SetCurseForgeKey', 'OpenExternal', 'SearchContent', 'GetContentVersions', 'InstallContent']) adapter[method] = async () => { throw new Error('Only available in the desktop app. Browser preview writes no files.'); };
    return adapter;
  }
  const helpers = { LIMITS, escapeHTML, integerInRange, validateProfile, validateServer, validateSettings, normalizeState, profileBusy, providerAllowed, pollDelay, officialVerificationURL, progressText, hasNativeEnvironment, waitForBindings, createPreviewAdapter };
  if (typeof module !== 'undefined' && module.exports) module.exports = helpers;
  if (!root.document) return;

  function start() {
    const $ = id => document.getElementById(id), esc = escapeHTML, dialog = $('action-dialog');
    let backend, state, preview = false, busy = false, connectionGeneration = 0, stateTimer, refreshPromise;
    let modalGeneration = 0, modalType = '', opener, modalGuard = null, modalLoading = false;
    let authFlow = null, authTimer, logTimer, logsTarget = null, searchResults = [], searchFilter;
    const trackedOperations = new Map();
    const rendered = new Map();
    const ready = () => !!state && !!backend && !preview;
    function message(id, value) { $(id).textContent = value; $(id).hidden = !value; }
    function errorText(e) { return e instanceof Error ? e.message : (typeof e === 'string' ? e : 'Something went wrong. Please retry.'); }
    function fail(e, target = dialog.open ? 'dialog-error' : 'global-error') { message(target, errorText(e)); }
    function patch(id, html) {
      const node = $(id); if (rendered.get(id) === html) return; rendered.set(id, html);
      const key = node.contains(document.activeElement) ? document.activeElement.dataset.focus : null;
      node.innerHTML = html;
      if (key) Array.from(node.querySelectorAll('[data-focus]')).find(e => e.dataset.focus === key)?.focus({ preventScroll: true });
    }
    function nativeButton(label, action, id, kind, disabled = false, primary = false) {
      return `<button class="button${primary ? ' primary' : ''}" data-action="${action}" data-id="${esc(id)}" data-kind="${kind}" data-focus="${esc(action + ':' + kind + ':' + id)}"${!ready() || busy || disabled ? ' disabled' : ''}>${label}</button>`;
    }
    function syncControls() {
      document.querySelectorAll('[data-native]').forEach(el => { el.disabled = !ready() || busy; el.title = preview ? 'Only available in the desktop app' : ''; });
      $('cf-enabled').disabled = !ready() || busy || !state?.curseforge_configured;
      if (!state?.curseforge_configured) $('cf-enabled').checked = false;
      $('remove-key').disabled = !ready() || busy || !state?.curseforge_configured;
      $('cf-option').disabled = !state || !providerAllowed(state, 'curseforge');
      if ($('cf-option').disabled && $('provider').value === 'curseforge') $('provider').value = 'modrinth';
      $('sign-in').disabled = !ready() || busy || !!authFlow;
      $('save-settings').textContent = busy && document.activeElement?.id === 'save-settings' ? 'Saving…' : 'Save settings';
      if (dialog.open) {
        dialog.setAttribute('aria-busy', String(busy || modalLoading));
        dialog.querySelectorAll('[data-submit]').forEach(el => { el.dataset.label ||= el.textContent; el.textContent = busy ? 'Working…' : el.dataset.label; el.disabled = busy || modalLoading || !ready() || (modalGuard && !modalGuard()); });
        dialog.querySelectorAll('[data-edit]').forEach(el => { el.disabled = busy || !!el.closest('[hidden]'); });
      }
      if ($('send-command')) $('send-command').disabled = !ready() || busy || processFor(state, 'server', logsTarget?.id)?.status !== 'running';
      renderLists();
    }
    function profileCard(p, kind, index) {
      const process = processFor(state, kind, p.id), working = state.operations.some(o => o.target_id === p.id && o.state === 'running');
      const active = processActive(process), blocked = profileBusy(state, kind, p.id), supported = loaderValid(p.loader);
      const status = working ? 'Installing / updating' : active ? process.status : p.installed ? 'Installed · Stopped' : 'Not installed';
      let actions = '';
      if (!p.installed) {
        actions += nativeButton(p.version ? 'Change version' : 'Choose version', 'version', p.id, kind, blocked);
        actions += nativeButton('Install', 'install', p.id, kind, blocked || !p.version || !supported, true);
      } else if (active) actions += nativeButton('Stop', 'stop', p.id, kind, process.status !== 'running');
      else actions += nativeButton(kind === 'server' ? 'Start server' : 'Play', 'start', p.id, kind, blocked || !supported || (kind === 'client' && !state.accounts.some(a => a.id === state.selected_account_id)), true);
      actions += nativeButton('Logs', 'logs', p.id, kind) + nativeButton('Open folder', 'folder', p.id, kind);
      return `<article class="server-card" style="--stagger:${Math.min(index, 5) * 35}ms"><div class="server-card-header"><div class="card-name"><h2>${esc(p.name)}</h2><span class="status ${active ? 'running' : ''}">${esc(status)}</span></div><span class="configured">${esc(p.loader.toUpperCase())}</span></div><dl><div><dt>Minecraft</dt><dd>${esc(p.version || 'Choose a version')}${p.loader_version ? ' · Loader ' + esc(p.loader_version) : ''}</dd></div>${kind === 'server' ? `<div><dt>Port</dt><dd>${esc(p.port)}</dd></div><div><dt>Memory</dt><dd>${esc(p.memory_mb)} MiB</dd></div>` : ''}</dl>${!supported ? '<p class="error">Unsupported loader. Only Vanilla and Fabric can run.</p>' : ''}${kind === 'client' && p.installed && !state.selected_account_id ? '<p>Sign in and select a Microsoft account to play.</p>' : ''}${process?.status === 'stopped' && process.exit_code != null ? `<p>Last exit code: ${esc(process.exit_code)}. Check logs for details.</p>` : ''}<div class="inline-actions">${actions}</div></article>`;
    }
    function renderLists() {
      if (!state) return;
      for (const [kind, key, id] of [['client', 'instances', 'instance-list'], ['server', 'servers', 'server-list']]) patch(id, state[key].length ? state[key].map((p, i) => profileCard(p, kind, i)).join('') : `<div class="empty-state"><h2>${kind === 'client' ? 'Your next world starts here.' : 'No servers yet.'}</h2><p>${kind === 'client' ? 'New profile → Install → Play. Vanilla or Fabric, in a folder of its own.' : 'Create a server profile or install a server modpack.'}</p></div>`);
      patch('account-list', (state.accounts_error ? `<div class="notice error">${esc(state.accounts_error)}</div>` : '') + (state.accounts.length ? state.accounts.map(a => `<div class="account-row${a.id === state.selected_account_id ? ' selected' : ''}"><strong>${esc(a.name)}</strong>${a.id === state.selected_account_id ? '<span class="configured">Selected</span>' : ''}<div class="inline-actions">${nativeButton('Select', 'select-account', a.id, 'account', a.id === state.selected_account_id)}${nativeButton('Sign out', 'remove-account', a.id, 'account')}</div></div>`).join('') : '<div class="account-row"><p>No account connected. Microsoft sign-in is required to play; there is no offline mode.</p></div>'));
      $('operations-panel').hidden = !state.operations.length;
      patch('operation-list', state.operations.map(o => `<article class="operation"><div><strong>${esc(o.label || o.kind)}</strong><p>${esc(o.state)} · ${esc(progressText(o.progress))}</p>${o.state === 'running' && Number(o.progress.total) > 0 ? `<progress aria-label="${esc(o.label)} progress" max="${Number(o.progress.total)}" value="${Math.max(0, Number(o.progress.done) || 0)}"></progress>` : ''}${o.error ? `<p class="error">${esc(o.error)}</p>` : ''}${['failed', 'cancelled'].includes(o.state) ? '<p>Review the error, then retry from the profile or content page.</p>' : ''}</div>${o.state === 'running' ? nativeButton('Cancel', 'cancel-operation', o.id, 'operation') : ''}</article>`).join(''));
    }
    function applyState(next, settings = false) {
      state = normalizeState(next);
      for (const [id, operation] of trackedOperations) { if (state.operations.some(o => o.id === id)) trackedOperations.delete(id); else state.operations.push(operation); }
      $('connection-panel').hidden = true; $('browser-banner').hidden = !preview;
      $('workspace-mode').textContent = preview ? 'Read-only browser preview' : 'Local workspace · Native connection';
      $('data-path').textContent = preview ? 'Browser preview has no data folder.' : state.data_dir;
      $('key-status').textContent = state.curseforge_configured ? 'API key configured. Enable requests above to use CurseForge.' : 'No API key configured. Modrinth needs no key.';
      if (settings) { $('java-path').value = state.settings.java_path; $('client-memory').value = state.settings.client_memory_mb; $('cf-enabled').checked = state.settings.curseforge_enabled; }
      syncControls(); scheduleState();
    }
    function scheduleState() {
      clearTimeout(stateTimer);
      if (ready() && !busy && (state.operations.some(o => o.state === 'running') || state.processes.some(processActive))) stateTimer = setTimeout(() => { refresh().catch(e => { fail(e, 'global-error'); scheduleState(); }); }, 1500);
    }
    async function refresh(settings = false) {
      if (!backend) return;
      if (refreshPromise) { await refreshPromise; if (settings) applyState(state, true); return; }
      refreshPromise = (async () => { applyState(await backend.GetState(), settings); })();
      try { await refreshPromise; } finally { refreshPromise = null; }
    }
    async function action(task, { secret = false, success = '' } = {}) {
      if (!ready() || busy) return false;
      busy = true; message('global-error', ''); message('dialog-error', ''); message('notice', ''); syncControls();
      let completed = false;
      try { if (refreshPromise) await refreshPromise; clearTimeout(stateTimer); await task(); completed = true; await refresh(); if (success) message('notice', success); return true; }
      catch (e) { fail(completed ? new Error('Request accepted, but status could not be refreshed. Use Refresh status before retrying the action.') : secret ? new Error('The secure request failed. Please retry in the desktop app.') : e, completed ? 'global-error' : undefined); return completed; }
      finally { busy = false; syncControls(); scheduleState(); }
    }
    async function stateAction(method, ...args) { applyState(await backend[method](...args)); }
    async function operationAction(method, ...args) {
      const op = await backend[method](...args);
      if (!op?.id) throw new Error('No operation ID returned. Refresh status before retrying.');
      trackedOperations.set(op.id, { ...op, progress: op.progress || {} });
      // This is confirmed native state, not optimistic download progress.
      if (!state.operations.some(o => o.id === op.id)) state.operations.push({ ...op, progress: op.progress || {} });
    }
    function showView(name) {
      document.querySelectorAll('.view').forEach(v => { v.hidden = v.id !== 'view-' + name; });
      document.querySelectorAll('[data-view]').forEach(b => { b.classList.toggle('selected', b.dataset.view === name); if (b.dataset.view === name) b.setAttribute('aria-current', 'page'); else b.removeAttribute('aria-current'); });
      $(name + '-title').focus({ preventScroll: true }); $('main-content').scrollTop = 0;
    }
    document.querySelectorAll('[data-view]').forEach(b => b.addEventListener('click', () => showView(b.dataset.view)));
    function openModal(title, html, type = 'form') {
      if (dialog.open) return false;
      if (modalType) cleanupModal();
      opener = document.activeElement; modalGeneration++; modalType = type; modalGuard = null; modalLoading = false;
      $('dialog-title').textContent = title; $('dialog-body').innerHTML = html; message('dialog-error', ''); dialog.classList.toggle('log-dialog', type === 'logs'); dialog.showModal();
      ($('dialog-body').querySelector('input:not(:disabled), select:not(:disabled), button:not(:disabled), a') || $('dialog-close')).focus();
      return true;
    }
    function closeModal(force = false) { if (busy && !force && !['auth', 'logs'].includes(modalType)) return; dialog.close(); cleanupModal(); }
    async function cancelAuth() {
      clearTimeout(authTimer); const old = authFlow; authFlow = null;
      if (old && ready()) { try { await backend.CancelMicrosoftLogin(old.flow_id); } catch { message('global-error', 'Could not confirm sign-in cancellation. The device code will expire.'); } }
    }
    function cleanupModal() {
      if (dialog.open || !modalType) return; // Ignore a queued close event for a newer modal.
      modalType = ''; modalGeneration++; clearTimeout(logTimer); logsTarget = null; void cancelAuth(); modalGuard = null; modalLoading = false; $('dialog-body').textContent = '';
      const target = opener?.isConnected ? opener : document.querySelector('.nav-item.selected'); requestAnimationFrame(() => { if (!dialog.open) target?.focus({ preventScroll: true }); }); syncControls();
    }
    dialog.addEventListener('close', cleanupModal);
    dialog.addEventListener('cancel', e => { if (busy && !['auth', 'logs'].includes(modalType)) e.preventDefault(); });
    $('dialog-close').addEventListener('click', () => closeModal());
    dialog.addEventListener('keydown', e => {
      if (e.key !== 'Tab') return;
      const controls = Array.from(dialog.querySelectorAll('button, input, select, a, [tabindex="0"]')).filter(el => !el.disabled && el.getClientRects().length);
      const first = controls[0], last = controls[controls.length - 1];
      if (e.shiftKey && document.activeElement === first) { e.preventDefault(); last?.focus(); } else if (!e.shiftKey && document.activeElement === last) { e.preventDefault(); first?.focus(); }
    });
    const field = (id, label, attrs = '') => `<div class="field"><label for="${id}">${label}</label><input id="${id}" data-edit ${attrs}></div>`;
    const select = (id, label, options) => `<div class="field"><label for="${id}">${label}</label><select id="${id}" data-edit>${options}</select></div>`;
    const options = items => items.map(v => `<option value="${esc(v.id)}">${esc(v.name || v.id)}</option>`).join('');
    const loaders = '<option value="vanilla">Vanilla</option><option value="fabric">Fabric</option>';
    const serverFields = () => `<div class="field-row">${field('profile-port', 'Server port', `type="number" min="1024" max="65535" step="1" required value="${state.next_server_port}"`)}${field('profile-memory', 'Server memory · MiB', 'type="number" min="512" max="32768" step="1" required value="2048"')}</div>`;
    const submit = label => `<div class="dialog-actions"><button type="submit" class="button primary" data-submit>${label}</button></div>`;
    function profile(kind, id) { return state[kind === 'server' ? 'servers' : 'instances'].find(p => p.id === id); }
    function guardProfile(kind, id, installed) {
      const p = profile(kind, id);
      if (!p || profileBusy(state, kind, id)) throw new Error('This profile is busy or running. Wait until it stops before changing it.');
      if (installed !== undefined && p.installed !== installed) throw new Error('The profile changed. Close this dialog and try again.');
      return p;
    }
    function profileModal(kind, id) {
      if (!ready() || busy) return;
      const p = id ? profile(kind, id) : null;
      if (p && (p.installed || profileBusy(state, kind, id))) return;
      openModal(p ? 'Choose version & loader' : kind === 'server' ? 'New server profile' : 'New game profile', `<p class="dialog-description">${p ? 'Version and loader can only change before installation.' : 'Create a separate profile, then install its game files.'} Vanilla and Fabric only.</p><form id="profile-form">${!p ? field('profile-name', 'Profile name', 'required maxlength="120" autocomplete="off" placeholder="e.g. Weekend skyblock"') : ''}${select('profile-version', 'Minecraft version', '<option value="">Loading versions…</option>')}${select('profile-loader', 'Loader', loaders)}${kind === 'server' && !p ? serverFields() : ''}<button id="retry-versions" class="button small" type="button" hidden>Retry versions</button>${submit(p ? 'Save version' : 'Create profile')}</form>`);
      const generation = modalGeneration;
      $('profile-loader').value = p?.loader || 'vanilla';
      modalGuard = () => !!$('profile-version')?.value;
      async function loadVersions() {
        modalLoading = true; syncControls();
        try { const versions = await backend.GetMinecraftVersions(); if (generation !== modalGeneration) return; $('profile-version').innerHTML = '<option value="">Choose a version</option>' + options((versions || []).map(v => ({ id: v.id, name: `${v.id} · ${v.type}` }))); if (p?.version && versions.some(v => v.id === p.version)) $('profile-version').value = p.version; $('retry-versions').hidden = true; message('dialog-error', ''); }
        catch (e) { if (generation === modalGeneration) { fail(e); $('retry-versions').hidden = false; } }
        finally { if (generation === modalGeneration) { modalLoading = false; syncControls(); } }
      }
      $('retry-versions').onclick = loadVersions; $('profile-version').onchange = syncControls;
      $('profile-form').onsubmit = async e => {
        e.preventDefault(); if (modalLoading) return;
        const result = validateProfile({ name: p?.name || $('profile-name').value, version: $('profile-version').value, loader: $('profile-loader').value, port: p?.port || $('profile-port')?.value, memory_mb: p?.memory_mb || $('profile-memory')?.value }, kind);
        if (!result.valid) { fail(new Error(Object.values(result.errors)[0])); return; }
        const ok = await action(async () => { if (p) { guardProfile(kind, id, false); await stateAction('SetProfileVersion', kind, id, result.value.version, result.value.loader); } else await stateAction(kind === 'server' ? 'CreateServer' : 'CreateInstance', result.value); }, { success: p ? 'Version saved. Ready to install.' : 'Profile created. Install its game files when ready.' });
        if (ok) closeModal(true);
      };
      void loadVersions();
    }
    $('add-instance').onclick = () => profileModal('client'); $('add-server').onclick = () => profileModal('server');
    function confirmation(title, body, label, task, guard = null) {
      openModal(title, `<form id="confirm-form">${body}${submit(label)}</form>`);
      modalGuard = guard; syncControls();
      $('confirm-form').onsubmit = async e => { e.preventDefault(); if (guard && !guard()) return; if (await action(task)) closeModal(true); };
    }
    function startProfile(kind, id) {
      const p = profile(kind, id); if (!p || !p.installed || profileBusy(state, kind, id)) return;
      if (kind === 'server') {
        confirmation('Start ' + p.name, '<p>Running this server requires agreement to the Minecraft EULA.</p><p><a href="https://www.minecraft.net/eula" data-external>Read the official Minecraft EULA ↗</a></p><label class="check"><input id="accept-eula" type="checkbox">I have read and agree to the Minecraft EULA.</label>', 'Accept EULA & start server', async () => { guardProfile(kind, id, true); if (!$('accept-eula').checked) throw new Error('Please review and accept the EULA.'); await stateAction('StartServer', id, true); }, () => $('accept-eula')?.checked && !profileBusy(state, kind, id));
        $('accept-eula').onchange = syncControls;
      } else void action(async () => { guardProfile(kind, id, true); if (!state.selected_account_id) throw new Error('Sign in and select your Microsoft account first.'); await stateAction('StartClient', id); });
    }
    function stopProfile(kind, id) {
      if (kind === 'server') void action(() => stateAction('StopServer', id));
      else confirmation('Stop Minecraft?', '<p>Force-stopping the client can lose unsaved progress. Save and quit in Minecraft whenever possible.</p>', 'Stop client · may lose progress', () => stateAction('StopClient', id));
    }
    async function beginLogin() {
      if (!ready() || busy || dialog.open) return;
      openModal('Sign in with Microsoft', '<p role="status">Requesting a secure device code…</p>', 'auth');
      const generation = modalGeneration;
      try {
        const flow = await backend.BeginMicrosoftLogin();
        if (generation !== modalGeneration || !dialog.open) { await backend.CancelMicrosoftLogin(flow.flow_id); return; }
        const url = officialVerificationURL(flow.verification_uri);
        if (!url || !flow.flow_id || !flow.user_code) { if (flow.flow_id) await backend.CancelMicrosoftLogin(flow.flow_id); throw new Error('Invalid device sign-in response.'); }
        authFlow = { flow_id: flow.flow_id, interval: pollDelay(flow.poll_interval_sec), expires: Date.parse(flow.expires_at) };
        $('dialog-body').innerHTML = `<p>Open the official Microsoft page and enter this code. Never share it.</p><code class="device-code">${esc(flow.user_code)}</code><p><a href="${esc(url)}" data-external>${esc(url)} ↗</a></p><p id="auth-status" class="field-help" role="status">Waiting for Microsoft approval…</p><p class="field-help">Close this dialog to cancel. No passwords or tokens are collected here.</p>`;
        $('dialog-body').querySelector('a').focus();
        scheduleAuth(generation); syncControls();
      } catch { if (generation === modalGeneration) { fail(new Error('Could not begin Microsoft sign-in. Close this dialog and try again.')); } }
    }
    function scheduleAuth(generation) { clearTimeout(authTimer); if (authFlow && generation === modalGeneration) authTimer = setTimeout(() => pollAuth(generation), authFlow.interval); }
    async function pollAuth(generation) {
      const flow = authFlow; if (!flow || generation !== modalGeneration || !dialog.open) return;
      try {
        if (Number.isFinite(flow.expires) && Date.now() >= flow.expires) { await cancelAuth(); if (generation === modalGeneration) message('auth-status', 'Code expired. Close this dialog and sign in again.'); return; }
        const result = await backend.PollMicrosoftLogin(flow.flow_id);
        if (generation !== modalGeneration || flow !== authFlow) return;
        if (result.status === 'complete') { authFlow = null; await refresh(); if (generation === modalGeneration) closeModal(true); message('notice', 'Microsoft sign-in complete. Your account is ready.'); return; }
        if (['expired', 'denied'].includes(result.status)) { await cancelAuth(); if (generation === modalGeneration) message('auth-status', result.status === 'expired' ? 'Code expired. Close and sign in again.' : 'Sign-in was denied. Close and try again when ready.'); return; }
        if (!['pending', 'slow_down'].includes(result.status)) throw new Error('Unknown authentication status');
        flow.interval = result.poll_interval_sec ? pollDelay(result.poll_interval_sec) : result.status === 'slow_down' ? flow.interval + 5000 : flow.interval;
        message('auth-status', result.status === 'slow_down' ? 'Microsoft asked us to wait a little longer…' : 'Waiting for Microsoft approval…'); scheduleAuth(generation);
      } catch { if (generation === modalGeneration) { await cancelAuth(); if (generation === modalGeneration) fail(new Error('Sign-in could not be completed. Close this dialog and try again.')); } }
    }
    $('sign-in').onclick = beginLogin;
    function openLogs(kind, id) {
      if (!ready() || busy) return;
      openModal('Logs · ' + profile(kind, id).name, `<p id="log-status" role="status">Loading native logs…</p><pre id="logs" class="logs" tabindex="0" aria-label="Process logs"></pre>${kind === 'server' ? `<form id="command-form"><label for="server-command">Server command</label><div class="input-action"><input id="server-command" autocomplete="off" placeholder="e.g. list" maxlength="4096" required><button id="send-command" class="button">Send</button></div><p class="field-help">Commands are available only while the server is running.</p></form>` : ''}`, 'logs');
      const generation = modalGeneration; logsTarget = { kind, id };
      async function loadLogs() {
        try { const lines = await backend.GetLogs(kind, id); if (generation !== modalGeneration) return; const node = $('logs'), bottom = node.scrollHeight - node.clientHeight - node.scrollTop < 40; node.textContent = (lines || []).join('\n'); if (bottom) node.scrollTop = node.scrollHeight; $('log-status').textContent = lines?.length ? `${lines.length} lines · updates every second while open` : 'No log output yet.'; }
        catch { if (generation === modalGeneration) $('log-status').textContent = 'Could not load logs. Retrying…'; }
        finally { if (generation === modalGeneration && dialog.open) logTimer = setTimeout(loadLogs, 1000); }
      }
      if ($('command-form')) $('command-form').onsubmit = async e => { e.preventDefault(); const command = $('server-command').value.trim(); if (!command || processFor(state, 'server', id)?.status !== 'running') return; const ok = await action(() => backend.SendServerCommand(id, command), { success: 'Command sent to the server.' }); if (ok && generation === modalGeneration) $('server-command').value = ''; };
      syncControls(); void loadLogs();
    }
    document.addEventListener('click', e => {
      const link = e.target.closest('[data-external]');
      if (link) { e.preventDefault(); if (ready()) backend.OpenExternal(link.href).catch(() => fail(new Error('Could not open the official link. Try again.'))); return; }
      const b = e.target.closest('[data-action]'); if (!b || b.disabled || !ready() || busy) return;
      const { action: act, id, kind } = b.dataset;
      if (act === 'version') profileModal(kind, id);
      if (act === 'install') void action(async () => { const p = guardProfile(kind, id, false); if (!p.version || !loaderValid(p.loader)) throw new Error('Choose a supported version and loader first.'); await operationAction('InstallProfile', kind, id); });
      if (act === 'start') startProfile(kind, id);
      if (act === 'stop') stopProfile(kind, id);
      if (act === 'logs') openLogs(kind, id);
      if (act === 'folder') void action(() => backend[kind === 'server' ? 'OpenServerFolder' : 'OpenInstanceFolder'](id), { success: 'Folder open request sent.' });
      if (act === 'cancel-operation') void action(() => backend.CancelOperation(id));
      if (act === 'select-account') void action(() => stateAction('SelectAccount', id));
      if (act === 'remove-account') confirmation('Sign out on this device?', '<p>This removes the saved account from NeoLauncher. Your worlds are kept.</p>', 'Sign out', async () => { await cancelAuth(); await stateAction('RemoveAccount', id); });
      if (act === 'content-install') contentModal(searchResults[Number(id)]);
    });
    $('settings-form').onsubmit = async e => {
      e.preventDefault(); const result = validateSettings({ java_path: $('java-path').value, client_memory_mb: $('client-memory').value, curseforge_enabled: $('cf-enabled').checked && state?.curseforge_configured });
      if (!result.valid) { fail(new Error(Object.values(result.errors)[0])); return; }
      await action(async () => applyState(await backend.SaveSettings(result.value), true), { success: 'Settings saved on this device.' });
    };
    $('browse-java').onclick = () => { const memory = $('client-memory').value, cf = $('cf-enabled').checked; void action(async () => { applyState(await backend.ChooseJava(), true); $('client-memory').value = memory; $('cf-enabled').checked = cf && state.curseforge_configured; }, { success: 'Java selection updated. Save any other preference changes.' }); };
    $('open-data').onclick = () => action(() => backend.OpenDataFolder(), { success: 'Folder open request sent.' });
    $('refresh-state').onclick = () => action(() => refresh());
    $('key-form').onsubmit = async e => {
      e.preventDefault(); if (!ready() || busy) return;
      let key = $('cf-key').value.trim(); $('cf-key').value = '';
      if (!key) { fail(new Error('Enter an API key, or use Remove key.')); return; }
      try { await action(async () => applyState(await backend.SetCurseForgeKey(key)), { secret: true, success: 'Key saved. Enable CurseForge requests and save settings when ready.' }); } finally { key = ''; $('cf-key').value = ''; }
    };
    $('remove-key').onclick = () => action(async () => { $('cf-key').value = ''; applyState(await backend.SetCurseForgeKey('')); $('cf-enabled').checked = false; }, { secret: true, success: 'Key removed. CurseForge is disabled.' });

    function contentGate(provider) { if (!providerAllowed(state, provider)) throw new Error('CurseForge is disabled. Configure a key and enable requests in Settings.'); }
    $('search-form').onsubmit = async e => {
      e.preventDefault();
      const filter = { provider: $('provider').value, kind: $('content-kind').value, query: $('search-query').value.trim(), game_version: $('content-version').value.trim(), loader: $('content-loader').value, side: $('content-side').value };
      const ok = await action(async () => {
        contentGate(filter.provider); $('search-status').textContent = 'Searching provider…'; $('content-results').innerHTML = ''; searchResults = [];
        const results = await backend.SearchContent(filter); searchFilter = filter;
        searchResults = (results || []).filter(r => r.provider === filter.provider && r.kind === filter.kind);
        $('search-status').textContent = searchResults.length ? `${searchResults.length} results · ${filter.provider} · ${filter.side}` : 'No results. Try a different query, version or loader.';
        $('content-results').innerHTML = searchResults.map((r, i) => `<article class="server-card content-card" style="--stagger:${Math.min(i, 5) * 35}ms"><span class="eyebrow">${esc(r.provider)} / ${esc(r.kind)}</span><h2>${esc(r.name)}</h2><p>${esc(r.description)}</p><button class="button" data-action="content-install" data-id="${i}" data-kind="content">Choose version & install</button></article>`).join('');
      });
      if (!ok && ready()) $('search-status').textContent = 'Search failed. Review the message above, then Search to retry.';
    };
    function contentModal(project) {
      if (!project || !ready() || busy) return;
      try { contentGate(project.provider); } catch (e) { fail(e); return; }
      const pack = project.kind === 'modpack';
      openModal((pack ? 'Install pack · ' : 'Install mod · ') + project.name, `<p class="dialog-description">${pack ? 'Creates a NEW profile. Existing worlds are never overwritten. Minecraft version comes from the pack; only Vanilla / Fabric packs are supported.' : 'Mods require an installed, stopped Fabric profile. Versions are filtered for the target profile.'}</p><form id="content-install-form">${select('target-kind', 'Install for', '<option value="client">Client</option><option value="server">Server</option>')}${pack ? field('pack-name', 'New profile name', `required maxlength="120" value="${esc(project.name)}"`) : select('target-id', 'Installed Fabric profile', '')}<div id="pack-server-fields" hidden>${pack ? serverFields() : ''}</div>${select('content-release', 'Content version', '<option value="">Choose a target first</option>')}<button id="retry-releases" class="button small" type="button">Reload versions</button>${pack ? '<label class="check"><input id="include-optional" type="checkbox" data-edit>Include optional pack files</label>' : ''}<p id="release-hint" class="field-help" role="status"></p>${submit(pack ? 'Install into new profile' : 'Install mod')}</form>`);
      const generation = modalGeneration; let releaseGeneration = 0;
      $('target-kind').value = searchFilter.side;
      function targetValid() { if (pack) return true; const id = $('target-id')?.value, kind = $('target-kind')?.value, p = profile(kind, id); return !!p && p.installed && p.loader === 'fabric' && !profileBusy(state, kind, id); }
      modalGuard = () => !!$('content-release')?.value && targetValid() && providerAllowed(state, project.provider);
      function targets() {
        const kind = $('target-kind').value;
        $('pack-server-fields').hidden = !pack || kind !== 'server';
        if (!pack) { const candidates = state[kind === 'server' ? 'servers' : 'instances'].filter(p => p.installed && p.loader === 'fabric' && !profileBusy(state, kind, p.id)); $('target-id').innerHTML = candidates.length ? options(candidates.map(p => ({ id: p.id, name: p.name + ' · ' + p.version }))) : '<option value="">No available installed Fabric profiles</option>'; }
        void releases();
      }
      async function releases() {
        const request = ++releaseGeneration; modalLoading = true; $('content-release').innerHTML = '<option value="">Loading compatible versions…</option>'; syncControls();
        try {
          contentGate(project.provider);
          if (!targetValid()) { $('content-release').innerHTML = '<option value="">No available target</option>'; $('release-hint').textContent = 'Install a Fabric profile and stop it before adding mods.'; return; }
          const p = pack ? null : profile($('target-kind').value, $('target-id').value);
          const versions = await backend.GetContentVersions({ provider: project.provider, project_id: project.id, game_version: p?.version || searchFilter.game_version, loader: p?.loader || searchFilter.loader });
          if (generation !== modalGeneration || request !== releaseGeneration) return;
          $('content-release').innerHTML = versions?.length ? options(versions) : '<option value="">No compatible versions</option>';
          $('release-hint').textContent = versions?.length ? 'The native installer validates loader and side compatibility before installing.' : 'No compatible releases. Try another target or search filter.'; message('dialog-error', '');
        } catch (e) { if (generation === modalGeneration && request === releaseGeneration) { fail(e); $('content-release').innerHTML = '<option value="">Could not load versions</option>'; } }
        finally { if (generation === modalGeneration && request === releaseGeneration) { modalLoading = false; syncControls(); } }
      }
      $('target-kind').onchange = targets; if (!pack) $('target-id').onchange = releases;
      $('retry-releases').onclick = releases; $('content-release').onchange = syncControls;
      $('content-install-form').onsubmit = async e => {
        e.preventDefault(); if (modalLoading || !modalGuard()) return;
        const kind = $('target-kind').value;
        const input = { provider: project.provider, kind: project.kind, project_id: project.id, version_id: $('content-release').value, target_kind: kind, target_id: pack ? '' : $('target-id').value, name: pack ? $('pack-name').value.trim() : project.name, port: pack && kind === 'server' ? Number($('profile-port').value) : 0, memory_mb: pack && kind === 'server' ? Number($('profile-memory').value) : 0, include_optional: pack && $('include-optional').checked };
        if (pack) { const check = validateProfile({ ...input, version: '', loader: 'vanilla' }, kind, false); if (!check.valid) { fail(new Error(Object.values(check.errors)[0])); return; } }
        const ok = await action(async () => { contentGate(project.provider); if (!pack) { const target = guardProfile(kind, input.target_id, true); if (target.loader !== 'fabric') throw new Error('Mods require Fabric.'); } await operationAction('InstallContent', input); });
        if (ok) closeModal(true);
      };
      targets();
    }
    const windowActions = { minimize: 'WindowMinimise', maximize: 'WindowToggleMaximise', 'close-window': 'Quit' };
    function windowControls() { for (const [id, method] of Object.entries(windowActions)) { $(id).disabled = typeof root.runtime?.[method] !== 'function'; $(id).onclick = async () => { try { await root.runtime?.[method]?.(); } catch (e) { fail(e, 'global-error'); } }; } }
    windowControls();
    const runtimeTimer = setInterval(windowControls, 1000);
    async function connect() {
      const generation = ++connectionGeneration; $('connection-panel').hidden = false; $('connection-message').textContent = 'Connecting to the desktop app…'; $('retry').hidden = true; $('enter-preview').hidden = true;
      try { const native = await waitForBindings(root); if (generation !== connectionGeneration) return; backend = native; preview = false; await refresh(true); }
      catch (e) { if (generation === connectionGeneration) { $('connection-message').textContent = errorText(e); $('retry').hidden = false; $('enter-preview').hidden = hasNativeEnvironment(root); } }
      finally { syncControls(); }
    }
    $('retry').onclick = connect;
    $('enter-preview').onclick = async () => { if (hasNativeEnvironment(root)) { void connect(); return; } connectionGeneration++; backend = createPreviewAdapter(); preview = true; applyState(await backend.GetState(), true); };
    root.addEventListener('pagehide', () => { clearInterval(runtimeTimer); clearTimeout(stateTimer); clearTimeout(logTimer); void cancelAuth(); }, { once: true });
    void connect(); setTimeout(() => { if (!state && !hasNativeEnvironment(root)) $('enter-preview').hidden = false; }, 500);
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start, { once: true }); else start();
})(typeof window !== 'undefined' ? window : globalThis);
