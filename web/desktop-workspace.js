// The host owns placement; an expanded component keeps its DOM and controller.
export function createDesktopWorkspace({ onClose = () => {} } = {}) {
  const panel = document.getElementById('desktop-workbench');
  const body = document.getElementById('desktop-workbench-body');
  const heading = document.getElementById('desktop-workbench-title');
  const closeButton = document.getElementById('desktop-workbench-close');
  const scrim = document.getElementById('desktop-workbench-scrim');
  const shell = document.querySelector('.app-shell');
  const background = [document.querySelector('.main-panel'), document.getElementById('sidebar')].filter(Boolean);
  const overlay = window.matchMedia('(max-width:1199px)');
  let current = null;
  let previousInert = [];

  const focus = element => {
    if (element?.isConnected && typeof element.focus === 'function') element.focus({ preventScroll: true });
  };
  function setOverlay() {
    const modal = !!current && overlay.matches;
    if (modal && !previousInert.length) {
      previousInert = background.map(element => ({ element, inert: element.inert }));
      for (const { element } of previousInert) element.inert = true;
    } else if (!modal && previousInert.length) {
      for (const { element, inert } of previousInert) element.inert = inert;
      previousInert = [];
    }
    if (panel) {
      panel.setAttribute('role', modal ? 'dialog' : 'complementary');
      if (modal) panel.setAttribute('aria-modal', 'true');
      else panel.removeAttribute('aria-modal');
    }
    if (scrim) scrim.hidden = !modal;
  }
  function anchor() {
    const placeholder = document.createElement('div');
    placeholder.className = 'desktop-workbench-placeholder';
    const text = document.createElement('span');
    text.textContent = '正在工作台查看';
    const restore = document.createElement('button');
    restore.type = 'button';
    restore.textContent = '收回对话';
    restore.addEventListener('click', () => close());
    placeholder.append(text, restore);
    return placeholder;
  }
  function close({ restoreFocus = true } = {}) {
    if (!current) return;
    const saved = current;
    current = null;
    const active = document.activeElement;
    const inside = saved.node.contains(active);
    delete saved.node.dataset.uiExpanded;
    if (saved.placeholder.isConnected) saved.placeholder.replaceWith(saved.node);
    else saved.node.remove();
    panel.hidden = true;
    body.replaceChildren();
    shell?.classList.remove('workbench-open');
    setOverlay();
    if (restoreFocus) {
      if (inside && active?.isConnected) focus(active);
      else focus(saved.returnFocus);
    }
    onClose(saved.key);
  }
  function expand(node, { title = '互动内容', key = '' } = {}) {
    if (!(node instanceof HTMLElement) || !node.isConnected || !panel || !body) return false;
    if (current?.node === node) {
      focus(closeButton);
      return true;
    }
    close({ restoreFocus: false });
    const returnFocus = document.activeElement;
    const focusedChild = node.contains(returnFocus) ? returnFocus : null;
    const placeholder = anchor();
    node.replaceWith(placeholder);
    current = { node, placeholder, key, returnFocus };
    node.dataset.uiExpanded = 'true';
    heading.textContent = String(title).slice(0, 160);
    body.replaceChildren(node);
    panel.hidden = false;
    shell?.classList.add('workbench-open');
    setOverlay();
    focus(focusedChild || closeButton);
    return true;
  }
  function placement(node) {
    if (!current || current.node !== node) return node;
    // renderHistory replaces its chat DOM. Give it a fresh anchor while the
    // actual input/slider stays mounted here, including current input focus.
    current.placeholder.remove();
    current.placeholder = anchor();
    return current.placeholder;
  }
  function reconcile() {
    if (current && !current.placeholder.isConnected) close({ restoreFocus: false });
  }
  function keydown(event) {
    if (!current || document.querySelector('dialog[open]')) return;
    if (event.key === 'Escape') {
      event.preventDefault();
      close();
    } else if (event.key === 'Tab' && overlay.matches) {
      const focusable = [...panel.querySelectorAll('button, input, select, textarea, a[href], [tabindex]')]
        .filter(element => !element.disabled && element.tabIndex >= 0 && element.getClientRects().length);
      const first = focusable[0] || closeButton;
      const last = focusable[focusable.length - 1] || closeButton;
      if (event.shiftKey && (document.activeElement === first || !panel.contains(document.activeElement))) {
        event.preventDefault(); focus(last);
      } else if (!event.shiftKey && (document.activeElement === last || !panel.contains(document.activeElement))) {
        event.preventDefault(); focus(first);
      }
    }
  }
  const closeClick = () => close();
  closeButton?.addEventListener('click', closeClick);
  scrim?.addEventListener('click', closeClick);
  document.addEventListener('keydown', keydown);
  overlay.addEventListener('change', setOverlay);
  return {
    expand, close, placement, reconcile,
    reset: () => close({ restoreFocus: false }),
    get activeKey() { return current?.key ?? null; },
    destroy() {
      close({ restoreFocus: false });
      closeButton?.removeEventListener('click', closeClick);
      scrim?.removeEventListener('click', closeClick);
      document.removeEventListener('keydown', keydown);
      overlay.removeEventListener('change', setOverlay);
    },
  };
}
