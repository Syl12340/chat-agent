export function createLocalAgentUI({state,json,drawer,onError}) {
  const $=id=>document.getElementById(id), dialog=$('local-agent-dialog'), devices=$('local-agent-devices'), operations=$('local-agent-operations'), message=$('local-agent-message');
  const text=(node,value)=>{node.textContent=value;};
  const card=(title,meta)=>{const article=document.createElement('article');article.className='workbench-card';const h=document.createElement('h3');h.textContent=title;const p=document.createElement('p');p.className='life-meta';p.textContent=meta;article.append(h,p);return article;};
  async function refresh(){
    if(!state.token)return; message.hidden=true;
    try{
      const data=await json('/api/local-agent/status');devices.replaceChildren();operations.replaceChildren();
      if(!data.devices?.length)devices.append(card('尚未配置执行端','在服务器配置设备 ID 与专用令牌后，本机端会在这里出现。'));
      for(const item of data.devices||[]){devices.append(card(item.device_id,item.online?`在线 · ${item.capabilities?.join(' / ')||'能力未上报'}`:`离线${item.last_seen?` · 上次 ${new Date(item.last_seen).toLocaleString()}`:''}`));}
      if(!data.operations?.length)operations.append(card('暂无任务','模型调用本机工具后，操作标识与状态会显示在这里。'));
      for(const item of data.operations||[]){const node=card(`${item.kind} · ${item.target||item.operation_id}`,`${item.status} · ${item.operation_id}`);if(['queued','running'].includes(item.status)){const button=document.createElement('button');button.type='button';button.className='life-action';button.textContent='取消';button.addEventListener('click',async()=>{try{await json(`/api/local-agent/operations/${encodeURIComponent(item.operation_id)}/cancel`,{method:'POST'});await refresh();}catch(error){report(error);}});node.append(button);}operations.append(node);}
    }catch(error){report(error);}
  }
  function report(error){if(error?.status===401){onError(error);return;}text(message,error?.message||'无法读取本机工作台。');message.hidden=false;}
  $('local-agent-open').addEventListener('click',()=>{drawer(false);dialog.showModal();void refresh();});
  $('local-agent-close').addEventListener('click',()=>dialog.close());$('local-agent-refresh').addEventListener('click',refresh);
  return {refresh,update(){ $('local-agent-open').disabled=!state.token||!state.config; }};
}
