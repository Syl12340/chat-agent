// UI Protocol v1: finite validation, never an HTML/script/schema interpreter.
const safeID = /^[A-Za-z0-9_-]+$/;
const forbidden = new Set(['__proto__', 'prototype', 'constructor']);
const encoder = new TextEncoder();
const len = value => [...value].length;
function fail(message) { throw new Error(message); }
function object(value, required, optional = []) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) fail('需要对象');
  const keys = Object.keys(value);
  if (keys.some(k => forbidden.has(k) || !required.includes(k) && !optional.includes(k))) fail('包含不支持的字段');
  if (required.some(k => !Object.hasOwn(value, k))) fail('缺少必需字段');
}
function string(value, max, min = 1) {
  if (typeof value !== 'string' || len(value) < min || len(value) > max) fail('文字长度不符合限制');
}
function id(value, max = 24) { string(value, max); if (!safeID.test(value)) fail('ID 格式无效'); }
function array(value, min, max) { if (!Array.isArray(value) || value.length < min || value.length > max) fail('数量不符合限制'); }
function number(value, min, max) { if (typeof value !== 'number' || !Number.isFinite(value) || value < min || value > max) fail('数值超出范围'); }
function optionalText(value, key, max, min = 1) { if (Object.hasOwn(value, key)) string(value[key], max, min); }
function unique(items, key = value => value.id) { if (new Set(items.map(key)).size !== items.length) fail('ID 或控件重复'); }
function point(value) { object(value, ['x', 'y']); number(value.x, -1e6, 1e6); number(value.y, -1e6, 1e6); }
function safeJSON(raw) {
  const value = JSON.parse(raw, (key, item) => { if (forbidden.has(key)) fail('包含不安全字段'); return item; });
  // JSON.parse otherwise accepts duplicate keys. Scan the already validated JSON
  // tokens so escaped-equivalent keys cannot mean different things to Go and JS.
  const objects = [];
  for (let i = 0; i < raw.length; i++) {
    const c = raw[i];
    if (c === '{') objects.push(new Set());
    else if (c === '[') objects.push(null);
    else if (c === '}' || c === ']') objects.pop();
    else if (c === '"') {
      const start = i++;
      while (i < raw.length && raw[i] !== '"') { if (raw[i] === '\\') i++; i++; }
      let next = i + 1; while (/\s/.test(raw[next] || '') && next < raw.length) next++;
      if (raw[next] === ':') {
        const key = JSON.parse(raw.slice(start, i + 1)); const keys = objects.at(-1);
        if (keys?.has(key)) fail('JSON 字段重复');
        keys?.add(key);
      }
    }
  }
  return value;
}
export function validateDefinition(value) {
  try {
    object(value, ['schema_version', 'id', 'type', 'props']);
    if (value.schema_version !== 1) fail('不支持的协议版本');
    id(value.id, 64);
    const p = value.props;
    if (value.type === 'quiz.single_choice' || value.type === 'quiz.fill_blank') {
      const choice = value.type === 'quiz.single_choice';
      object(p, ['question', choice ? 'options' : 'fields'], ['title', 'submit_label']);
      string(p.question, 1024); optionalText(p, 'title', 80);
      if (Object.hasOwn(p, 'submit_label') && p.submit_label !== '提交答案') fail('不支持的提交动作');
      const items = choice ? p.options : p.fields;
      array(items, choice ? 2 : 1, choice ? 8 : 4);
      for (const item of items) {
        object(item, ['id', 'label'], choice ? [] : ['placeholder']);
        id(item.id); string(item.label, choice ? 256 : 120);
        optionalText(item, 'placeholder', 128, 0);
      }
      unique(items);
    } else if (value.type === 'chart.line') {
      object(p, ['source'], ['title', 'x_label', 'y_label']);
      for (const key of ['title', 'x_label', 'y_label']) optionalText(p, key, 80);
      const s = p.source;
      if (s?.kind === 'samples') {
        object(s, ['kind', 'series']); array(s.series, 1, 3); let total = 0;
        for (const line of s.series) {
          object(line, ['points'], ['name']); optionalText(line, 'name', 80);
          array(line.points, 1, 256); total += line.points.length; line.points.forEach(point);
        }
        if (total > 256) fail('采样点总数超过 256');
      } else if (s?.kind === 'sine') {
        object(s, ['kind', 'amplitude', 'frequency', 'duration'], ['phase', 'offset', 'controls']);
        number(s.amplitude, 0, 10); number(s.frequency, .1, 5); number(s.duration, .1, 5);
        if (Object.hasOwn(s, 'phase')) number(s.phase, -Math.PI, Math.PI);
        if (Object.hasOwn(s, 'offset')) number(s.offset, -10, 10);
        if (Object.hasOwn(s, 'controls')) {
          array(s.controls, 0, 3); unique(s.controls, v => v);
          if (s.controls.some(k => !['amplitude', 'frequency', 'phase'].includes(k))) fail('不支持的滑块');
        }
      } else fail('不支持的数据源');
    } else if (value.type === 'diagram.vector') {
      object(p, ['vectors'], ['title', 'x_label', 'y_label']);
      for (const key of ['title', 'x_label', 'y_label']) optionalText(p, key, 80);
      array(p.vectors, 1, 16);
      for (const v of p.vectors) { object(v, ['id', 'from', 'to'], ['label']); id(v.id); optionalText(v, 'label', 80); point(v.from); point(v.to); }
      unique(p.vectors);
    } else fail('不支持的组件类型');
    return {definition: value, error: ''};
  } catch (error) { return {definition: null, error: error.message || '组件格式无效'}; }
}

// Scan complete top-level fences first. A chat-ui-looking fence inside a longer
// Markdown fence, quote or indented code block remains ordinary source code.
export function extractSegments(raw) {
  raw = String(raw ?? '');
  const lines = raw.match(/[^\n]*\n|[^\n]+$/g) || [];
  const segments = []; let plain = ''; let ordinal = 0; let bytes = 0;
  const flush = () => { if (plain) { segments.push({kind: 'text', raw: plain}); plain = ''; } };
  for (let i = 0; i < lines.length;) {
    const opening = /^( {0,3})(`{3,}|~{3,})([^\r\n]*)\r?\n?$/.exec(lines[i]);
    if (!opening || opening[2][0] === '`' && opening[3].includes('`')) { plain += lines[i++]; continue; }
    const first = i; const marker = opening[2][0]; const size = opening[2].length;
    let j = i + 1;
    const closing = new RegExp('^ {0,3}' + (marker === '`' ? '`' : '~') + '{' + size + ',}[ \\t]*\\r?\\n?$');
    while (j < lines.length && !closing.test(lines[j])) j++;
    const closed = j < lines.length;
    const end = closed ? j + 1 : lines.length;
    const block = lines.slice(first, end).join('');
    if (marker === '`' && opening[3].trim() === 'chat-ui') {
      flush();
      const json = lines.slice(first + 1, j).join('');
      const segment = {kind: 'ui', raw: block, json, ordinal: ordinal++, closed, definition: null, error: ''};
      const count = encoder.encode(json).length; bytes += count;
      // A small but otherwise invalid declaration can still duplicate another
      // component's ID. Count it too, without parsing incomplete/large sources.
      if (closed && count <= 8192) {
        try { const candidate = safeJSON(json); if (typeof candidate?.id === 'string' && /^[A-Za-z0-9_-]{1,64}$/.test(candidate.id)) segment.candidateID = candidate.id; } catch {}
      }
      if (!closed) segment.error = '正在生成互动内容';
      else if (segment.ordinal >= 4 || count > 8192 || bytes > 32768) segment.error = '互动内容超出数量或大小限制';
      else {
        try { Object.assign(segment, validateDefinition(safeJSON(json))); }
        catch { segment.error = 'JSON 格式无效或包含不安全字段'; }
      }
      segments.push(segment);
    } else plain += block;
    i = end;
  }
  flush();
  const counts = new Map();
  for (const s of segments) if (s.candidateID) counts.set(s.candidateID, (counts.get(s.candidateID) || 0) + 1);
  for (const s of segments) if (s.candidateID && counts.get(s.candidateID) > 1) { s.definition = null; s.error = '同一消息中的组件 ID 重复'; }
  return segments;
}

export function parseUIResponse(content) {
  try {
    const value = safeJSON(content); object(value, ['chat_ui_response']);
    const r = value.chat_ui_response;
    object(r, ['schema_version', 'component_id', 'component_type', 'source', 'answer']);
    if (r.schema_version !== 1) return null;
    id(r.component_id, 64); object(r.source, ['message_index', 'sha256']);
    number(r.source.message_index, 0, 1e6);
    if (!Number.isInteger(r.source.message_index) || typeof r.source.sha256 !== 'string' || !/^[0-9a-f]{64}$/.test(r.source.sha256)) return null;
    if (r.component_type === 'quiz.single_choice') { object(r.answer, ['option_id']); id(r.answer.option_id); }
    else if (r.component_type === 'quiz.fill_blank') {
      object(r.answer, ['fields']); array(r.answer.fields, 1, 4);
      for (const f of r.answer.fields) { object(f, ['id', 'value']); id(f.id); string(f.value, 128); if (!f.value.trim()) return null; }
      unique(r.answer.fields);
    } else return null;
    return r;
  } catch { return null; }
}

export function componentKey(context, segment) {
  return JSON.stringify([context.role, context.session, context.messageIndex, context.sha256 || '', segment.ordinal, segment.definition.id]);
}
