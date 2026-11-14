const $ = id => document.getElementById(id);

// One switch, one conversation: "let this conversation look at the server".
// It is owner-only, it is not a model tool, and granting it never authorises a
// maintenance action — that still needs a per-call approval ticket.
export function createHostTools({state,json,onError}) {
  const section=$('host-tools'), allow=$('host-tools-allow'), note=$('host-tools-note'), status=$('host-tools-status');
  let value=null, pending=false, pendingAllow=null, failure='';
  const render=()=>{
    const session=state.current, sessions=value?.sessions || [], env=value?.env_sessions || [];
    const fromEnv=Boolean(session && env.includes(session));
    // While a save is in flight the checkbox shows what was asked for, not the
    // last server state. Reading .checked after a render that used the old value
    // is how the first version always posted "allow: false".
    const granted=pending && pendingAllow!==null
      ? pendingAllow
      : Boolean(session && (sessions.includes(session) || fromEnv));
    allow.checked=granted;
    allow.disabled=pending || !session || fromEnv;
    note.hidden=!fromEnv;
    note.textContent=fromEnv ? '这个会话由服务器环境配置固定授权，页面上不能关闭。' : '';
    const text=failure || (pending ? '正在保存…'
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
