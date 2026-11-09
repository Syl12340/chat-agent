// Durable server turn queue. Reconnection only reads; it never re-submits or resumes.
export function splitReply(text) {
  // Keep block markup, references, and math in one render so parser context
  // never changes when the same reply is copied or reopened.
  if (!text || text.length > 600 || /[`$\\\[\]<>|~]|^(?:\t| {4}|\s*(?:#{1,6}\s|[-*+]\s|\d+[.)]\s|[-*_]{3,}\s*$))/m.test(text)) return [text];
  const paragraphs = text.split(/\n\s*\n/).map(part => part.trim()).filter(Boolean);
  if (paragraphs.length <= 1) return [text];
  if (paragraphs.length <= 3) return paragraphs;
  const groupSize = Math.ceil(paragraphs.length / 3), groups = [];
  for (let i=0;i<paragraphs.length;i+=groupSize) groups.push(paragraphs.slice(i,i+groupSize).join('\n\n'));
  return groups;
}
export function createTurnsUI({ state, json, storage, onError, onHistory, onState, onSettled, createSession, onDraftRestore, onAccepted }) {
  const $ = id => document.getElementById(id);
  const ui = Object.fromEntries(['turn-queue','turn-queue-label','turn-queue-list','turn-queue-error','turn-resume','turn-refresh','turn-unknown','turn-unknown-text','turn-retry','turn-restore-draft'].map(id => [id,$(id)]));
  const storageKey = 'pocket-turn-unconfirmed';
  let session = null, generation = 0, submission = 0, timer = null, flight = null, readVersion = 0, pending = null, history = null, failures = 0, queueBusy = false;
  let signature = '', terminalSignature = '';
  Object.assign(state,{submitting:false,submitUnknown:false,queuedCount:0,queuePaused:false});
  const path = (sid,suffix = '') => `/api/sessions/${encodeURIComponent(sid)}${suffix}`;
  const current = (sid,stamp) => state.token && session === sid && state.current === sid && generation === stamp;
  const canRead = () => state.token && session && state.current === session && navigator.onLine && document.visibilityState === 'visible';
  const say = text => { ui['turn-queue-error'].textContent = text || ''; ui['turn-queue-error'].hidden = !text; };
  const savePending = () => { if (pending) storage.set(storageKey,JSON.stringify(pending)); else storage.remove(storageKey); };
  function restorePending(sid) {
    try {
      const value = JSON.parse(storage.get(storageKey) || 'null');
      if (value?.session !== sid || !/^[A-Za-z0-9_-]{1,128}$/.test(value.request_id) || typeof value.content !== 'string' || new TextEncoder().encode(value.content).length > 65536 || !['auto','listen','chat','solve'].includes(value.mode)) return null;
      const item={session:sid,request_id:value.request_id,content:value.content,mode:value.mode,phase:value.phase === 'rejected' ? 'rejected':'uncertain'};
      if (value.model !== undefined) {
        if (typeof value.model !== 'string' || value.model.length > 128 || !['off','medium','high','ultra'].includes(value.thinking)) return null;
        item.model=value.model; item.thinking=value.thinking;
      }
      if (value.image_ids !== undefined) {
        if (!Array.isArray(value.image_ids) || value.image_ids.length>3 || !value.image_ids.every(id=>typeof id==='string' && /^[A-Za-z0-9_-]{1,128}$/.test(id)) || new Set(value.image_ids).size!==value.image_ids.length) return null;
        item.image_ids=[...value.image_ids];
      }
      if (!item.content.trim() && !item.image_ids?.length) return null;
      return item;
    } catch {}
    return null;
  }
  function cancelRead() { clearTimeout(timer); timer = null; readVersion++; flight?.controller.abort(); flight = null; }
  function renderQueue() {
    const turns = history?.turns || [], queued = turns.filter(item => item.status === 'queued');
    state.queuedCount = queued.length; state.queuePaused = Boolean(history?.queue_paused); state.submitUnknown = Boolean(pending && pending.phase !== 'submitting');
    ui['turn-queue'].hidden = !queued.length && !history?.active && !pending && !state.queuePaused;
    ui['turn-queue-label'].textContent = state.queuePaused ? queued.length ? `已暂停 · ${queued.length} 条等待继续` : '聊天已暂停，恢复后可发送新消息' : queued.length ? `${queued.length} 条等待发送` : history?.active ? '正在回复，你可以继续输入' : pending ? '上一条提交需要确认' : '消息队列';
    ui['turn-queue-list'].replaceChildren();
    for (const item of queued) {
      const row = document.createElement('div'); row.className = 'turn-queue-item'; row.dataset.requestId = item.id;
      const label = document.createElement('span'); label.textContent = answerLabel(item.content) || item.content || `图片消息 · ${item.images?.length || 0} 张`; row.append(label);
      const button = document.createElement('button'); button.type = 'button'; button.textContent = '撤回'; button.disabled = queueBusy || state.submitting;
      button.addEventListener('click',() => queueAction(async sid => json(path(sid,`/turns/${encodeURIComponent(item.id)}`),{method:'DELETE'})));
      row.append(button); ui['turn-queue-list'].append(row);
    }
    ui['turn-resume'].hidden = !state.queuePaused;
    ui['turn-resume'].textContent = queued.length ? '继续队列' : '恢复聊天';
    ui['turn-resume'].disabled = queueBusy || state.submitting || Boolean(pending) || Boolean(history?.active);
    ui['turn-refresh'].disabled = queueBusy || !state.token;
    ui['turn-unknown'].hidden = !pending || pending.phase === 'submitting';
    if (pending) ui['turn-unknown-text'].textContent = `${pending.phase === 'rejected' ? '这条尚未提交':'这条可能已被接收，请查询或用同一标识确认'}：${answerLabel(pending.content) || pending.content}${pending.image_ids?.length ? `（${pending.image_ids.length} 张图片）`:''}`;
    ui['turn-retry'].disabled = state.submitting || queueBusy || pending?.status === 409;
    ui['turn-retry'].textContent = pending?.phase === 'rejected' ? '重试提交':'用同一标识确认提交';
    ui['turn-restore-draft'].hidden = pending?.phase !== 'rejected';
    ui['turn-restore-draft'].textContent = answerLabel(pending?.content) ? '放弃这次答案' : '放回输入框';
    onState();
  }
  function schedule() {
    clearTimeout(timer); timer = null;
    if (!canRead()) return;
    const waiting = history?.active || (!history?.queue_paused && (history?.turns || []).some(item => item.status === 'queued')) || pending;
    if (waiting) timer = setTimeout(() => refresh().catch(report),failures ? Math.min(5000,1000 * failures) : pending && !history?.active ? 1500:600);
  }
  function apply(value) {
    const previousActive = Boolean(history?.active);
    history = {...value,turns:Array.isArray(value.turns) ? value.turns:[]};
    const matchesPending = item => item.id === pending.request_id && item.content === pending.content && item.mode === pending.mode &&
      (pending.model===undefined || item.model===pending.model && item.thinking===pending.thinking) &&
      (pending.image_ids===undefined || JSON.stringify((item.images || []).map(image=>image.id))===JSON.stringify(pending.image_ids));
    if (pending && history.turns.some(matchesPending)) { const accepted=pending; pending = null; savePending(); say(''); onAccepted?.(accepted); }
    state.streaming = Boolean(history.active); state.external = false;
    const nextSignature = JSON.stringify([history.messages,history.turns,history.active,history.queue_paused,history.activity,history.ui_context_floor]);
    if (signature !== nextSignature) { signature = nextSignature; onHistory(history); }
    renderQueue();
    const terminal = JSON.stringify(history.turns.filter(item => !['queued','running'].includes(item.status)).map(item => [item.id,item.status]));
    if ((previousActive && !history.active) || (terminalSignature && terminalSignature !== terminal)) Promise.resolve(onSettled?.()).catch(report);
    terminalSignature = terminal;
  }
  function report(error) {
    if (error?.name === 'AbortError') return;
    if (error?.status === 401) onError(error);
    else say(error?.message || '连接暂时不可用，恢复后只会读取状态。');
  }
  async function refresh() {
    if (!canRead()) return;
    if (flight) return flight.promise;
    const sid = session, stamp = generation, version = readVersion, controller = new AbortController();
    const item = {controller,promise:null};
    item.promise = (async () => {
      try {
        const value = await json(path(sid,'/messages'),{signal:controller.signal});
        if (!current(sid,stamp) || version !== readVersion) return;
        failures = 0; apply(value);
      } catch (error) { if (current(sid,stamp) && version === readVersion && error.name !== 'AbortError') { failures++; report(error); } }
      finally { if (flight === item) { flight = null; schedule(); } }
    })();
    flight = item;
    return item.promise;
  }
  function watch(sid,value) {
    if (session !== sid) {
      cancelRead(); generation++; queueBusy = false; state.stopping = false; session = sid; pending = restorePending(sid); history = null; signature = ''; terminalSignature = ''; failures = 0; say('');
    }
    if (value) apply(value);
    renderQueue(); schedule();
  }
  function detach() { cancelRead(); generation++; queueBusy = false; state.stopping = false; session = null; history = null; pending = null; signature = ''; terminalSignature = ''; state.streaming = false; state.external = false; state.queuedCount = 0; state.queuePaused = false; state.submitUnknown = false; renderQueue(); }
  function clear() { submission++; state.submitting = false; queueBusy = false; storage.remove(storageKey); detach(); say(''); }
  async function submit(content,mode = 'auto',retry = false,options = {}) {
    if (!state.token || state.submitting || (!retry && pending)) return false;
    const ticket = ++submission, initialGeneration = generation, initialRole = state.roleID, initialToken = state.token;
    state.submitting = true; renderQueue();
    let sid = session;
    try {
      if (!state.current) {
        const created = await createSession();
        if (!state.token || submission !== ticket || generation !== initialGeneration) return false;
        sid = created.id; watch(sid,{id:sid,messages:[],turns:[],active:false,role_id:state.roleID,queue_paused:false});
      } else if (session !== state.current) { sid = state.current; watch(sid); }
      sid = session;
      const stamp = generation;
      if (!retry) {
        const bytes = new Uint8Array(16); crypto.getRandomValues(bytes);
        pending = {session:sid,request_id:Array.from(bytes,v=>v.toString(16).padStart(2,'0')).join(''),content,mode:mode || 'auto',phase:'submitting'};
        if (options.model !== undefined) { pending.model=options.model; pending.thinking=options.thinking; }
        if (options.image_ids !== undefined) pending.image_ids=[...options.image_ids];
      }
      if (!pending || pending.session !== sid) return false;
      const payload = {...pending}; pending.phase = 'submitting'; savePending(); say(''); renderQueue();
      try {
        const body={request_id:payload.request_id,content:payload.content,mode:payload.mode};
        if (payload.model!==undefined) { body.model=payload.model; body.thinking=payload.thinking; }
        if (payload.image_ids!==undefined) body.image_ids=payload.image_ids;
        const accepted = await json(path(sid,'/turns'),{method:'POST',body:JSON.stringify(body)});
        if (!current(sid,stamp) || submission !== ticket) return true;
        pending = null; savePending(); cancelRead(); onAccepted?.(payload);
        const optimistic = history || {id:sid,messages:[],turns:[],role_id:state.roleID};
        const turns = [...(optimistic.turns || [])]; const old = turns.findIndex(item => item.id === accepted.id);
        if (old < 0) turns.push(accepted); else turns[old] = accepted;
        apply({...optimistic,turns,active:accepted.status === 'running' || accepted.status === 'queued' && !optimistic.queue_paused || optimistic.active});
        await refresh(); return true;
      } catch (error) {
        if (!current(sid,stamp) || submission !== ticket) return false;
        if (error.status === 401) { onError(error); return false; }
        pending.phase = error.status && error.status < 500 ? 'rejected':'uncertain'; pending.status = error.status || null; savePending();
        say(error.message || '提交状态未知，没有自动重发。'); renderQueue(); cancelRead(); await refresh(); return false;
      }
    } catch (error) { if (submission === ticket) { if (!pending && state.token === initialToken && state.roleID === initialRole) onDraftRestore(content,mode,options); report(error); } return false; }
    finally { if (submission === ticket) { state.submitting = false; renderQueue(); schedule(); } }
  }
  async function queueAction(work) {
    if (!session || queueBusy || state.submitting) return;
    const sid = session, stamp = generation; queueBusy = true; renderQueue(); say('');
    try { await work(sid); if (current(sid,stamp)) { cancelRead(); await refresh(); } }
    catch (error) { if (current(sid,stamp)) { report(error); await refresh(); } }
    finally { if (current(sid,stamp)) { queueBusy = false; renderQueue(); schedule(); } }
  }
  async function stop() {
    if (!session || state.stopping) return;
    const sid = session, stamp = generation; state.stopping = true; renderQueue();
    try { await json(path(sid,'/cancel'),{method:'POST'}); if (current(sid,stamp)) { cancelRead(); await refresh(); } }
    catch (error) { if (current(sid,stamp)) report(error); }
    finally { if (current(sid,stamp)) { state.stopping = false; renderQueue(); schedule(); } }
  }
  ui['turn-resume'].addEventListener('click',() => queueAction(sid => json(path(sid,'/turns/resume'),{method:'POST'})));
  ui['turn-refresh'].addEventListener('click',() => { cancelRead(); refresh().catch(report); });
  ui['turn-retry'].addEventListener('click',() => { if (pending && pending.phase !== 'submitting') submit(pending.content,pending.mode,true); });
  ui['turn-restore-draft'].addEventListener('click',async () => {
    if (!pending || pending.phase !== 'rejected') return;
    const original=pending;
    if (!answerLabel(pending.content) && (await onDraftRestore(pending.content,pending.mode,pending) === false || pending!==original)) return;
    pending = null; savePending(); say(''); renderQueue(); schedule();
  });
  document.addEventListener('visibilitychange',() => { if (document.visibilityState === 'visible') refresh().catch(report); else cancelRead(); });
  window.addEventListener('online',() => refresh().catch(report));
  return {watch,detach,clear,submit,stop,refresh,pendingSnapshot:()=>({turns:history?.turns || [],pending:pending ? {...pending}:null})};
}

function answerLabel(content) {
  try {
    const r=JSON.parse(content)?.chat_ui_response;
    if (r?.schema_version!==1) return '';
    if (r.component_type==='quiz.single_choice' && typeof r.answer?.option_id==='string') return `互动答案 · ${r.answer.option_id}`;
    if (r.component_type==='quiz.fill_blank' && Array.isArray(r.answer?.fields)) return `互动答案 · ${r.answer.fields.map(f=>f.value).join(' / ')}`;
  } catch {}
  return '';
}
