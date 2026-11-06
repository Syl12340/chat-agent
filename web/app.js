import { createTaskUI } from '/tasks.js';
import { saveMarkdown } from '/native.js';
import { createCompanionUI } from '/companion.js';
import { createRoleProposalsUI } from '/role-proposals.js';
import { createTopicsUI } from '/topics.js';
import { createLifeUI } from '/life.js';
import { createTurnsUI } from '/turns.js';
import { createActivitiesUI } from '/activities.js';
import { createNativeCare } from '/native-care.js';
import { createVoiceUI } from '/voice.js';
import { createDeviceAuth } from '/device-auth.js';
import { createChatInput } from '/chat-input.js';
import { createUIRenderer } from '/ui/render.js';
import { createDesktopWorkspace } from '/desktop-workspace.js';
import { createMarkdownRenderer } from '/markdown.js';
const $ = id => document.getElementById(id);
const ui = Object.fromEntries(['sidebar','drawer-scrim','menu','new-chat','sessions','session-count','logout','chat-title','connection-label','demo-badge','conversation','welcome','messages','prompt','composer','send','stop','notice','model-label','login-overlay','login-form','access-token','login-button','login-error','settings','settings-dialog','close-settings','connection-info','reset-chat','delete-session','offline-banner','install'].map(id => [id, $(id)]));
for (const id of ['role-picker','memory-open','memory-count','role-open','memory-dialog','memory-close','memory-role-name','memory-list','memory-form','memory-content','memory-status','memory-save','memory-cancel-edit','memory-form-label','memory-export','memory-message','role-dialog','role-close','role-id-label','role-title','role-form','role-file','role-content','role-save','role-skills','role-message','compact-context','compact-message','summary-details','summary-content']) ui[id] = $(id);
const storage = {
  get(key) { try { return sessionStorage.getItem(key); } catch { return null; } },
  set(key, value) { try { sessionStorage.setItem(key, value); } catch {} },
  remove(key) { try { sessionStorage.removeItem(key); } catch {} }
};
const nativeShell = /PocketAgentAndroid\//.test(navigator.userAgent);
const persistent = {
  get() { try { return localStorage.getItem('pocket-token') || ''; } catch { return ''; } },
  save(token) { localStorage.setItem('pocket-token', token); },
  clear() { try { localStorage.removeItem('pocket-token'); } catch {} }
};
async function nativeAuth() {
  if (!nativeShell || window.PocketAndroidAuth) return window.PocketAndroidAuth;
  await new Promise(resolve => {
    const ready = () => { clearTimeout(timer); window.removeEventListener('pocket-native-ready', ready); resolve(); };
    const timer = setTimeout(ready, 5000);
    window.addEventListener('pocket-native-ready', ready);
  });
  return window.PocketAndroidAuth;
}
async function rememberToken(token, checkCurrent) {
  checkCurrent(); persistent.clear(); storage.remove('pocket-token');
  // Only short-lived device sessions enter the native updater vault.
  if (nativeShell && token.startsWith('dvc1_')) {
    const auth = await nativeAuth(); checkCurrent();
    if (!auth) throw new Error('客户端设备会话存储未就绪，请刷新后重试。');
    await auth.save(token); checkCurrent();
  }
}

function forgetToken() {
  storage.remove('pocket-token'); persistent.clear();
  if (nativeShell) window.PocketAndroidAuth?.forget().catch(() => notice('清除本机登录信息失败，请重试退出登录。'));
}
const state = { token: storage.get('pocket-token') || '', current: null, sessions: [], config: null, loading: false, streaming: false, external: false, stopping: false, controller: null, follow: true };
let chatInput = null;
let interactiveUI = null, historyRevision = 0, uiFloor = 0, uiSourceMessages = [];
const workspace = createDesktopWorkspace();
const richText = createMarkdownRenderer({copyText,notice});
const richMessages = new Map();
const reasoningViews = new Map();
Object.assign(state, { roles: [], roleID: 'companion', memories: [], memoryFilter: 'pending', memoryEditing: null, roleDocument: null, summary: null, compacting: false });
const deviceAuth = createDeviceAuth({state,onToken:token=>connect(token,'device'),onError:handleError,adminRequest:json});
const taskUI = createTaskUI({ state, request, json, roleName, drawer, onError: handleError });
const companionUI = createCompanionUI({ state, json, drawer, onError: handleError });
const roleProposalsUI = createRoleProposalsUI({state,json,drawer,onError:handleError});
const topicsUI=createTopicsUI({state,json,drawer,onError:handleError});
const nativeCare = createNativeCare({state});
const voiceUI = createVoiceUI({ state, request, notice, restoreDraft, onError: handleError });
const lifeUI = createLifeUI({ state, json, drawer, onError: handleError, onOpenSession: id => selectSession(id), onCareReady: (role,items)=>nativeCare.deliver(role,items) });
const turnUI = createTurnsUI({ state, json, storage, onError: handleError, onHistory: applyTurnHistory, onState: updateUI, onSettled: refreshAfterTurn, createSession: createRemoteSession, onDraftRestore: restoreTurnDraft, onAccepted: payload=>chatInput?.accepted(payload) });
const activitiesUI = createActivitiesUI({ state, json, drawer, onError: handleError, onContinue: openActivityChat, onChanged: () => turnUI.refresh() });
chatInput = createChatInput({state,request,json,createSession:createRemoteSession,notice,onChange:updateUI,onImageLoad:()=>scrollBottom()});
interactiveUI = createUIRenderer({onSubmit:submitUIAnswer,onExpand:(node,meta)=>workspace.expand(node,meta),onPlace:node=>workspace.placement(node),canSubmit:uiSubmitPermission});
// A manual connection owns authentication immediately, even while native restore awaits.
let authEpoch = 0, connectionWork = Promise.resolve(), connectionAbort = null, renewalWork = null;
const staleAuth = () => new DOMException('认证请求已过期', 'AbortError');
let sessionSelection = 0;
let installPrompt;

function notice(text = '') {
  ui.notice.textContent = text;
  ui.notice.hidden = !text;
}
function drawer(open) {
  ui.sidebar.classList.toggle('open', open);
  ui['drawer-scrim'].hidden = !open;
  ui.menu.setAttribute('aria-expanded', String(open));
}
function updateUI() {
  document.querySelector('.app-shell').inert = !ui['login-overlay'].hidden;
  const busy = state.streaming || state.external || state.compacting;
  const locked = !state.token || !ui['login-overlay'].hidden || state.loading;
  ui.prompt.disabled = locked || state.compacting;
  ui.send.hidden = false;
  ui.send.disabled = locked || state.compacting || state.submitting || state.submitUnknown || state.imageBusy || !chatInput?.ready() || (!ui.prompt.value.trim() && !state.draftImageCount);
  ui.send.setAttribute('aria-label', state.submitting ? '正在提交消息' : busy ? '发送到消息队列' : '发送消息');
  ui.stop.hidden = !(busy || state.queuedCount);
  ui.stop.disabled = state.stopping || !state.current;
  const navigationLocked = locked || busy || state.submitting || state.submitUnknown || state.imageBusy;
  ui['new-chat'].disabled = navigationLocked;
  ui['reset-chat'].disabled = navigationLocked || Boolean(state.queuedCount) || !state.current;
  ui['delete-session'].disabled = navigationLocked || Boolean(state.queuedCount) || !state.current;
  ui.logout.disabled = state.submitting || state.loading;
  for (const id of ['role-picker','memory-open','role-open']) ui[id].disabled = navigationLocked;
  $('tasks-open').disabled = navigationLocked;
  $('companion-open').disabled = navigationLocked;
  $('role-proposals-open').disabled = navigationLocked;
  ui['compact-context'].disabled = navigationLocked || Boolean(state.queuedCount) || !state.current;
  ui.conversation.setAttribute('aria-busy', String(busy || state.loading));
  ui.sessions.querySelectorAll('button:not(.session-delete)').forEach(button => { button.disabled = navigationLocked; });
  ui['connection-label'].textContent = state.loading ? '正在读取会话' : state.submitting ? '正在提交，你可以继续输入' : state.queuePaused ? '队列已暂停，可手动继续' : busy ? '正在回复，可以继续输入或停止' : state.config ? state.config.demo ? '本地演示 · 未连接真实模型' : '已连接 · 随时可以开始' : '等待连接';
  lifeUI.lock();
  topicsUI.lock();
  activitiesUI.lock();
  voiceUI.lock();
  chatInput?.lock();
  interactiveUI?.reconcileResponses(answerTurns(turnUI.pendingSnapshot().turns),turnUI.pendingSnapshot().pending);
  interactiveUI?.update();
}

function uiSubmitPermission(context) {
  if (!state.token || !ui['login-overlay'].hidden || context.session !== state.current || context.role !== state.roleID) return '请在原会话中登录后提交';
  if (!context.eligible || context.messageIndex < uiFloor) return '这条互动已过期或回答尚未完成';
  if (state.loading || state.streaming || state.external || state.compacting || state.submitting || state.submitUnknown || state.queuedCount || state.queuePaused || state.stopping || state.imageBusy) return '请先等待当前回复和队列完成';
  if (ui.prompt.value.trim() || chatInput?.hasUnsentImages()) return '输入框还有草稿，请先发送或清空';
  return chatInput?.ready() ? true : '模型选项尚未就绪';
}
async function submitUIAnswer({content,context}) {
  const allowed=uiSubmitPermission(context);
  if (allowed!==true) { notice(allowed); return {status:'rejected'}; }
  const epoch=authEpoch, session=state.current, role=state.roleID;
  const options=chatInput.selection();
  state.follow=false;
  await turnUI.submit(content,'auto',false,options);
  if (epoch!==authEpoch || session!==state.current || role!==state.roleID) return {status:'stale'};
  const snapshot=turnUI.pendingSnapshot();
  const pending=snapshot.pending?.content===content ? snapshot.pending : null;
  const accepted=[...snapshot.turns].reverse().find(t=>t.content===content);
  return {request_id:pending?.request_id || accepted?.id,status:pending?.phase || accepted?.status || 'rejected'};
}
function clearInteractiveUI() {
  historyRevision++; workspace.reset(); interactiveUI?.syncScope('', ''); richMessages.clear(); reasoningViews.clear(); uiFloor=0; uiSourceMessages=[];
}
function answerTurns(turns) {
  // The queue trims old payloads but retains their committed message ranges.
  // UI answers never merge, so the original user envelope remains recoverable.
  return turns.map(t=>!t.content && t.batch_id===t.id && uiSourceMessages[t.start_index]?.role==='user'
    ? {...t,content:uiSourceMessages[t.start_index].content} : t);
}

async function renewDeviceSession() {
  if (renewalWork) return renewalWork;
  const stamp=authEpoch;
  const work=(async()=>{
    const token=await deviceAuth.refresh();
    const check=()=>{if(stamp!==authEpoch||state.authKind!=='device')throw staleAuth();};
    check();state.token=token;await rememberToken(token,check);return token;
  })();
  renewalWork=work;
  try{return await work;}finally{if(renewalWork===work)renewalWork=null;}
}
async function request(path, options = {}) {
  const epoch = authEpoch, token = state.token;
  const {deviceRetried=false,...fetchOptions}=options;
  const response = await fetch(path, { signal: connectionAbort?.signal, ...fetchOptions, headers: { 'Authorization': `Bearer ${token}`, ...(fetchOptions.body ? { 'Content-Type': 'application/json' } : {}), ...fetchOptions.headers }, cache: 'no-store' });
  if (epoch !== authEpoch || token !== state.token) throw staleAuth();
  if(response.status===401 && state.authKind==='device' && !deviceRetried){
    try {await renewDeviceSession();}
    catch(error){
      if(epoch!==authEpoch)throw staleAuth();
      if(['device_pending','device_revoked'].includes(error.code)){logout();deviceAuth.showError(error);throw staleAuth();}
      throw error;
    }
    if(epoch!==authEpoch)throw staleAuth();
    // Authentication rejected before the handler ran; retry exactly once.
    return request(path,{...fetchOptions,deviceRetried:true});
  }
  if (!response.ok) {
    const body = await response.json().catch(() => ({}));
    if (epoch !== authEpoch || token !== state.token) throw staleAuth();
    const error = new Error(body.error || `请求失败（${response.status}）`);
    error.status = response.status; error.code=body.code;
    throw error;
  }
  return response;
}

async function json(path, options) {
  const epoch = authEpoch;
  const response = await request(path, options), token = state.token;
  const value = await response.json();
  if (epoch !== authEpoch || token !== state.token) throw staleAuth();
  return value;
}
function handleError(error) {
  if (error.name === 'AbortError') return;
  if (error.status === 401) { logout(); ui['login-error'].textContent = '口令已失效，请重新连接。'; ui['login-error'].hidden = false; }
  else notice(error.message || '连接失败，请稍后重试。');
}
function logout() {
  clearInteractiveUI();
  deviceAuth.logout(state.token);
  authEpoch++; connectionAbort?.abort(); connectionAbort = null;
  ui['login-button'].disabled = false; ui['login-button'].textContent = '进入 chat-agent →';
  voiceUI.clear();
  taskUI.clear();
  companionUI.clear();
  roleProposalsUI.clear(); topicsUI.clear();
  lifeUI.clear();
  nativeCare.clear();
  activitiesUI.clear();
  turnUI.clear(); sessionSelection++;
  chatInput.clear();
  state.controller?.abort();
  forgetToken();
  storage.remove('pocket-session');
  state.token = ''; state.authKind = ''; state.current = null; state.config = null; state.external = false; state.loading = false; state.compacting = false; state.stopping = false;
  state.roles = []; state.roleID = 'companion'; state.memories = []; state.roleDocument = null; state.memoryEditing = null; state.summary = null;
  for (const id of ['memory-dialog','role-dialog','settings-dialog']) ui[id].close();
  ui['memory-list'].replaceChildren(); ui['role-content'].value = ''; ui['memory-content'].value = ''; ui['role-skills'].replaceChildren(); ui['memory-count'].hidden = true;
  ui.messages.replaceChildren(); ui.sessions.replaceChildren();
  ui.welcome.hidden = false; ui['login-overlay'].hidden = false;
  ui['access-token'].value = ''; ui['login-error'].hidden = true;
  ui['chat-title'].textContent = '新的开始'; ui['demo-badge'].hidden = true;
  ui['session-count'].textContent = '0';
  drawer(false); notice(); updateUI();
  ui['access-token'].focus();
}

function connect(token, authKind = 'owner') {
  if(authKind!=='device')deviceAuth.cancel();
  const epoch = ++authEpoch;
  connectionAbort?.abort();
  ui['login-button'].disabled = true;
  ui['login-button'].textContent = '正在连接…';
  ui['login-error'].hidden = true;
  // Finish the superseded attempt before a new one mutates shared panels.
  connectionWork = connectionWork.catch(() => {}).then(async () => {
    if (epoch !== authEpoch) return;
    const controller = new AbortController(); connectionAbort = controller;
    const checkCurrent = () => { if (epoch !== authEpoch) throw staleAuth(); };
    const step = async promise => { const value = await promise; checkCurrent(); return value; };
    state.token = token; state.authKind = authKind;
    try {
      state.config = await step(json('/api/status'));
      await rememberToken(token, checkCurrent).catch(error => { checkCurrent(); notice(error.message || '无法记住登录状态'); });
      checkCurrent();
      ui['access-token'].value = '';
      ui['login-overlay'].hidden = true;
      ui['demo-badge'].hidden = !state.config.demo;
      ui['model-label'].textContent = state.config.model;
      await step(chatInput.refreshOptions());
      await step(loadRoles());
      await step(refreshSessions());
      const remembered = storage.get('pocket-session');
      const selected = state.sessions.find(item => item.id === remembered) || state.sessions[0];
      if (selected && !selected.unavailable) await step(selectSession(selected.id));
      else { updateUI(); taskUI.refresh().catch(handleError); }
      await step(refreshMemoryCount());
      await step(lifeUI.refresh({ auto: false }));
      nativeCare.refresh();
      deviceAuth.refreshManagement();
      await step(activitiesUI.refresh());
      if (state.config.demo) notice('当前为本地演示模式，回复用于体验界面，没有调用真实模型。');
    } catch (error) {
      if (epoch !== authEpoch || error.name === 'AbortError') return;
      ui['login-overlay'].hidden = false;
      ui['login-error'].textContent = error.status === 401 ? '口令不正确，请再试一次。' : '无法连接服务，请确认服务已启动且网络可用。';
      ui['login-error'].hidden = false;
      if (error.status === 401) { forgetToken(); state.token = ''; }
    } finally {
      if (connectionAbort === controller) connectionAbort = null;
      if (epoch === authEpoch) {
        ui['login-button'].disabled = false;
        ui['login-button'].textContent = '进入 chat-agent →';
        updateUI();
      }
    }
  });
  return connectionWork;
}

function sessionDate(value) {
  const date = new Date(value);
  if (date.toDateString() === new Date().toDateString()) return '今天';
  return date.toLocaleDateString('zh-CN', { month: 'numeric', day: 'numeric' });
}
function renderSessions() {
  ui.sessions.replaceChildren();
  ui['session-count'].textContent = String(state.sessions.length);
  if (!state.sessions.length) {
    const empty = document.createElement('p'); empty.className = 'list-empty'; empty.textContent = '还没有会话，聊点什么吧。'; ui.sessions.append(empty);
  }
  const filter=($('session-filter')?.value || '').trim().toLocaleLowerCase();
  const visible=state.sessions.filter(item=>!filter || `${item.title || ''} ${roleName(item.role_id || 'companion')}`.toLocaleLowerCase().includes(filter));
  if (filter && !visible.length) { const empty=document.createElement('p'); empty.className='list-empty'; empty.textContent='没有匹配的会话标题'; ui.sessions.append(empty); }
  for (const item of visible) {
    const row = document.createElement('div');
    row.className = `session-row${item.id === state.current ? ' active-row' : ''}`;
    const button = document.createElement('button');
    button.className = `session-item${item.id === state.current ? ' active' : ''}`;
    button.title = item.title;
    const icon = document.createElementNS('http://www.w3.org/2000/svg', 'svg'); icon.setAttribute('viewBox', '0 0 24 24'); icon.setAttribute('aria-hidden', 'true');
    const path = document.createElementNS(icon.namespaceURI, 'path'); path.setAttribute('d', 'M4 4h16v12H9l-5 4V4z'); icon.append(path);
    const label = document.createElement('span'); label.className = 'session-text'; label.textContent = item.title;
    const date = document.createElement('span'); date.className = 'session-date'; date.textContent = item.active ? '生成中' : sessionDate(item.updated_at);
    button.append(icon, label, date);
    const role = document.createElement('span'); role.className = 'session-role'; role.textContent = roleName(item.role_id || 'companion'); button.insertBefore(role, date);
    button.addEventListener('click', () => selectSession(item.id).catch(handleError));
    const remove = document.createElement('button');
    remove.className = 'session-delete'; remove.type = 'button';
    remove.setAttribute('aria-label', `删除会话 ${item.title}`); remove.title = '删除这个会话';
    remove.textContent = '×';
    remove.disabled = item.active;
    remove.addEventListener('click', event => { event.stopPropagation(); deleteSession(item).catch(handleError); });
    row.append(button, remove);
    ui.sessions.append(row);
  }
  updateUI();
  const current = state.sessions.find(item => item.id === state.current);
  ui['chat-title'].textContent = current?.title || '新的开始';
}
async function refreshSessions() { state.sessions = await json('/api/sessions'); renderSessions(); }

// Deleting a conversation removes its journal on the server. Role-scoped memory
// and shared activities belong to the role and stay untouched.
async function deleteSession(item) {
  const label = item.title || '这个会话';
  if (!confirm(`删除「${label}」？对话记录会从服务器移除，角色长期记忆和共同活动不受影响。此操作无法撤销。`)) return;
  await json(`/api/sessions/${encodeURIComponent(item.id)}`, { method: 'DELETE' });
  const wasCurrent = state.current === item.id;
  state.sessions = state.sessions.filter(s => s.id !== item.id);
  if (wasCurrent) {
    clearInteractiveUI(); turnUI.detach(); sessionSelection++;
    chatInput.discard();
    state.current = null;
    ui.messages.replaceChildren(); ui.welcome.hidden = false;
    ui['chat-title'].textContent = '新的开始';
  }
  renderSessions();
}

async function copyText(text, button) {
  try {
    if (navigator.clipboard?.writeText) await navigator.clipboard.writeText(text);
    else {
      const input=document.createElement('textarea'); input.className='sr-only'; input.value=text; document.body.append(input); input.select();
      const copied=document.execCommand('copy'); input.remove(); if (!copied) throw new Error('clipboard unavailable');
    }
    const label=button.textContent; button.textContent='已复制'; setTimeout(()=>{if(button.isConnected)button.textContent=label;},1500);
  } catch { notice('暂时无法复制，请长按或选中文字复制。'); }
}

function card(role, text = '', key = '', images = [], context = null) {
  ui.welcome.hidden = true;
  const article = document.createElement('article'); article.className = `message ${role}`; article.dataset.role = role;
  if (key) article.dataset.messageId = key;
  const avatar = document.createElement('div'); avatar.className = 'message-avatar'; avatar.textContent = 'P'; avatar.setAttribute('aria-hidden', 'true');
  const content = document.createElement('div'); content.className = 'message-content';
  const label = document.createElement('span'); label.className = 'message-role'; label.textContent = roleName();
  const logs = document.createElement('div'); logs.className = 'tool-logs';
  const body = document.createElement('div'); body.className = 'message-body';
  const status = document.createElement('div'); status.className = 'message-status';
  const attachments=document.createElement('div'); attachments.className='message-attachments';
  chatInput.renderImages(attachments,images);
  const actions=document.createElement('div'); actions.className='message-actions';
  const copy=document.createElement('button'); copy.type='button'; copy.className='message-copy'; copy.textContent='复制回答'; copy.setAttribute('aria-label','复制这条回答');
  content.append(label, logs, attachments, body, status); if(role==='assistant'){actions.append(copy);content.append(actions);}
  article.append(avatar, content); ui.messages.append(article);
  const result = { node: article, body, status, logs, tools: new Map(), text: '',
    appendSegment(value,source) {
      this.text=[this.text,value].filter(Boolean).join('\n\n');
      copy.disabled=!this.text;
      const bubble=document.createElement('div'); bubble.className='reply-bubble';
      const budget=richText.createBudget();
      let cached=null, ordinal=0;
      if(source.messageIndex>=0){
        const cacheKey=JSON.stringify([source.role,source.session,source.messageIndex]);
        cached=richMessages.get(cacheKey);
        if(cached?.raw!==value){cached={raw:value,index:source.messageIndex,pieces:[]};richMessages.set(cacheKey,cached);}
        while(richMessages.size>96){
          const free=[...richMessages].find(([k,c])=>k!==cacheKey && !c.pieces.some(p=>p.node.contains(document.activeElement)));
          if(!free)break;richMessages.delete(free[0]);
        }
      }
      bubble.append(interactiveUI.renderMessage(value,source,part=>{
        const slot=ordinal++, old=cached?.pieces[slot];
        if(old){budget.count+=old.count;budget.bytes+=old.bytes;return old.node;}
        const node=document.createElement('div');node.className='rich-text-segment';
        const before={...budget};node.append(richText.render(part,{budget}));
        if(cached)cached.pieces[slot]={node,count:budget.count-before.count,bytes:budget.bytes-before.bytes};
        return node;
      })); body.append(bubble);
    },
    setText(value,source=context) {
      this.text=''; body.replaceChildren();
      if (role==='user') { this.text=value; renderUserContent(body,value); }
      else this.appendSegment(value,source || {role:state.roleID,session:state.current,messageIndex:-1,sha256:'',eligible:false,floor:uiFloor});
    }
  };
  copy.addEventListener('click',()=>copyText(result.text,copy));
  copy.disabled=!text;
  const original=result.setText; result.setText=function(value){original.call(this,value);copy.disabled=!value;};
  result.setText(text); return result;
}

function renderUserContent(body,text) {
  let response;
  try { response=JSON.parse(text)?.chat_ui_response; } catch {}
  if (response?.schema_version!==1 || !['quiz.single_choice','quiz.fill_blank'].includes(response.component_type)) {body.textContent=text;return;}
  const summary=document.createElement('p'); summary.className='ui-answer-summary';
  if (response.component_type==='quiz.single_choice' && typeof response.answer?.option_id==='string') summary.textContent=`互动答案 · 选择 ${response.answer.option_id}`;
  else if (Array.isArray(response.answer?.fields) && response.answer.fields.every(f=>typeof f.id==='string' && typeof f.value==='string')) summary.textContent=`互动答案 · ${response.answer.fields.map(f=>`${f.id}：${f.value}`).join(' / ')}`;
  else {body.textContent=text;return;}
  const details=document.createElement('details'), label=document.createElement('summary'), source=document.createElement('pre');
  label.textContent='查看原始答案'; source.textContent=text; details.append(label,source); body.append(summary,details);
}

const toolStarts = new Map();
const toolHintKeys = ['query','url','title','content','name','action','id'];

function toolHint(data) {
  if (!data.arguments) return '';
  let args;
  try { args = JSON.parse(data.arguments); } catch { return ''; }
  if (!args || typeof args !== 'object') return '';
  for (const key of toolHintKeys) {
    const value = args[key];
    if (typeof value === 'string' && value.trim()) return `「${value.trim().slice(0, 60)}」`;
  }
  return '';
}

function elapsed(ms) {
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) return `${Math.max(seconds, 1)} 秒`;
  return `${Math.floor(seconds / 60)} 分 ${seconds % 60} 秒`;
}

function toolLog(message, data, pending = false) {
  let log = message.tools.get(data.id);
  if (!log) {
    const details = document.createElement('details'); details.className = 'tool-log';
    const summary = document.createElement('summary'), output = document.createElement('pre'); details.append(summary, output); message.logs.append(details);
    log = { summary, output, node: details }; message.tools.set(data.id, log);
  }
  const names = {shared_activity_update:'更新活动记录',shared_activity_open:'接续活动记录',companion_preferences_update:'保存交流偏好',episode_propose:'记录事件候选',skill_load:'读取相处指引',knowledge_manage:'整理记忆',care_preferences:'调整关怀安排',web_search:'联网搜索',web_fetch:'读取网页',memory_search:'查找记忆',memory_write:'记录候选'};
  const label = names[data.name] || data.name || '工具';
  const hint = toolHint(data);
  let state;
  if (pending) {
    if (!toolStarts.has(data.id)) toolStarts.set(data.id, Date.now());
    state = `正在执行 · ${elapsed(Date.now() - toolStarts.get(data.id))}`;
  } else {
    const started = toolStarts.get(data.id);
    toolStarts.delete(data.id);
    state = data.error ? '未完成' : '已完成';
    if (started) state += ` · ${elapsed(Date.now() - started)}`;
  }
  log.node.classList.toggle('running', pending);
  log.node.classList.toggle('failed', !pending && Boolean(data.error));
  log.summary.textContent = `${label}${hint ? ' ' + hint : ''} · ${state}`;
  log.output.textContent = pending ? '等待工具返回结果…' : data.error || data.output || '执行完成';
}

// Only the provider's reasoning_content is shown. Treat it as inert text:
// no generated components, tools, HTML, links or memory writes are evaluated.
function showReasoning(article, text, key, status = 'completed') {
  if (!text || !article) return;
  let view = reasoningViews.get(key);
  if (!view) {
    const node=document.createElement('details'), summary=document.createElement('summary'), body=document.createElement('div');
    node.className='reasoning-log'; body.className='reasoning-body'; node.append(summary,body);
    view={node,summary,body,manual:false};
    summary.addEventListener('click',()=>{view.manual=true;});
    reasoningViews.set(key,view);
  }
  if (!view.manual) view.node.open=status!=='completed';
  view.summary.textContent=status==='running' ? '正在思考…' : status==='completed' ? '思考过程' : '思考过程 · 本次未完成';
  if (view.body.textContent!==text) view.body.textContent=text;
  article.querySelector('.message-role').after(view.node);
  while(reasoningViews.size>96) {
    const unused=[...reasoningViews].find(([k,v])=>k!==key && !v.node.isConnected);
    if (!unused) break; reasoningViews.delete(unused[0]);
  }
}
function renderHistory(messages, turns = [], hashes = new Map()) {
  chatInput.beforeHistoryRender();
  const focus=interactiveUI.captureFocus();
  const richFocus=document.activeElement?.closest?.('.rich-text-segment') ? document.activeElement : null;
  const top = ui.conversation.scrollTop;
  ui.messages.replaceChildren(); ui.welcome.hidden = messages.length > 0;
  const relevant = turns.filter(t => t.content || t.text || t.reasoning_content || t.error || t.images?.length);
  const roots = relevant.filter(t => t.batch_id === t.id && t.status !== 'withdrawn');
  const members = new Map();
  for (const t of relevant) if (t.batch_id) { if (!members.has(t.batch_id)) members.set(t.batch_id,[]); members.get(t.batch_id).push(t); }
  for (const batch of members.values()) batch.sort((a,b) => a.sequence - b.sequence);
  const userKeys = new Map();
  for (const root of roots) {
    const bound = root.status === 'running' ? messages.length : root.end_index;
    for (const [offset,t] of (members.get(root.id) || []).entries()) if (root.start_index + offset < bound) userKeys.set(root.start_index + offset,`${state.current}:user:${t.id}`);
  }
  const owner = index => [...turns].reverse().find(root => root.batch_id===root.id && index >= root.start_index && (root.status === 'running' || index < root.end_index));
  let current = null, currentStart = -1;
  const calls = new Map(), inserted = new Set();
  let legacyUser = 0;
  const incomplete = root => {
    if (inserted.has(root.id)) return; inserted.add(root.id);
    const end = root.status === 'running' ? messages.length : root.end_index;
    for (const [offset,t] of (members.get(root.id) || []).entries()) {
      if (root.start_index + offset >= end) { const user = card('user',t.content,`${state.current}:user:${t.id}`,t.images || []); user.status.textContent = root.status === 'running' ? '已接收':'这次未完成'; }
    }
    const committed = messages.slice(root.start_index,end).filter(m => m.role === 'assistant').map(m => m.content || '').join('');
    const remainder = root.text?.startsWith(committed) ? root.text.slice(committed.length) : root.text || '';
    if (root.status === 'running' && !remainder && committed && messages.slice(root.start_index,end).some(m => m.role === 'assistant' && !m.tool_calls?.length && m.content)) return;
    if (!current || currentStart < root.start_index) { current = card('assistant','',`${state.current}:turn:${root.id}`); currentStart = end; }
    if (remainder) current.setText([current.text,remainder].filter(Boolean).join('\n\n'));
    for (const tool of root.tools || []) if (!calls.has(`${root.id}:${tool.id}`)) { toolLog(current,tool,!tool.output && !tool.error); calls.set(`${root.id}:${tool.id}`,{card:current,name:tool.name}); }
    current.node.classList.add(root.status === 'running' ? 'pending':'unfinished');
    current.status.textContent = root.status === 'running' ? (root.text ? '正在回复…':root.thinking && root.thinking!=='off' ? '正在思考或等待模型回应…':'正在等待模型回应…') : root.error || '这段回复未完成，没有自动重试。';
    current = null; currentStart = -1;
  };
  for (let index=0;index<messages.length;index++) {
    for (const root of roots) if (['failed','canceled','interrupted'].includes(root.status) && root.end_index === index) incomplete(root);
    const message = messages[index], root = owner(index), key = root ? `${state.current}:turn:${root.id}`:`${state.current}:message:${index}`;
    if (message.role === 'user') { legacyUser = index; card('user',message.content,userKeys.get(index) || `${state.current}:message:${index}`,message.images || []); current = null; currentStart = -1; }
    else if (message.role === 'assistant') {
      const source={role:state.roleID,session:state.current,messageIndex:index,sha256:hashes.get(index) || '',eligible:Boolean(hashes.get(index) && root?.status==='completed' && index===root.end_index-1 && !message.tool_calls?.length),floor:uiFloor};
      if (!current) { current = card('assistant',message.content,key,[],source); currentStart = index; }
      else if (message.content) current.appendSegment(message.content,source);
      if (!root && message.reasoning_content) showReasoning(current.node,message.reasoning_content,`${state.current}:reasoning:${index}`);
      for (const call of message.tool_calls || []) { toolLog(current,{id:call.id,name:call.function.name},true); calls.set(`${root?.id || `legacy-${legacyUser}`}:${call.id}`,{card:current,name:call.function.name}); }
      if (!message.tool_calls?.length) {
        if (!root || root.status === 'completed') {
          voiceUI.attach(current);
          const voice=current.node.querySelector('.voice-read');
          if(voice)current.node.querySelector('.message-actions')?.append(voice);
        }
        current = null; currentStart = -1;
      }
    } else if (message.role === 'tool') {
      const call = calls.get(`${root?.id || `legacy-${legacyUser}`}:${message.tool_call_id}`);
      if (call) toolLog(call.card,{id:message.tool_call_id,name:call.name,output:message.content,error:message.content.startsWith('Error: ') ? message.content.slice(7):''});
    }
  }
  for (const root of roots) if (root.status === 'running' || ['failed','canceled','interrupted'].includes(root.status) && root.end_index >= messages.length) incomplete(root);
  for (const root of roots) {
    const end=root.status==='running' ? messages.length : root.end_index;
    const reasoning=root.reasoning_content || messages.slice(root.start_index,end).filter(m=>m.role==='assistant').map(m=>m.reasoning_content || '').filter(Boolean).join('\n\n');
    showReasoning(ui.messages.querySelector(`[data-message-id="${CSS.escape(`${state.current}:turn:${root.id}`)}"].assistant`),reasoning,`${state.current}:reasoning:turn:${root.id}`,root.status);
  }
  for (const t of relevant.filter(t => t.status === 'queued').sort((a,b) => a.sequence - b.sequence)) {
    const pending = card('user',t.content,`${state.current}:user:${t.id}`,t.images || []); pending.node.classList.add('queued'); pending.status.textContent = '待发送';
  }
  workspace.reconcile();
  interactiveUI.reconcileResponses(answerTurns(turns),turnUI.pendingSnapshot().pending);
  interactiveUI.update(); interactiveUI.restoreFocus(focus);
  if(richFocus?.isConnected && document.activeElement===document.body)richFocus.focus({preventScroll:true});
  if (!messages.length && !relevant.length) ui.conversation.scrollTop = 0;
  else if (!state.follow) ui.conversation.scrollTop = top;
  else scrollBottom(true);
  for (const root of roots) if(root.model) {
    for(const node of ui.messages.querySelectorAll(`[data-message-id="${CSS.escape(`${state.current}:turn:${root.id}`)}"] .message-actions`)) {
      const label=document.createElement('span'); label.className='message-model';
      label.textContent=root.model+({medium:' · 思考 中',high:' · 思考 高',ultra:' · 思考 超高'}[root.thinking] || ''); node.append(label);
    }
  }
  updateLatest(!state.follow);
  // Only the newest completed turn can be purged: removing an older one would
  // leave later turns pointing at message indexes that no longer exist.
  const newest = [...roots].filter(t => t.status === 'completed').pop();
  if (newest && newest.end_index === messages.length) {
    const node = ui.messages.querySelector(`[data-message-id="${CSS.escape(`${state.current}:user:${newest.id}`)}"]`);
    if (node) {
      const remove = document.createElement('button');
      remove.className = 'delete-turn'; remove.type = 'button';
      remove.textContent = '删除这轮记录';
      remove.addEventListener('click', event => { event.stopPropagation(); purgeTurn(newest); });
      node.append(remove);
    }
  }
}
// Physically removes one turn: its messages leave the journal file, not just the view.
async function purgeTurn(turn) {
  if (!confirm('删除这一轮的对话记录？它会从服务器上的聊天记录中移除，无法撤销。')) return;
  await json(`/api/sessions/${encodeURIComponent(state.current)}/turns/${encodeURIComponent(turn.id)}?purge=1`, { method: 'DELETE' });
  await refreshSessions();
  await selectSession(state.current);
}
let unreadBelow = false;
function updateLatest(changed = false) {
  if (changed && !state.follow) unreadBelow = true;
  if (state.follow) unreadBelow = false;
  const button=$('scroll-latest');
  button.hidden=state.follow || !ui.messages.childElementCount;
  button.classList.toggle('has-new',unreadBelow);
  $('scroll-latest-label').textContent=unreadBelow?'查看新回复':'回到最新消息';
}
function scrollBottom(force = false) { if (force || state.follow) { ui.conversation.scrollTop = ui.conversation.scrollHeight; if(force)state.follow=true; updateLatest(); } }
async function applyTurnHistory(history) {
  if (!state.token || history.id !== state.current) return;
  const revision=++historyRevision, epoch=authEpoch, session=state.current;
  if (state.roleID !== (history.role_id || 'companion')) { roleProposalsUI.clear(); topicsUI.clear(); state.memories = []; ui['memory-count'].hidden = true; }
  state.roleID = history.role_id || 'companion'; state.summary = history.summary; ui['role-picker'].value = state.roleID; showSummary();
  chatInput.syncScope();
  interactiveUI.syncScope(state.roleID,state.current);
  uiFloor=history.ui_context_floor || 0;
  activitiesUI.context(history.activity || null);
  const hashes=new Map();
  try {
    await Promise.all((history.messages || []).map(async(message,index)=>{
      if(message.role!=='assistant' || !message.content?.includes('chat-ui') || !crypto.subtle) return;
      const hash=await crypto.subtle.digest('SHA-256',new TextEncoder().encode(message.content));
      hashes.set(index,Array.from(new Uint8Array(hash),b=>b.toString(16).padStart(2,'0')).join(''));
    }));
  } catch { notice('当前浏览器无法校验互动来源，组件保持只读。'); }
  if (revision!==historyRevision || epoch!==authEpoch || session!==state.current) return;
  uiSourceMessages=history.messages || [];
  for(const [key,entry] of richMessages)if(uiSourceMessages[entry.index]?.role!=='assistant' || uiSourceMessages[entry.index].content!==entry.raw)richMessages.delete(key);
  interactiveUI.retainSources([...hashes].map(([message_index,sha256])=>({message_index,sha256})));
  renderHistory(history.messages || [],history.turns || [],hashes);
}
async function refreshAfterTurn() {
  if (!state.token) return;
  await refreshSessions(); await refreshMemoryCount(); await taskUI.refresh(); await lifeUI.refresh(); await activitiesUI.refresh();
}
async function selectSession(id) {
  if (state.submitting || state.compacting || state.imageBusy) return;
  voiceUI.clear();
  if(id!==state.current)clearInteractiveUI();
  chatInput.discard(); state.follow=true;
  const selected = ++sessionSelection;
  turnUI.detach(); state.loading = true; updateUI(); notice();
  try {
    const history = await json(`/api/sessions/${encodeURIComponent(id)}/messages`);
    if (selected !== sessionSelection || !state.token) return;
    state.current = id; storage.set('pocket-session',id); turnUI.watch(id,history);
    renderSessions(); drawer(false);
    if (history.messages?.length || history.turns?.some(turn => turn.content || turn.text || turn.images?.length)) scrollBottom(true);
    else ui.conversation.scrollTop = 0;
    taskUI.refresh().catch(handleError); lifeUI.refresh().catch(handleError); activitiesUI.refresh().catch(handleError);
  } finally { if (selected === sessionSelection) { state.loading = false; updateUI(); } }
}
async function createRemoteSession() {
  const targetRole = state.roleID, auth = state.token;
  const item = await json('/api/sessions',{method:'POST',body:JSON.stringify({role_id:targetRole})});
  if (state.token !== auth || state.roleID !== targetRole) throw new Error('连接或角色已变化，请重新打开会话。');
  state.current = item.id; state.external = false; state.summary = null; showSummary(); storage.set('pocket-session',item.id);
  state.sessions.unshift(item); renderSessions(); return item;
}
async function newChat() {
  if (state.streaming || state.external || state.loading || state.compacting || state.submitting || state.submitUnknown || state.imageBusy) return;
  voiceUI.clear();
  clearInteractiveUI();
  chatInput.discard(); state.follow=true;
  const selected = ++sessionSelection;
  state.loading = true; turnUI.detach(); updateUI(); notice();
  try {
    const item = await createRemoteSession();
    if (selected !== sessionSelection || !state.token) return false;
    turnUI.watch(item.id,{id:item.id,messages:[],turns:[],active:false,role_id:state.roleID,queue_paused:false});
    drawer(false); ui.prompt.value = ''; ui.prompt.style.height = 'auto'; lifeUI.refresh().catch(handleError); return true;
  } catch (error) { handleError(error); return false; }
  finally { if (selected === sessionSelection) { state.loading = false; updateUI(); ui.prompt.focus(); } }
}

async function openActivityChat(activity) {
  if (!state.token || state.loading || state.streaming || state.external || state.compacting || state.submitting || state.submitUnknown || state.queuedCount) throw new Error('请先停止生成并处理当前队列，再开始活动聊天。');
  if (ui.prompt.value.trim() || chatInput.hasUnsentImages()) throw new Error('输入框里还有草稿，请先发送或清空，再开始活动聊天。');
  if (activity.role_id !== state.roleID || activity.status !== 'active') throw new Error('请在当前角色下恢复活动，再接着聊。');
  voiceUI.clear();
  clearInteractiveUI();
  const selected = ++sessionSelection, auth = state.token, role = state.roleID;
  state.loading = true; turnUI.detach(); updateUI(); notice();
  try {
    const item = await createRemoteSession();
    if (selected !== sessionSelection || state.token !== auth || state.roleID !== role) return;
    turnUI.watch(item.id,{id:item.id,messages:[],turns:[],active:false,role_id:role,queue_paused:false});
    await json(`/api/activities/${encodeURIComponent(activity.id)}/attach?role_id=${encodeURIComponent(role)}`,{method:'POST',body:JSON.stringify({revision:activity.revision,session_id:item.id})});
    if (selected !== sessionSelection || state.token !== auth || state.roleID !== role) return;
    await turnUI.refresh();
    const starters = {bedtime:'睡前陪我轻松聊一会儿吧。',reflection:'我们聊聊今天的一件小事吧。',interest:'我们接着聊这个感兴趣的话题吧。',small_step:'陪我看看这个小目标，现在能做怎样的一小步？'};
    ui.prompt.value = activity.progress ? '我们从上次保存的进展接着聊吧。' : starters[activity.kind] || '我们接着聊这个活动吧。';
    ui.prompt.style.height = 'auto'; drawer(false); notice('活动已关联。开场放在输入框里，点发送后才会调用模型。');
  } finally { if (selected === sessionSelection) { state.loading = false; updateUI(); ui.prompt.focus(); } }
}

function restoreDraft(text) {
  const existing = ui.prompt.value;
  ui.prompt.value = existing.trim() ? `${text}\n\n${existing}` : text;
  notice(existing.trim() ? '未提交的内容已放在输入框前面，可以编辑后再发送。' : '内容已放回输入框，可以编辑后再发送。'); ui.prompt.style.height = `${Math.min(ui.prompt.scrollHeight,160)}px`; updateUI(); ui.prompt.focus(); return true;
}
async function restoreTurnDraft(text,mode,options={}) {
  if(state.imageBusy)return false;
  try { await chatInput.restoreUploaded(options); restoreDraft(text); return true; }
  catch(error){ handleError(error); return false; }
}
async function sendMessage(event) {
  event?.preventDefault();
  const text = ui.prompt.value.trim();
  if ((!text && !chatInput.hasImages()) || !state.token || state.loading || state.compacting || state.submitting || state.submitUnknown || state.imageBusy) return;
  if (new TextEncoder().encode(text).length > 65536) { notice('消息过长，请缩短到 64 KiB 以内。'); return; }
  voiceUI.stop();
  try {
    const options=await chatInput.prepare();
    if (ui.prompt.value.trim()===text) {ui.prompt.value='';ui.prompt.style.height='auto';}
    state.follow=true;notice();updateUI();
    await turnUI.submit(text,'auto',false,options);
  } catch(error){handleError(error);}
  updateUI();ui.prompt.focus();
}
async function stopMessage() { await turnUI.stop(); }

ui['login-form'].addEventListener('submit', event => { event.preventDefault(); if($('legacy-login-fields').hidden){deviceAuth.connect();return;} const token=ui['access-token'].value.trim();if(token)connect(token); });
ui.logout.addEventListener('click', logout);
ui['new-chat'].addEventListener('click', newChat);
$('session-filter').addEventListener('input',renderSessions);
ui.menu.addEventListener('click', () => drawer(!ui.sidebar.classList.contains('open')));
ui['drawer-scrim'].addEventListener('click', () => drawer(false));
ui.composer.addEventListener('submit', sendMessage);
ui.stop.addEventListener('click', stopMessage);
ui.prompt.addEventListener('input', () => { ui.prompt.style.height = 'auto'; ui.prompt.style.height = `${Math.min(ui.prompt.scrollHeight, 160)}px`; updateUI(); });
ui.prompt.addEventListener('keydown', event => { if (event.key === 'Enter' && !event.shiftKey && !event.isComposing && !matchMedia('(pointer: coarse)').matches) { event.preventDefault(); sendMessage(); } });
ui.conversation.addEventListener('scroll', () => { state.follow = ui.conversation.scrollHeight - ui.conversation.scrollTop - ui.conversation.clientHeight < 100; updateLatest(); }, { passive: true });
document.addEventListener('focusin',event=>{ if(event.target.closest?.('.chat-ui-card')) {state.follow=false;updateLatest();} });
$('scroll-latest').addEventListener('click',()=>scrollBottom(true));
if(window.ResizeObserver)new ResizeObserver(entries=>{document.querySelector('.main-panel').style.setProperty('--composer-height',`${Math.ceil(entries[0].target.getBoundingClientRect().height)}px`);}).observe(document.querySelector('.composer-area'));
document.querySelectorAll('[data-prompt]').forEach(button => button.addEventListener('click', () => { if (!state.streaming && !state.external && !state.loading) { ui.prompt.value = button.dataset.prompt; sendMessage(); } }));
document.addEventListener('keydown', event => { if (event.key === 'Escape') drawer(false); if (event.key.toLowerCase() === 'n' && !event.ctrlKey && !event.metaKey && !event.altKey && !['INPUT', 'TEXTAREA', 'BUTTON'].includes(document.activeElement.tagName) && ui['login-overlay'].hidden && !document.querySelector('dialog[open]')) newChat(); });
ui['delete-session'].addEventListener('click', () => {
  const current = state.sessions.find(item => item.id === state.current);
  if (!current) return;
  deleteSession(current).catch(handleError);
});
ui['reset-chat'].addEventListener('click', async () => {
  if (!state.current || !confirm('重置会清空当前对话上下文，原始聊天记录和本角色长期记忆仍保留。是否继续？')) return;
  voiceUI.clear();
  clearInteractiveUI();
  try { await json(`/api/sessions/${state.current}/reset`, { method: 'POST' }); await refreshSessions(); await selectSession(state.current); }
  catch (error) { handleError(error); }
});
ui.settings.addEventListener('click', () => {
  if (!state.config) return;
  ui['connection-info'].replaceChildren();
  for (const [label, value] of [['服务', location.host], ['模型', state.config.model], ['模式', state.config.demo ? '本地演示，无真实模型' : '自定义模型 API'], ['时区', state.config.time_zone], ['提醒', '服务端收件箱，无关闭应用推送'], ['网页抓取', state.config.web_fetch ? '限定域名，已开启' : '未开启'], ['网页搜索', state.config.web_search ? '自定义端点，已开启' : '未开启'], ['响应期限', `${state.config.timeout_seconds} 秒`]]) {
    const dt = document.createElement('dt'), dd = document.createElement('dd'); dt.textContent = label; dd.textContent = value; ui['connection-info'].append(dt, dd);
  }
  ui['settings-dialog'].showModal(); deviceAuth.refreshManagement();
});
ui['close-settings'].addEventListener('click', () => ui['settings-dialog'].close());
window.addEventListener('beforeinstallprompt', event => { event.preventDefault(); installPrompt = event; ui.install.hidden = false; });
ui.install.addEventListener('click', async () => { if (installPrompt) { await installPrompt.prompt(); installPrompt = null; ui.install.hidden = true; } });
function networkStatus() { ui['offline-banner'].hidden = navigator.onLine; }
window.addEventListener('online', networkStatus); window.addEventListener('offline', networkStatus); networkStatus();
if ('serviceWorker' in navigator && window.isSecureContext) navigator.serviceWorker.register('/sw.js').catch(() => {});
async function restoreConnection() {
  storage.remove('pocket-token'); persistent.clear();
  updateUI(); await deviceAuth.start();
}
window.addEventListener('pocket-native-session-required',()=>{
  if(state.authKind==='device' && state.token)renewDeviceSession().catch(error=>{deviceAuth.showError(error);notice(error.message);});
  else deviceAuth.start();
});

restoreConnection();

function roleName(id = state.roleID) { return state.roles.find(role => role.id === id)?.name || id; }
async function loadRoles() {
  state.roles = await json('/api/roles');
  ui['role-picker'].replaceChildren();
  for (const role of state.roles) { const option = document.createElement('option'); option.value = role.id; option.textContent = role.name; ui['role-picker'].append(option); }
  ui['role-picker'].value = state.roleID;
}
function dialogMessage(id, message = '') { ui[id].textContent = message; ui[id].hidden = !message; }
function memoryURL(id = '') { return `/api/memory${id ? `/${encodeURIComponent(id)}` : ''}?role_id=${encodeURIComponent(state.roleID)}`; }
async function refreshMemoryCount() {
  if (!state.token) return;
  const role = state.roleID;
  const entries = await json(memoryURL());
  if (role !== state.roleID) return;
  const count = entries.filter(entry => entry.status === 'pending').length;
  ui['memory-count'].textContent = String(count); ui['memory-count'].hidden = !count;
}
function resetMemoryForm() {
  state.memoryEditing = null; ui['memory-content'].value = ''; ui['memory-status'].value = 'pending';
  ui['memory-form-label'].textContent = '添加一条值得记住的事实'; ui['memory-save'].textContent = '保存候选'; ui['memory-cancel-edit'].hidden = true;
}
function renderMemories() {
  ui['memory-list'].replaceChildren();
  document.querySelectorAll('[data-memory-filter]').forEach(button => button.classList.toggle('selected', button.dataset.memoryFilter === state.memoryFilter));
  const entries = state.memories.filter(entry => entry.status === state.memoryFilter);
  if (!entries.length) { const empty = document.createElement('div'); empty.className = 'memory-empty'; empty.textContent = state.memoryFilter === 'pending' ? '还没有待审核的记忆。你可以手动添加，或明确告诉助手“记住……” 。' : '这个角色还没有已审核的记忆。确认候选后，它就可以用于新会话。'; ui['memory-list'].append(empty); }
  for (const entry of entries) {
    const article = document.createElement('article'); article.className = 'memory-entry'; article.dataset.memoryId = entry.id;
    const content = document.createElement('p'); content.className = 'memory-entry-content'; content.textContent = entry.content;
    const footer = document.createElement('div'); footer.className = 'memory-entry-footer';
    const source = document.createElement('span'); source.className = 'memory-entry-source'; source.textContent = `${entry.source?.startsWith('merged:') ? '合并记录' : entry.source === 'agent' ? '助手提议' : '手动添加'} · ${new Date(entry.updated_at).toLocaleDateString('zh-CN')}`;
    const actions = document.createElement('div'); actions.className = 'memory-entry-actions';
    const action = (label, className, handler) => { const button = document.createElement('button'); button.type = 'button'; button.className = className; button.textContent = label; button.addEventListener('click', handler); actions.append(button); };
    if (entry.status === 'pending') action('确认使用', 'approve', () => memoryAction(async () => { await json(memoryURL(entry.id), { method: 'PATCH', body: JSON.stringify({ status: 'approved', revision: entry.revision }) }); state.memoryFilter = 'approved'; dialogMessage('memory-message', '已审核，这条事实可以用于当前角色的新请求。'); }));
    action('编辑', 'edit', () => { state.memoryEditing = entry; ui['memory-content'].value = entry.content; ui['memory-status'].value = entry.status; ui['memory-form-label'].textContent = '修改这条记忆'; ui['memory-save'].textContent = '更新记忆'; ui['memory-cancel-edit'].hidden = false; ui['memory-content'].focus(); });
    action('删除', 'delete', () => { if (confirm('删除这条长期记忆？它将不再用于之后的记忆检索，已有聊天记录不会被删除。')) memoryAction(async () => { await json(`${memoryURL(entry.id)}&revision=${entry.revision}`, { method: 'DELETE' }); if (state.memoryEditing?.id === entry.id) resetMemoryForm(); dialogMessage('memory-message', '记忆已删除。'); }); });
    footer.append(source, actions); article.append(content, footer); ui['memory-list'].append(article);
  }
  const count = state.memories.filter(entry => entry.status === 'pending').length; ui['memory-count'].textContent = String(count); ui['memory-count'].hidden = !count;
}
async function loadMemories() { const role = state.roleID; const entries = await json(memoryURL()); if (role !== state.roleID) return; state.memories = entries; renderMemories(); }
async function memoryAction(work) {
  ui['memory-save'].disabled = true; ui['memory-export'].disabled = true;
  ui['memory-list'].querySelectorAll('button').forEach(button => { button.disabled = true; });
  try { await work(); await loadMemories(); }
  catch (error) { if (error.status === 401) handleError(error); else dialogMessage('memory-message', error.message); }
  finally { ui['memory-save'].disabled = false; ui['memory-export'].disabled = false; ui['memory-list'].querySelectorAll('button').forEach(button => { button.disabled = false; }); }
}
ui['memory-open'].addEventListener('click', async () => {
  drawer(false); resetMemoryForm(); dialogMessage('memory-message'); ui['memory-role-name'].textContent = roleName(); state.memoryFilter = 'pending'; state.memories = [];
  ui['memory-list'].replaceChildren(); ui['memory-dialog'].showModal();
  await memoryAction(async () => {});
});
ui['memory-close'].addEventListener('click', () => ui['memory-dialog'].close());
ui['memory-cancel-edit'].addEventListener('click', resetMemoryForm);
ui['memory-status'].addEventListener('change', () => { if (!state.memoryEditing) ui['memory-save'].textContent = ui['memory-status'].value === 'approved' ? '确认并保存' : '保存候选'; });
ui['memory-form'].addEventListener('submit', event => {
  event.preventDefault();
  const content = ui['memory-content'].value.trim(), status = ui['memory-status'].value;
  if (!content || new TextEncoder().encode(content).length > 4096) { dialogMessage('memory-message', '记忆内容需为 1–4096 字节。'); return; }
  memoryAction(async () => {
    const editing = state.memoryEditing;
    const saved = await json(memoryURL(editing?.id), { method: editing ? 'PATCH' : 'POST', body: JSON.stringify({ content, status, ...(editing ? { revision: editing.revision } : {}) }) });
    state.memoryFilter = saved.status; resetMemoryForm(); dialogMessage('memory-message', saved.status === 'approved' ? '记忆已保存，可以用于本角色的之后对话。' : '已保存为候选，审核后才会用于对话。');
  });
});
for (const button of document.querySelectorAll('[data-memory-filter]')) button.addEventListener('click', () => { state.memoryFilter = button.dataset.memoryFilter; renderMemories(); });
ui['memory-export'].addEventListener('click', async () => {
  try { const role = state.roleID; const response = await request(memoryURL('export')); saveMarkdown(await response.text(), `${role}-memory.md`); }
  catch (error) { dialogMessage('memory-message', error.message); }
});
ui['role-picker'].addEventListener('change', async () => {
  const previous = state.roleID; roleProposalsUI.clear(); topicsUI.clear(); state.roleID = ui['role-picker'].value;
  state.memories = []; ui['memory-count'].hidden = true;
  if (!await newChat()) { state.roleID = previous; ui['role-picker'].value = previous; return; }
  notice(`已和“${roleName()}”开启新会话，旧会话与其他角色的记忆保持独立。`);
  taskUI.refresh().catch(handleError);
  lifeUI.refresh().catch(handleError);
  refreshMemoryCount().catch(handleError);
});
const documentKeys = { 'IDENTITY.md': 'identity', 'RULES.md': 'rules', 'PROFILE.md': 'profile' };
function showRoleDocument() { if (state.roleDocument) ui['role-content'].value = state.roleDocument[documentKeys[ui['role-file'].value]]; }
function showRoleSkills() {
  ui['role-skills'].replaceChildren();
  for (const skill of state.roleDocument?.skills || []) { const details = document.createElement('details'); details.className = 'role-skill'; const summary = document.createElement('summary'); summary.textContent = skill.name; const content = document.createElement('div'); content.className = 'role-skill-body'; content.textContent = skill.body; details.append(summary, content); ui['role-skills'].append(details); }
  if (!state.roleDocument?.skills.length) ui['role-skills'].textContent = '当前角色还没有 Skills。';
}
ui['role-open'].addEventListener('click', async () => {
  const roleID = state.roleID; state.roleDocument = null;
  drawer(false); dialogMessage('role-message'); ui['role-content'].value = ''; ui['role-skills'].replaceChildren(); ui['role-id-label'].textContent = state.roleID; ui['role-title'].textContent = `${roleName()} · 角色设置`; ui['role-dialog'].showModal();
  ui['role-save'].disabled = true;
  try { const document = await json(`/api/roles/${encodeURIComponent(roleID)}`); if (state.roleID !== roleID) return; state.roleDocument = document; showRoleDocument(); showRoleSkills(); }
  catch (error) { dialogMessage('role-message', error.message); }
  finally { ui['role-save'].disabled = false; }
});
ui['role-close'].addEventListener('click', () => ui['role-dialog'].close());
ui['role-file'].addEventListener('change', showRoleDocument);
ui['role-form'].addEventListener('submit', async event => {
  event.preventDefault(); const content = ui['role-content'].value;
  if (!state.roleDocument || state.roleDocument.id !== state.roleID) { dialogMessage('role-message', '请先等待当前角色文档加载完成。'); return; }
  const roleID = state.roleDocument.id;
  if (!content.trim() || new TextEncoder().encode(content).length > 16384) { dialogMessage('role-message', '文档内容需为 1–16384 字节。'); return; }
  ui['role-save'].disabled = true;
  try { const document = await json(`/api/roles/${encodeURIComponent(roleID)}`, { method: 'PATCH', body: JSON.stringify({ file: ui['role-file'].value, content }) }); if (state.roleID !== roleID) return; state.roleDocument = document; await loadRoles(); renderSessions(); dialogMessage('role-message', '当前文档已保存，之后的新请求会使用这份配置。'); }
  catch (error) { dialogMessage('role-message', error.message); }
  finally { ui['role-save'].disabled = false; }
});
function showSummary() {
  ui['summary-details'].hidden = !state.summary; ui['summary-content'].textContent = state.summary?.content || '';
}
ui['compact-context'].addEventListener('click', async () => {
  if (!state.current || state.loading || state.streaming || state.external) return;
  state.compacting = true; updateUI(); ui['compact-message'].textContent = '正在整理较早的对话，完整记录会保留…';
  try { const result = await json(`/api/sessions/${state.current}/compact`, { method: 'POST' }); state.summary = result.summary; showSummary(); ui['compact-message'].textContent = result.changed ? '已整理较早的对话。最近几轮与完整记录保持不变。' : '当前会话还很短，暂时不需要整理。'; }
  catch (error) { ui['compact-message'].textContent = error.message; }
  finally { state.compacting = false; updateUI(); }
});
