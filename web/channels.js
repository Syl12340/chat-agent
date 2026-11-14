const $ = id => document.getElementById(id);

function ensurePanel(){
  let section=$('channel-settings');
  if(section)return section;
  section=document.createElement('section');section.id='channel-settings';section.className='channel-settings';section.hidden=true;section.setAttribute('aria-labelledby','channel-settings-title');
  section.innerHTML='<div class="device-management-heading"><h3 id="channel-settings-title">聊天渠道</h3><button id="channel-refresh" type="button" class="text-button">刷新</button></div><p>QQ 与应用共用同一个 Agent。私聊账号只需绑定一次，群聊不会读取私人记忆。</p><div id="channel-list" class="channel-list"></div><button id="channel-code" type="button" class="secondary-button">生成 QQ 绑定码</button><code id="channel-code-value" class="channel-code" hidden></code><label for="channel-source">把已有 IM 会话接到当前会话</label><select id="channel-source"><option value="">选择一个已有 IM 会话</option></select><button id="channel-link-current" type="button" class="secondary-button" disabled>关联到当前会话</button><p id="channel-status" role="status" hidden></p>';
  const anchor=$('host-tools');if(anchor)anchor.before(section);else $('settings-dialog')?.append(section);return section;
}

export function createChannelSettings({json,getSessions,getCurrent,onLinked,onError}) {
  const section=ensurePanel(), list=$('channel-list'), code=$('channel-code-value'), source=$('channel-source'), link=$('channel-link-current'), status=$('channel-status');
  const show=value=>{status.textContent=value||'';status.hidden=!value;};
  const mask=value=>value?.length>12?`${value.slice(0,6)}…${value.slice(-4)}`:(value||'');
  async function refresh(){
    if(!section)return;
    const data=await json('/api/channels');
    list.replaceChildren();
    for(const item of data.identities||[]){const row=document.createElement('div');row.className='channel-identity';const strong=document.createElement('strong'),small=document.createElement('small');strong.textContent=item.platform==='qq-official'?'QQ 官方机器人':item.platform;small.textContent=`已绑定 · ${mask(item.remote_user_id)}`;row.append(strong,small);list.append(row);}
    if(!list.childElementCount){const empty=document.createElement('p');empty.className='channel-empty';empty.textContent='还没有绑定 IM 账号。';list.append(empty);}
    source.replaceChildren(new Option('选择一个已有 IM 会话',''));
    for(const item of getSessions().filter(value=>value.channel && value.id!==getCurrent()))source.add(new Option(`${item.channel.platform==='qq-official'?'QQ':item.channel.platform} · ${item.title}`,item.id));
    link.disabled=!getCurrent()||!source.value;
    section.hidden=false;
  }
  $('channel-refresh')?.addEventListener('click',()=>refresh().catch(onError));
  $('channel-code')?.addEventListener('click',async()=>{try{const value=await json('/api/channels/link-codes',{method:'POST'});code.textContent=value.code;code.hidden=false;show(`10 分钟内在 QQ 私聊中发送：绑定 ${value.code}`);}catch(error){onError(error);}});
  source?.addEventListener('change',()=>{link.disabled=!getCurrent()||!source.value;});
  link?.addEventListener('click',async()=>{if(!getCurrent()||!source.value)return;try{await json(`/api/sessions/${encodeURIComponent(getCurrent())}/channel-link`,{method:'POST',body:JSON.stringify({channel_session_id:source.value})});show('后续 IM 消息会进入当前会话；旧会话记录保持不变。');await onLinked();await refresh();}catch(error){onError(error);}});
  return {refresh};
}
