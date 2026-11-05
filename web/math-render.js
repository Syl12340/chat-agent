import katex from '/vendor/katex/katex.mjs';

const encoder = new TextEncoder();
export const createMathBudget = () => ({count: 0, bytes: 0});

// Only the pinned, trusted renderer may create MathML. Model text never enters
// HTML, and one formula cannot define macros for a later formula/message.
export function renderMath(source, {display = false, budget = createMathBudget(), copyText} = {}) {
  const host = document.createElement(display ? 'figure' : 'span');
  host.className = display ? 'math-block' : 'math-inline';
  const bytes = encoder.encode(source).length;
  let error = '';
  if (bytes > 4096 || budget.count >= 16 || budget.bytes + bytes > 32768) error = '公式超过展示限制';
  else { budget.count++; budget.bytes += bytes; }
  if (!error && typeof window.MathMLElement === 'undefined') error = '当前浏览器不支持数学排版';
  if (!error) {
    try {
      const staging = document.createElement('span');
      katex.render(source, staging, {displayMode: display, output: 'mathml', trust: false,
        throwOnError: true, strict: 'error', maxExpand: 100, maxSize: 20,
        macros: {}, globalGroup: false});
      const math = staging.querySelector('math');
      if (!math) throw new Error('MathML unavailable');
      // MathML uses native layout. Drop optional inline styling and URL/event
      // attributes so the existing CSP needs no unsafe-inline exception.
      for (const node of [math, ...math.querySelectorAll('*')]) {
        for (const attr of [...node.attributes]) if (/^(?:style|href|src|on.*)$/i.test(attr.name)) node.removeAttribute(attr.name);
      }
      math.setAttribute('display', display ? 'block' : 'inline');
      host.append(math);
    } catch { error = '公式暂时无法排版'; }
  }
  if (error) {
    host.classList.add('math-fallback');
    const code = document.createElement('code'); code.textContent = source; host.append(code);
    host.title = `${error}，已保留源码`;
  }
  if (display) {
    const caption = document.createElement('figcaption');
    const label = document.createElement('span'); label.textContent = error ? `${error} · 原始公式` : '公式';
    const copy = document.createElement('button'); copy.type = 'button'; copy.className = 'code-copy'; copy.textContent = '复制公式';
    copy.addEventListener('click', () => copyText?.(source, copy)); caption.append(label, copy); host.append(caption);
  }
  return host;
}
