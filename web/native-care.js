// Device notifications only receive already-synced care; no background polling.
export function createNativeCare({state}) {
  const $ = id => document.getElementById(id);
  let generation = 0, busy = false, status = null;
  const bridge = () => window.PocketAndroidCare;
  const nativeShell = /PocketAgentAndroid\//.test(navigator.userAgent);
  function render() {
    const controls=$('native-care-controls'); if(!controls)return;
    controls.hidden=!nativeShell;
    $('native-care-enable').disabled=busy||!state.token||!bridge();
    $('native-care-disable').disabled=busy||!state.token||!bridge()||!status?.enabled;
    $('native-care-status').textContent=!bridge()?'升级到 0.11 Android 包后可开启手机通知。':!status?'正在读取本机通知设置…':!status.enabled?'本机通知未开启。':!status.permission?'本机已开启，系统通知权限或通知频道尚未允许。':'本机通知已开启；只通知手机同步后出现的新关怀。';
  }
  async function refresh() {
    render();if(!bridge()||!state.token)return;
    const stamp=generation,auth=state.token;
    try { const value=await bridge().status();if(stamp===generation&&auth===state.token){status=value;render();} } catch { if(stamp===generation){status=null;render();} }
  }
  async function configure(enabled) {
    if(!bridge()||busy||!state.token)return;
    const stamp=generation,auth=state.token;busy=true;render();
    try { const value=await bridge().configure(enabled);if(stamp===generation&&auth===state.token){status=value;render();setTimeout(()=>refresh(),800);} }
    catch { if(stamp===generation)$('native-care-status').textContent='本机通知设置暂时不可用，请稍后重试。'; }
    finally { if(stamp===generation){busy=false;render();} }
  }
  async function deliver(role,items) {
    if(!bridge()||!state.token||state.roleID!==role||document.visibilityState!=='visible')return;
    const auth=state.token,stamp=generation;
    try {
      const value=await bridge().status();
      if(stamp!==generation||auth!==state.token||state.roleID!==role||document.visibilityState!=='visible')return;
      status=value;render();if(!value.enabled||!value.permission)return;
      const ready=items.filter(x=>x.status==='ready').sort((a,b)=>Date.parse(a.attempted_at||a.created_at)-Date.parse(b.attempted_at||b.created_at)).slice(-20).map(x=>({id:x.id,title:x.title,content:x.content,attempted_at:x.attempted_at||x.created_at}));
      await bridge().deliver(role,ready);
    } catch { /* A notification failure never changes the server inbox. */ }
  }
  function clear() { generation++;busy=false;status=null;bridge()?.configure(false).catch(()=>{});render(); }
  $('native-care-enable')?.addEventListener('click',()=>configure(true));
  $('native-care-disable')?.addEventListener('click',()=>configure(false));
  window.addEventListener('pocket-native-ready',refresh);
  window.addEventListener('focus',refresh);
  document.addEventListener('visibilitychange',()=>{if(document.visibilityState==='visible')refresh();});
  render();return {refresh,deliver,clear};
}
