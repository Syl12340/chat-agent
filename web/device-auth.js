// Device identity is a signing key, not a hardware/browser tracking identifier.
const SESSION_MARGIN = 60000;
const toBase64 = bytes => btoa(String.fromCharCode(...new Uint8Array(bytes)));
const fromBase64 = value => Uint8Array.from(atob(value), c => c.charCodeAt(0));

async function browserKey() {
  const db = await new Promise((resolve,reject) => {
    const request=indexedDB.open('chat-agent-device',1);
    request.onupgradeneeded=()=>request.result.createObjectStore('identity');
    request.onsuccess=()=>resolve(request.result);request.onerror=()=>reject(new Error('无法打开本机设备存储'));
  });
  const read=()=>new Promise((resolve,reject)=>{const tx=db.transaction('identity');const q=tx.objectStore('identity').get('p256');q.onsuccess=()=>resolve(q.result);q.onerror=()=>reject(new Error('无法读取设备密钥'));});
  try {
    let pair=await read();
    if(!pair){
      const generated=await crypto.subtle.generateKey({name:'ECDSA',namedCurve:'P-256'},false,['sign','verify']);
      try {await new Promise((resolve,reject)=>{const tx=db.transaction('identity','readwrite');tx.objectStore('identity').add(generated,'p256');tx.oncomplete=resolve;tx.onabort=()=>reject(new Error('设备密钥已在另一个页面创建'));});}catch{}
      pair=await read();
    }
    if(!pair?.privateKey||!pair.publicKey)throw new Error('无法保存设备密钥，请检查浏览器存储权限');
    return pair;
  } finally {db.close();}
}

// WebCrypto emits IEEE-P1363 r||s; the Android/Go contract uses ASN.1 DER.
function derSignature(raw) {
  const integer=value=>{let a=Array.from(value);while(a.length>1&&a[0]===0)a.shift();if(a[0]&128)a.unshift(0);return [2,a.length,...a];};
  const bytes=new Uint8Array(raw);
  if(bytes.length!==64)throw new Error('浏览器签名格式不受支持');
  const r=integer(bytes.slice(0,32)),s=integer(bytes.slice(32));
  return new Uint8Array([48,r.length+s.length,...r,...s]);
}

export function createDeviceAuth({state, onToken, onError, adminRequest}) {
  const $=id=>document.getElementById(id);
  const native=/ChatAgentAndroid\//.test(navigator.userAgent);
  let identityPromise=null, identity=null, pair=null, cached=null, flight=null, epoch=0, controller=null;
  let maintenance=false, paused=false, retryTimer=0;
  try{paused=localStorage.getItem('chat-agent-device-paused')==='1';}catch{}
  const say=text=>{if($('device-auth-state'))$('device-auth-state').textContent=text;};
  const setPaused=value=>{paused=value;try{localStorage.setItem('chat-agent-device-paused',value?'1':'0');}catch{}};
  const stopRetry=()=>{clearTimeout(retryTimer);retryTimer=0;};
  const retryPending=()=>{
    if(paused||retryTimer)return;
    say('设备已登记，等待管理员授权；批准后会自动连接。');
    retryTimer=setTimeout(()=>{retryTimer=0;login(true);},30000);
  };
  const valid=stamp=>{if(stamp!==epoch)throw new DOMException('设备连接已取消','AbortError');};
  async function publicJSON(path,body,signal) {
    const response=await fetch(path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body),cache:'no-store',signal});
    const result=await response.json().catch(()=>({}));
    if(!response.ok){const e=new Error(result.error||`设备连接失败（${response.status}）`);e.code=result.code;e.status=response.status;throw e;}
    return result;
  }
  async function getIdentity() {
    if(identityPromise)return identityPromise;
    identityPromise=(async()=>{
      if(native){
        if(!window.ChatAndroidDevice)await new Promise(resolve=>{const ready=()=>{clearTimeout(timer);window.removeEventListener('chat-native-ready',ready);resolve();};const timer=setTimeout(ready,5000);window.addEventListener('chat-native-ready',ready);});
        if(!window.ChatAndroidDevice)throw new Error('请更新 Android 客户端以启用设备鉴权');
        identity=await window.ChatAndroidDevice.identity();
      }else{
        if(!window.isSecureContext||!crypto.subtle||!window.indexedDB)throw new Error('设备注册需要 HTTPS 或本机安全连接');
        pair=await browserKey();
        const spki=await crypto.subtle.exportKey('spki',pair.publicKey),digest=await crypto.subtle.digest('SHA-256',spki);
        identity={device_id:Array.from(new Uint8Array(digest),v=>v.toString(16).padStart(2,'0')).join(''),public_key:toBase64(spki),name:'浏览器设备'};
      }
      if(!/^[0-9a-f]{64}$/.test(identity.device_id)||!identity.public_key)throw new Error('设备身份不可用');
      if($('device-fingerprint'))$('device-fingerprint').textContent=identity.device_id.match(/.{1,8}/g).join(' ');
      if($('device-name')&&!$('device-name').value)$('device-name').value=identity.name||'我的设备';
      return identity;
    })().catch(error=>{identityPromise=null;throw error;});
    return identityPromise;
  }
  async function issue(force=false) {
    if(!force&&cached&&Date.parse(cached.expires_at)>Date.now()+SESSION_MARGIN)return cached.token;
    if(flight)return flight;
    const stamp=epoch;controller=new AbortController();const signal=controller.signal;
    flight=(async()=>{
      const id=await getIdentity();valid(stamp);
      const registration=await publicJSON('/api/device/register',{public_key:id.public_key,name:($('device-name')?.value||id.name||'我的设备').trim()},signal);valid(stamp);
      const device=registration.device;
      if(device.status!=='approved'){
        const error=new Error(device.status==='revoked'?'此设备的访问权限已撤销，请联系管理员。':'设备已登记，等待管理员授权。授权后点击连接即可。');
        error.code=device.status==='revoked'?'device_revoked':'device_pending';throw error;
      }
      say('正在确认设备身份…');
      const challenge=await publicJSON('/api/device/challenge',{device_id:id.device_id},signal);valid(stamp);
      if(challenge.server_id!==registration.server_id)throw new Error('服务器身份发生变化，请重新连接');
      const payload=fromBase64(challenge.payload),lines=new TextDecoder('utf-8',{fatal:true}).decode(payload).split('\n');
      if(lines.length!==6||lines[0]!=='chat-agent-device-v1'||lines[1]!==challenge.server_id||lines[2]!==id.device_id||lines[3]!==challenge.challenge_id||lines[5]!==challenge.expires_at)throw new Error('服务器身份请求格式无效');
      const signature=native?(await window.ChatAndroidDevice.sign(challenge.payload)).signature:toBase64(derSignature(await crypto.subtle.sign({name:'ECDSA',hash:'SHA-256'},pair.privateKey,payload)));
      valid(stamp);
      const result=await publicJSON('/api/device/verify',{device_id:id.device_id,challenge_id:challenge.challenge_id,signature},signal);valid(stamp);
      if(!result.token?.startsWith('dvc1_')||result.server_id!==challenge.server_id)throw new Error('设备会话无效');
      cached=result;setPaused(false);stopRetry();say('设备已授权，正在连接…');return result.token;
    })().finally(()=>{if(stamp===epoch){flight=null;controller=null;}});
    return flight;
  }
  function showError(error){if(error.name!=='AbortError')say(error.message||'设备连接失败，请稍后再试');}
  async function login(automatic=false) {
    const stamp=epoch;
    $('device-connect').disabled=true;
    try{
      const id=await getIdentity();valid(stamp);
      if(automatic&&(paused||id.suspended)){say('此设备已退出。点击连接即可重新进入。');return;}
      say('正在登记设备…');const token=await issue();valid(stamp);await onToken(token);
    }catch(error){if(stamp===epoch){showError(error);if(error.code==='device_pending')retryPending();}}
    finally{if(stamp===epoch)$('device-connect').disabled=false;}
  }
  function cancel(){epoch++;stopRetry();controller?.abort();controller=null;flight=null;cached=null;if($('device-connect'))$('device-connect').disabled=false;}
  function logout(token) {
    cancel();setPaused(true);
    if(token?.startsWith('dvc1_'))fetch('/api/device/logout',{method:'POST',headers:{Authorization:`Bearer ${token}`},cache:'no-store',keepalive:true}).catch(()=>{});
    say('此设备已退出。点击连接即可重新进入。');
    if($('device-connect'))$('device-connect').disabled=false;
  }
  async function refreshManagement() {
    const area=$('device-management');if(!area)return;
    area.hidden=!state.config?.device_admin;
    if(area.hidden){$('device-list').replaceChildren();return;}
    const auth=state.token;
    try{
      const result=await adminRequest('/api/devices');if(auth!==state.token)return;
      $('device-list').replaceChildren();$('device-management-status').textContent='新设备需授权后才能连接；撤销后不再接受新的请求。';
      for(const d of result.devices){
        const row=document.createElement('div');row.className='device-card';
        const title=document.createElement('strong');title.textContent=d.name||'未命名设备';
        const info=document.createElement('p');info.textContent=`${d.status==='approved'?'已授权':d.status==='revoked'?'已撤销':'等待授权'}${d.admin?' · 管理员':''}${d.id===state.config.device_id?' · 当前设备':''}`;
        const fingerprint=document.createElement('code');fingerprint.textContent=d.fingerprint||d.id;
        const button=document.createElement('button');button.type='button';button.className='secondary-button';button.textContent=d.status==='approved'?'撤销授权':'授权此设备';
        button.addEventListener('click',async()=>{
          const revoke=d.status==='approved';
          if(!confirm(`${revoke?'撤销':'允许'}设备“${d.name||d.id.slice(0,12)}”访问？`))return;
          button.disabled=true;
          try{await adminRequest(`/api/devices/${encodeURIComponent(d.id)}`,{method:'PATCH',body:JSON.stringify({revision:d.revision,status:revoke?'revoked':'approved'})});await refreshManagement();}catch(error){onError(error);}finally{button.disabled=false;}
        });
        row.append(title,info,fingerprint,button);$('device-list').append(row);
      }
    }catch(error){if(auth===state.token)$('device-management-status').textContent=error.message;}
  }
  $('device-connect')?.addEventListener('click',()=>{setPaused(false);login();});
  $('device-admin-toggle')?.addEventListener('click',()=>{maintenance=!maintenance;$('device-admin-toggle').setAttribute('aria-expanded',String(maintenance));$('legacy-login-fields').hidden=!maintenance;$('device-auth-panel').hidden=maintenance;$('device-admin-toggle').textContent=maintenance?'返回设备连接':'管理员维护入口';});
  $('device-refresh')?.addEventListener('click',refreshManagement);
  return {start:()=>login(true),connect:()=>{setPaused(false);return login();},refresh:()=>issue(true),logout,cancel,showError,refreshManagement,isDevice:token=>token?.startsWith('dvc1_')};
}
