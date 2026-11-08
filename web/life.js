// Role-scoped event memory and opt-in care. All generated content is text only.
export function createLifeUI({ state, json, onError, drawer, onOpenSession, onCareReady }) {
  const $ = id => document.getElementById(id);
  const ids = ['life-dialog','life-close','life-role','life-list','life-form','life-title','life-content','life-status','life-event-at','life-followup-at','life-allow-followup','life-save','life-cancel-edit','life-editor-title','life-message','life-refresh','life-filter','life-query','life-time-zone','care-dialog','care-close','care-role','care-list','care-message','care-refresh','care-settings-form','care-settings-save','care-settings-reload','care-enabled','care-auto-generate','care-daily-limit','care-quiet-start','care-quiet-end','care-daily-time','care-time-zone','care-min-interval','care-snooze-until'];
  ids.push('care-auto-probability');
  const ui = Object.fromEntries(ids.map(id => [id, $(id)]));
  let role = '', generation = 0, operation = 0, mutationBusy = false;
  let episodes = [], care = [], settings = null, editing = null, settingsRevision = null;
  let episodeConflict = false, settingsConflict = false, refreshFlight = null, autoFlight = null, nextAutoCheck = 0;
  const generationControllers = new Set();
  const views = {life:0,care:0};
  const endpoint = (path, target = role) => `${path}?role_id=${encodeURIComponent(target)}`;
  const ready = () => Boolean(state.token && state.config && navigator.onLine);
  const chatting = () => state.streaming || state.external || state.compacting;
  const current = (target, stamp) => state.token && role === target && state.roleID === target && generation === stamp;
  const roleName = target => state.roles?.find(item => item.id === target)?.name || target;
  const say = (kind, text = '') => { const node = ui[`${kind}-message`]; node.textContent = text; node.hidden = !text; };
  const format = value => {
    if (!value) return '';
    const date = new Date(value); if (!Number.isFinite(date.getTime())) return '';
    try { return new Intl.DateTimeFormat('zh-CN', { timeZone: state.config?.time_zone || 'Asia/Shanghai', month: 'numeric', day: 'numeric', hour: '2-digit', minute: '2-digit', hour12: false }).format(date); }
    catch { return date.toLocaleString(); }
  };
  const localValue = value => { if (!value) return ''; const d = new Date(value); return Number.isFinite(d.getTime()) ? new Date(d.getTime() - d.getTimezoneOffset() * 60000).toISOString().slice(0,16) : ''; };
  const fieldDate = id => { const text = ui[id].value; if (!text) return null; const d = new Date(text); if (!Number.isFinite(d.getTime())) throw new Error('请选择有效的时间。'); return d.toISOString(); };
  const cancelGeneration = () => { for (const controller of generationControllers) controller.abort(); generationControllers.clear(); };
  const snoozed = () => settings?.snooze_until && new Date(settings.snooze_until).getTime() > Date.now();
  function mayNotify() {
    if (!settings?.enabled || snoozed() || document.visibilityState !== 'visible') return false;
    try {
      const parts=new Intl.DateTimeFormat('en-GB',{timeZone:state.config?.time_zone || 'Asia/Shanghai',hour:'2-digit',minute:'2-digit',hourCycle:'h23'}).formatToParts(new Date());
      const minute=Number(parts.find(x=>x.type==='hour').value)*60+Number(parts.find(x=>x.type==='minute').value);
      const parse=value=>{const [h,m]=value.split(':').map(Number);return h*60+m;};
      const start=parse(settings.quiet_start),end=parse(settings.quiet_end);
      return start===end || !(start<end ? minute>=start && minute<end : minute>=start || minute<end);
    } catch { return false; }
  }
  function badge() {
    const count = state.token && role === state.roleID ? care.filter(item => item.status === 'ready' || item.status === 'due').length : 0;
    const node = $('care-count'); if (node) { node.textContent = String(count); node.hidden = !count; }
    const banner = $('care-banner'), view = $('care-view');
    if (banner) banner.hidden = !count;
    if (view) view.textContent = `有 ${count} 条关怀待查看 · 打开收件箱 →`;
  }
  function resetEditor() {
    editing = null; episodeConflict = false; ui['life-form'].reset();
    ui['life-status'].value = 'open'; ui['life-editor-title'].textContent = '留下一件愿意下次接着聊的事';
    ui['life-save'].textContent = '保存并用于对话'; ui['life-cancel-edit'].hidden = true;
    ui['life-followup-at'].required = false; lock();
  }
  function setSettingsForm(value) {
    settingsRevision = value.revision; settingsConflict = false;
    ui['care-enabled'].checked = Boolean(value.enabled); ui['care-auto-generate'].checked = Boolean(value.auto_generate);
    ui['care-daily-limit'].value = String(value.daily_limit ?? 1);
    ui['care-auto-probability'].value = String(value.auto_probability ?? 50);
    ui['care-min-interval'].value = String(value.min_interval_minutes ?? 180);
    ui['care-snooze-until'].value = localValue(value.snooze_until);
    ui['care-quiet-start'].value = value.quiet_start || '22:00'; ui['care-quiet-end'].value = value.quiet_end || '08:00'; ui['care-daily-time'].value = value.daily_time || '';
    lock();
  }
  function adoptRole() {
    const target = state.roleID || 'companion';
    if (target === role) return;
    cancelGeneration(); generation++; operation++; mutationBusy = false; role = target;
    episodes = []; care = []; settings = null; editing = null; settingsRevision = null;
    episodeConflict = false; settingsConflict = false; nextAutoCheck = 0;
    refreshFlight = null; autoFlight = null;
    ui['life-dialog'].close(); ui['care-dialog'].close(); ui['life-form'].reset(); ui['care-settings-form'].reset();
    ui['life-list'].replaceChildren(); ui['care-list'].replaceChildren(); say('life'); say('care'); badge();
  }
  function lock() {
    const unavailable = !state.token || !state.config;
    for (const id of ['life-open','care-open']) { const node = $(id); if (node) node.disabled = unavailable; }
    const writeLocked = unavailable || mutationBusy || chatting();
    ui['life-save'].disabled = writeLocked || episodeConflict;
    ui['life-refresh'].disabled = mutationBusy || unavailable; ui['care-refresh'].disabled = mutationBusy || unavailable;
    ui['care-settings-save'].disabled = unavailable || mutationBusy || settingsRevision === null || settingsConflict;
    ui['care-settings-reload'].disabled = unavailable || mutationBusy;
    ui['care-auto-generate'].disabled = unavailable || mutationBusy || !ui['care-enabled'].checked;
    ui['care-auto-probability'].disabled = unavailable || mutationBusy;
    for (const node of ui['life-list'].querySelectorAll('button[data-write]')) node.disabled = writeLocked;
    for (const node of ui['care-list'].querySelectorAll('button[data-write]')) node.disabled = unavailable || mutationBusy;
  }
  const element = (tag, className, text = '') => { const node = document.createElement(tag); if (className) node.className = className; node.textContent = text; return node; };
  function action(parent, label, work, write = true) {
    const button = element('button', 'life-action', label); button.type = 'button'; if (write) button.dataset.write = 'true';
    button.addEventListener('click', work); parent.append(button); return button;
  }
  function renderEpisodes() {
    ui['life-list'].replaceChildren();
    const labels = { pending:'待确认', open:'正在经历', resolved:'已告一段落', hidden:'不主动提及' };
    const rank = { pending:0, open:1, resolved:2, hidden:3 }, q = ui['life-query'].value.trim().toLocaleLowerCase(), filter = ui['life-filter'].value;
    const matches = episodes.filter(item => (filter === 'all' || item.status === filter) && (!q || `${item.title} ${item.content}`.toLocaleLowerCase().includes(q))).sort((a,b) => (rank[a.status] ?? 4) - (rank[b.status] ?? 4) || new Date(b.updated_at) - new Date(a.updated_at));
    if (!matches.length) ui['life-list'].append(element('p','memory-empty','还没有匹配的事件。可以手动留下，也可以在聊天中明确说“记住这件事”。'));
    for (const item of matches.slice(0,100)) {
      const article = element('article', `life-card life-${item.status}`); article.dataset.episodeId = item.id;
      article.append(element('h3','life-card-title',item.title), element('p','life-content',item.content));
      const pieces = [labels[item.status] || item.status];
      if (item.event_at) pieces.push(`事情时间：${format(item.event_at)}`);
      if (item.followup_at) pieces.push(`跟进时间：${format(item.followup_at)}${item.allow_followup ? '' : '（未允许关怀）'}`);
      article.append(element('p','life-meta',pieces.join(' · ')));
      if (item.source_text) { const details = document.createElement('details'); details.className = 'life-source'; details.append(element('summary','', '当时怎么说的'), element('p','life-content',item.source_text)); article.append(details); }
      const actions = element('div','life-actions');
      const patchStatus = status => mutate('life', async target => json(endpoint(`/api/episodes/${encodeURIComponent(item.id)}`,target),{method:'PATCH',body:JSON.stringify({revision:item.revision,status})}));
      if (item.status === 'pending') action(actions,'确认并用于对话',() => patchStatus('open'));
      if (item.status === 'open') action(actions,'已告一段落',() => patchStatus('resolved'));
      if (item.status === 'open' || item.status === 'resolved') action(actions,'不主动提及',() => patchStatus('hidden'));
      if (item.status === 'hidden' || item.status === 'resolved') action(actions,'重新接着聊',() => patchStatus('open'));
      action(actions,'编辑',() => {
        editing = {...item}; episodeConflict = false;
        ui['life-title'].value = item.title; ui['life-content'].value = item.content; ui['life-status'].value = item.status;
        ui['life-event-at'].value = localValue(item.event_at); ui['life-followup-at'].value = localValue(item.followup_at); ui['life-allow-followup'].checked = Boolean(item.allow_followup);
        ui['life-followup-at'].required = Boolean(item.allow_followup); ui['life-editor-title'].textContent = '更新这件事'; ui['life-save'].textContent = '保存修改'; ui['life-cancel-edit'].hidden = false;
        say('life','修改已确认的事件后，后续对话会使用更新后的记忆；原聊天仍可查看。'); lock(); ui['life-title'].focus();
      });
      if (item.source_session_id && onOpenSession) action(actions,'查看原对话', async () => {
        ui['life-dialog'].close(); try { await onOpenSession(item.source_session_id); } catch (error) { report('life',error); }
      },false);
      action(actions,'删除',() => { if (confirm('删除这件事？后续对话不再检索它，原聊天记录仍保留。')) mutate('life', async target => json(`${endpoint(`/api/episodes/${encodeURIComponent(item.id)}`,target)}&revision=${encodeURIComponent(item.revision)}`,{method:'DELETE'})); });
      article.append(actions); ui['life-list'].append(article);
    }
    if (matches.length > 100) ui['life-list'].append(element('p','life-meta','当前显示前 100 条，可缩小关键词或状态范围。'));
    lock();
  }
  function renderCare() {
    ui['care-list'].replaceChildren();
    const labels = {due:'等待生成',generating:'正在生成',ready:'待查看',seen:'已看过',canceled:'已取消',failed:'生成未完成',interrupted:'生成已中断',skipped:'本次未主动搭话（未调用模型）'};
    const rank = {ready:0,due:1,generating:2,failed:3,interrupted:3,seen:4,skipped:5,canceled:6};
    const items = [...care].sort((a,b) => (rank[a.status] ?? 9) - (rank[b.status] ?? 9) || new Date(b.created_at) - new Date(a.created_at));
    if (!items.length) ui['care-list'].append(element('p','memory-empty',settings?.enabled ? '暂时没有到期关怀。可以给已确认事件设置跟进时间，或设置每日轻问候。' : '关怀默认关闭。你可以在下面按自己的节奏开启。'));
    for (const item of items.slice(0,100)) {
      const article = element('article',`life-card care-${item.status}`); article.dataset.careId = item.id;
      article.append(element('h3','life-card-title',item.title),element('p','life-meta',`${labels[item.status] || item.status}${item.due_at ? ` · ${format(item.due_at)}` : ''}`));
      if (item.status === 'skipped') {
        const probability = Number.isInteger(item.probability) ? `（本次主动概率 ${item.probability}%）` : '';
        article.append(element('p','life-meta',`这次自动闲聊未被随机选中${probability}。明确的事件跟进仍按设置进行。`));
      }
      if (item.content) article.append(element('p','life-content care-content',item.content));
      if (item.error) article.append(element('p','life-meta',item.error));
      const actions = element('div','life-actions');
      if (item.session_id && onOpenSession) action(actions,'回到这段对话', async () => {
        ui['care-dialog'].close(); try { await onOpenSession(item.session_id); } catch (error) { report('care',error); }
      },false);
      if (item.status === 'due') action(actions,'生成问候',() => generateManual(item));
      if (item.status === 'ready') action(actions,'已看过',() => mutate('care',target => json(endpoint(`/api/care/${encodeURIComponent(item.id)}`,target),{method:'PATCH',body:JSON.stringify({revision:item.revision,status:'seen'})})));
      if (['due','ready'].includes(item.status)) action(actions,'取消',() => mutate('care',target => json(endpoint(`/api/care/${encodeURIComponent(item.id)}`,target),{method:'PATCH',body:JSON.stringify({revision:item.revision,status:'canceled'})})));
      if (item.status === 'failed' || item.status === 'interrupted') article.append(element('p','life-meta','这次不会自动重试或再次调用模型。'));
      if (actions.childElementCount) article.append(actions);
      ui['care-list'].append(article);
    }
    badge(); lock();
  }
  function report(kind,error,quiet = false) {
    if (error?.name === 'AbortError') { if (!quiet) say(kind,'生成已中止，请刷新收件箱查看状态。'); return; }
    if (error?.status === 401) { onError(error); return; }
    if (!quiet || ui[`${kind}-dialog`].open) say(kind,error?.message || '暂时无法连接，请稍后再试。');
  }
  async function refresh(options = {}) {
    if (!ready()) { lock(); return; }
    adoptRole();
    const target = role, stamp = generation;
    if (refreshFlight?.generation === stamp) return refreshFlight.promise;
    const flight = {generation:stamp,promise:null};
    flight.promise = (async () => {
      const results = await Promise.all([json(endpoint('/api/episodes',target)),json(endpoint('/api/care/settings',target)),json(endpoint('/api/care',target))]);
      if (!current(target,stamp)) return;
      episodes = results[0]; settings = results[1]; care = results[2];
      if (mayNotify()) { try { Promise.resolve(onCareReady?.(target,care.filter(item=>item.status==='ready'))).catch(()=>{}); } catch {} }
      if (ui['life-dialog'].open) renderEpisodes();
      if (ui['care-dialog'].open) renderCare(); else badge();
      lock();
      if (options.auto !== false) void autoCheck(target,stamp);
    })();
    refreshFlight = flight;
    try { await flight.promise; } finally { if (refreshFlight === flight) refreshFlight = null; }
  }
  async function autoCheck(target,stamp) {
    if (!current(target,stamp) || document.visibilityState !== 'visible' || !navigator.onLine || !settings?.enabled || !settings.auto_generate || snoozed() || chatting() || mutationBusy || autoFlight || Date.now() < nextAutoCheck) return;
    const controller = new AbortController(), marker = {generation:stamp};
    generationControllers.add(controller); autoFlight = marker; nextAutoCheck = Date.now() + 15000;
    try {
      const result = await json(endpoint('/api/care/check',target),{method:'POST',signal:controller.signal});
      if (current(target,stamp) && (result.generated || result.reason === 'random_skip' || result.item?.status === 'skipped')) await refresh({auto:false});
    } catch (error) {
      if (current(target,stamp)) { nextAutoCheck = Date.now() + 60000; report('care',error,true); }
    } finally { generationControllers.delete(controller); if (autoFlight === marker) autoFlight = null; }
  }
  async function mutate(kind,work,after) {
    if (!ready() || mutationBusy) return;
    adoptRole(); const target = role, stamp = generation, view = views[kind], ticket = ++operation;
    mutationBusy = true; say(kind); lock();
    try {
      const result = await work(target);
      if (!current(target,stamp)) return;
      if (after && views[kind] === view) after(result);
      await refresh({auto:false});
    } catch (error) {
      if (current(target,stamp) && views[kind] === view) {
        if (error.status === 409) {
          if (kind === 'life' && editing) episodeConflict = true;
          if (kind === 'care' && work.settingsWrite) settingsConflict = true;
          await refresh({auto:false}).catch(() => {});
        }
        report(kind,error);
      }
    } finally { if (operation === ticket) { mutationBusy = false; lock(); } }
  }
  async function generateManual(item) {
    const controller = new AbortController(); generationControllers.add(controller);
    try { await mutate('care',target => json(endpoint(`/api/care/${encodeURIComponent(item.id)}/generate`,target),{method:'POST',signal:controller.signal})); }
    finally { generationControllers.delete(controller); }
  }
  async function open(kind) {
    if (!ready()) return;
    adoptRole(); const target = role, stamp = generation;
    drawer(false); say(kind); ui[`${kind}-role`].textContent = roleName(target);
    const zone = kind === 'care' ? `每日问候和安静时段使用时区：${state.config?.time_zone || 'Asia/Shanghai'}` : `日期输入使用设备时区；列表显示：${state.config?.time_zone || 'Asia/Shanghai'}`;
    ui[`${kind}-time-zone`].textContent = zone;
    if (kind === 'life') { resetEditor(); renderEpisodes(); }
    else { settingsRevision = null; settingsConflict = false; renderCare(); }
    ui[`${kind}-dialog`].showModal(); const view = ++views[kind]; lock();
    try { await refresh({auto:false}); if (kind === 'care' && current(target,stamp) && views[kind] === view && ui['care-dialog'].open && settings) setSettingsForm(settings); }
    catch (error) { if (current(target,stamp) && views[kind] === view) report(kind,error); }
  }
  $('life-open')?.addEventListener('click',() => open('life'));
  $('care-open')?.addEventListener('click',() => open('care'));
  $('care-view')?.addEventListener('click',() => open('care'));
  for (const kind of ['life','care']) {
    ui[`${kind}-dialog`].addEventListener('close',() => { views[kind]++; });
    ui[`${kind}-close`].addEventListener('click',() => ui[`${kind}-dialog`].close());
    ui[`${kind}-refresh`].addEventListener('click',() => refresh({auto:false}).catch(error => report(kind,error)));
  }
  ui['life-cancel-edit'].addEventListener('click',() => { resetEditor(); say('life'); });
  ui['life-filter'].addEventListener('change',renderEpisodes); ui['life-query'].addEventListener('input',renderEpisodes);
  ui['life-allow-followup'].addEventListener('change',() => { ui['life-followup-at'].required = ui['life-allow-followup'].checked; });
  ui['life-form'].addEventListener('submit',event => {
    event.preventDefault();
    let body;
    try {
      body = {title:ui['life-title'].value.trim(),content:ui['life-content'].value.trim(),status:ui['life-status'].value,event_at:fieldDate('life-event-at'),followup_at:fieldDate('life-followup-at'),allow_followup:ui['life-allow-followup'].checked};
      if (!body.title || !body.content || new TextEncoder().encode(body.title).length > 256 || new TextEncoder().encode(body.content).length > 4096) throw new Error('标题最多 256 字节、内容最多 4096 字节，不能留空。');
      if (body.allow_followup && !body.followup_at) throw new Error('允许关怀时，请同时设置跟进时间。');
      if (editing) body.revision = editing.revision;
    } catch (error) { say('life',error.message); return; }
    const old = editing;
    mutate('life',target => json(endpoint(old ? `/api/episodes/${encodeURIComponent(old.id)}` : '/api/episodes',target),{method:old ? 'PATCH':'POST',body:JSON.stringify(body)}),() => { resetEditor(); say('life',old ? '事件已更新，之后会使用新的内容。原聊天仍可查看。':'已保存，可以用于这个角色的之后对话。'); });
  });
  ui['care-enabled'].addEventListener('change',lock);
  ui['care-settings-reload'].addEventListener('click',async () => { const target = role, stamp = generation, view = views.care; try { await refresh({auto:false}); if (current(target,stamp) && views.care === view && ui['care-dialog'].open && settings) { setSettingsForm(settings); say('care','已重新读取当前设置。'); } } catch (error) { if (current(target,stamp) && views.care === view) report('care',error); } });
  ui['care-settings-form'].addEventListener('submit',event => {
    event.preventDefault(); if (settingsRevision === null || settingsConflict) return;
    const limit = Number(ui['care-daily-limit'].value);
    if (!Number.isInteger(limit) || limit < 1 || limit > 5) { say('care','每日模型调用上限请选择 1–5 次。'); return; }
    const probability = Number(ui['care-auto-probability'].value);
    if (ui['care-auto-probability'].value === '' || !Number.isInteger(probability) || probability < 0 || probability > 100) { say('care','自动闲聊概率请选择 0–100 的整数。'); return; }
    const interval=Number(ui['care-min-interval'].value);
    if (!Number.isInteger(interval) || interval<15 || interval>1440) {say('care','两次问候间隔请选择 15–1440 分钟。');return;}
    let until;try{until=fieldDate('care-snooze-until');}catch(error){say('care',error.message);return;}
    const body = {revision:settingsRevision,enabled:ui['care-enabled'].checked,auto_generate:ui['care-enabled'].checked && ui['care-auto-generate'].checked,auto_probability:probability,daily_limit:limit,min_interval_minutes:interval,snooze_until:until,quiet_start:ui['care-quiet-start'].value,quiet_end:ui['care-quiet-end'].value,daily_time:ui['care-daily-time'].value};
    const work = target => json(endpoint('/api/care/settings',target),{method:'PATCH',body:JSON.stringify(body)}); work.settingsWrite = true;
    mutate('care',work,result => { settings = result; setSettingsForm(result); nextAutoCheck = 0; say('care',body.enabled ? '关怀设置已保存。只在约定范围内准备问候。':'关怀已关闭，尚未交付的问候将停止。'); });
  });
  function clear() {
    cancelGeneration(); generation++; operation++; mutationBusy = false; role = ''; episodes = []; care = []; settings = null; editing = null; settingsRevision = null; episodeConflict = false; settingsConflict = false; refreshFlight = null; autoFlight = null; nextAutoCheck = 0;
    ui['life-dialog'].close(); ui['care-dialog'].close(); ui['life-form'].reset(); ui['care-settings-form'].reset(); ui['life-list'].replaceChildren(); ui['care-list'].replaceChildren(); say('life'); say('care'); badge(); lock();
  }
  const poll = () => { if (ready() && document.visibilityState === 'visible' && !mutationBusy) refresh().catch(error => report('care',error,true)); };
  setInterval(poll,15000);
  document.addEventListener('visibilitychange',() => { if (document.visibilityState === 'visible') poll(); else cancelGeneration(); });
  window.addEventListener('online',poll);
  lock();
  return {clear,refresh,lock};
}
