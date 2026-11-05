import {componentKey, extractSegments, parseUIResponse} from './protocol.js';
import {createUIState} from './state.js';
import {renderQuiz} from './quiz.js';
import {renderChart} from './charts.js';
import {renderVectors} from './vectors.js';
import {renderTable, renderBars, renderFlow} from './structures.js';

const names = {'quiz.single_choice': '单选练习', 'quiz.fill_blank': '简短填空', 'chart.line': '折线图', 'diagram.vector': '二维矢量', 'table.data': '数据表', 'chart.bar': '柱状图', 'diagram.flow': '流程与关系'};
let serial = 0;
function element(tag, cls, text) {
  const node = document.createElement(tag); if (cls) node.className = cls;
  if (text !== undefined) node.textContent = text; return node;
}
function fallback(segment, reason = segment.error) {
  const node = element('section', 'chat-ui-fallback code-block');
  const toolbar = element('div', 'code-toolbar'); toolbar.append(element('span', '', segment.closed ? `chat-ui · ${reason || '仅供查看'}` : '正在生成互动内容'));
  const copy = element('button', 'code-copy', '复制'); copy.type = 'button';
  copy.addEventListener('click', async () => {
    try { await navigator.clipboard.writeText(segment.raw); copy.textContent = '已复制'; }
    catch { copy.textContent = '请选中文本复制'; }
  });
  toolbar.append(copy);
  const pre = element('pre'); pre.append(element('code', '', segment.closed ? segment.json : segment.json.slice(0, 2048)));
  node.append(toolbar, pre); return node;
}
function matches(response, state) {
  return response.component_id === state.definition.id && response.component_type === state.definition.type &&
    response.source.message_index === state.context.messageIndex && response.source.sha256 === state.context.sha256;
}
function applyAnswer(state, response) {
  if (response.component_type === 'quiz.single_choice') state.choice = response.answer.option_id;
  else state.fields = Object.fromEntries(response.answer.fields.map(f => [f.id, f.value]));
}

export function createUIRenderer({onSubmit, onExpand, onPlace, canSubmit = () => true} = {}) {
  const store = createUIState(64); let scope = ''; let lastTurns = [], lastPending = null;
  function sourceAllowed(state) {
    const c = state.context;
    if (!state.alive || JSON.stringify([c.role, c.session]) !== scope) return '此互动来源已失效';
    if (!c.eligible || !/^[0-9a-f]{64}$/.test(c.sha256 || '')) return '完整回答保存后才可互动';
    if (c.messageIndex < (c.floor || 0)) return '此互动已不在当前有效上下文，请在聊天中重新提问';
    return true;
  }
  function permission(state) { return sourceAllowed(state) === true ? canSubmit(state.context) : sourceAllowed(state); }
  function refresh(state) { state.view?.update(); state.node?.classList.toggle('chat-ui-readonly', sourceAllowed(state) !== true); }
  function create(definition, context, key) {
    const state = {definition, context: {...context}, key, serial: ++serial, fields: {}, choice: '', dirty: false, busy: false, alive: true, status: '', request_id: '', notice: ''};
    const node = element('section', 'chat-ui-card'); state.node = node; node.dataset.uiKey = key;
    const header = element('header', 'chat-ui-heading');
    header.append(element('strong', '', definition.props.title || names[definition.type]));
    if (onExpand) {
      const expand = element('button', 'chat-ui-expand', '展开'); expand.type = 'button';
      expand.addEventListener('click', () => { if (state.alive) onExpand(node, {title: definition.props.title || names[definition.type], key}); });
      header.append(expand);
    }
    node.append(header);
    const changed = () => { state.notice = ''; refresh(state); };
    const submit = async answer => {
      if (permission(state) !== true || state.busy || !onSubmit) return;
      const snapshot = {...state.context};
      const content = JSON.stringify({chat_ui_response: {schema_version: 1, component_id: definition.id, component_type: definition.type,
        source: {message_index: snapshot.messageIndex, sha256: snapshot.sha256}, answer}});
      state.busy = true; state.status = 'sending'; state.notice = ''; refresh(state);
      try {
        const result = await onSubmit({content, definition, context: snapshot, state});
        if (!state.alive || JSON.stringify([snapshot.role, snapshot.session]) !== scope) return;
        if (result?.status === 'rejected') {
          state.busy = false; state.status = 'rejected'; state.notice = '答案未提交，请查看会话中的提示。';
        } else {
          state.request_id = result?.request_id || state.request_id;
          state.status = result?.status || 'queued';
          state.busy = !['completed', 'failed', 'canceled', 'withdrawn', 'interrupted'].includes(state.status);
        }
      } catch (error) {
        if (!state.alive || JSON.stringify([snapshot.role, snapshot.session]) !== scope) return;
        state.busy = false; state.status = 'rejected'; state.notice = error?.message || '答案未提交，请稍后重试。';
      }
      refresh(state);
    };
    const options = {submit, allowed: () => permission(state), changed, interactive: () => sourceAllowed(state) === true};
    const registry = {'quiz.single_choice':renderQuiz, 'quiz.fill_blank':renderQuiz, 'chart.line':renderChart,
      'diagram.vector':renderVectors, 'table.data':renderTable, 'chart.bar':renderBars, 'diagram.flow':renderFlow};
    state.view = registry[definition.type](definition, state, options);
    node.append(state.view.node);
    state.dispose = () => { state.alive = false; state.view.dispose?.(); node.classList.add('chat-ui-readonly'); node.querySelectorAll('button,input').forEach(el => { el.disabled = true; }); };
    return state;
  }
  function reconcileOne(state) {
    let found = null;
    for (const turn of lastTurns) {
      const response = parseUIResponse(turn.content);
      if (response && matches(response, state)) found = {turn, response};
    }
    if (found) {
      const {turn, response} = found;
      // Restoring a saved response is read-only state recovery, never a submit.
      if (state.observedRequest !== turn.id) {
        applyAnswer(state, response); state.observedRequest = turn.id; state.request_id = turn.id; state.dirty = false;
      }
      state.status = turn.status;
      state.busy = ['queued', 'running'].includes(turn.status);
      state.notice = turn.status === 'completed' ? '本轮反馈已完成；可以修改答案后再次明确提交。'
        : ['failed', 'canceled', 'interrupted'].includes(turn.status) ? '本轮未完成，请查看会话提示；不会自动重交。' : '';
    }
    const pending = lastPending && parseUIResponse(lastPending.content);
    if (pending && matches(pending, state)) {
      applyAnswer(state, pending); state.request_id = lastPending.request_id;
      state.busy = lastPending.phase !== 'rejected'; state.status = lastPending.phase === 'submitting' ? 'sending' : lastPending.phase === 'rejected' ? 'rejected' : 'uncertain';
      state.notice = lastPending.phase === 'rejected' ? '答案未提交，请查看会话中的提示。' : '';
    }
    refresh(state);
  }
  return {
    renderMessage(raw, context, renderMarkdown) {
      const fragment = document.createDocumentFragment();
      for (const segment of extractSegments(raw)) {
        if (segment.kind === 'text') { fragment.append(renderMarkdown(segment.raw)); continue; }
        if (!segment.definition) { fragment.append(fallback(segment)); continue; }
        const key = componentKey(context, segment);
        const state = store.get(key, () => create(segment.definition, context, key));
        if (!state) {
          const placeholder = fallback(segment, '本会话互动状态已达上限，可手动激活空闲组件');
          const activate = element('button', 'chat-ui-reset', '激活此互动'); activate.type = 'button';
          activate.addEventListener('click', () => {
            if (JSON.stringify([context.role, context.session]) !== scope || !placeholder.isConnected) return;
            const active = store.get(key, () => create(segment.definition, context, key), true);
            if (!active) { activate.textContent = '请先保留或提交正在填写的答案'; return; }
            reconcileOne(active); placeholder.replaceWith(onPlace?.(active.node) || active.node);
          });
          placeholder.append(activate); fragment.append(placeholder); continue;
        }
        state.context = {...context}; reconcileOne(state);
        fragment.append(onPlace?.(state.node) || state.node);
      }
      return fragment;
    },
    syncScope(role, session) { const next = JSON.stringify([role, session]); if (scope !== next) { scope = next; lastTurns = []; lastPending = null; store.syncScope(role, session); } },
    reconcileResponses(turns = [], pending = null) { lastTurns = Array.isArray(turns) ? turns : []; lastPending = pending; for (const state of store.values()) reconcileOne(state); },
    retainSources(value) {
      const sources = value instanceof Set ? value : new Set((value || []).map(v => `${v.message_index}:${v.sha256}`));
      store.retainSources(sources);
    },
    update() { for (const state of store.values()) refresh(state); },
    captureFocus() {
      const node = document.activeElement;
      if (!node?.closest('.chat-ui-card')) return null;
      return {node, start: node.selectionStart, end: node.selectionEnd, direction: node.selectionDirection};
    },
    restoreFocus(value) {
      if (!value?.node?.isConnected || value.node.disabled || (document.activeElement !== document.body && document.activeElement !== value.node)) return;
      value.node.focus({preventScroll: true});
      if (typeof value.start === 'number' && typeof value.node.setSelectionRange === 'function') value.node.setSelectionRange(value.start, value.end, value.direction || 'none');
    },
    destroy() { store.clear(); scope = ''; lastTurns = []; lastPending = null; },
  };
}
