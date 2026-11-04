// Human review is the only UI path that applies a generated PROFILE.md proposal.
export function createRoleProposalsUI({ state, json, onError, drawer }) {
  const $ = id => document.getElementById(id);
  const dialog = $('role-proposals-dialog'), list = $('role-proposals-list');
  const message = $('role-proposals-message'), refresh = $('role-proposals-refresh'), filter = $('role-proposals-filter');
  let generation = 0, activeRole = '', proposals = [], busy = false, loaded = false;
  const say = text => { message.textContent = text; message.hidden = !text; };
  const url = (role, id = '') => `/api/roles/${encodeURIComponent(role)}/proposals${id ? `/${encodeURIComponent(id)}` : ''}`;
  const context = () => ({ generation, role: activeRole, token: state.token });
  const current = ctx => ctx.generation === generation && ctx.role === activeRole && ctx.role === state.roleID && ctx.token === state.token && Boolean(state.token && state.config) && dialog.open;
  const element = (tag, className, text = '') => { const node = document.createElement(tag); node.className = className; node.textContent = text; return node; };
  function lock() {
    refresh.disabled = busy || !state.token || !state.config;
    filter.disabled = busy;
    list.setAttribute('aria-busy', String(busy));
    for (const button of list.querySelectorAll('button')) button.disabled = busy || !loaded || activeRole !== state.roleID || !state.token || !state.config;
  }
  function reset() {
    generation++; activeRole = ''; proposals = []; busy = false; loaded = false;
    list.replaceChildren(); $('role-proposals-role').textContent = '当前角色'; say(''); lock();
  }
  function clear() { reset(); dialog.close(); }
  function report(error) {
    if (error?.status === 401) { onError(error); return; }
    say(error?.message || '暂时无法读取角色建议，请稍后重试。');
  }
  function action(parent, label, className, work) {
    const button = element('button', `life-action ${className}`, label); button.type = 'button';
    button.addEventListener('click', work); parent.append(button);
  }
  function render() {
    list.replaceChildren();
    const labels = { pending: '待审核 · 尚未影响角色', applied: '已应用', rejected: '已拒绝 · 未应用' };
    const rank = { pending: 0, applied: 1, rejected: 2 };
    const items = proposals.filter(item => filter.value === 'all' || item.status === filter.value)
      .sort((a, b) => (rank[a.status] ?? 3) - (rank[b.status] ?? 3) || new Date(b.created_at) - new Date(a.created_at));
    if (!items.length && loaded) list.append(element('p', 'memory-empty', '这里还没有这个状态的角色变化建议。你可以在聊天中提出希望调整的交流方式。'));
    for (const item of items.slice(0, 100)) {
      const card = element('article', 'role-proposal-card'); card.dataset.proposalId = item.id;
      card.append(element('h3', 'life-card-title', '交流风格 · PROFILE.md'), element('p', 'life-meta', labels[item.status] || item.status));
      card.append(element('h4', 'role-proposal-label', '为什么提出这个变化'), element('p', 'life-content', item.reason || '未提供原因。'));
      const comparison = element('div', 'role-proposal-comparison');
      for (const [label, content] of [['修改前', item.before], ['建议修改后', item.content]]) {
        const section = element('section', 'role-proposal-version');
        const documentText = element('pre', 'role-proposal-content', content || '（空文档）');
        documentText.tabIndex = 0; documentText.setAttribute('aria-label', `${label}的完整设定`);
        section.append(element('h4', 'role-proposal-label', label), documentText);
        comparison.append(section);
      }
      card.append(comparison);
      if (item.source_session_id) card.append(element('p', 'life-meta', `来源会话：${item.source_session_id}`));
      const actions = element('div', 'life-actions');
      if (item.status === 'pending') {
        action(actions, '批准并应用', 'role-proposal-approve', () => {
          if (busy || !loaded || activeRole !== state.roleID || !state.token || !dialog.open) return;
          if (confirm('批准这项建议并更新当前角色的 PROFILE.md？请先核对修改前后内容，之后的对话会使用新设定。')) void mutate(item, 'approve');
        });
        action(actions, '拒绝建议', '', () => void mutate(item, 'reject'));
      }
      action(actions, '删除建议记录', 'role-proposal-delete', () => {
        if (busy || !loaded || activeRole !== state.roleID || !state.token || !dialog.open) return;
        if (confirm('删除这条建议记录？删除已应用的记录不会撤销角色设定的变化。')) void mutate(item, 'delete');
      });
      card.append(actions); list.append(card);
    }
    if (items.length > 100) list.append(element('p', 'life-meta', '当前显示前 100 条，请选择状态缩小范围。'));
    lock();
  }
  async function read(ctx) {
    const result = await json(url(ctx.role));
    if (!current(ctx)) return;
    proposals = (Array.isArray(result) ? result : []).filter(item => item.role_id === ctx.role && item.file === 'PROFILE.md');
    loaded = true; render();
  }
  async function load() {
    if (busy || !state.token || !state.config || activeRole !== state.roleID || !dialog.open) return;
    const ctx = context(); busy = true; say(''); lock();
    try { await read(ctx); }
    catch (error) { if (current(ctx)) report(error); }
    finally { if (current(ctx)) { busy = false; lock(); } }
  }
  async function mutate(item, action) {
    if (busy || !loaded || !state.token || !state.config || activeRole !== state.roleID || !dialog.open || item.role_id !== activeRole || !proposals.includes(item)) return;
    const ctx = context(); busy = true; say(''); lock();
    try {
      if (action === 'delete') await json(`${url(ctx.role, item.id)}?revision=${encodeURIComponent(item.revision)}`, { method: 'DELETE' });
      else {
        const result=await json(url(ctx.role, item.id), { method: 'PATCH', body: JSON.stringify({ revision: item.revision, action }) });
        if(action==='reject' && result.status==='applied') action='settled';
      }
      if (!current(ctx)) return;
      // Never leave an actionable stale revision visible when the refresh fails.
      loaded = false; proposals = []; render();
      await read(ctx);
      if (current(ctx)) say(action === 'approve' ? '已批准并应用。之后的新请求会使用更新后的交流风格。' : action === 'settled' ? '当前角色档案已包含该建议，已确认其应用状态。' : action === 'reject' ? '已拒绝这项建议，角色设定未改变。' : '建议记录已删除。');
    } catch (error) {
      if (!current(ctx)) return;
      if (error?.status === 409) {
        loaded = false; proposals = []; render();
        try { await read(ctx); } catch (refreshError) { if (current(ctx)) report(refreshError); }
        if (current(ctx)) say('候选或角色设定已变化，本次未覆盖。请重新读取并核对；若修改前的设定已过期，请在聊天中重新生成建议。');
      } else report(error);
    } finally { if (current(ctx)) { busy = false; lock(); } }
  }
  $('role-proposals-open').addEventListener('click', () => {
    if (!state.token || !state.config) return;
    reset(); activeRole = state.roleID;
    $('role-proposals-role').textContent = `${state.roles?.find(role => role.id === activeRole)?.name || activeRole} · ${activeRole}`;
    filter.value = 'pending'; drawer(false); if (!dialog.open) dialog.showModal(); void load();
  });
  $('role-proposals-close').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', reset);
  refresh.addEventListener('click', () => void load());
  filter.addEventListener('change', render);
  return { clear };
}
