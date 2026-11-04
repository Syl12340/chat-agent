// No background polling or model calls from this panel. Explicit chat tools can
// also save progress; refresh adopts their revision without replacing drafts.
export function createActivitiesUI({ state, json, drawer, onError, onContinue, onChanged }) {
  const $ = id => document.getElementById(id);
  const ui = Object.fromEntries(['activities-dialog','activities-role','activities-list','activities-form','activities-kind','activities-title','activities-goal','activities-progress','activities-progress-field','activities-editor-title','activities-save','activities-cancel-edit','activities-reload-edit','activities-message','activities-refresh','activities-pending','activities-pending-text','activities-confirm','activities-dismiss-pending'].map(id => [id,$(id)]));
  const templates = {
    bedtime:{title:'睡前聊一会儿',goal:'睡前轻松聊几句，慢慢放松，随时可以结束。'},
    reflection:{title:'一天里的一件小事',goal:'聊聊今天留在心里的一件小事，不急着总结或给建议。'},
    interest:{title:'共同兴趣',goal:'围绕我喜欢的话题聊一聊，记下想留到下次的部分。'},
    small_step:{title:'小目标陪伴',goal:'一起想一件我愿意做的小事，按我的节奏继续，不催促或打分。'}
  };
  const labels = {active:'正在一起做',paused:'已暂停',completed:'已结束'};
  const prefix = 'chat-agent-activity-pending:';
  const drafts = new Map();
  let role = '', generation = 0, view = 0, operation = 0, busy = false, items = [], editing = null, conflict = false, pending = null, bound = null, flight = null;
  const endpoint = (path,target = role) => `${path}?role_id=${encodeURIComponent(target)}`;
  const ready = () => Boolean(state.token && state.config);
  const chatting = () => Boolean(state.streaming || state.external || state.compacting || state.loading || state.submitting || state.submitUnknown || state.queuedCount);
  const current = (target,stamp,auth = state.token) => ready() && state.token === auth && role === target && state.roleID === target && generation === stamp;
  const shown = (target,stamp,visible,auth) => current(target,stamp,auth) && ui['activities-dialog'].open && view === visible;
  const say = (text = '') => { ui['activities-message'].textContent = text; ui['activities-message'].hidden = !text; };
  const element = (tag,className,text) => { const node = document.createElement(tag); node.className = className; node.textContent = text || ''; return node; };
  const bytes = text => new TextEncoder().encode(text).length;
  const changed = () => { try { Promise.resolve(onChanged?.()).catch(() => {}); } catch {} };
  const key = target => prefix + target;
  function readPending(target) {
    try {
      const value = JSON.parse(sessionStorage.getItem(key(target)) || 'null');
      const p = value?.payload;
      if (p && templates[p.kind] && /^[A-Za-z0-9_-]{1,128}$/.test(p.idempotency_key) && typeof p.title === 'string' && p.title.trim() && bytes(p.title) <= 256 && typeof p.goal === 'string' && p.goal.trim() && bytes(p.goal) <= 2048) return {payload:p,conflict:Boolean(value.conflict)};
    } catch {}
    return null;
  }
  function persistPending(target,value) { try { if (value) sessionStorage.setItem(key(target),JSON.stringify(value)); else sessionStorage.removeItem(key(target)); } catch {} }
  const editorDraft = () => ({editing,conflict,kind:ui['activities-kind'].value,title:ui['activities-title'].value,goal:ui['activities-goal'].value,progress:ui['activities-progress'].value});
  function displayDraft(value) {
    editing = value?.editing || null; conflict = Boolean(value?.conflict);
    const kind = templates[value?.kind] ? value.kind : 'bedtime';
    ui['activities-kind'].value = kind;
    ui['activities-title'].value = value?.title ?? templates[kind].title;
    ui['activities-goal'].value = value?.goal ?? templates[kind].goal;
    ui['activities-progress'].value = value?.progress || '';
    ui['activities-progress-field'].hidden = !editing;
    ui['activities-cancel-edit'].hidden = !editing;
    ui['activities-reload-edit'].hidden = !editing || !conflict;
    ui['activities-editor-title'].textContent = editing ? '记下愿意留到下次的进展' : '留下一件想一起做的事';
    ui['activities-save'].textContent = editing ? '保存修改' : '创建并打开聊天';
  }
  function resetEditor() { drafts.delete(role); displayDraft(null); lock(); }
  function banner() {
    const node = $('activity-banner'); if (!node) return;
    node.hidden = !bound;
    if ($('activity-current')) $('activity-current').textContent = bound ? `${labels[bound.status] || '共同活动'} · ${bound.title}` : '';
    if ($('activity-manage')) $('activity-manage').disabled = !ready();
  }
  function adoptRole() {
    const target = state.roleID || 'companion';
    if (target === role) return;
    if (role) drafts.set(role,editorDraft());
    role = target; generation++; operation++; busy = false; items = []; bound = null; flight = null; pending = readPending(role);
    ui['activities-dialog'].close(); displayDraft(drafts.get(role) || pending?.payload); say(''); banner();
  }
  function lock() {
    adoptRole();
    const unavailable = !ready(), writeLocked = unavailable || busy || chatting();
    if ($('activities-open')) $('activities-open').disabled = unavailable;
    ui['activities-refresh'].disabled = unavailable || busy;
    ui['activities-save'].disabled = writeLocked || conflict || Boolean(pending);
    ui['activities-kind'].disabled = writeLocked || Boolean(editing) || Boolean(pending);
    for (const id of ['activities-title','activities-goal','activities-progress']) ui[id].disabled = busy || Boolean(pending && !editing);
    ui['activities-cancel-edit'].disabled = busy;
    ui['activities-reload-edit'].disabled = unavailable || busy;
    ui['activities-confirm'].disabled = writeLocked || Boolean(pending?.conflict);
    ui['activities-dismiss-pending'].hidden = !pending?.conflict;
    ui['activities-dismiss-pending'].disabled = writeLocked;
    ui['activities-pending'].hidden = !pending;
    if (pending) ui['activities-pending-text'].textContent = pending.conflict ? `这条创建请求发生冲突，暂不能再次提交。请重新读取列表检查“${pending.payload.title}”。确认现有记录后，可结束确认并保留草稿。` : `“${pending.payload.title}”可能已经保存。可以先重新读取列表，或用同一标识确认保存；不会自动重新提交。`;
    for (const button of ui['activities-list'].querySelectorAll('button')) button.disabled = writeLocked || Boolean(pending);
    banner();
  }
  function report(error) { say(error.message || '共同活动暂时不可用，请稍后重试。'); if (error.status === 401) onError(error); }
  function action(parent,label,work) { const button = element('button','activities-action',label); button.type = 'button'; button.addEventListener('click',work); parent.append(button); }
  function edit(item) {
    if (busy || chatting() || pending) return;
    displayDraft({editing:{...item},kind:item.kind,title:item.title,goal:item.goal,progress:item.progress}); say(''); lock(); ui['activities-title'].focus();
  }
  function upsert(item) {
    const index = items.findIndex(value => value.id === item.id);
    if (index < 0) items.unshift(item); else items[index] = item;
    if (bound?.id === item.id) bound = item;
  }
  function render() {
    ui['activities-list'].replaceChildren();
    if (!items.length) ui['activities-list'].append(element('p','memory-empty','还没有共同活动。可以从下面选一种方式，慢慢开始。'));
    for (const item of [...items].sort((a,b) => new Date(b.updated_at)-new Date(a.updated_at))) {
      const card = element('article',`activities-card activities-${item.status}`); card.dataset.activityId = item.id;
      card.append(element('h3','',item.title),element('p','activities-meta',`${labels[item.status] || item.status} · ${templates[item.kind]?.title || item.kind}`),element('p','activities-copy',item.goal));
      if (item.progress) card.append(element('p','activities-meta','已经留下的进展'),element('p','activities-copy',item.progress));
      const actions = element('div','activities-actions');
      action(actions,item.status === 'active' ? '接着聊' : '恢复并接着聊',() => continueActivity(item));
      action(actions,'编辑进展',() => edit(item));
      if (item.status === 'active') action(actions,'暂停',() => patch(item,{status:'paused'}));
      if (item.status !== 'completed') action(actions,'结束',() => patch(item,{status:'completed'}));
      action(actions,'删除',() => {
        if (!confirm(`删除“${item.title}”及保存的活动进展？原聊天仍会保留。`)) return;
        mutate(async target => { await json(endpoint(`/api/activities/${encodeURIComponent(item.id)}`,target)+`&revision=${item.revision}`,{method:'DELETE'}); return {deleted:item.id}; });
      });
      card.append(actions); ui['activities-list'].append(card);
    }
    lock();
  }
  async function refresh() {
    adoptRole(); if (!ready()) return;
    if (flight) return flight;
    const target = role, stamp = generation, visible = view, auth = state.token;
    const request = (async () => {
      const result = await json(endpoint('/api/activities',target));
      if (!current(target,stamp,auth)) return;
      items = Array.isArray(result) ? result : [];
      if (bound) bound = items.find(item => item.id === bound.id) || null;
      if (ui['activities-dialog'].open && view === visible) render();
      banner(); return items;
    })();
    flight = request;
    try { return await request; } finally { if (flight === request) flight = null; }
  }
  async function mutate(work,after) {
    if (!ready() || busy || chatting() || pending) return;
    const target = role, stamp = generation, visible = view, auth = state.token, ticket = ++operation;
    drafts.set(target,editorDraft()); busy = true; say(''); lock();
    try {
      const result = await work(target);
      if (!current(target,stamp,auth)) return;
      if (result?.deleted) { items = items.filter(item => item.id !== result.deleted); if (bound?.id === result.deleted) bound = null; if (editing?.id === result.deleted) drafts.delete(target); }
      else if (result?.id) upsert(result);
      const draft = drafts.get(target);
      if (draft?.editing?.id === result?.id && draft.title.trim() === result.title && draft.goal.trim() === result.goal && draft.progress.trim() === result.progress) drafts.delete(target);
      changed();
      if (shown(target,stamp,visible,auth)) { if (result?.deleted && editing?.id === result.deleted) resetEditor(); render(); if (after) await after(result); }
      banner();
    } catch (error) {
      if (shown(target,stamp,visible,auth)) {
        if (error.status === 409 && editing) { conflict = true; drafts.set(role,editorDraft()); ui['activities-reload-edit'].hidden = false; }
        report(error);
      }
    } finally { if (ticket === operation) { busy = false; lock(); } }
  }
  const patch = (item,changes,after) => mutate(target => json(endpoint(`/api/activities/${encodeURIComponent(item.id)}`,target),{method:'PATCH',body:JSON.stringify({revision:item.revision,...changes})}),after);
  async function enter(item) {
    await onContinue(item);
    if (ui['activities-dialog'].open) ui['activities-dialog'].close();
  }
  function continueActivity(item) {
    if (item.status !== 'active') return patch(item,{status:'active'},enter);
    return mutate(async () => item,enter);
  }
  async function create(payload) {
    if (!ready() || busy || chatting()) return;
    const target = role, stamp = generation, visible = view, auth = state.token, ticket = ++operation;
    pending = {payload,conflict:false}; persistPending(target,pending); busy = true; say(''); lock();
    try {
      const result = await json(endpoint('/api/activities',target),{method:'POST',body:JSON.stringify(payload)});
      if (!current(target,stamp,auth)) return;
      pending = null; persistPending(target,null); drafts.delete(target); upsert(result);
      changed();
      if (shown(target,stamp,visible,auth)) { resetEditor(); render(); try { await enter(result); } catch (error) { if (shown(target,stamp,visible,auth)) say(`活动已保存。${error.message || '可以稍后在列表中点“接着聊”。'}`); } }
    } catch (error) {
      if (!current(target,stamp,auth)) return;
      if (error.status && error.status < 500 && error.status !== 409) { pending = null; persistPending(target,null); }
      else { pending.conflict = error.status === 409; persistPending(target,pending); }
      if (shown(target,stamp,visible,auth)) report(error);
    } finally { if (ticket === operation) { busy = false; lock(); } }
  }
  async function open() {
    if (!ready()) return;
    if (ui['activities-dialog'].open) return;
    adoptRole(); drawer(false); view++;
    ui['activities-role'].textContent = state.roles?.find(item => item.id === role)?.name || role;
    displayDraft(drafts.get(role) || pending?.payload); say(''); ui['activities-dialog'].showModal(); render();
    const target = role, stamp = generation, visible = view, auth = state.token;
    try { await refresh(); if (shown(target,stamp,visible,auth)) render(); } catch (error) { if (shown(target,stamp,visible,auth)) report(error); }
  }
  $('activities-open')?.addEventListener('click',open);
  $('activity-manage')?.addEventListener('click',open);
  $('activities-close').addEventListener('click',() => ui['activities-dialog'].close());
  ui['activities-dialog'].addEventListener('close',() => { if (role && !busy) drafts.set(role,editorDraft()); view++; });
  ui['activities-refresh'].addEventListener('click',async () => { const target = role, stamp = generation, visible = view, auth = state.token; try { await refresh(); if (shown(target,stamp,visible,auth)) say('列表已更新；正在编辑的草稿保持不变。'); } catch (error) { if (shown(target,stamp,visible,auth)) report(error); } });
  ui['activities-kind'].addEventListener('change',() => { if (!editing && !pending) { const value = templates[ui['activities-kind'].value]; ui['activities-title'].value = value.title; ui['activities-goal'].value = value.goal; } });
  ui['activities-cancel-edit'].addEventListener('click',() => { resetEditor(); say(''); });
  ui['activities-reload-edit'].addEventListener('click',async () => {
    if (!editing || busy || !confirm('读取最新记录会替换当前未保存的草稿，继续吗？')) return;
    const id = editing.id, target = role, stamp = generation, visible = view, auth = state.token;
    try { await refresh(); if (!shown(target,stamp,visible,auth)) return; const latest = items.find(item => item.id === id); if (latest) { edit(latest); say('已读取最新记录，可以继续编辑。'); } else say('活动已被删除；当前草稿仍保留，可以复制后取消编辑。'); }
    catch (error) { if (shown(target,stamp,visible,auth)) report(error); }
  });
  ui['activities-confirm'].addEventListener('click',() => { if (pending && !pending.conflict) create(pending.payload); });
  ui['activities-dismiss-pending'].addEventListener('click',() => {
    if (!pending?.conflict || busy || chatting() || !confirm('请先检查列表中是否已有该活动。结束确认会保留原内容；再次创建将使用新的标识。')) return;
    const payload = pending.payload; pending = null; persistPending(role,null); displayDraft({kind:payload.kind,title:payload.title,goal:payload.goal}); drafts.set(role,editorDraft()); say('内容已保留在编辑器。已有相同活动时，可以直接使用列表中的“接着聊”。'); lock();
  });
  ui['activities-form'].addEventListener('submit',event => {
    event.preventDefault(); if (busy || chatting() || pending || conflict) return;
    const title = ui['activities-title'].value.trim(), goal = ui['activities-goal'].value.trim(), progress = ui['activities-progress'].value.trim();
    if (!title || !goal || bytes(title) > 256 || bytes(goal) > 2048 || bytes(progress) > 4096) { say('名称和想做的事不能为空；名称最多 256 字节、目标 2048 字节、进展 4096 字节。'); return; }
    if (editing) return patch(editing,{title,goal,progress},() => { resetEditor(); say('进展已保存，之后可以接着聊。'); });
    const id = new Uint8Array(16); crypto.getRandomValues(id);
    create({kind:ui['activities-kind'].value,title,goal,idempotency_key:Array.from(id,value => value.toString(16).padStart(2,'0')).join('')});
  });
  function clear() {
    generation++; operation++; view++; busy = false; items = []; editing = null; pending = null; bound = null; conflict = false; flight = null; drafts.clear();
    try { for (let index = sessionStorage.length-1; index >= 0; index--) { const name = sessionStorage.key(index); if (name?.startsWith(prefix)) sessionStorage.removeItem(name); } } catch {}
    role = ''; ui['activities-dialog'].close(); displayDraft(null); ui['activities-list'].replaceChildren(); say(''); banner(); lock();
  }
  function context(activity) { adoptRole(); bound = activity && activity.role_id === role ? {...activity} : null; banner(); }
  displayDraft(null); lock();
  return {clear,refresh,lock,context};
}
