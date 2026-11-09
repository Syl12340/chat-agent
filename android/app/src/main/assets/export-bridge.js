(() => {
  const marker = __CHAT_MARKER__;
  const receive = event => {
    if (event.data !== marker || event.ports.length !== 1 || (event.source !== null && event.source !== window)) return;
    if (event.origin && event.origin !== 'null' && event.origin !== location.origin) return;
    window.removeEventListener('message', receive);
    const port = event.ports[0];
    let sequence = 0;
    const pending = new Map();
    port.onmessage = event => {
      try {
        const reply = JSON.parse(event.data), item = pending.get(reply.id);
        if (!item) return;
        pending.delete(reply.id); clearTimeout(item.timer);
        if (reply.ok) item.resolve(reply); else item.reject(new Error('本机操作未完成，请检查设置后重试'));
      } catch {}
    };
    const request = (type, payload = {}, timeout = 5000) => new Promise((resolve, reject) => {
      const id = String(++sequence);
      const timer = setTimeout(() => { pending.delete(id); reject(new Error('本机操作未响应')); }, timeout);
      pending.set(id, { resolve, reject, timer });
      try { port.postMessage(JSON.stringify({ ...payload, type, marker, id })); }
      catch (error) { pending.delete(id); clearTimeout(timer); reject(error); }
    });
    Object.defineProperty(window, 'ChatAndroidVoice', { configurable: true, value: Object.freeze({
      status: () => request('voice_status'),
      recognize: () => request('voice_recognize', {}, 120000)
    }) });
    Object.defineProperty(window, 'ChatAndroidAuth', { configurable: true, value: Object.freeze({
      restore: () => request('auth_restore').then(reply => reply.token || ''),
      save: token => request('auth_save', { token }),
      forget: () => request('auth_forget')
    }) });
    Object.defineProperty(window, 'ChatAndroidPreferences', { configurable: true, value: Object.freeze({
      load: scope => request('preferences_load', {scope}).then(reply => reply.value || null),
      save: (scope,value) => request('preferences_save', {scope,value}).then(() => undefined)
    }) });
    Object.defineProperty(window, 'ChatAndroidDevice', { configurable: true, value: Object.freeze({
      identity: () => request('device_identity', {}, 30000).then(({ device_id, public_key, name, suspended }) => ({ device_id, public_key, name, suspended })),
      sign(payloadBase64) {
        if (typeof payloadBase64 !== 'string' || payloadBase64.length > 2732) return Promise.reject(new Error('设备认证挑战无效'));
        return request('device_sign', { payload: payloadBase64 }, 30000).then(({ signature }) => ({ signature }));
      }
    }) });
    Object.defineProperty(window, 'ChatAndroidCare', { configurable: true, value: Object.freeze({
      status: () => request('care_status'),
      configure: enabled => request('care_configure', { enabled: Boolean(enabled) }),
      deliver(role, items) {
        if (typeof role !== 'string' || !Array.isArray(items) || items.length > 20) return Promise.reject(new Error('通知内容无效'));
        return request('care_deliver', { role, items });
      }
    }) });
    // Capability list, so the page never has to guess from object presence.
    // The version is the shell's versionCode, substituted at injection time.
    Object.defineProperty(window, 'ChatAndroidCapabilities', { configurable: true, value: Object.freeze(
      () => Object.freeze({
        version: __CHAT_BRIDGE_VERSION__,
        features: Object.freeze([
          'device.identity', 'device.sign', 'auth', 'preferences',
          'care', 'voice', 'exports', 'shell', 'files.picker'
        ])
      })
    ) });
    Object.defineProperty(window, 'ChatAndroidShell', { configurable: true, value: Object.freeze({
      info: () => request('shell_info', {}, 4000).then(({ host, encrypted }) => ({ host: String(host || ''), encrypted: Boolean(encrypted) })),
      refresh: () => request('shell_refresh', {}, 15000),
      checkUpdate: () => request('shell_update', {}, 120000),
      openServer: () => request('shell_server', {}, 15000)
    }) });
    Object.defineProperty(window, 'ChatAndroidExports', { configurable: true, value: Object.freeze({
      saveText(content, filename) {
        if (typeof content !== 'string' || content.length > 4 * 1024 * 1024 || typeof filename !== 'string') return false;
        try { port.postMessage(JSON.stringify({ type: 'save_markdown', marker, content, filename })); return true; }
        catch { return false; }
      }
    }) });
    window.dispatchEvent(new Event('chat-native-ready'));
  };
  window.addEventListener('message', receive);
})();
