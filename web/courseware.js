export function createCoursewareUI({state,json,drawer,onDraft,onError,roleName,fetchBytes}) {
  const $=id=>document.getElementById(id), dialog=$('courseware-dialog'), list=$('courseware-list'), message=$('courseware-message'), form=$('courseware-form'), file=$('courseware-file');
  let busy=false;
  function report(error){message.textContent=error?.message||'课件操作失败。';message.hidden=false;if(error?.status===401)onError(error);}
  function encode(buffer){const bytes=new Uint8Array(buffer);let out='';for(let i=0;i<bytes.length;i+=0x8000)out+=String.fromCharCode(...bytes.subarray(i,i+0x8000));return btoa(out);}
  function action(parent,label,run){const b=document.createElement('button');b.type='button';b.className='life-action';b.textContent=label;b.disabled=busy;b.addEventListener('click',run);parent.append(b);return b;}
  // Courseware bytes sit behind the API's authentication too, so a bare link comes
  // back 401. Both the export and the original page go through fetchBytes.
  async function saveBytes(path,name){const url=URL.createObjectURL(await fetchBytes(path));const a=document.createElement('a');a.href=url;a.download=name;document.body.append(a);a.click();a.remove();setTimeout(()=>URL.revokeObjectURL(url),60000);}
  async function previewBytes(trigger,path,fallbackName){
    // The window is claimed before awaiting so the popup is not blocked.
    const tab=window.open('','_blank');
    if(tab)tab.opener=null;
    trigger.disabled=true;
    try{const url=URL.createObjectURL(await fetchBytes(path));if(tab)tab.location=url;else await saveBytes(path,fallbackName||'courseware');}
    catch(error){tab?.close();throw error;}
    finally{trigger.disabled=false;}
  }
  // Extraction may be partial or impossible; saying which is the point of storing
  // the status at all. A reader who is told "text incomplete" can open the original
  // instead of being taught from half a lecture.
  function describe(doc){
    const status=doc.extraction_status||'complete';
    const size=`${Math.ceil(doc.bytes/1024)} KB`;
    const slide=doc.locator==='slide'?' · 可定位幻灯片':'';
    if(status==='failed')return `${size} · 未能提取文本 · 原件已保存${slide}`;
    if(status==='limited'){
      const why=(doc.extraction_warnings||[])[0]||'只提取到部分内容';
      return `${size} · 文本不完整 · ${why}${slide}`;
    }
    return `${size} · 已提取 ${Math.ceil(doc.text_bytes/1024)} KB 文本${slide}`;
  }
  async function sourcePreview(doc,card){let offset=0;let host=card.querySelector('.courseware-source');if(!host){host=document.createElement('section');host.className='courseware-source';card.append(host);}async function load(nextOffset){const data=await json(`/api/courseware/${encodeURIComponent(doc.id)}/source?role_id=${encodeURIComponent(state.roleID)}&offset=${Math.max(0,nextOffset)}&limit=4000`);offset=data.source_ref.start;const heading=document.createElement('div');heading.className='life-meta';const location=data.source_ref.start_page?`幻灯片 ${data.source_ref.start_page}${data.source_ref.end_page!==data.source_ref.start_page?`–${data.source_ref.end_page}`:''} · `:'';heading.textContent=`${location}原文行 ${data.source_ref.start_line}–${data.source_ref.end_line} · ref ${data.source_ref.sha256.slice(0,10)}`;const pre=document.createElement('pre');pre.textContent=data.excerpt;const nav=document.createElement('div');nav.className='life-actions';const previous=action(nav,'上一段',()=>load(Math.max(0,offset-4000)).catch(report));previous.disabled=offset===0;const nextButton=action(nav,'下一段',()=>load(data.source_ref.end).catch(report));nextButton.disabled=data.source_ref.end>=doc.text_bytes;const exportLink=document.createElement('button');exportLink.type='button';exportLink.className='life-action';exportLink.textContent='导出这段原文与引用';exportLink.addEventListener('click',()=>saveBytes(`/api/courseware/${encodeURIComponent(doc.id)}/export?role_id=${encodeURIComponent(state.roleID)}&offset=${data.source_ref.start}&limit=${Math.max(500,data.source_ref.end-data.source_ref.start)}`,`${doc.id}-source.md`).catch(report));nav.append(exportLink);host.replaceChildren(heading,pre,nav);}await load(offset);}
  async function refresh(){if(!state.token)return;try{const docs=await json(`/api/courseware?role_id=${encodeURIComponent(state.roleID)}`);list.replaceChildren();if(!docs.length){const p=document.createElement('p');p.className='memory-empty';p.textContent='还没有课件。上传后，可以从这里把分析、讲解或测试请求放进输入框。';list.append(p);}for(const doc of docs){const card=document.createElement('article');card.className='workbench-card';const h=document.createElement('h3');h.textContent=doc.name;const meta=document.createElement('p');meta.className='life-meta';meta.textContent=describe(doc);const actions=document.createElement('div');actions.className='life-actions';const draft=kind=>{const wording={analyse:'分析这份课件的知识结构、关键概念、前置依赖与容易混淆之处。使用 courseware_read 返回的 source_ref 标注依据，并把阶段总结保存成可更新的学习面板。',explain:'请根据这份课件讲解核心内容：先给直觉，再给严谨表述和一个例子；使用 source_ref 标注依据，并把进度与后续问题保存成学习面板。',quiz:'请根据这份课件给我一组由浅入深的自测题，使用可交互 quiz 卡片；我作答前不要泄露答案，讲评时标注 source_ref，并更新同一个学习面板而不是重复创建。'}[kind];onDraft(`${wording}\n\n课件：${doc.name}\n课件 ID：${doc.id}`);dialog.close();};const sourceButton=action(actions,'原文',()=>sourcePreview(doc,card).catch(report));if((doc.extraction_status||'complete')==='failed'){sourceButton.disabled=true;sourceButton.title='这份课件没有可读文本，请打开原件';}const page=document.createElement('button');page.type='button';page.className='life-action';page.textContent='看原页';page.title='打开上传的原件，对照提取结果';page.addEventListener('click',()=>previewBytes(page,`/api/courseware/${encodeURIComponent(doc.id)}/file?role_id=${encodeURIComponent(state.roleID)}`,doc.name).catch(report));actions.append(page);action(actions,'分析',()=>draft('analyse'));action(actions,'讲解',()=>draft('explain'));action(actions,'测试',()=>draft('quiz'));action(actions,'删除',async()=>{if(!confirm(`删除课件“${doc.name}”？`))return;try{await json(`/api/courseware/${encodeURIComponent(doc.id)}?role_id=${encodeURIComponent(state.roleID)}&revision=${doc.revision}`,{method:'DELETE'});await refresh();}catch(error){report(error);}});card.append(h,meta,actions);list.append(card);}}catch(error){report(error);}}
  form.addEventListener('submit',async event=>{event.preventDefault();if(busy||!file.files[0])return;const chosen=file.files[0];busy=true;message.textContent='正在本地提取课件文本…';message.hidden=false;try{const data=encode(await chosen.arrayBuffer());const doc=await json(`/api/courseware?role_id=${encodeURIComponent(state.roleID)}`,{method:'POST',body:JSON.stringify({name:chosen.name,media_type:chosen.type,data_base64:data})});form.reset();const status=doc?.extraction_status||'complete';message.textContent=status==='failed'?'原件已保存，但这份课件无法提取文本：'+((doc.extraction_warnings||[])[0]||'未知原因'):status==='limited'?'课件已上传，但文本不完整：'+((doc.extraction_warnings||[])[0]||''):'课件已上传并提取。';await refresh();}catch(error){report(error);}finally{busy=false;}});
  $('courseware-open').addEventListener('click',()=>{drawer(false);$('courseware-role').textContent=roleName();dialog.showModal();void refresh();});$('courseware-close').addEventListener('click',()=>dialog.close());$('courseware-refresh').addEventListener('click',refresh);
  return {refresh,update(){ $('courseware-open').disabled=!state.token||!state.config; }};
}
