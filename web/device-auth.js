// The signing key authenticates. System information only labels the credential.
const SESSION_MARGIN = 60000;
const toBase64 = bytes => btoa(String.fromCharCode(...new Uint8Array(bytes)));
const fromBase64 = value => Uint8Array.from(atob(value), c => c.charCodeAt(0));
const isDigest = value => typeof value === 'string' && /^[0-9a-f]{64}$/.test(value);

function browserName() {
  const ua = navigator.userAgent;
  const hint = navigator.userAgentData?.platform ||
    [/Android/i, /iPhone|iPad/i, /Windows/i, /Macintosh/i, /Linux/i].find(pattern => pattern.test(ua))?.source;
  const platform = hint === 'iPhone|iPad' ? 'iOS' : hint === 'Macintosh' ? 'macOS' : hint;
  return platform ? `${String(platform).slice(0, 32)} 浏览器` : '浏览器设备';
}

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
  let identityPromise=null, pair=null, cached=null, flight=null, epoch=0, controller=null;
  let maintenance=false, paused=false, retryTimer=0;
  const launch = new URLSearchParams(location.hash.slice(1));
  let claimCode = launch.get('claim') || '';
  if (launch.has('claim')) {
    launch.delete('claim');
    const remainder = launch.toString();
    history.replaceState(null, '', location.pathname + location.search + (remainder ? '#' + remainder : ''));
  }
  if (!/^[0-9a-f]{64}$/.test(claimCode)) claimCode = '';
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
      let identity;
      if(native){
        if(!window.ChatAndroidDevice)await new Promise(resolve=>{const ready=()=>{clearTimeout(timer);window.removeEventListener('chat-native-ready',ready);resolve();};const timer=setTimeout(ready,5000);window.addEventListener('chat-native-ready',ready);});
        if(!window.ChatAndroidDevice)throw new Error('请更新 Android 客户端以启用设备鉴权');
        const bridge=window.ChatAndroidDevice;
        let serverID='';
        if (bridge.candidates) {
          try {
            const discovered=await publicJSON('/api/device/resolve', {candidates:[]});
            if (!isDigest(discovered.server_id)) throw new Error('服务器设备身份无效');
            serverID=discovered.server_id;
          } catch (error) { if (error.status!==404) throw error; }
        }
        if (serverID) {
          const candidates=await bridge.candidates(serverID);
          const found=candidates.length ? await publicJSON('/api/device/resolve', {candidates}) : {server_id:serverID,device_id:''};
          if (found.server_id!==serverID || (found.device_id && !candidates.includes(found.device_id))) throw new Error('服务器设备身份发生变化，请重新连接');
          identity={...await bridge.identity(serverID,found.device_id),server_id:serverID};
        } else identity=await bridge.identity();
      }else{
        if(!window.isSecureContext||!crypto.subtle||!window.indexedDB)throw new Error('设备注册需要 HTTPS 或本机安全连接');
        pair=await browserKey();
        const spki=await crypto.subtle.exportKey('spki',pair.publicKey),digest=await crypto.subtle.digest('SHA-256',spki);
        identity={device_id:Array.from(new Uint8Array(digest),v=>v.toString(16).padStart(2,'0')).join(''),public_key:toBase64(spki),name:browserName()};
      }
      if(!/^[0-9a-f]{64}$/.test(identity.device_id)||!identity.public_key)throw new Error('设备身份不可用');
      if($('device-fingerprint'))$('device-fingerprint').textContent=identity.device_id.match(/.{1,8}/g).join(' ');
      if($('device-name')&&!$('device-name').value)$('device-name').value=identity.name||'我的设备';
      return identity;
    })().catch(error=>{identityPromise=null;throw error;});
    return identityPromise;
  }
  async function issue(force=false, enroll=false) {
    if(!force&&cached&&Date.parse(cached.expires_at)>Date.now()+SESSION_MARGIN)return cached.token;
    if(flight)return flight;
    const stamp=epoch;controller=new AbortController();const signal=controller.signal;
    flight=(async()=>{
      const id=await getIdentity();valid(stamp);
      const registrationInput={public_key:id.public_key,name:($('device-name')?.value||id.name||'我的设备').trim()};
      let registeredServer='';
      if(claimCode){
        try{registeredServer=(await publicJSON('/api/device/register',{...registrationInput,claim_code:claimCode},signal)).server_id;}
        catch(error){if(error.code!=='instance_already_claimed')throw error;}
        claimCode='';
      }
      const requestChallenge=()=>publicJSON('/api/device/challenge',{device_id:id.device_id},signal);
      let challenge;
      try { challenge=await requestChallenge(); }
      catch (error) {
        if (error.code!=='device_not_found' || !enroll) throw error;
        valid(stamp);
        const registration=await publicJSON('/api/device/register',registrationInput,signal);
        registeredServer=registration.server_id;
        if (registration.device.status==='pending') {
          error.code='device_pending'; error.message='设备已登记，等待管理员授权。'; throw error;
        }
        challenge=await requestChallenge();
      }
      say('正在确认设备身份…');
      valid(stamp);
      if(!isDigest(challenge.server_id) || (registeredServer && challenge.server_id!==registeredServer) || (id.server_id && challenge.server_id!==id.server_id))throw new Error('服务器身份发生变化，请重新连接');
      const payload=fromBase64(challenge.payload),lines=new TextDecoder('utf-8',{fatal:true}).decode(payload).split('\n');
      if(lines.length!==6||lines[0]!=='chat-agent-device-v1'||lines[1]!==challenge.server_id||lines[2]!==id.device_id||lines[3]!==challenge.challenge_id||lines[5]!==challenge.expires_at)throw new Error('服务器身份请求格式无效');
      const signature=native?(await window.ChatAndroidDevice.sign(challenge.payload)).signature:toBase64(derSignature(await crypto.subtle.sign({name:'ECDSA',hash:'SHA-256'},pair.privateKey,payload)));
      valid(stamp);
      const result=await publicJSON('/api/device/verify',{device_id:id.device_id,challenge_id:challenge.challenge_id,signature},signal);valid(stamp);
      if(!result.token?.startsWith('dvc1_')||result.server_id!==challenge.server_id||result.device?.id!==id.device_id)throw new Error('设备会话无效');
      if(id.server_id)await window.ChatAndroidDevice.remember(id.server_id,id.device_id);
      valid(stamp);
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
      say('正在检查设备授权…');const token=await issue(false,!automatic);valid(stamp);await onToken(token);
    }catch(error){if(stamp===epoch){
      if(error.code==='device_not_found'){say('此入口尚未登记。点击「登记此入口」后，再在信任设备中核对指纹。');$('device-connect').textContent='登记此入口';}
      else {showError(error);if(error.code==='device_pending')retryPending();}
    }}
    finally{if(stamp===epoch)$('device-connect').disabled=false;}
  }
  function cancel(){epoch++;stopRetry();controller?.abort();controller=null;flight=null;cached=null;if($('device-connect'))$('device-connect').disabled=false;}
  function logout(token) {
    cancel();setPaused(true);
    if(token?.startsWith('dvc1_'))fetch('/api/device/logout',{method:'POST',headers:{Authorization:`Bearer ${token}`},cache:'no-store',keepalive:true}).catch(()=>{});
    say('此设备已退出。点击连接即可重新进入。');
    if($('device-connect'))$('device-connect').disabled=false;
  }
  const labelFor = device => device.name || '未命名设备';
  function deviceGroups(devices) {
    const grouped=new Map();
    for (const device of devices) {
      const root=device.group_id || device.id;
      if(!grouped.has(root)) grouped.set(root,[]);
      grouped.get(root).push(device);
    }
    return Array.from(grouped,([id,keys])=>{
      const display=keys.find(key=>key.id===id) || keys[0];
      const active=keys.find(key=>key.status==='approved');
      return {id,keys,display,action:active||display,status:active?'approved':display.status,current:keys.some(key=>key.id===state.config.device_id)};
    }).sort((a,b)=>Number(b.current)-Number(a.current)||labelFor(a.display).localeCompare(labelFor(b.display),'zh-CN'));
  }
  const element = (tag,text='') => { const node=document.createElement(tag); node.textContent=text; return node; };
  function managementButton(text, work) {
    const button=element('button',text); button.type='button';
    button.addEventListener('click',async()=>{
      button.disabled=true;
      try { await work(); } catch (error) { onError(error); } finally { button.disabled=false; }
    });
    return button;
  }
  function deviceCard(group,trusted) {
    const row=element('div'); row.className='device-card';
    const key=group.action, fingerprint=group.display.fingerprint || group.id;
    const title=element('strong',labelFor(group.display));
    const info=element('p',`${group.keys.length} 个入口 · ${fingerprint.slice(-12)}${group.current?' · 当前设备':''}`);
    const details=element('details'); details.append(element('summary','查看完整指纹'));
    for(const item of group.keys) {
      details.append(element('code',`${item.fingerprint||item.id}${item.status==='revoked'?'（已撤销）':''}`));
    }
    const revoke=group.status==='approved';
    const update=managementButton(revoke?'撤销信任':'信任此入口',async()=>{
      const question=revoke?`撤销「${labelFor(group.display)}」的全部 ${group.keys.length} 个入口？`:`确认已核对「${labelFor(key)}」的完整指纹？`;
      if(!confirm(question))return;
      await adminRequest(`/api/devices/${encodeURIComponent(key.id)}`,{method:'PATCH',body:JSON.stringify({revision:key.revision,status:revoke?'revoked':'approved'})});
      await refreshManagement();
    });
    update.dataset.action=revoke?'revoke':'approve';
    const targets=trusted.filter(item=>item.id!==group.id);
    if(revoke && targets.length) {
      const select=element('select'); select.setAttribute('aria-label','同一台设备的另一项记录');
      select.append(new Option('选择同一台设备',''));
      for(const target of targets)select.append(new Option(`${labelFor(target.display)} · ${target.id.slice(-12)}`,target.id));
      const merge=managementButton('合并设备入口',async()=>{
        const target=targets.find(item=>item.id===select.value);
        if(!target || !confirm(`把这些入口合并到「${labelFor(target.display)}」？合并后可一起撤销。`))return;
        await adminRequest(`/api/devices/${encodeURIComponent(key.id)}/merge`,{method:'POST',body:JSON.stringify({into:target.action.id,revision:key.revision,target_revision:target.action.revision})});
        await refreshManagement();
      });
      details.append(select,merge);
    }
    row.append(title,info,details,update);
    return row;
  }
  function devicePage(groups,trusted,searchable=false) {
    const content=element('div'); content.className='device-page';
    const cards=element('div'); cards.className='device-list';
    const paging=element('div'); paging.className='device-page-nav';
    const counter=element('span'); counter.setAttribute('role','status');
    let page=0,results=groups;
    const show=()=>{
      const pages=Math.max(1,Math.ceil(results.length/6));
      cards.replaceChildren(...results.slice(page*6,page*6+6).map(group=>deviceCard(group,trusted)));
      counter.textContent=results.length?`${page+1} / ${pages}`:'没有匹配的设备';
      previous.hidden=next.hidden=pages===1;
      previous.disabled=page===0; next.disabled=page+1>=pages;
      paging.hidden=results.length>0 && pages===1;
    };
    const previous=element('button','上一页'), next=element('button','下一页');
    previous.type=next.type='button';
    previous.addEventListener('click',()=>{page--;show();});
    next.addEventListener('click',()=>{page++;show();});
    paging.append(previous,counter,next);
    if(searchable && groups.length>6) {
      const search=element('input'); search.type='search'; search.placeholder='搜索名称或指纹'; search.setAttribute('aria-label',search.placeholder);
      search.addEventListener('input',()=>{
        const query=search.value.toLowerCase().replace(/\s/g,'');
        results=groups.filter(group=>group.keys.some(key=>key.id.includes(query)||labelFor(key).toLowerCase().replace(/\s/g,'').includes(query)));
        page=0;show();
      });
      content.append(search);
    }
    content.append(cards,paging); show(); return content;
  }
  async function refreshManagement() {
    const area=$('device-management');if(!area)return;
    area.hidden=!state.config?.device_admin;
    if(area.hidden){$('device-list').replaceChildren();return;}
    const auth=state.token;
    try{
      const result=await adminRequest('/api/devices');if(auth!==state.token)return;
      const account=state.config.account;
      $('device-management-title').textContent='信任设备';
      area.querySelector(':scope > p').textContent=`${account?.name || '当前实例'}的信任设备权限等效，共享会话、模型和工作区。`;
      const groups=deviceGroups(result.devices), trusted=groups.filter(group=>group.status==='approved');
      const list=$('device-list'); list.replaceChildren();
      if(trusted.length) list.append(devicePage(trusted,trusted));
      for(const [status,title] of [['pending','待信任的入口'],['revoked','已撤销的入口']]) {
        const rows=groups.filter(group=>group.status===status);
        if(!rows.length)continue;
        const details=element('details');details.append(element('summary',`${title}（${rows.length}）`),devicePage(rows,trusted,true)); list.append(details);
      }
      $('device-management-status').textContent=trusted.length?`${trusted.length} 台信任设备`:'还没有信任设备';
    }catch(error){if(auth===state.token)$('device-management-status').textContent=error.message;}
  }
  $('device-connect')?.addEventListener('click',()=>{setPaused(false);login();});
  $('device-admin-toggle')?.addEventListener('click',()=>{maintenance=!maintenance;$('device-admin-toggle').setAttribute('aria-expanded',String(maintenance));$('legacy-login-fields').hidden=!maintenance;$('device-auth-panel').hidden=maintenance;$('device-admin-toggle').textContent=maintenance?'返回设备连接':'管理员维护入口';});
  $('device-refresh')?.addEventListener('click',refreshManagement);
  return {start:()=>login(true),connect:()=>{setPaused(false);return login();},refresh:()=>issue(true),logout,cancel,showError,refreshManagement,isDevice:token=>token?.startsWith('dvc1_')};
}
