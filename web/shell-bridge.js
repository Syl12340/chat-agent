import { Features, supports, waitForShell } from '/native-capabilities.js';

// Shell actions used to live in a native Android title bar above the WebView.
// They now render inside the page sidebar and reach the shell through the
// PocketAndroidShell bridge, so the page owns the whole screen.
//
// Nothing is shown outside the native shell: in a browser these controls have no
// meaning, and a dead button is worse than no button.

export function createShellBridge({ onNotice } = {}) {
  const $ = id => document.getElementById(id);
  const panel = $('shell-status');
  if (!panel) return { refresh: async () => {} };

  const say = text => { if (onNotice) onNotice(text); };
  let wired = false;

  function describe(host, encrypted) {
    const label = $('shell-host');
    if (!label) return;
    label.textContent = host ? `${encrypted ? '加密 · ' : ''}${host}` : '尚未设置服务器';
    label.title = host || '';
  }

  function wire(shell) {
    if (wired) return;
    wired = true;
    panel.hidden = false;

    const bind = (id, run) => {
      const button = $(id);
      if (!button) return;
      button.addEventListener('click', async () => {
        button.disabled = true;
        try { await run(); }
        catch (error) { say(error?.message || '本机操作未完成'); }
        finally { button.disabled = false; }
      });
    };

    // Refresh and update both reload the page, so a resolved promise means the
    // shell accepted the request rather than that the work finished.
    bind('shell-refresh', () => shell.refresh());
    bind('shell-update', () => shell.checkUpdate());
    bind('shell-server', () => shell.openServer());

    shell.info()
      .then(({ host, encrypted }) => describe(host, encrypted))
      .catch(() => describe('', false));
  }

  function start() {
    // The injected bridge defines these on the window once the port is open.
    // A shell without the shell_* channel simply has no sidebar block; that is
    // a capability gap, not an error.
    waitForShell().then(ready => {
      if (ready && supports(Features.shell) && window.PocketAndroidShell) wire(window.PocketAndroidShell);
    });
  }

  start();
  return { refresh: () => window.PocketAndroidShell?.info().then(({ host, encrypted }) => describe(host, encrypted)).catch(() => {}) };
}
