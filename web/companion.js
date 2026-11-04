// Small settings UI; durable preferences live on the server, per role.
export function createCompanionUI({ state, json, onError, drawer }) {
  const $ = id => document.getElementById(id);
  const fields = ['preferred_name', 'reply_length', 'tone', 'advice', 'questions'];
  const defaults = { preferred_name: '', reply_length: 'default', tone: 'default', advice: 'default', questions: 'default' };
  const dialog = $('companion-dialog'), form = $('companion-form'), save = $('companion-save'), message = $('companion-message');
  let snapshot = null, activeRole = null, generation = 0;
  const display = prefs => fields.forEach(key => { $(`companion-${key}`).value = prefs[key]; });
  const say = text => { message.textContent = text; message.hidden = !text; };
  const url = role => `/api/roles/${encodeURIComponent(role)}/companion`;
  const clear = () => { generation++; snapshot = null; activeRole = null; save.disabled = true; $('companion-defaults').disabled = false; display(defaults); say(''); dialog.close(); };
  $('companion-close').addEventListener('click', () => dialog.close());
  $('companion-defaults').addEventListener('click', () => { display(defaults); say('已恢复为角色默认值，点击保存后生效。'); });
  $('companion-open').addEventListener('click', async () => {
    const role = state.roleID, current = ++generation;
    activeRole = role; snapshot = null; display(defaults); say(''); save.disabled = true;
    $('companion-role-name').textContent = state.roles.find(item => item.id === role)?.name || role;
    drawer(false); dialog.showModal();
    try {
      const result = await json(url(role));
      if (generation !== current || role !== state.roleID || !dialog.open) return;
      snapshot = result; display(result.preferences); save.disabled = false;
    } catch (error) { if (generation === current) { say(error.message); if (error.status === 401) onError(error); } }
  });
  form.addEventListener('submit', async event => {
    event.preventDefault();
    if (!snapshot || activeRole !== state.roleID) return;
    const role = activeRole, current = generation;
    const patch = Object.fromEntries(fields.map(key => [key, $(`companion-${key}`).value]));
    patch.revision = snapshot.revision;
    save.disabled = true; $('companion-defaults').disabled = true; say('');
    try {
      const result = await json(url(role), { method: 'PATCH', body: JSON.stringify(patch) });
      if (current !== generation || state.roleID !== role) return;
      snapshot = result; display(result.preferences); say('已保存。之后的对话和这个角色的新会话都会沿用。');
    } catch (error) {
      if (current !== generation) return;
      say(error.message);
      if (error.status === 409) { snapshot = null; say('偏好已在别处修改，请关闭后重新打开，再保存。'); }
      if (error.status === 401) onError(error);
    } finally {
      if (current === generation) { save.disabled = !snapshot; $('companion-defaults').disabled = false; }
    }
  });
  return { clear };
}
