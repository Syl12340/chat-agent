// Composer settings only: no conversation text, tokens or model reasoning.
const LEVELS = new Set(['off', 'medium', 'high', 'ultra']);
const forbidden = new Set(['__proto__', 'constructor', 'prototype']);
const validModel = id => typeof id === 'string' && id.length > 0 && id.length <= 128 && !/[\u0000-\u001f\u007f-\u009f]/.test(id) && !forbidden.has(id);
const empty = () => ({version:2,current_model:'',levels:Object.create(null)});

export function composerIdentity(config, authKind = '') {
  const method=config?.auth_method || authKind;
  if (method==='owner') return 'owner';
  if (method==='device' && typeof config?.device_id==='string' && /^[A-Za-z0-9_-]{1,128}$/.test(config.device_id)) return `device:${config.device_id}`;
  return '';
}
export function validatePreference(value) {
  if (!value || typeof value!=='object' || Array.isArray(value) || value.version!==2 ||
      Object.keys(value).some(k=>!['version','current_model','levels'].includes(k)) ||
      !(value.current_model==='' || validModel(value.current_model)) || !value.levels || typeof value.levels!=='object' || Array.isArray(value.levels)) return null;
  const entries=Object.entries(value.levels);
  if(entries.length>64 || entries.some(([id,level])=>!validModel(id) || !LEVELS.has(level)))return null;
  return {version:2,current_model:value.current_model,levels:Object.assign(Object.create(null),Object.fromEntries(entries))};
}
export function createComposerPreferences({storage,origin='',scope='',initial,onSave}={}) {
  const key=scope ? `chat-agent-composer:v2:${JSON.stringify([origin,scope])}` : '';
  const read=k=>{try{return storage?.getItem(k) || '';}catch{return '';}};
  const write=(k,v)=>{try{storage?.setItem(k,v);return storage?.getItem(k)===v;}catch{return false;}};
  let saved=validatePreference(initial), migrated=false;
  if(!saved && key){try{saved=validatePreference(JSON.parse(read(key)));}catch{}}
  // The legacy unscoped pair belongs only to the first authenticated identity
  // claiming it. A failed marker write never permits repeated cross-user import.
  const marker='chat-agent-composer:v2-migration';
  if(key && !read(marker) && write(marker,JSON.stringify([origin,scope]))) {
    if(!saved)try {
      const old=JSON.parse(read('chat-agent-composer') || 'null');
      if(validModel(old?.model) && LEVELS.has(old?.thinking)) {
        saved=empty();saved.current_model=old.model;saved.levels[old.model]=old.thinking;migrated=true;
      }
    } catch {}
  }
  saved=saved || empty();
  const snapshot=()=>({version:2,current_model:saved.current_model,levels:{...saved.levels}});
  const persist=()=>{
    if(!key)return;
    const value=snapshot();write(key,JSON.stringify(value));
    try{Promise.resolve(onSave?.(value)).catch(()=>{});}catch{}
  };
  if(migrated)persist();
  function resolve(options, override={}) {
    const models=Array.isArray(options?.models) ? options.models : [];
    const wanted=override.model || saved.current_model || options?.default_model || '';
    const selected=models.find(m=>m.id===wanted) || models.find(m=>m.id===options?.default_model) || models[0];
    const id=selected?.id || '';
    const available=(selected?.thinking || []).filter(v=>LEVELS.has(v));
    const defaultLevel=available.includes(options?.default_thinking) ? options.default_thinking : available.includes('off') ? 'off' : available[0] || 'off';
    const requested=LEVELS.has(override.thinking) ? override.thinking : saved.levels[id] || defaultLevel;
    const thinking=available.includes(requested) ? requested : defaultLevel;
    return {model:id,thinking,fallback: id!==wanted && wanted ? 'model' : thinking!==requested ? 'thinking' : ''};
  }
  return {
    resolve,snapshot,
    rememberModel(id){if(validModel(id)){saved.current_model=id;persist();}},
    rememberThinking(id,level){if(validModel(id) && LEVELS.has(level)){
      if(!Object.hasOwn(saved.levels,id) && Object.keys(saved.levels).length>=64)delete saved.levels[Object.keys(saved.levels)[0]];
      saved.current_model=id;saved.levels[id]=level;persist();
    }},
  };
}
