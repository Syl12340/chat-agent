function el(tag, className, text) {
  const node = document.createElement(tag); if (className) node.className = className;
  if (text !== undefined) node.textContent = text; return node;
}
export function renderQuiz(definition, state, {submit, allowed, changed}) {
  const p = definition.props; const form = el('form', 'chat-ui-quiz');
  const group = el('fieldset', 'chat-ui-fields');
  group.append(el('legend', 'chat-ui-question', p.question));
  const inputs = [];
  if (definition.type === 'quiz.single_choice') {
    for (const option of p.options) {
      const label = el('label', 'chat-ui-option'); const input = el('input');
      input.type = 'radio'; input.name = `ui-${state.serial}`; input.value = option.id;
      input.checked = state.choice === option.id;
      input.addEventListener('change', () => { state.choice = option.id; state.dirty = true; changed(); });
      label.append(input, el('span', '', option.label)); group.append(label); inputs.push(input);
    }
  } else {
    for (const field of p.fields) {
      const label = el('label', 'chat-ui-field'); const input = el('input'); input.type = 'text';
      input.autocomplete = 'off'; input.maxLength = 256; input.placeholder = field.placeholder || '';
      input.value = state.fields[field.id] || ''; input.dataset.uiField = field.id;
      input.addEventListener('input', () => {
        const short = [...input.value].slice(0, 128).join('');
        if (short !== input.value) input.value = short;
        state.fields[field.id] = short; state.dirty = true; changed();
      });
      label.append(el('span', '', field.label), input); group.append(label); inputs.push(input);
    }
  }
  const button = el('button', 'chat-ui-submit', '提交答案'); button.type = 'submit';
  const note = el('p', 'chat-ui-notice', '发送到当前会话，由模型反馈。不会自动保存为记忆。');
  note.setAttribute('aria-live', 'polite');
  form.append(group, button, note);
  const answer = () => definition.type === 'quiz.single_choice'
    ? (p.options.some(o => o.id === state.choice) ? {option_id: state.choice} : null)
    : (p.fields.every(f => typeof state.fields[f.id] === 'string' && state.fields[f.id].trim() && [...state.fields[f.id]].length <= 128)
      ? {fields: p.fields.map(f => ({id: f.id, value: state.fields[f.id]}))} : null);
  form.addEventListener('submit', event => {
    event.preventDefault();
    if (allowed() !== true || state.busy || !answer()) { update(); return; }
    submit(answer());
  });
  function update() {
    const permission = allowed(); const locked = permission !== true || state.busy;
    group.disabled = locked;
    button.disabled = locked || !answer();
    button.textContent = state.busy ? ({uncertain:'正在确认提交',queued:'待发送',sending:'正在提交'}[state.status] || '等待反馈') : '提交答案';
    note.textContent = state.notice || (state.busy ? (state.status === 'uncertain' ? '提交状态待确认，请使用当前会话的确认入口。' : state.status === 'queued' ? '答案已排队，尚未生成反馈。' : '正在提交或等待本轮反馈。')
      : permission !== true ? String(permission || '当前仅供预览') : '发送到当前会话并调用模型；反馈不会自动保存为记忆。');
    for (const input of inputs) {
      if (input.type === 'radio') input.checked = state.choice === input.value;
      else if (document.activeElement !== input) input.value = state.fields[input.dataset.uiField] || '';
    }
  }
  update(); return {node: form, update, dispose: () => { group.disabled = true; button.disabled = true; }};
}
