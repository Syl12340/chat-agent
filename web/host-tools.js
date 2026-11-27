const $ = id => document.getElementById(id);

// One switch, one conversation: "let this conversation look at the server".
// It is owner-only, it is not a model tool, and granting it never authorises a
// maintenance action — that still needs a per-call approval ticket.
export function createHostTools({state,json,onError}) {
  const section=$('host-tools'), allow=$('host-tools-allow'), note=$('host-tools-note'), status=$('host-tools-status'), title=$('host-tools-title'), description=section.querySelector(':scope > p'), label=allow.closest('label');
  let value=null, pending=false, pendingAllow=null, failure='';
  const render=()=>{
    const session=state.current, sessions=value?.sessions || [], env=value?.env_sessions || [];
    const automatic=Boolean(value?.auto_grant), fromEnv=Boolean(session && env.includes(session));
    // While a save is in flight the checkbox shows what was asked for, not the
    // last server state. Reading .checked after a render that used the old value
    // is how the first version always posted "allow: false".
    const granted=pending && pendingAllow!==null
      ? pendingAllow
      : Boolean(session && (automatic || sessions.includes(session) || fromEnv));
    allow.checked=granted;
    allow.disabled=pending || !session || fromEnv || automatic;
    const workspace = value?.workspace_available ? `${value.scope_label || '服务器'}工作区可在应用内私聊直接使用。${value.shell_available ? '支持沙箱 Shell，每条命令在对话中确认。' : '此实例当前未开放沙箱 Shell。'}` : '';
    note.textContent=[workspace, fromEnv && !automatic ? '当前会话的运维权限由服务器配置。' : ''].filter(Boolean).join(' ');
    note.hidden=!note.textContent;
    const text=failure || (pending ? '正在保存…'
      : automatic ? ''
      : fromEnv ? '由服务器配置授权。'
      : granted ? '当前会话已授权使用运维工具。'
      : '当前会话的运维工具已关闭。');
    status.textContent=text; status.hidden=!text;
  };
  async function refresh(){
    section.hidden=!state.config?.device_admin;
    if(section.hidden){value=null;return;}
    try{
      value=await json('/api/host-tools');
      section.hidden=!value.configured;
      const operations = value.tools?.some(tool => tool.startsWith('server_') || tool === 'workspace_exec');
      description.hidden = label.hidden = value.auto_grant || !operations;
      title.textContent = value.auto_grant ? `${value.scope_label || '服务器'}工作区` : '服务器工作区与运维';
      description.textContent = '可以为当前会话单独开启服务器运维和预设动作。维护动作仍需逐次批准。';
      label.lastChild.textContent = ' 允许当前会话使用运维工具';
      failure='';
      render();
    }catch(error){section.hidden=true;value=null;onError(error);}
  }
  allow.addEventListener('change',async()=>{
    const session=state.current;
    if(!session || pending || !value) return;
    const want=allow.checked;
    pending=true; pendingAllow=want; failure=''; render();
    try{
      value=await json('/api/host-tools/sessions',{method:'POST',body:JSON.stringify({session,allow:want})});
    }catch(error){
      failure=error.message; onError(error);
      try{value=await json('/api/host-tools');}catch{/* keep the last known state */}
    }finally{pending=false; pendingAllow=null; render();}
  });
  return {refresh,clear(){section.hidden=true;value=null;failure='';status.textContent='';}};
}
