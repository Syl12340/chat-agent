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
    note.hidden=!fromEnv&&!automatic;
    note.textContent=automatic ? '可直接在对话中读取工作区。修改文件和执行命令时需要确认。' : fromEnv ? '这个会话由服务器环境配置固定授权，页面上不能关闭。' : '';
    const text=failure || (pending ? '正在保存…'
      : automatic ? `已启用：当前会话直接使用${value?.scope_label||'本机'}工作区。`
      : fromEnv ? '由服务器配置授权。'
      : granted ? '已授权：当前会话可以查看服务器状态、日志和版本。'
      : '未授权：当前会话看不到服务器工具。');
    status.textContent=text; status.hidden=!text;
  };
  async function refresh(){
    section.hidden=!state.config?.device_admin;
    if(section.hidden){value=null;return;}
    try{
      value=await json('/api/host-tools');
      section.hidden=!value.configured;
      if(value.instance){title.textContent='服务器工作区';description.textContent=`${value.instance} 的私有工作区可在应用内的对话直接使用。`;label.lastChild.textContent=' 私有工作区已启用';}
      else if(value.auto_grant){title.textContent='本机工作区';description.textContent='本地 Agent loop 直接读取当前电脑工作区。文件写入仍需逐次批准；Windows 尚无强制 shell 沙箱，因此不会开放任意命令。';label.lastChild.textContent=' 本机工作区已直接启用';}
      else{title.textContent='服务器运维';description.textContent='开启后，这个会话可以查看服务器状态、日志和版本，也能在你的服务器沙箱里读写文件。维护动作仍然要在每次执行前单独批准，开关本身不等于执行许可。';label.lastChild.textContent=' 允许当前会话查看和管理服务器';}
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
