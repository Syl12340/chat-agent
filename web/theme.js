// Light-mode accent themes. Dark mode stays true black with the fixed lavender
// accent, so overrides are only written while the page resolves to light.
// Loaded as a classic script in <head> so a saved colour paints before first frame.
(() => {
  const KEY = 'pocket-accent';
  const PRESETS = [
    { id: 'default', name: '紫', hue: null },
    { id: 'indigo', name: '靛蓝', hue: 222 },
    { id: 'teal', name: '青', hue: 187 },
    { id: 'green', name: '绿', hue: 152 },
    { id: 'amber', name: '橙', hue: 28 },
    { id: 'rose', name: '玫红', hue: 340 },
  ];
  const VARS = [
    '--primary', '--on-primary', '--primary-container', '--on-primary-container',
    '--secondary', '--secondary-container', '--on-secondary-container',
    '--tertiary-container', '--on-tertiary-container',
    '--surface', '--surface-low', '--surface-container', '--surface-high', '--surface-highest',
    '--on-surface-variant', '--outline', '--outline-variant',
  ];

  function hsl(h, s, l) {
    s /= 100; l /= 100;
    const k = n => (n + h / 30) % 12;
    const a = s * Math.min(l, 1 - l);
    const f = n => l - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)));
    const hex = x => Math.round(255 * x).toString(16).padStart(2, '0');
    return `#${hex(f(0))}${hex(f(8))}${hex(f(4))}`;
  }

  function palette(hue) {
    const warm = (hue + 52) % 360;
    return {
      '--primary': hsl(hue, 42, 39),
      '--on-primary': '#ffffff',
      '--primary-container': hsl(hue, 72, 92),
      '--on-primary-container': hsl(hue, 52, 16),
      '--secondary': hsl(hue, 20, 43),
      '--secondary-container': hsl(hue, 40, 92),
      '--on-secondary-container': hsl(hue, 30, 17),
      '--tertiary-container': hsl(warm, 68, 90),
      '--on-tertiary-container': hsl(warm, 45, 17),
      '--surface': hsl(hue, 30, 99),
      '--surface-low': hsl(hue, 16, 96),
      '--surface-container': hsl(hue, 18, 94),
      '--surface-high': hsl(hue, 20, 91),
      '--surface-highest': hsl(hue, 22, 89),
      '--on-surface-variant': hsl(hue, 10, 30),
      '--outline': hsl(hue, 6, 47),
      '--outline-variant': hsl(hue, 14, 78),
    };
  }

  const media = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;
  const root = document.documentElement;
  let current = 'default';

  function read() {
    try { return localStorage.getItem(KEY) || 'default'; } catch { return 'default'; }
  }
  function write(id) {
    try { localStorage.setItem(KEY, id); } catch { /* private mode: keep it session-only */ }
  }
  function preset(id) { return PRESETS.find(p => p.id === id) || PRESETS[0]; }
  function isDark() { return Boolean(media && media.matches); }

  function apply(id) {
    current = preset(id).id;
    VARS.forEach(name => root.style.removeProperty(name));
    if (current !== 'default' && !isDark()) {
      const values = palette(preset(current).hue);
      Object.entries(values).forEach(([name, value]) => root.style.setProperty(name, value));
    }
    reflect();
  }

  function reflect() {
    const group = document.getElementById('accent-options');
    if (!group) return;
    group.querySelectorAll('[data-accent]').forEach(node => {
      const on = node.dataset.accent === current;
      node.setAttribute('aria-checked', on ? 'true' : 'false');
      node.classList.toggle('selected', on);
    });
    const note = document.getElementById('accent-note');
    if (note) note.hidden = !(isDark() && current !== 'default');
  }

  function mount() {
    const group = document.getElementById('accent-options');
    if (!group || group.childElementCount) { reflect(); return; }
    PRESETS.forEach(item => {
      const node = document.createElement('button');
      node.type = 'button';
      node.className = 'accent-swatch';
      node.dataset.accent = item.id;
      node.setAttribute('role', 'radio');
      node.setAttribute('aria-checked', 'false');
      node.title = item.name;
      const dot = document.createElement('span');
      dot.className = 'accent-dot';
      dot.style.background = item.hue === null ? 'var(--primary)' : hsl(item.hue, 42, 39);
      const label = document.createElement('span');
      label.textContent = item.name;
      node.append(dot, label);
      node.addEventListener('click', () => { write(item.id); apply(item.id); });
      group.append(node);
    });
    reflect();
  }

  apply(read());

  if (media) {
    const sync = () => apply(current);
    if (media.addEventListener) media.addEventListener('change', sync);
    else if (media.addListener) media.addListener(sync);
  }
  if (document.readyState === 'loading') document.addEventListener('DOMContentLoaded', mount);
  else mount();
})();
