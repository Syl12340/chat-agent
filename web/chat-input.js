// Image drafts stay on this device until Send. Server image references, never
// credentials or base64, are retained by the durable submission queue.
const SAFE_ID = /^[A-Za-z0-9_-]{1,128}$/;
const LEVELS = {off:'关闭思考',medium:'思考 · 中',high:'思考 · 高',ultra:'思考 · 超高'};
const abort = () => new DOMException('会话已变化','AbortError');

async function imageSize(file) {
  const data = new Uint8Array(await file.slice(0,262144).arrayBuffer());
  const view = new DataView(data.buffer);
  if (data.length >= 24 && data[0] === 137 && data[1] === 80 && data[2] === 78 && data[3] === 71 && view.getUint32(4) === 0x0d0a1a0a) {
    return {width:view.getUint32(16),height:view.getUint32(20),type:'image/png'};
  }
  if (data[0] === 255 && data[1] === 216) {
    let offset = 2;
    while (offset+4 <= data.length) {
      if (data[offset++] !== 255) break;
      while (data[offset] === 255) offset++;
      const marker = data[offset++];
      if (marker === 217 || marker === 218) break;
      if (marker === 1 || marker >= 208 && marker <= 215) continue;
      if (offset+2 > data.length) break;
      const length = view.getUint16(offset);
      if (length < 2 || offset+length > data.length) break;
      if ([192,193,194,195,197,198,199,201,202,203,205,206,207].includes(marker) && length >= 7) {
        return {height:view.getUint16(offset+3),width:view.getUint16(offset+5),type:'image/jpeg'};
      }
      offset += length;
    }
  }
  throw new Error('请选择有效的 JPEG 或 PNG 图片。');
}

async function prepareImage(file) {
  if (!file.size || file.size > 12*1024*1024) throw new Error('原图不能超过 12 MiB，请先缩小图片。');
  const size = await imageSize(file);
  if (!size.width || !size.height || size.width*size.height > 40000000) throw new Error('图片尺寸过大，请先缩小后再添加。');
  const ratio = Math.min(1,1600/Math.max(size.width,size.height));
  const width = Math.max(1,Math.round(size.width*ratio)), height = Math.max(1,Math.round(size.height*ratio));
  let source, temporary;
  try {
    if (window.createImageBitmap) source = await createImageBitmap(file,{imageOrientation:'from-image',resizeWidth:width,resizeHeight:height,resizeQuality:'high'});
    else {
      temporary = URL.createObjectURL(file);
      source = new Image(); source.src = temporary;
      await source.decode();
    }
    const canvas = document.createElement('canvas');
    const scale = Math.min(1,1600/Math.max(source.width || source.naturalWidth,source.height || source.naturalHeight));
    canvas.width = Math.max(1,Math.round((source.width || source.naturalWidth)*scale));
    canvas.height = Math.max(1,Math.round((source.height || source.naturalHeight)*scale));
    const context = canvas.getContext('2d');
    if (!context) throw new Error('这台设备暂时无法处理图片，请更新客户端或浏览器。');
    context.drawImage(source,0,0,canvas.width,canvas.height);
    const blob = await new Promise(resolve => canvas.toBlob(resolve,size.type,.85));
    canvas.width = canvas.height = 1;
    if (!blob || blob.size > 4*1024*1024) throw new Error('处理后的图片超过 4 MiB，请缩小后再试。');
    return blob;
  } finally {
    source?.close?.();
    if (temporary) URL.revokeObjectURL(temporary);
  }
}

export function createChatInput({state,request,json,createSession,notice,onChange,onImageLoad}) {
  const $ = id => document.getElementById(id);
  const picker = $('model-picker'), thinking = $('thinking-picker'), add = $('image-add'), files = $('image-files');
  const strip = $('image-drafts'), hint = $('image-hint'), preview = $('image-viewer'), large = $('image-viewer-image');
  let epoch = 0, drafts = [], options = null, loading = false, selecting = false, uploads = null, model = '', level = 'off';
  let historyScope = '', boundRole = state.roleID, boundSession = state.current;
  const cache = new Map();
  const pendingImages = new WeakMap();
  const nativeVersion=navigator.userAgent.match(/PocketAgentAndroid\/(\d+)\.(\d+)/);
  const oldNative=Boolean(nativeVersion && Number(nativeVersion[1])===0 && Number(nativeVersion[2])<19);
  const observer = window.IntersectionObserver ? new IntersectionObserver(entries => {
    for (const entry of entries) if (entry.isIntersecting) {
      observer.unobserve(entry.target);
      const details = pendingImages.get(entry.target);
      if (details) loadHistoryImage(entry.target,details);
    }
  },{root:$('conversation'),rootMargin:'200px'}) : null;
  function readPreference() { try { return JSON.parse(localStorage.getItem('chat-agent-composer') || '{}'); } catch { return {}; } }
  function savePreference() { try { localStorage.setItem('chat-agent-composer',JSON.stringify({model,thinking:level})); } catch {} }
  function selectedModel() { return options?.models.find(item => item.id === model); }
  function valid(stamp) { if (stamp !== epoch || !state.token) throw abort(); }
  function syncScope() {
    if (boundRole !== state.roleID || boundSession !== state.current) clearDrafts();
    boundRole = state.roleID; boundSession = state.current;
    const scope = `${state.roleID}:${state.current || ''}`;
    if (scope !== historyScope) { clearCache(); historyScope = scope; }
  }
  function lock() {
    const locked = !state.token || state.loading || state.compacting || state.submitting || state.submitUnknown || selecting || state.imageBusy;
    picker.disabled = locked || !options;
    thinking.disabled = locked || !options;
    add.disabled = locked || oldNative || !selectedModel()?.vision || drafts.length >= 3;
    add.title=oldNative?'请通过顶部“更新”升级到 0.19 后上传图片':'添加 JPEG 或 PNG 图片';
    files.disabled = add.disabled;
    strip.querySelectorAll('button').forEach(button => { button.disabled = locked; });
    if (state.imageBusy) hint.textContent = '正在上传图片…';
    else if (selecting) hint.textContent = '正在处理图片…';
    else if (drafts.length) hint.textContent = `${drafts.length}/3 张图片 · 点击发送后上传并交给所选模型理解`;
    else hint.textContent = oldNative && state.token?'图片上传需 Android 0.19，请通过顶部“更新”升级客户端。':'';
    hint.hidden = !hint.textContent;
  }
  function renderDrafts() {
    strip.replaceChildren(); strip.hidden = !drafts.length;
    for (const draft of drafts) {
      const tile = document.createElement('div'); tile.className = 'image-draft';
      const image = document.createElement('img'); image.src = draft.url; image.alt = '待发送图片';
      const button = document.createElement('button'); button.type = 'button'; button.textContent = '×'; button.setAttribute('aria-label','移除这张图片');
      button.addEventListener('click',() => removeDraft(draft));
      tile.append(image,button); strip.append(tile);
    }
    state.draftImageCount = drafts.length;
    lock(); onChange();
  }
  function cleanupUploaded(draft) {
    if (!draft.remote || !draft.session || !state.token) return;
    json(`/api/sessions/${encodeURIComponent(draft.session)}/images/${encodeURIComponent(draft.remote.id)}`,{method:'DELETE'}).catch(error => {
      if (error.name !== 'AbortError' && error.status !== 404 && error.status !== 409) notice('临时图片暂未清理，可随这条会话一起删除。');
    });
  }
  function removeDraft(draft) {
    if (state.submitting || state.submitUnknown || state.imageBusy) return;
    drafts = drafts.filter(item => item !== draft);
    URL.revokeObjectURL(draft.url); cleanupUploaded(draft); renderDrafts();
  }
  function clearDrafts(discardUploaded = true) {
    epoch++; uploads?.abort(); uploads = null; selecting = false; state.imageBusy = false;
    for (const draft of drafts) { URL.revokeObjectURL(draft.url); if (discardUploaded) cleanupUploaded(draft); }
    drafts = []; files.value = ''; state.draftImageCount = 0; renderDrafts();
  }
  async function choose(selected) {
    if (add.disabled || selecting || !selected?.length) return;
    const available = 3-drafts.length;
    if (selected.length > available) notice('每条消息最多添加 3 张图片。');
    const stamp = epoch; selecting = true; state.imageBusy = true; lock(); onChange();
    try {
      for (const file of Array.from(selected).slice(0,available)) {
        const blob = await prepareImage(file); valid(stamp);
        drafts.push({blob,url:URL.createObjectURL(blob),remote:null,session:null});
      }
      valid(stamp); renderDrafts();
    } catch (error) { if (error.name !== 'AbortError') notice(error.message || '图片暂时无法读取，请重试。'); }
    finally { if (stamp === epoch) { selecting = false; state.imageBusy = false; files.value = ''; renderDrafts(); } }
  }
  function renderOptions() {
    picker.replaceChildren();
    for (const item of options.models) { const option = document.createElement('option'); option.value=item.id; option.textContent=item.label || item.id; picker.append(option); }
    if (!selectedModel()) model = options.default_model;
    if (!selectedModel()) model = options.models[0]?.id || '';
    picker.value = model;
    const available = selectedModel()?.thinking || ['off'];
    if (!available.includes(level)) level = 'off';
    thinking.replaceChildren();
    for (const item of ['off','medium','high','ultra'].filter(item => available.includes(item))) {
      const option=document.createElement('option'); option.value=item; option.textContent=LEVELS[item]; thinking.append(option);
    }
    thinking.value = level; $('model-label').textContent = selectedModel()?.label || model || 'chat-agent';
    savePreference(); lock();
  }
  async function refreshOptions() {
    if (!state.token || loading) return;
    const stamp = epoch; loading = true;
    try {
      const value = await json('/api/chat/options'); valid(stamp);
      if (!Array.isArray(value.models) || !value.models.length || value.models.length > 64) throw new Error('暂无可用的对话模型。');
      options = {default_model:value.default_model,models:value.models.filter(item => typeof item.id === 'string' && Array.isArray(item.thinking))};
      const saved = readPreference(); model = saved.model || options.default_model; level = saved.thinking || 'off';
      renderOptions();
    } catch (error) { if (error.name !== 'AbortError') notice(error.message || '模型列表暂时不可用，请刷新连接。'); }
    finally { loading = false; lock(); onChange(); }
  }
  async function prepare() {
    if (!options || !selectedModel()) throw new Error('模型列表尚未就绪，请稍后重试。');
    if (drafts.length && !selectedModel().vision) throw new Error('这个模型不支持图片，请更换模型或移除图片。');
    const stamp = epoch, selection = {model,thinking:level,image_ids:[]};
    const previous = state.current, role = state.roleID;
    state.imageBusy = true; uploads = new AbortController(); lock(); onChange();
    try {
      if (drafts.length && !state.current) {
        const item = await createSession(); valid(stamp);
        // Creating a first session binds these drafts instead of clearing them.
        if (previous || state.roleID !== role || state.current !== item.id) throw abort();
        boundSession = item.id; boundRole = role;
      }
      const sid = state.current;
      for (const draft of drafts) {
        valid(stamp);
        if (state.current !== sid || state.roleID !== role) throw abort();
        if (!draft.remote) {
          const result = await json(`/api/sessions/${encodeURIComponent(sid)}/images`,{method:'POST',body:draft.blob,headers:{'Content-Type':draft.blob.type},signal:uploads.signal});
          valid(stamp);
          if (!SAFE_ID.test(result.id)) throw new Error('图片保存结果无效，请重试。');
          draft.remote=result; draft.session=sid;
        }
        if (draft.session !== sid) throw new Error('图片所属会话已变化，请重新添加。');
        selection.image_ids.push(draft.remote.id);
      }
      valid(stamp); return selection;
    } finally { if (stamp === epoch) { state.imageBusy=false; uploads=null; lock(); onChange(); } }
  }
  function accepted(payload) {
    if (payload.session !== state.current) return;
    const ids = payload.image_ids || [];
    if (drafts.length && drafts.every(draft => ids.includes(draft.remote?.id))) clearDrafts(false);
  }
  async function restoreUploaded(selection = {}) {
    const ids=selection.image_ids || [];
    if (drafts.length && JSON.stringify(drafts.map(draft=>draft.remote?.id))!==JSON.stringify(ids)) throw new Error('输入区还有其他图片，请先移除，再恢复这条消息。');
    if (options?.models.some(item=>item.id===selection.model)) { model=selection.model; level=selection.thinking || 'off'; renderOptions(); }
    if (!ids.length || drafts.length) return;
    const stamp=epoch, session=state.current;
    const recovered=[];
    state.imageBusy=true; uploads=new AbortController(); lock(); onChange();
    try {
      for (const id of ids) {
        if (!SAFE_ID.test(id)) throw new Error('图片标识无效，请重新添加。');
        const response=await request(`/api/sessions/${encodeURIComponent(session)}/images/${encodeURIComponent(id)}`,{signal:uploads.signal});
        const blob=await response.blob(); valid(stamp);
        if (session!==state.current || blob.size>4*1024*1024 || !['image/jpeg','image/png'].includes(blob.type)) throw new Error('图片暂时无法恢复，请重新添加。');
        recovered.push({blob,url:URL.createObjectURL(blob),remote:{id},session});
      }
      valid(stamp); drafts=recovered;
    } catch(error) { for(const draft of recovered)URL.revokeObjectURL(draft.url); throw error; }
    finally { if (stamp===epoch) { state.imageBusy=false; uploads=null; renderDrafts(); } }
  }
  function showImage(url) { large.src=url; if (!preview.open) preview.showModal(); }
  function clearCache() {
    observer?.disconnect(); preview.close(); large.removeAttribute('src');
    for (const item of cache.values()) { item.controller.abort(); if (item.url) URL.revokeObjectURL(item.url); }
    cache.clear();
  }
  async function loadHistoryImage(tile,{session,id,stamp}) {
    if (!tile.isConnected || stamp !== epoch || session !== state.current) return;
    const key = `${session}:${id}`;
    let item = cache.get(key);
    if (item) { cache.delete(key); cache.set(key,item); }
    else {
      // Drafts also own up to three URLs, so history keeps at most 29.
      while (cache.size >= 29) {
        const first = cache.keys().next().value, old = cache.get(first);
        old.controller.abort(); if (old.url) URL.revokeObjectURL(old.url); cache.delete(first);
      }
      item={controller:new AbortController(),url:null,promise:null}; cache.set(key,item);
      item.promise=(async()=>{
        const response=await request(`/api/sessions/${encodeURIComponent(session)}/images/${encodeURIComponent(id)}`,{signal:item.controller.signal});
        const blob=await response.blob();
        if (blob.size>4*1024*1024 || !['image/png','image/jpeg'].includes(blob.type)) throw new Error('图片内容不可用');
        if (stamp!==epoch || item.controller.signal.aborted || session!==state.current) throw abort();
        item.url=URL.createObjectURL(blob); return item.url;
      })();
    }
    tile.textContent='正在读取图片…';
    try {
      const url=await item.promise;
      if (!tile.isConnected || stamp!==epoch || session!==state.current) return;
      const image=document.createElement('img'); image.alt='对话中的图片'; image.loading='lazy'; image.decoding='async'; image.src=url;
      image.addEventListener('load',()=>onImageLoad?.(),{once:true});
      tile.replaceChildren(image); tile.setAttribute('aria-label','放大查看图片');
      tile.onclick=()=>{
        const saved=cache.get(key)?.url;
        if(saved)showImage(saved);
        else loadHistoryImage(tile,{session,id,stamp}).then(()=>{const current=cache.get(key)?.url;if(current && tile.isConnected)showImage(current);});
      };
    } catch (error) {
      if (cache.get(key)===item && !item.url) cache.delete(key);
      if (!tile.isConnected || stamp!==epoch) return;
      tile.textContent='图片暂不可用 · 点击重试'; tile.onclick=()=>loadHistoryImage(tile,{session,id,stamp});
    }
  }
  function renderImages(host,images=[]) {
    if (!images.length || !state.current) return;
    const group=document.createElement('div'); group.className='message-images';
    for (const metadata of images.slice(0,3)) {
      if (!SAFE_ID.test(metadata.id)) continue;
      const button=document.createElement('button'); button.type='button'; button.className='message-image'; button.textContent='点击查看图片';
      const details={session:state.current,id:metadata.id,stamp:epoch}; pendingImages.set(button,details);
      button.onclick=()=>loadHistoryImage(button,details); group.append(button);
      if (observer) observer.observe(button);
    }
    host.append(group);
  }
  files.addEventListener('change',()=>choose(files.files)); add.addEventListener('click',()=>files.click());
  $('composer').addEventListener('paste',event=>{
    const selected=Array.from(event.clipboardData?.files || []);
    if (selected.length) { event.preventDefault(); choose(selected); }
  });
  const composer=$('composer');
  composer.addEventListener('dragover',event=>{ if (!add.disabled && event.dataTransfer?.types.includes('Files')) { event.preventDefault(); composer.classList.add('image-dragover'); } });
  composer.addEventListener('dragleave',event=>{ if (!composer.contains(event.relatedTarget)) composer.classList.remove('image-dragover'); });
  composer.addEventListener('drop',event=>{ composer.classList.remove('image-dragover'); if (event.dataTransfer?.files.length) { event.preventDefault(); choose(event.dataTransfer.files); } });
  picker.addEventListener('change',()=>{ model=picker.value; renderOptions(); onChange(); });
  thinking.addEventListener('change',()=>{ level=thinking.value; savePreference(); onChange(); });
  $('image-viewer-close').addEventListener('click',()=>preview.close());
  preview.addEventListener('close',()=>large.removeAttribute('src'));
  preview.addEventListener('click',event=>{if(event.target===preview)preview.close();});
  return {refreshOptions,lock,prepare,accepted,restoreUploaded,renderImages,syncScope,beforeHistoryRender:()=>observer?.disconnect(),ready:()=>Boolean(options && selectedModel()),hasImages:()=>Boolean(drafts.length),selection:()=>({model,thinking:level,image_ids:[]}),
    clear(){ clearDrafts(false); clearCache(); options=null; model=''; level='off'; boundRole=state.roleID; boundSession=null; picker.replaceChildren(); thinking.replaceChildren(); lock(); },
    discard(){ clearDrafts(); clearCache(); boundRole=state.roleID; boundSession=state.current; },
    hasUnsentImages:()=>Boolean(drafts.length)};
}
