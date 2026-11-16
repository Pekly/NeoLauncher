/* Plain WebView2 frontend. Pure helpers are also exported for Node's test runner. */
(function (root) {
  'use strict';

  const LIMITS = Object.freeze({ minMemory: 512, maxMemory: 32768, minPort: 1024, maxPort: 65535 });
  const CAPABILITIES = Object.freeze({ signIn: false, install: false, play: false, serverLaunch: false });
  const text = value => typeof value === 'string' ? value : '';
  const escapeHTML = value => String(value ?? '').replace(/[&<>"']/g, character => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' })[character]);
  const integerInRange = (value, min, max) => /^\d+$/.test(String(value)) && Number.isSafeInteger(Number(value)) && Number(value) >= min && Number(value) <= max;
  const memoryValid = value => integerInRange(value, LIMITS.minMemory, LIMITS.maxMemory);
  const portValid = value => integerInRange(value, LIMITS.minPort, LIMITS.maxPort);

  function validateServer(input) {
    const name = text(input.name).trim();
    const version = text(input.version).trim();
    const errors = {};
    if (!name || Array.from(name).length > 60 || /[\r\n\u2028\u2029]/.test(text(input.name))) errors.name = 'Enter a name of 1–60 characters, without line breaks.';
    if (version.length > 48 || (version && !/^[A-Za-z0-9._+\-]+$/.test(version))) errors.version = 'Use up to 48 letters, numbers, dots, underscores, + or -.';
    if (!portValid(input.port)) errors.port = 'Enter a whole port number from 1,024 to 65,535.';
    if (!memoryValid(input.memory_mb)) errors.memory_mb = 'Enter whole MiB from 512 to 32,768.';
    return { valid: Object.keys(errors).length === 0, errors, value: { name, version, port: Number(input.port), memory_mb: Number(input.memory_mb) } };
  }

  function validateSettings(input) {
    const errors = {};
    if (!memoryValid(input.client_memory_mb)) errors.client_memory_mb = 'Enter whole MiB from 512 to 32,768.';
    return { valid: Object.keys(errors).length === 0, errors, value: { java_path: text(input.java_path).trim(), client_memory_mb: Number(input.client_memory_mb) } };
  }

  function normalizeState(value) {
    if (!value || value.schema_version !== 1 || !value.settings || !memoryValid(value.settings.client_memory_mb) || typeof value.settings.java_path !== 'string' || typeof value.data_dir !== 'string' || (value.servers != null && !Array.isArray(value.servers))) {
      throw new Error('The desktop app returned an unsupported workspace. Please restart the app.');
    }
    const servers = (value.servers || []).map(server => {
      if (!server || typeof server.id !== 'string' || !server.id || typeof server.name !== 'string' || typeof server.version !== 'string' || typeof server.directory !== 'string' || !portValid(server.port) || !memoryValid(server.memory_mb)) {
        throw new Error('A server profile could not be read. Check your launcher data and retry.');
      }
      return { id: server.id, name: server.name, version: server.version, port: Number(server.port), memory_mb: Number(server.memory_mb), directory: server.directory, created_at: text(server.created_at) };
    });
    return { schema_version: 1, data_dir: value.data_dir, settings: { java_path: value.settings.java_path, client_memory_mb: Number(value.settings.client_memory_mb) }, servers, next_server_port: portValid(value.next_server_port) ? Number(value.next_server_port) : 25565 };
  }

  function hasNativeEnvironment(host) {
    return !!(host.go?.app?.App || host.runtime || host._wails || host.WailsInvoke || host.chrome?.webview || host.location?.hostname === 'wails.localhost' || host.location?.protocol === 'wails:');
  }

  function waitForBindings(host, timeout = 8000, interval = 75) {
    const start = Date.now();
    return new Promise((resolve, reject) => {
      function check() {
        const app = host.go?.app?.App;
        if (typeof app?.GetState === 'function') return resolve(app);
        if (Date.now() - start >= timeout) return reject(new Error('The desktop connection is not available. Retry once the app has started.'));
        setTimeout(check, interval);
      }
      check();
    });
  }

  // Deliberately opt-in, ephemeral and empty. Never writes local files or storage.
  function createPreviewAdapter() {
    let current = { schema_version: 1, data_dir: '', settings: { java_path: '', client_memory_mb: 4096 }, servers: [], next_server_port: 25565 };
    const copy = () => JSON.parse(JSON.stringify(current));
    const unsupported = async () => { throw new Error('This action is only available in the desktop app. Browser preview writes no files.'); };
    return {
      GetState: async () => copy(),
      SaveSettings: async input => {
        const result = validateSettings(input);
        if (!result.valid) throw new Error(Object.values(result.errors)[0]);
        current.settings = result.value;
        return copy();
      },
      CreateServer: async input => {
        const result = validateServer(input);
        if (!result.valid) throw new Error(Object.values(result.errors)[0]);
        if (current.servers.some(server => server.port === result.value.port)) throw new Error('This port is already assigned to a preview profile. Choose another port.');
        current.servers.push({ ...result.value, id: `preview-${current.servers.length + 1}`, directory: '', created_at: new Date().toISOString() });
        let port = 25565;
        while (current.servers.some(server => server.port === port) && port < 65535) port++;
        current.next_server_port = port;
        return copy();
      },
      ChooseJava: unsupported,
      OpenDataFolder: unsupported,
      OpenServerFolder: unsupported
    };
  }

  function serverCardHTML(server, preview, index) {
    const esc = escapeHTML;
    return `<article class="server-card" style="--stagger:${Math.min(index, 5) * 35}ms">
      <div class="server-card-header"><svg aria-hidden="true"><use href="#i-server"/></svg><h2>${esc(server.name)}</h2><span class="configured">Configured</span></div>
      <dl><div><dt>Version label</dt><dd>${server.version ? esc(server.version) : 'Not specified'} <span class="optional">· Not installed</span></dd></div><div><dt>Port</dt><dd>${esc(server.port)}</dd></div><div><dt>Memory</dt><dd>${esc(server.memory_mb)} MiB</dd></div></dl>
      <button class="button open-server-folder" data-server-id="${esc(server.id)}" aria-label="Open folder for ${esc(server.name)}" title="${esc(preview ? 'Unavailable in browser preview — no files written' : server.directory)}"${preview ? ' disabled' : ''}><svg aria-hidden="true"><use href="#i-folder"/></svg>Open folder</button>
    </article>`;
  }

  const helpers = { LIMITS, CAPABILITIES, escapeHTML, integerInRange, validateServer, validateSettings, normalizeState, hasNativeEnvironment, waitForBindings, createPreviewAdapter, serverCardHTML };
  if (typeof module !== 'undefined' && module.exports) module.exports = helpers;
  if (!root.document) return;

  function start() {
    const $ = id => document.getElementById(id);
    let backend = null;
    let state = null;
    let preview = false;
    let busy = null;
    let connectionGeneration = 0;
    let dialogOpener = null;
    let currentView = 'play';
    const dialog = $('server-dialog');
    const serverFields = { name: 'server-name', version: 'server-version', port: 'server-port', memory_mb: 'server-memory' };
    const settingsFields = { client_memory_mb: 'client-memory' };

    function message(id, value) {
      $(id).textContent = value;
      $(id).hidden = !value;
    }
    function clearNotices() { message('notice', ''); message('global-error', ''); }
    function errorText(error) {
      return error instanceof Error ? error.message : (typeof error === 'string' && error ? error : 'Something went wrong. Please try again.');
    }
    function reportError(id, error) {
      message(id, errorText(error));
      $(id).scrollIntoView({ block: 'nearest' });
    }
    function syncControls() {
      const blocked = !state || !!busy;
      for (const id of ['add-server', 'empty-add-server', 'java-path', 'client-memory', 'save-settings']) $(id).disabled = blocked;
      for (const id of ['browse-java', 'open-data']) {
        $(id).disabled = blocked || preview;
        $(id).title = preview ? 'Only available in the desktop app' : '';
      }
      document.querySelectorAll('.open-server-folder').forEach(button => { button.disabled = blocked || preview; });
      dialog.querySelectorAll('input, button').forEach(control => { control.disabled = busy === 'create'; });
      $('save-settings').textContent = busy === 'settings' ? 'Saving…' : 'Save settings';
      $('browse-java').textContent = busy === 'java' ? 'Choosing…' : 'Browse…';
      $('create-server').textContent = busy === 'create' ? 'Creating…' : 'Create profile';
      $('server-form').setAttribute('aria-busy', String(busy === 'create'));
      $('settings-form').setAttribute('aria-busy', String(busy === 'settings' || busy === 'java'));
      // These remain disabled regardless of state or browser adapter.
      $('play-button').disabled = !CAPABILITIES.play;
    }
    function applyState(next, updateSettings = false) {
      state = normalizeState(next);
      $('browser-banner').hidden = !preview;
      $('connection-panel').hidden = true;
      $('servers-loading').hidden = true;
      $('servers-empty').hidden = state.servers.length !== 0;
      $('server-list').innerHTML = state.servers.map((server, index) => serverCardHTML(server, preview, index)).join('');
      $('server-count').textContent = String(state.servers.length);
      $('server-count').hidden = state.servers.length === 0;
      $('data-path').textContent = preview ? 'Browser preview — no data folder or files are created.' : state.data_dir;
      $('workspace-mode').textContent = preview ? 'Browser preview · In-memory only' : 'Local workspace · Early preview';
      $('settings-hint').textContent = preview ? 'Preview only. Resets when this tab reloads.' : 'Local preferences, not an installation.';
      $('create-hint').textContent = preview ? 'Preview only — no files will be written.' : 'Saved to your launcher data folder.';
      $('dialog-description').textContent = preview ? 'Try a profile in this tab. No setup files are written, and no server will run.' : "Create a profile and setup files. This won't download or start a server.";
      if (updateSettings) {
        $('java-path').value = state.settings.java_path;
        $('client-memory').value = state.settings.client_memory_mb;
      }
      syncControls();
    }
    function showView(name, focus = true) {
      if (!['play', 'servers', 'settings'].includes(name)) return;
      currentView = name;
      document.querySelectorAll('.view').forEach(view => { view.hidden = view.id !== `view-${name}`; });
      document.querySelectorAll('[data-view]').forEach(button => {
        const selected = button.dataset.view === name;
        button.classList.toggle('selected', selected);
        if (selected) button.setAttribute('aria-current', 'page'); else button.removeAttribute('aria-current');
      });
      $('main-content').scrollTop = 0;
      if (focus) $(`${name}-title`).focus({ preventScroll: true });
    }
    async function runAction(kind, task, errorTarget = 'global-error') {
      if (!backend || !state || busy) return;
      busy = kind;
      clearNotices();
      message(errorTarget, '');
      syncControls();
      try { await task(); }
      catch (error) { reportError(errorTarget, error); }
      finally { busy = null; syncControls(); }
    }
    function displayErrors(fields, errors) {
      let first = null;
      for (const [key, id] of Object.entries(fields)) {
        const invalid = !!errors[key];
        $(id).setAttribute('aria-invalid', String(invalid));
        message(`${id}-error`, errors[key] || '');
        if (invalid && !first) first = $(id);
      }
      first?.focus();
    }
    function openServerDialog(event) {
      if (!state || busy) return;
      dialogOpener = event.currentTarget;
      $('server-form').reset();
      $('server-port').value = state.next_server_port;
      displayErrors(serverFields, {});
      message('dialog-error', '');
      dialog.showModal();
      $('server-name').focus();
    }
    function closeServerDialog() { if (busy !== 'create') dialog.close(); }
    dialog.addEventListener('cancel', event => { if (busy === 'create') event.preventDefault(); });
    dialog.addEventListener('keydown', event => {
      if (event.key !== 'Tab') return;
      const controls = Array.from(dialog.querySelectorAll('button, input')).filter(control => !control.disabled && control.getClientRects().length);
      const first = controls[0];
      const last = controls[controls.length - 1];
      if (!first) { event.preventDefault(); return; }
      if (event.shiftKey && document.activeElement === first) { event.preventDefault(); last.focus(); }
      else if (!event.shiftKey && document.activeElement === last) { event.preventDefault(); first.focus(); }
    });
    dialog.addEventListener('close', () => {
      // The empty-state opener disappears after the first profile is created.
      const target = dialogOpener && dialogOpener.getClientRects().length ? dialogOpener : $('add-server');
      requestAnimationFrame(() => target.focus());
    });
    $('add-server').addEventListener('click', openServerDialog);
    $('empty-add-server').addEventListener('click', openServerDialog);
    $('dialog-close').addEventListener('click', closeServerDialog);
    $('cancel-server').addEventListener('click', closeServerDialog);
    document.querySelectorAll('[data-view], [data-go]').forEach(button => {
      const view = button.dataset.view || button.dataset.go;
      if (button.dataset.view) { button.setAttribute('aria-label', view[0].toUpperCase() + view.slice(1)); button.title = view[0].toUpperCase() + view.slice(1); }
      button.addEventListener('click', () => showView(view));
    });
    for (const fields of [serverFields, settingsFields]) {
      for (const id of Object.values(fields)) $(id).addEventListener('input', () => {
        $(id).removeAttribute('aria-invalid');
        message(`${id}-error`, '');
      });
    }
    $('server-form').addEventListener('submit', event => {
      event.preventDefault();
      if (busy || !state) return;
      const result = validateServer(Object.fromEntries(Object.entries(serverFields).map(([key, id]) => [key, $(id).value])));
      displayErrors(serverFields, result.errors);
      if (!result.valid) return;
      void runAction('create', async () => {
        applyState(await backend.CreateServer(result.value));
        dialog.close();
        message('notice', preview ? 'Preview profile added. No files written.' : 'Server profile configured. Setup files are saved; no server is running.');
      }, 'dialog-error');
    });
    $('settings-form').addEventListener('submit', event => {
      event.preventDefault();
      if (busy || !state) return;
      const result = validateSettings({ java_path: $('java-path').value, client_memory_mb: $('client-memory').value });
      displayErrors(settingsFields, result.errors);
      if (!result.valid) return;
      void runAction('settings', async () => {
        applyState(await backend.SaveSettings(result.value), true);
        message('notice', preview ? 'Preview settings updated. No files written.' : 'Settings saved on this device.');
      });
    });
    $('browse-java').addEventListener('click', () => {
      if (preview) return;
      const memoryDraft = $('client-memory').value;
      const previousPath = state?.settings.java_path;
      const pathDraft = $('java-path').value;
      void runAction('java', async () => {
        applyState(await backend.ChooseJava(), true);
        $('client-memory').value = memoryDraft;
        if (state.settings.java_path === previousPath) {
          $('java-path').value = pathDraft;
          message('notice', 'Java selection unchanged.');
        } else message('notice', 'Java path updated. Any other edits still need to be saved.');
      });
    });
    async function openFolder(button, method, id) {
      if (preview) return;
      await runAction('folder', async () => {
        const original = button.innerHTML;
        button.textContent = 'Opening…';
        try {
          await (id === undefined ? backend[method]() : backend[method](id));
          message('notice', 'Folder open request sent to Windows.');
        } finally { button.innerHTML = original; }
      });
    }
    $('open-data').addEventListener('click', event => { void openFolder(event.currentTarget, 'OpenDataFolder'); });
    $('server-list').addEventListener('click', event => {
      const button = event.target.closest('.open-server-folder');
      if (!button || button.disabled || !state?.servers.some(server => server.id === button.dataset.serverId)) return;
      void openFolder(button, 'OpenServerFolder', button.dataset.serverId);
    });

    const windowActions = { minimize: 'WindowMinimise', maximize: 'WindowToggleMaximise', 'close-window': 'Quit' };
    function refreshWindowControls() {
      for (const [id, method] of Object.entries(windowActions)) $(id).disabled = typeof root.runtime?.[method] !== 'function';
      if (hasNativeEnvironment(root)) $('enter-preview').hidden = true;
    }
    for (const [id, method] of Object.entries(windowActions)) $(id).addEventListener('click', async () => {
      if (typeof root.runtime?.[method] !== 'function') return;
      $(id).disabled = true;
      try { await root.runtime[method](); }
      catch (error) { reportError('global-error', error); }
      finally { refreshWindowControls(); }
    });
    refreshWindowControls();
    const runtimeTimer = setInterval(() => {
      refreshWindowControls();
      if (Object.values(windowActions).every(method => typeof root.runtime?.[method] === 'function')) clearInterval(runtimeTimer);
    }, 250);
    root.addEventListener('pagehide', () => clearInterval(runtimeTimer), { once: true });

    async function connect() {
      const generation = ++connectionGeneration;
      busy = 'connect';
      $('connection-panel').hidden = false;
      $('connection-message').textContent = 'Connecting to the desktop app…';
      $('retry').hidden = true;
      $('enter-preview').hidden = true;
      clearNotices();
      syncControls();
      try {
        const native = await waitForBindings(root);
        if (generation !== connectionGeneration) return;
        backend = native;
        preview = false;
        $('enter-preview').hidden = true;
        $('connection-message').textContent = 'Loading your local workspace…';
        const next = await backend.GetState();
        if (generation !== connectionGeneration) return;
        applyState(next, true);
      } catch (error) {
        if (generation !== connectionGeneration) return;
        $('connection-message').textContent = errorText(error);
        $('retry').hidden = false;
        $('enter-preview').hidden = hasNativeEnvironment(root);
      } finally {
        if (generation === connectionGeneration) { busy = null; syncControls(); }
      }
    }
    $('retry').addEventListener('click', () => { void connect(); });
    $('enter-preview').addEventListener('click', async () => {
      // A late native binding always wins over browser mode. Never auto-fallback.
      if (hasNativeEnvironment(root)) { void connect(); return; }
      ++connectionGeneration;
      busy = 'connect';
      $('enter-preview').disabled = true;
      try {
        backend = createPreviewAdapter();
        preview = true;
        applyState(await backend.GetState(), true);
        showView(currentView);
      } catch (error) { reportError('global-error', error); }
      finally { busy = null; $('enter-preview').disabled = false; syncControls(); }
    });
    void connect();
    // Offer an explicit browser-only escape hatch while continuing to await Wails.
    setTimeout(() => {
      if (!state && !hasNativeEnvironment(root)) $('enter-preview').hidden = false;
    }, 1200);
  }

  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', start, { once: true });
  else start();
})(typeof window !== 'undefined' ? window : globalThis);
