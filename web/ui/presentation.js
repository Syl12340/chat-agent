// Presentation Protocol v1. All content is rendered with DOM text nodes. This
// module never interprets HTML, script, CSS or arbitrary URLs.
const protocol = 'ca.presentation';
const allowedTones = new Set(['', 'muted', 'info', 'success', 'warning', 'danger']);
const allowedEffects = new Set(['insert_draft', 'copy', 'open_https', 'toast', 'refresh', 'replace_view', 'patch_view']);
const budgets = {
  tool_card: {bytes:8192,nodes:40,depth:6,children:16},
  panel: {bytes:32768,nodes:120,depth:8,children:32},
  strip: {bytes:2048,nodes:8,depth:3,children:4},
};
const safeID = /^[A-Za-z0-9_.:-]{1,80}$/;
const appliedOperations = new Set();
const length = value => [...String(value ?? '')].length;
// Most node fields are optional on the wire.  Treat an omitted field as valid,
// but keep the stricter branch for node kinds that require actual text.
const text = (value, max, required = false) => value === undefined ? !required : typeof value === 'string' && length(value) <= max && (!required || value.trim());
const ownKeys = (value, allowed) => value && typeof value === 'object' && !Array.isArray(value) && Object.keys(value).every(key => allowed.includes(key) && !['__proto__','prototype','constructor'].includes(key));

function effectValid(effect, capabilities) {
  if (!ownKeys(effect,['type','text','url','view','patch','operation_id']) || !allowedEffects.has(effect.type) || !capabilities.effects.has(effect.type)) return false;
  if (effect.operation_id !== undefined && !safeID.test(effect.operation_id)) return false;
  if (['insert_draft','copy','toast'].includes(effect.type)) return text(effect.text,4000,true);
  if (effect.type === 'open_https') {
    try { const url=new URL(effect.url); return url.protocol==='https:' && !url.username && !url.password; } catch { return false; }
  }
  if (effect.type === 'refresh') return true;
  if (effect.type === 'replace_view') return Boolean(validatePresentation(effect.view,capabilities).view);
  if (effect.type === 'patch_view') {
    if (!ownKeys(effect.patch,['revision','root']) || !Number.isSafeInteger(effect.patch.revision) || effect.patch.revision<=0 || !effect.patch.root) return false;
    return nodeValid(effect.patch.root,1,budgets.panel,{nodes:0},capabilities);
  }
  return false;
}

function nodeValid(node, depth, budget, state, capabilities) {
  const fields=['type','id','text','label','value','unit','tone','progress','headers','rows','children','actions'];
  if (!ownKeys(node,fields) || !capabilities.components.has(node.type) || ++state.nodes>budget.nodes || depth>budget.depth) return false;
  if (node.id!==undefined && !safeID.test(node.id)) return false;
  if (!text(node.text,1000) || !text(node.label,120) || !text(node.value,240) || !text(node.unit,40) || !allowedTones.has(node.tone||'')) return false;
  const children=node.children||[]; if(!Array.isArray(children)||children.length>budget.children)return false;
  if (node.type==='stack'||node.type==='row') { if(!children.length)return false; }
  else if(children.length)return false;
  if(['text','callout','empty_state'].includes(node.type)&&!text(node.text,1000,true))return false;
  if(node.type==='metric'&&(!text(node.label,120,true)||!text(node.value,240,true)))return false;
  if(node.type==='progress'&&(typeof node.progress!=='number'||!Number.isFinite(node.progress)||node.progress<0||node.progress>1))return false;
  if(node.type==='table'){
    if(!Array.isArray(node.headers)||!node.headers.length||node.headers.length>8||node.headers.some(v=>!text(v,80,true))||!Array.isArray(node.rows)||node.rows.length>20)return false;
    if(node.rows.some(row=>!Array.isArray(row)||row.length!==node.headers.length||row.some(v=>!text(v,240))))return false;
  }
  if(node.type==='actions'){
    if(!Array.isArray(node.actions)||!node.actions.length||node.actions.length>4)return false;
    const ids=new Set(); for(const action of node.actions){
      if(!ownKeys(action,['id','label','expected_revision','effect'])||!safeID.test(action.id)||ids.has(action.id)||!text(action.label,60,true)||action.expected_revision!==undefined&&(!Number.isSafeInteger(action.expected_revision)||action.expected_revision<0)||!effectValid(action.effect,capabilities))return false;
      ids.add(action.id);
    }
  }
  return children.every(child=>nodeValid(child,depth+1,budget,state,capabilities));
}

export const presentationCapabilities = Object.freeze({
  protocol, version:1,
  components:['stack','row','text','metric','progress','table','actions','callout','empty_state'],
  effects:[...allowedEffects],
});

function normalizedCapabilities(value={}) {
  return {components:new Set(value.components||presentationCapabilities.components),effects:new Set(value.effects||presentationCapabilities.effects)};
}

export function validatePresentation(value, supported={}) {
  try {
    const caps=normalizedCapabilities(supported), budget=budgets[value?.surface];
    if(!ownKeys(value,['protocol','version','surface','id','revision','operation_id','min_app','root','fallback'])||value.protocol!==protocol||value.version!==1||!budget)return {view:null,error:'不支持的呈现协议'};
    if(value.id!==undefined&&!safeID.test(value.id)||value.operation_id!==undefined&&!safeID.test(value.operation_id)||value.revision!==undefined&&(!Number.isSafeInteger(value.revision)||value.revision<0)||value.min_app!==undefined&&!/^\d+\.\d+\.\d+(?:[-+][A-Za-z0-9.-]+)?$/.test(value.min_app)||!text(value.fallback,1000))return {view:null,error:'呈现元数据无效'};
    if(new TextEncoder().encode(JSON.stringify(value)).length>budget.bytes)return {view:null,error:'呈现内容过大'};
    if(!nodeValid(value.root,1,budget,{nodes:0},caps))return {view:null,error:'呈现结构不受支持'};
    return {view:value,error:''};
  } catch { return {view:null,error:'呈现内容无效'}; }
}

const element=(tag,cls,textValue)=>{const node=document.createElement(tag);if(cls)node.className=cls;if(textValue!==undefined)node.textContent=textValue;return node;};

function renderNode(definition, context) {
  let node;
  switch(definition.type){
    case 'stack': case 'row':
      node=element('div',`presentation-${definition.type}`); definition.children.forEach(child=>node.append(renderNode(child,context))); break;
    case 'text': node=element('p','presentation-text',definition.text); break;
    case 'callout': node=element('aside','presentation-callout',definition.text); break;
    case 'empty_state': node=element('div','presentation-empty',definition.text); break;
    case 'metric':
      node=element('div','presentation-metric'); node.append(element('span','presentation-label',definition.label),element('strong','presentation-value',definition.value),element('span','presentation-unit',definition.unit||'')); break;
    case 'progress':
      node=element('div','presentation-progress'); const label=element('span','presentation-label',definition.label||'进度'); const bar=element('progress'); bar.max=1;bar.value=definition.progress;node.append(label,bar); break;
    case 'table':
      node=element('div','presentation-table-wrap'); const table=element('table','presentation-table'),head=element('thead'),tr=element('tr');definition.headers.forEach(v=>tr.append(element('th','',v)));head.append(tr);const body=element('tbody');definition.rows.forEach(row=>{const line=element('tr');row.forEach(v=>line.append(element('td','',v)));body.append(line);});table.append(head,body);node.append(table);break;
    case 'actions':
      node=element('div','presentation-actions');definition.actions.forEach(action=>{const button=element('button','presentation-action',action.label);button.type='button';button.disabled=action.expected_revision!==undefined&&action.expected_revision!==context.view.revision;button.addEventListener('click',async()=>{if(button.disabled||!context.onEffect)return;button.disabled=true;try{await context.onEffect(action.effect,{action,view:context.view,host:context.host});}finally{if(button.isConnected)button.disabled=action.expected_revision!==undefined&&action.expected_revision!==context.view.revision;}});node.append(button);});break;
  }
  if(definition.tone)node.dataset.tone=definition.tone;if(definition.id)node.dataset.presentationNode=definition.id;return node;
}

export function renderPresentation(value,{onEffect,supported,host}={}){
  const checked=validatePresentation(value,supported);
  if(!checked.view){return text(value?.fallback,1000,true)?element('p','presentation-fallback',value.fallback):null;}
  const root=element('section','presentation-view');root.dataset.surface=value.surface;if(value.id)root.dataset.presentationId=value.id;if(value.revision!==undefined)root.dataset.revision=String(value.revision);
  root.append(renderNode(value.root,{onEffect,view:value,host}));return root;
}

export async function applyPresentationEffect(effect,{insertDraft,toast,refresh,replace,patch,confirmOpen=window.confirm}={}){
  if(!effectValid(effect,normalizedCapabilities()))throw new Error('不支持的呈现动作');
  if(effect.operation_id&&appliedOperations.has(effect.operation_id))return;
  if(effect.operation_id)appliedOperations.add(effect.operation_id);
  try{
    switch(effect.type){
      case 'insert_draft': insertDraft?.(effect.text); break;
      case 'copy': await navigator.clipboard.writeText(effect.text); toast?.('已复制'); break;
      case 'open_https': if(confirmOpen(`打开这个 HTTPS 链接？\n${effect.url}`)) window.open(effect.url,'_blank','noopener,noreferrer'); break;
      case 'toast': toast?.(effect.text); break;
      case 'refresh': await refresh?.(); break;
      case 'replace_view': replace?.(effect.view); break;
      case 'patch_view': patch?.(effect.patch); break;
    }
  }catch(error){if(effect.operation_id)appliedOperations.delete(effect.operation_id);throw error;}
  while(appliedOperations.size>128)appliedOperations.delete(appliedOperations.values().next().value);
}
