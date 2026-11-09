const labels = {
  shared_activity_update: '更新活动记录', shared_activity_open: '接续活动记录',
  companion_preferences_update: '保存交流偏好', episode_propose: '记录事件候选',
  skill_load: '读取相处指引', knowledge_manage: '整理记忆', care_preferences: '调整关怀安排',
  web_search: '联网搜索', web_fetch: '读取网页', memory_search: '查找记忆', memory_write: '记录候选',
  get_current_time: '读取时间', image_generate: '生成图像'
};
const statuses = { running: '执行中', completed: '已完成', unfinished: '未完成', approval_required: '等待授权' };

// A tool that answered instantly should not advertise "0.0 秒": a duration is only
// worth space when it tells the reader something. Below this the time is noise.
const DURATION_FLOOR_MS = 100;

export function describeTool(data, pending) {
  const status = Object.hasOwn(statuses, data.status) ? data.status : pending ? 'running' : data.error ? 'unfinished' : 'completed';
  const title = data.view?.title || (Object.hasOwn(labels, data.name) ? labels[data.name] : data.name) || '工具';
  const hint = data.view?.summary ? `「${data.view.summary}」` : '';
  const duration = Number.isFinite(data.elapsed_ms) && data.elapsed_ms >= DURATION_FLOOR_MS ? `${(data.elapsed_ms / 1000).toFixed(1)} 秒` : '';
  const endpoint = data.view?.endpoint === 'server' ? '服务端' : data.view?.endpoint === 'local' ? '本地' : '';
  return {
    status,
    title: [title, hint, statuses[status], duration, endpoint].filter(Boolean).join(' · '),
    detail: status === 'running' ? '等待工具返回结果…' : data.error || data.output || statuses[status]
  };
}

export function searchResults(data) {
  if (data.view?.card !== 'search' || data.status !== 'completed') return null;
  let results;
  try { results = JSON.parse(data.output)?.results; } catch { return null; }
  if (!Array.isArray(results) || !results.length) return null;
  const list = document.createElement('ul');
  for (const result of results.slice(0, 5)) {
    if (typeof result?.title !== 'string' || typeof result?.url !== 'string') continue;
    let url;
    try { url = new URL(result.url); } catch { continue; }
    if (url.protocol !== 'https:' || url.username || url.password) continue;
    const row = document.createElement('li');
    const link = document.createElement('a');
    link.href = url.href; link.target = '_blank'; link.rel = 'noopener noreferrer';
    link.textContent = result.title;
    row.append(link); list.append(row);
  }
  return list.childElementCount ? list : null;
}
