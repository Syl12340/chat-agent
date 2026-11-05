import {createMathBudget, renderMath} from './math-render.js';

const encoder = new TextEncoder();
const el = (tag, cls, text) => {
  const node = document.createElement(tag); if (cls) node.className = cls;
  if (text !== undefined) node.textContent = text; return node;
};
const escaped = (text, index) => { let n = 0; while (index > 0 && text[--index] === '\\') n++; return n % 2 === 1; };
const findClosing = (text, marker, start) => {
  for (let i = start; i < text.length; i++) if (text.startsWith(marker, i) && !escaped(text, i)) return i;
  return -1;
};
function tableCells(line) {
  let value = line.trim(); if (value.startsWith('|')) value = value.slice(1);
  if (value.endsWith('|') && !escaped(value, value.length - 1)) value = value.slice(0, -1);
  const cells = []; let start = 0, code = 0;
  for (let i = 0; i < value.length; i++) {
    if (value[i] === '`' && !escaped(value, i)) code = code ? 0 : 1;
    if (value[i] === '|' && !code && !escaped(value, i)) { cells.push(value.slice(start, i).trim()); start = i + 1; }
  }
  cells.push(value.slice(start).trim()); return cells;
}
const fenceStart = line => /^( {0,3})(`{3,}|~{3,})([^\n]*)$/.exec(line);
const listStart = line => /^( *)([-+*]|\d{1,9}[.)])\s+(.+)$/.exec(line);
const rule = line => /^(?: {0,3})(?:(?:\*\s*){3,}|(?:-\s*){3,}|(?:_\s*){3,})$/.test(line);

export function createMarkdownRenderer({copyText, notice} = {}) {
  const copy = copyText || (async (text, button) => {
    try { await navigator.clipboard.writeText(text); button.textContent = '已复制'; }
    catch { notice?.('请选中文字复制。'); }
  });
  function math(source, display, budget) { return renderMath(source, {display, budget, copyText: copy}); }
  function inline(text, parent, budget, depth = 0) {
    if (depth > 8) { parent.append(document.createTextNode(text)); return; }
    let plain = '';
    const flush = () => { if (plain) { parent.append(document.createTextNode(plain)); plain = ''; } };
    for (let i = 0; i < text.length;) {
      // Math delimiters precede ordinary backslash escapes. Code spans precede
      // math so code and literal money remain untouched.
      if (text[i] === '`') {
        const marker = /^`+/.exec(text.slice(i))[0]; const end = text.indexOf(marker, i + marker.length);
        if (end >= 0 && text[end + marker.length] !== '`') {
          flush(); parent.append(el('code', '', text.slice(i + marker.length, end).replace(/\n/g, ' '))); i = end + marker.length; continue;
        }
      }
      if (text.startsWith('\\(', i) || text.startsWith('\\[', i)) {
        const display = text[i + 1] === '['; const closing = display ? '\\]' : '\\)'; const end = findClosing(text, closing, i + 2);
        if (end >= 0) { flush(); parent.append(math(text.slice(i + 2, end), display, budget)); i = end + 2; continue; }
        // Incomplete streaming math is source text, including its delimiter.
        plain += text.slice(i); break;
      }
      if (text.startsWith('$$', i) && !escaped(text, i)) {
        const end = findClosing(text, '$$', i + 2);
        if (end >= 0) { flush(); parent.append(math(text.slice(i + 2, end), true, budget)); i = end + 2; continue; }
        plain += text.slice(i); break;
      }
      if (text[i] === '$' && !escaped(text, i) && text[i + 1] !== '$') {
        const end = findClosing(text, '$', i + 1), candidate = end >= 0 ? text.slice(i + 1, end) : '';
        // Deliberately conservative single dollars: mathematical syntax is
        // required; plain '$5' and '$5 to $10' never become formulas.
        if (candidate && !/^\s|\s$/.test(candidate) && !candidate.includes('\n') && /[\\^_=]/.test(candidate) && !/\d/.test(text[i - 1] || '') && !/\d/.test(text[end + 1] || '')) {
          flush(); parent.append(math(candidate, false, budget)); i = end + 1; continue;
        }
      }
      if (text[i] === '\\' && /[!"#$%&'()*+,\-./:;<=>?@[\\\]^_`{|}~]/.test(text[i + 1] || '')) { plain += text[i + 1]; i += 2; continue; }
      if (text.startsWith('![', i)) {
        // Model-authored image URLs are kept as source. Actual attachments use
        // the authenticated image pipeline rather than remote image requests.
        const end = text.indexOf(')', i + 2); if (end >= 0) { plain += text.slice(i, end + 1); i = end + 1; continue; }
      }
      if (text[i] === '[') {
        const endLabel = text.indexOf('](', i + 1);
        if (endLabel >= 0) {
          const end = text.indexOf(')', endLabel + 2);
          if (end >= 0) {
            try {
              const url = new URL(text.slice(endLabel + 2, end), location.href);
              if (!['https:', 'http:'].includes(url.protocol) || url.username || url.password) throw new Error('URL');
              flush(); const a = el('a'); a.href = url.href; a.target = '_blank'; a.rel = 'noopener noreferrer';
              inline(text.slice(i + 1, endLabel), a, budget, depth + 1); parent.append(a); i = end + 1; continue;
            } catch { /* Preserve an unsupported link as literal source. */ }
          }
        }
      }
      let matched = false;
      for (const [marker, tag] of [['**', 'strong'], ['__', 'strong'], ['~~', 'del'], ['*', 'em'], ['_', 'em']]) {
        if (!text.startsWith(marker, i) || marker === '_' && /[\p{L}\p{N}]/u.test(text[i - 1] || '')) continue;
        const end = findClosing(text, marker, i + marker.length);
        if (end > i + marker.length) { flush(); const node = el(tag); inline(text.slice(i + marker.length, end), node, budget, depth + 1); parent.append(node); i = end + marker.length; matched = true; break; }
      }
      if (matched) continue;
      if (text[i] === '\n') {
        if (plain.endsWith('  ')) { plain = plain.slice(0, -2); flush(); parent.append(el('br')); }
        else plain += '\n';
        i++; continue;
      }
      plain += text[i++];
    }
    flush();
  }
  function jsonTree(source) {
    if (encoder.encode(source).length > 32768) return null;
    let value;
    try { value = JSON.parse(source); } catch { return null; }
    if (!value || typeof value !== 'object') return null;
    let count = 0;
    function check(item, depth) {
      if (++count > 500 || depth > 8) return false;
      return !item || typeof item !== 'object' || Object.values(item).every(child => check(child, depth + 1));
    }
    if (!check(value, 0)) return null;
    function branch(item, key, level) {
      if (item && typeof item === 'object') {
        const details = el('details', 'json-branch'); details.open = level === 0;
        const summary = el('summary'); summary.append(el('span', 'json-key', key), document.createTextNode(`${Array.isArray(item) ? ' […]' : ' {…}'} · ${Object.keys(item).length}`));
        const children = el('div', 'json-children');
        // The bounded tree is built once; expand/collapse never calls a model.
        for (const [k, child] of Object.entries(item)) children.append(branch(child, k, level + 1));
        details.append(summary, children); return details;
      }
      const line = el('div', 'json-leaf'); line.append(el('span', 'json-key', `${key}: `), el('span', typeof item === 'string' ? 'json-string' : 'json-value', JSON.stringify(item))); return line;
    }
    return branch(value, Array.isArray(value) ? '数组' : '对象', 0);
  }
  function codeBlock(source, language, closed = true) {
    const block = el('section', 'code-block rich-code');
    const toolbar = el('div', 'code-toolbar'); const label = el('span', '', `${language || '代码'}${closed ? '' : ' · 正在生成'}`);
    const actions = el('div', 'rich-code-actions'); const button = el('button', 'code-copy', '复制代码'); button.type = 'button';
    button.addEventListener('click', () => copy(source, button)); actions.append(button); toolbar.append(label, actions);
    const pre = el('pre'); pre.append(el('code', '', source.length > 32768 ? `${source.slice(0, 32768)}\n…展示已截断，可复制完整源码` : source));
    const tree = closed && language.toLowerCase() === 'json' ? jsonTree(source) : null;
    if (tree) {
      const toggle = el('button', 'code-copy', '树视图'); toggle.type = 'button'; tree.hidden = true;
      toggle.addEventListener('click', () => { tree.hidden = !tree.hidden; pre.hidden = !tree.hidden; toggle.textContent = tree.hidden ? '树视图' : '源码'; });
      actions.prepend(toggle);
    }
    const collapse = el('button', 'code-copy', '收起'); collapse.type = 'button';
    const body = el('div', 'rich-code-body'); body.append(pre); if (tree) body.append(tree);
    collapse.addEventListener('click', () => { body.hidden = !body.hidden; collapse.textContent = body.hidden ? '展开' : '收起'; }); actions.append(collapse);
    block.append(toolbar, body); return block;
  }
  function blocks(lines, parent, budget, depth = 0) {
    if (depth > 8) { parent.append(el('pre', '', lines.join('\n'))); return; }
    let i = 0;
    const special = n => !lines[n]?.trim() || fenceStart(lines[n]) || /^ {0,3}(?:#{1,6}\s|>)/.test(lines[n]) || listStart(lines[n]) || rule(lines[n]) || /^(?: {0,3})(?:\\\[|\$\$)/.test(lines[n]);
    while (i < lines.length) {
      const line = lines[i]; if (!line.trim()) { i++; continue; }
      const fence = fenceStart(line);
      if (fence && !(fence[2][0] === '`' && fence[3].includes('`'))) {
        const marker = fence[2][0], length = fence[2].length;
        const closing = new RegExp('^ {0,3}' + marker + '{' + length + ',}\\s*$');
        let end = i + 1; while (end < lines.length && !closing.test(lines[end])) end++;
        parent.append(codeBlock(lines.slice(i + 1, end).join('\n'), fence[3].trim().slice(0, 32), end < lines.length)); i = Math.min(end + 1, lines.length); continue;
      }
      const mathStart = /^ {0,3}(\\\[|\$\$)(.*)$/.exec(line);
      if (mathStart) {
        const close = mathStart[1] === '\\[' ? '\\]' : '$$'; let source = mathStart[2], end = i;
        let pos = findClosing(source, close, 0);
        while (pos < 0 && end + 1 < lines.length) { source += '\n' + lines[++end]; pos = findClosing(source, close, 0); }
        if (pos >= 0) { parent.append(math(source.slice(0, pos), true, budget)); const rest = source.slice(pos + close.length); if (rest.trim()) { const p = el('p'); inline(rest, p, budget); parent.append(p); } }
        else parent.append(el('pre', 'math-unfinished', lines.slice(i, end + 1).join('\n')));
        i = end + 1; continue;
      }
      const heading = /^ {0,3}(#{1,6})\s+(.+?)(?:\s+#+\s*)?$/.exec(line);
      if (heading) { const h = el(`h${heading[1].length}`); inline(heading[2], h, budget); parent.append(h); i++; continue; }
      if (i + 1 < lines.length && /^ {0,3}(?:={3,}|-{3,})\s*$/.test(lines[i + 1]) && !listStart(line)) {
        const h = el(lines[i + 1].trim()[0] === '=' ? 'h1' : 'h2'); inline(line, h, budget); parent.append(h); i += 2; continue;
      }
      if (rule(line)) { parent.append(el('hr')); i++; continue; }
      if (/^ {0,3}>/.test(line)) {
        const quoted = []; while (i < lines.length && /^ {0,3}>/.test(lines[i])) quoted.push(lines[i++].replace(/^ {0,3}> ?/, ''));
        const quote = el('blockquote'); blocks(quoted, quote, budget, depth + 1); parent.append(quote); continue;
      }
      const list = listStart(line);
      if (list) {
        const ordered = /^\d/.test(list[2]), indent = list[1].length, node = el(ordered ? 'ol' : 'ul');
        if (ordered && parseInt(list[2], 10) !== 1) node.start = parseInt(list[2], 10);
        while (i < lines.length) {
          const item = listStart(lines[i]);
          if (!item || item[1].length !== indent || /^\d/.test(item[2]) !== ordered) break;
          const content = [item[3]], contentIndent = indent + item[2].length + 1; i++;
          while (i < lines.length) {
            const next = listStart(lines[i]); if (next && next[1].length <= indent) break;
            if (!lines[i].trim()) {
              if (i + 1 < lines.length && /\S/.test(lines[i + 1]) && lines[i + 1].search(/\S/) > indent) { content.push(''); i++; continue; }
              break;
            }
            const spaces = lines[i].search(/\S/); if (spaces <= indent) break;
            content.push(lines[i].slice(Math.min(contentIndent, spaces))); i++;
          }
          const li = el('li'); const task = /^\[([ xX])\]\s+(.*)$/.exec(content[0]);
          if (task) { const input = el('input'); input.type = 'checkbox'; input.disabled = true; input.checked = task[1].toLowerCase() === 'x'; input.setAttribute('aria-label', '原文任务状态，只读'); li.className = 'md-task'; li.append(input); content[0] = task[2]; }
          const itemBody = el('div', 'md-list-content'); blocks(content, itemBody, budget, depth + 1); li.append(itemBody); node.append(li);
        }
        parent.append(node); continue;
      }
      if (i + 1 < lines.length && line.includes('|')) {
        const header = tableCells(line), separators = tableCells(lines[i + 1]);
        if (header.length <= 12 && header.length === separators.length && separators.every(cell => /^:?-{3,}:?$/.test(cell))) {
          const wrapper = el('div', 'md-table-scroll'); wrapper.tabIndex = 0; wrapper.setAttribute('role', 'region'); wrapper.setAttribute('aria-label', '表格，可横向滚动');
          const table = el('table'), head = el('thead'), row = el('tr'), body = el('tbody');
          header.forEach((cell, col) => { const th = el('th'); th.scope = 'col'; th.dataset.align = separators[col].endsWith(':') ? separators[col].startsWith(':') ? 'center' : 'right' : 'left'; inline(cell, th, budget); row.append(th); });
          head.append(row); table.append(head, body); i += 2; let rows = 0;
          while (i < lines.length && lines[i].includes('|') && lines[i].trim() && rows < 100) {
            const cells = tableCells(lines[i++]), tr = el('tr');
            header.forEach((_, col) => { const td = el('td'); td.dataset.align = row.children[col].dataset.align; inline(cells[col] || '', td, budget); tr.append(td); }); body.append(tr); rows++;
          }
          wrapper.append(table); parent.append(wrapper); continue;
        }
      }
      if (/^ {4}/.test(line)) {
        const source = []; while (i < lines.length && (/^ {4}/.test(lines[i]) || !lines[i].trim())) source.push(lines[i++].slice(4));
        parent.append(codeBlock(source.join('\n'), '', true)); continue;
      }
      const paragraph = [lines[i++]];
      while (i < lines.length && !special(i)) {
        if (i + 1 < lines.length && lines[i].includes('|') && tableCells(lines[i + 1]).every(cell => /^:?-{3,}:?$/.test(cell))) break;
        paragraph.push(lines[i++]);
      }
      const p = el('p'); inline(paragraph.join('\n'), p, budget); parent.append(p);
    }
  }
  return {
    createBudget: createMathBudget,
    render(text, {budget = createMathBudget()} = {}) {
      const fragment = document.createDocumentFragment();
      blocks(String(text ?? '').replace(/\r\n?/g, '\n').split('\n'), fragment, budget); return fragment;
    },
  };
}
