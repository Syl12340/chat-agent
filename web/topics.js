// Scheduled public-interest discovery. Reading this drawer never starts a scan.
export function createTopicsUI({ state, json, onError, drawer, onOpenSession }) {
  const $ = id => document.getElementById(id);
  const dialog = $('topics-dialog'), form = $('topics-settings-form'), list = $('topics-list');
  const message = $('topics-message'), tabs = [...dialog.querySelectorAll('[data-topics-view]')];
  const fields = ['enabled', 'queries', 'window-start', 'window-end', 'visits-per-day', 'scan-probability', 'daily-limit'];
  const controllers = new Set();
  let generation = 0, role = '', records = [], settings = null, busy = false, loaded = false;
  let conflict = false, view = 'share', timer = null, mutating = false;
  const ready = () => Boolean(state.token && state.config && navigator.onLine);
  const visible = () => dialog.open && document.visibilityState === 'visible';
  const context = () => ({ generation, role, token: state.token });
  const current = ctx => ctx.generation === generation && ctx.role === role && ctx.role === state.roleID && ctx.token === state.token && ready() && visible();
  const endpoint = (path, target = role) => `${path}?role_id=${encodeURIComponent(target)}`;
  const element = (tag, className, text = '') => { const node = document.createElement(tag); node.className = className; node.textContent = text; return node; };
  const say = (text = '') => { message.textContent = text; message.hidden = !text; };
  const labels = { scheduled: '已安排 · 尚未检索', skipped: '已跳过', running: '执行中', ready: '已准备好', silent: '本次没有分享', failed: '执行失败', interrupted: '执行中断', expired: '时段已过期', canceled: '已取消', seen: '已读' };
  const format = value => {
    const date = new Date(value);
    if (!value || !Number.isFinite(date.getTime())) return '';
    try { return new Intl.DateTimeFormat('zh-CN', { timeZone: state.config?.time_zone || 'Asia/Shanghai', month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false }).format(date); }
    catch { return date.toLocaleString(); }
  };
  function lock() {
    $('topics-open').disabled = !ready() || Boolean(state.loading);
    const unavailable = !ready() || role !== state.roleID || !dialog.open;
    $('topics-refresh').disabled = unavailable || busy;
    $('topics-settings-reload').disabled = unavailable || busy;
    $('topics-settings-save').disabled = unavailable || busy || !settings || conflict;
    for (const name of fields) $(`topics-${name}`).disabled = unavailable || busy || !settings;
    for (const button of list.querySelectorAll('button')) button.disabled = unavailable || busy || !loaded;
    list.setAttribute('aria-busy', String(busy));
  }
  function stop() {
    generation++;
    for (const controller of controllers) controller.abort();
    controllers.clear(); clearTimeout(timer); timer = null;
    if (mutating) { conflict = true; say('操作结果尚未确认。请重新读取设置与记录，再决定下一步。'); }
    mutating = false; busy = false;
  }
  function reset() {
    stop(); role = ''; records = []; settings = null; loaded = false; conflict = false;
    form.reset(); list.replaceChildren(); $('topics-role').textContent = '当前角色'; say(); lock();
  }
  function clear() { reset(); dialog.close(); }
  function report(error) {
    if (error?.name === 'AbortError') return;
    if (error?.status === 401) { onError(error); return; }
    say(error?.message || '共同话题暂时无法读取，请稍后重试。');
  }
  async function request(path, options = {}) {
    const controller = new AbortController(); controllers.add(controller);
    try { return await json(path, { ...options, signal: controller.signal }); }
    finally { controllers.delete(controller); }
  }
  function setSettings(value) {
    if (!value || value.role_id !== role) throw new Error('设置所属角色不一致，请重新读取。');
    settings = value; conflict = false;
    $('topics-enabled').checked = value.enabled === true;
    $('topics-queries').value = (value.queries || []).join('\n');
    $('topics-window-start').value = value.window_start || '09:00';
    $('topics-window-end').value = value.window_end || '21:00';
    $('topics-visits-per-day').value = String(value.visits_per_day ?? 2);
    $('topics-scan-probability').value = String(value.scan_probability ?? 70);
    $('topics-daily-limit').value = String(value.daily_limit ?? 3);
    $('topics-time-zone').textContent = `时间按服务端用户时区 ${state.config?.time_zone || 'Asia/Shanghai'} 解释。开始须早于结束，不跨天。`;
  }
  function action(parent, label, work, danger = false) {
    const button = element('button', `life-action${danger ? ' topics-delete' : ''}`, label); button.type = 'button';
    button.addEventListener('click', work); parent.append(button);
  }
  function sources(parent, items) {
    if (!Array.isArray(items) || !items.length) return;
    const links = element('ul', 'topics-sources');
    for (const source of items.slice(0, 5)) {
      let url;
      try { url = new URL(source.url); } catch { continue; }
      if (url.protocol !== 'https:' || url.username || url.password) continue;
      const row = document.createElement('li'), link = element('a', '', source.title || url.hostname);
      link.href = url.href; link.target = '_blank'; link.rel = 'noopener noreferrer';
      row.append(link); links.append(row);
    }
    if (links.children.length) parent.append(links);
  }
  function render() {
    list.replaceChildren();
    const shared = item => ['ready', 'seen'].includes(item.status) && Boolean(item.content?.trim());
    const items = records.filter(item => view === 'runs' || shared(item)).sort((a, b) => new Date(b.created_at) - new Date(a.created_at));
    if (!items.length && loaded) list.append(element('p', 'memory-empty', view === 'runs' ? '还没有运行记录。开启并保存设置后，服务器会在时间窗内按计划决定是否检索。' : '还没有准备好的分享。跳过、失败和未形成分享的记录可在“运行记录”查看。'));
    for (const item of items.slice(0, 100)) {
      const card = element('article', `topics-card${item.status === 'ready' ? ' topics-ready' : ''}`);
      card.append(element('h3', 'life-card-title', labels[item.status] || '未知状态'));
      const dates = [];
      if (format(item.due_at)) dates.push(`计划时间：${format(item.due_at)}`);
      if (format(item.updated_at)) dates.push(`更新：${format(item.updated_at)}`);
      if (dates.length) card.append(element('p', 'life-meta', dates.join(' · ')));
      if (view === 'share') {
        card.append(element('p', 'topics-content', item.content)); sources(card, item.sources);
      } else {
        if (item.query) card.append(element('p', 'life-meta', `配置查询：${item.query}`));
        if (item.reason) card.append(element('p', 'topics-reason', `运行说明：${item.reason}`));
        const count = value => Number.isFinite(value) && value >= 0 ? value : 0;
        if (['running','interrupted','canceled'].includes(item.status)) card.append(element('p','life-meta','执行次数尚未完整结算；中断或取消不代表之前没有发生调用。'));
        else card.append(element('p', 'life-meta', `搜索次数 ${count(item.search_count)} · 正文读取次数 ${count(item.read_count)} · 模型尝试次数 ${count(item.model_calls)}`));
        sources(card, item.sources);
      }
      const actions = element('div', 'life-actions topics-actions');
      if (item.session_id && onOpenSession) action(actions, '回到这段对话', async () => {
        dialog.close(); try { await onOpenSession(item.session_id); } catch (error) { onError(error); }
      });
      if (item.status === 'ready') action(actions, '标为已读', () => void mutate(item, 'seen'));
      if (['scheduled', 'running'].includes(item.status)) action(actions, '取消这次安排', () => void mutate(item, 'canceled'));
      action(actions, '删除记录', () => {
        if (!canWrite(item)) return;
        if (confirm('删除这条共同话题记录？这不会撤销已发生的搜索或模型调用，也不会返还当天额度。')) void mutate(item, 'delete');
      }, true);
      card.append(actions); list.append(card);
    }
    if (items.length > 100) list.append(element('p', 'life-meta', '当前显示最近 100 条记录。'));
    lock();
  }
  async function readRecords(ctx) {
    const result = await request(endpoint('/api/topics', ctx.role));
    if (!current(ctx)) return;
    if (!Array.isArray(result)) throw new Error('运行记录格式异常，请重新读取。');
    records = result.filter(item => item.role_id === ctx.role); loaded = true; render();
  }
  function schedule() {
    clearTimeout(timer); timer = null;
    if (visible() && ready() && role === state.roleID) timer = setTimeout(() => void refresh(), 30000);
  }
  function finish(ctx) {
    if (ctx.generation !== generation) return;
    if (mutating && ctx.token !== state.token && role === state.roleID && visible()) {
      conflict = true; loaded = false; records = []; render();
      say('连接凭据已更新，请重新读取设置与记录以确认操作结果。');
    }
    mutating = false; busy = false; lock(); schedule();
  }
  async function refresh(reloadSettings = false) {
    if (busy || !visible() || !ready() || role !== state.roleID) return;
    const ctx = context(); busy = true; lock();
    try {
      if (reloadSettings || !settings) {
        const value = await request(endpoint('/api/topics/settings', ctx.role));
        if (!current(ctx)) return;
        setSettings(value);
      }
      await readRecords(ctx);
    } catch (error) { if (current(ctx)) report(error); }
    finally { finish(ctx); }
  }
  const canWrite = item => !busy && loaded && ready() && visible() && role === state.roleID && item.role_id === role && records.includes(item);
  async function mutate(item, status) {
    if (!canWrite(item)) return;
    const ctx = context(); busy = true; mutating = true; say(); lock();
    try {
      const path = endpoint(`/api/topics/${encodeURIComponent(item.id)}`, ctx.role);
      if (status === 'delete') await request(`${path}&revision=${encodeURIComponent(item.revision)}`, { method: 'DELETE' });
      else await request(path, { method: 'PATCH', body: JSON.stringify({ revision: item.revision, status }) });
      if (!current(ctx)) return;
      records = []; loaded = false; render(); await readRecords(ctx);
      if (current(ctx)) say(status === 'seen' ? '已标为已读。' : status === 'canceled' ? '已取消这次安排。' : '记录已删除。');
    } catch (error) {
      if (!current(ctx)) return;
      records = []; loaded = false; render();
      try { await readRecords(ctx); } catch (readError) { if (current(ctx)) report(readError); }
      if (current(ctx)) {
        if (error?.status === 409) say('记录已变化，本次没有覆盖。已重新读取，请核对最新状态。');
        else report(error);
      }
    } finally { finish(ctx); }
  }
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (busy || !settings || conflict || !ready() || !visible() || role !== state.roleID || !form.reportValidity()) return;
    const queries = $('topics-queries').value.split('\n').map(value => value.trim()).filter(Boolean);
    if (queries.length > 5 || ($('topics-enabled').checked && !queries.length)) { say('请填写 1–5 个兴趣查询，每行一个。关闭时可以留空。'); return; }
    if ($('topics-window-start').value >= $('topics-window-end').value) { say('时间窗开始须早于结束，且在同一天内。'); return; }
    const body = { role_id: role, revision: settings.revision, enabled: $('topics-enabled').checked, queries, window_start: $('topics-window-start').value, window_end: $('topics-window-end').value, visits_per_day: Number($('topics-visits-per-day').value), scan_probability: Number($('topics-scan-probability').value), daily_limit: Number($('topics-daily-limit').value) };
    const ctx = context(); busy = true; mutating = true; say(); lock();
    try {
      const value = await request(endpoint('/api/topics/settings', ctx.role), { method: 'PATCH', body: JSON.stringify(body) });
      if (!current(ctx)) return;
      setSettings(value); records = []; loaded = false; render(); await readRecords(ctx);
      if (current(ctx)) say(value.enabled ? '设置已保存。服务器会在时间窗内按计划决定是否检索。' : '设置已保存，共同话题已关闭。');
    } catch (error) {
      if (!current(ctx)) return;
      conflict = true;
      if (error?.status === 409) say('设置已在别处变化，本次没有覆盖。草稿仍在，请重新读取设置后修改。');
      else { report(error); say(`${message.textContent} 请重新读取设置以确认保存结果。`); }
    } finally { finish(ctx); }
  });
  for (const tab of tabs) tab.addEventListener('click', () => {
    view = tab.dataset.topicsView;
    for (const node of tabs) { const selected = node === tab; node.classList.toggle('selected', selected); node.setAttribute('aria-pressed', String(selected)); }
    $('topics-records-help').textContent = view === 'runs' ? '这里只展示计划、状态、执行次数与来源等事实，不展示模型的隐藏思考。跳过不表示已经访问网页。' : '这里只展示已经准备好或标为已读的分享。打开列表不会自动标记已读。'; render();
  });
  $('topics-open').addEventListener('click', () => {
    if (!ready()) return;
    reset(); role = state.roleID;
    $('topics-role').textContent = `${state.roles?.find(item => item.id === role)?.name || role} · ${role}`;
    drawer(false); dialog.showModal(); lock(); void refresh(true);
  });
  $('topics-close').addEventListener('click', () => dialog.close());
  dialog.addEventListener('close', reset);
  $('topics-refresh').addEventListener('click', () => { say(); void refresh(); });
  $('topics-settings-reload').addEventListener('click', () => { say(); void refresh(true); });
  document.addEventListener('visibilitychange', () => {
    if (!visible()) { stop(); lock(); }
    else void refresh();
  });
  window.addEventListener('online', () => { lock(); if (visible()) void refresh(); });
  window.addEventListener('offline', () => { stop(); lock(); });
  lock();
  return { clear, lock, refresh };
}
