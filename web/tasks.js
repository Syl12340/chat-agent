// Role-scoped task and reminder UI. No system notification permission or push.
export function createTaskUI({ state, request, json, roleName, drawer, onError }) {
  const $ = id => document.getElementById(id);
  const ui = Object.fromEntries(['tasks-open','tasks-count','tasks-dialog','tasks-close','tasks-role','tasks-list','tasks-form','tasks-title','tasks-due','tasks-time','tasks-save','tasks-refresh','tasks-message','tasks-time-zone','reminder-banner','reminder-view'].map(id => [id, $(id)]));
  let role = '', todos = [], reminders = [], tab = 'todo', loading = false, pendingKey = null;
  const url = (kind, id = '', target = state.roleID) => `/api/${kind === 'todo' ? 'todos' : 'reminders'}${id ? `/${encodeURIComponent(id)}` : ''}?role_id=${encodeURIComponent(target)}`;
  const message = (text = '') => { ui['tasks-message'].textContent = text; ui['tasks-message'].hidden = !text; };
  const busy = () => !state.token || state.loading || state.streaming || state.external || state.compacting;
  const format = value => new Intl.DateTimeFormat('zh-CN', { timeZone: state.config?.time_zone || 'Asia/Shanghai', month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false }).format(new Date(value));
  function badge() {
    const due = reminders.filter(item => item.status === 'due').length;
    ui['tasks-count'].textContent = String(due); ui['tasks-count'].hidden = !due;
    ui['reminder-banner'].hidden = !due || !state.token;
    ui['reminder-view'].textContent = `有 ${due} 条到期提醒 · 查看收件箱 →`;
  }
  function clear() {
    ui['tasks-dialog'].close(); role = ''; todos = []; reminders = []; pendingKey = null;
    ui['tasks-list'].replaceChildren(); ui['tasks-form'].reset(); badge(); message();
  }
  function setTab(value) {
    tab = value; pendingKey = null;
    document.querySelectorAll('[data-task-tab]').forEach(button => button.classList.toggle('selected', button.dataset.taskTab === tab));
    ui['tasks-time'].hidden = tab !== 'reminder'; ui['tasks-due'].required = tab === 'reminder';
    ui['tasks-save'].textContent = tab === 'reminder' ? '添加提醒' : '添加待办';
    ui['tasks-title'].placeholder = tab === 'reminder' ? '例如：喝杯水，站起来活动一下' : '例如：整理本周的项目笔记';
    render();
  }
  function render() {
    ui['tasks-list'].replaceChildren();
    const rank = { due: 0, scheduled: 1, open: 0, done: 2, acknowledged: 3, canceled: 4 };
    const items = [...(tab === 'todo' ? todos : reminders)].sort((a, b) => rank[a.status] - rank[b.status] || (a.due_at && b.due_at ? new Date(a.due_at) - new Date(b.due_at) : 0));
    if (!items.length) { const empty = document.createElement('p'); empty.className = 'memory-empty'; empty.textContent = tab === 'todo' ? '还没有待办，先放下一件想完成的小事。' : '还没有提醒。服务端会记录到期状态，回来时依然能看到。'; ui['tasks-list'].append(empty); }
    const labels = { open: '待完成', done: '已完成', scheduled: '等待到期', due: '已到期', acknowledged: '已收到', canceled: '已取消' };
    for (const item of items) {
      const article = document.createElement('article'); article.className = `task-entry task-${item.status}`; article.dataset.taskId = item.id;
      const content = document.createElement('p'); content.className = 'memory-entry-content'; content.textContent = item.title;
      const footer = document.createElement('div'); footer.className = 'memory-entry-footer';
      const status = document.createElement('span'); status.className = 'task-status'; status.textContent = labels[item.status] + (item.due_at ? ` · ${format(item.due_at)}` : '');
      const actions = document.createElement('div'); actions.className = 'memory-entry-actions';
      const action = (label, name, work) => { const button = document.createElement('button'); button.type = 'button'; button.className = name; button.textContent = label; button.addEventListener('click', () => mutate(work)); actions.append(button); };
      const patch = status => () => json(url(item.kind, item.id), { method: 'PATCH', body: JSON.stringify({ status, revision: item.revision }) });
      if (item.kind === 'todo') action(item.status === 'open' ? '完成' : '重新打开', 'task-complete', patch(item.status === 'open' ? 'done' : 'open'));
      if (item.status === 'due') action('已收到', 'task-acknowledge', patch('acknowledged'));
      if (item.status === 'scheduled' || item.status === 'due') action('取消提醒', 'task-cancel', patch('canceled'));
      action('删除', 'delete', async () => { if (confirm('删除这条记录？未到期的提醒将不会再触发。')) await json(`${url(item.kind, item.id)}&revision=${item.revision}`, { method: 'DELETE' }); });
      footer.append(status, actions); article.append(content, footer); ui['tasks-list'].append(article);
    }
    badge();
  }
  async function refresh(full = ui['tasks-dialog'].open) {
    if (!state.token || !state.config) return;
    const target = state.roleID;
    if (role !== target) { role = target; todos = []; reminders = []; pendingKey = null; badge(); render(); }
    const load = async kind => {
      const response = await request(url(kind, '', target));
      const items = await response.json();
      return { items, schedulerOK: response.headers.get('X-Pocket-Scheduler-State') !== 'retrying' };
    };
    const [reminderResult, todoResult] = await Promise.all([load('reminder'), full ? load('todo') : Promise.resolve(null)]);
    if (target !== state.roleID || !state.token) return;
    reminders = reminderResult.items; if (todoResult) todos = todoResult.items;
    if (!reminderResult.schedulerOK) message('提醒状态写入暂时失败，服务端会重试；当前还不能视为已送达。');
    if (ui['tasks-dialog'].open) render(); else badge();
  }
  async function mutate(work) {
    if (loading) return;
    loading = true; ui['tasks-save'].disabled = true; ui['tasks-refresh'].disabled = true;
    ui['tasks-list'].querySelectorAll('button').forEach(button => { button.disabled = true; });
    try { await work(); await refresh(true); }
    catch (error) { if (error.status === 401) onError(error); else { message(error.message); if (error.status === 409) await refresh(true).catch(() => {}); } }
    finally { loading = false; ui['tasks-save'].disabled = false; ui['tasks-refresh'].disabled = false; ui['tasks-list'].querySelectorAll('button').forEach(button => { button.disabled = false; }); }
  }
  async function open(selected = 'todo') {
    if (busy()) return;
    drawer(false); message(); pendingKey = null; ui['tasks-form'].reset();
    ui['tasks-role'].textContent = roleName();
    ui['tasks-time-zone'].textContent = `设备时区：${Intl.DateTimeFormat().resolvedOptions().timeZone}；列表显示：${state.config.time_zone}`;
    if (role !== state.roleID) { todos = []; reminders = []; role = state.roleID; }
    setTab(selected); ui['tasks-dialog'].showModal();
    await mutate(async () => {});
  }
  ui['tasks-open'].addEventListener('click', () => open());
  ui['reminder-view'].addEventListener('click', () => open('reminder'));
  ui['tasks-close'].addEventListener('click', () => ui['tasks-dialog'].close());
  ui['tasks-refresh'].addEventListener('click', () => mutate(async () => {}));
  document.querySelectorAll('[data-task-tab]').forEach(button => button.addEventListener('click', () => setTab(button.dataset.taskTab)));
  ui['tasks-form'].addEventListener('submit', event => {
    event.preventDefault();
    const title = ui['tasks-title'].value.trim();
    if (!title || new TextEncoder().encode(title).length > 256) { message('标题需为 1–256 字节。'); return; }
    const body = { title };
    if (tab === 'reminder') { const date = new Date(ui['tasks-due'].value); if (!Number.isFinite(date.getTime())) { message('请选择有效的提醒时间。'); return; } body.due_at = date.toISOString(); }
    const signature = JSON.stringify([state.roleID, tab, body]);
    if (pendingKey?.signature !== signature) { const bytes = new Uint8Array(16); crypto.getRandomValues(bytes); pendingKey = { signature, key: Array.from(bytes, value => value.toString(16).padStart(2, '0')).join('') }; }
    body.idempotency_key = pendingKey.key;
    const kind = tab;
    mutate(async () => { await json(url(kind), { method: 'POST', body: JSON.stringify(body) }); pendingKey = null; ui['tasks-form'].reset(); message(kind === 'reminder' ? '提醒已保存，到期后会进入收件箱；关闭应用后的系统推送尚未提供。' : '待办已保存。'); });
  });
  const poll = () => { if (state.token && state.config && document.visibilityState === 'visible' && !loading) refresh().catch(error => { if (error.status === 401) onError(error); }); };
  setInterval(poll, 15000); document.addEventListener('visibilitychange', () => { if (document.visibilityState === 'visible') poll(); });
  return { refresh, clear };
}
