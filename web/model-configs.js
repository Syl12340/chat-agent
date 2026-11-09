const $ = id => document.getElementById(id);

export function createModelConfigs({state,json,onChanged,onError}) {
  const section=$('model-configs'), form=$('model-config-form'), list=$('model-config-list'), status=$('model-config-status');
  let editing='';
  const message=text=>{status.textContent=text || '';status.hidden=!text;};
  const values=()=>({
    id:$('model-config-id').value.trim(),label:$('model-config-label').value.trim(),base_url:$('model-config-url').value.trim(),
    api_key:$('model-config-key').value.trim() || undefined,model:$('model-config-model').value.trim(),vision:$('model-config-vision').checked,
    thinking:['off',...($('model-config-thinking').checked?['medium','high','ultra']:[])]
  });
  const reset=()=>{editing='';form.reset();$('model-config-id').disabled=false;$('model-config-submit').textContent='添加模型配置';$('model-config-cancel').hidden=true;$('model-config-key-hint').textContent='密钥只写入服务器，不会回显到页面。';};
  const edit=item=>{
    editing=item.id;$('model-config-id').value=item.id;$('model-config-id').disabled=true;$('model-config-label').value=item.label;
    $('model-config-url').value=item.base_url;$('model-config-model').value=item.model;$('model-config-key').value='';$('model-config-vision').checked=Boolean(item.vision);
    $('model-config-thinking').checked=item.thinking?.some(level=>level!=='off');$('model-config-submit').textContent='保存修改';$('model-config-cancel').hidden=false;
    $('model-config-key-hint').textContent='留空会保留服务器现有密钥；填写新值才会替换。';$('model-config-label').focus();
  };
  const render=items=>{
    list.replaceChildren();
    if(!items.length){const empty=document.createElement('p');empty.className='model-config-empty';empty.textContent='尚未添加服务器模型配置。';list.append(empty);return;}
    for(const item of items){
      const card=document.createElement('article'), title=document.createElement('strong'), detail=document.createElement('small'), actions=document.createElement('div');
      title.textContent=item.label || item.id;detail.textContent=`${item.model} · ${item.base_url} · ${item.configured?'密钥已保存':'缺少密钥'}`;
      const modify=document.createElement('button');modify.type='button';modify.textContent='修改';modify.addEventListener('click',()=>edit(item));
      const remove=document.createElement('button');remove.type='button';remove.textContent='删除';remove.className='danger';remove.addEventListener('click',async()=>{
        if(!confirm(`删除“${item.label || item.id}”的服务器模型配置？`))return;
        try{await json(`/api/models/${encodeURIComponent(item.id)}`,{method:'DELETE'});if(editing===item.id)reset();await refresh();await onChanged();message('模型配置已删除。');}catch(error){onError(error);message(error.message);}
      });
      actions.append(modify,remove);card.append(title,detail,actions);list.append(card);
    }
  };
  async function refresh(){
    section.hidden=!state.config?.device_admin;if(section.hidden){list.replaceChildren();reset();return;}
    try{const value=await json('/api/models');render(value.models || []);message();}catch(error){onError(error);message(error.message);}
  }
  form.addEventListener('submit',async event=>{
    event.preventDefault();message();const body=values();if(editing)body.id=editing;
    try{
	  const updating=Boolean(editing);
      await json(editing?`/api/models/${encodeURIComponent(editing)}`:'/api/models',{method:editing?'PUT':'POST',body:JSON.stringify(body)});
      reset();await refresh();await onChanged();message(updating?'模型配置已更新。':'模型配置已添加。');
    }catch(error){onError(error);message(error.message);}
  });
  $('model-config-cancel').addEventListener('click',reset);
  reset();
  return {refresh,clear(){section.hidden=true;list.replaceChildren();reset();message();}};
}
