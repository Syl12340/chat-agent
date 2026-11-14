// One tool card: what ran, what it was asked, and what came back.
//
// The card is a closed summary until opened. Everything below the fold is the
// record itself, formatted for reading rather than copied verbatim from the
// transport.

const labels = {
	get_current_time: '读取时间',
	web_search: '搜索网页',
	web_fetch: '读取网页',
	memory_search: '回想记忆',
	memory_write: '记住内容',
	knowledge_manage: '整理资料',
	episode_propose: '提出事件',
	skill_load: '加载技能',
	todo_add: '添加待办',
	todo_list: '查看待办',
	todo_complete: '完成待办',
	reminder_add: '设置提醒',
	reminder_list: '查看提醒',
	reminder_cancel: '取消提醒',
	server_status: '服务器体检',
	server_releases: '发布与备份',
	server_logs: '服务日志',
	server_service: '服务详情',
	workspace_list: '查看工作区',
	workspace_read: '读取文件',
	workspace_search: '搜索工作区',
	workspace_share: '发送文件',
	workspace_shell: '沙箱命令',
	local_workspace: '读取本机',
	local_workspace_write: '写入本机',
	local_action: '本机动作',
	local_file_share: '发送本机文件',
	courseware_list: '查看课件',
	courseware_read: '读取课件',
	image_generate: '生成图片',
	image_health: '图像能力检查',
	image_status: '图片任务状态',
	image_cancel: '取消图片任务',
	shared_activity_open: '共同活动',
	shared_activity_update: '更新活动',
	care_preferences: '关怀设置',
	companion_preferences_update: '偏好设置',
	decide: '请求确认'
};

const statuses = {
	running: '执行中',
	completed: '已完成',
	unfinished: '未完成',
	approval_required: '等待确认'
};

// A tool that answered instantly should not advertise "0.0 秒": a duration is only
// worth space when it tells the reader something. Below this the time is noise.
const DURATION_FLOOR_MS = 100;

function formatDuration(ms) {
	if (!Number.isFinite(ms) || ms < DURATION_FLOOR_MS) return '';
	return ms < 1000 ? `${Math.round(ms)} 毫秒` : `${(ms / 1000).toFixed(1)} 秒`;
}

// prettyJSON re-indents a JSON payload so a record reads as fields instead of one
// long line. Anything that is not JSON is returned untouched: a tool may
// legitimately answer with plain text.
function prettyJSON(text) {
	const trimmed = text.trim();
	if (!trimmed.startsWith('{') && !trimmed.startsWith('[')) return text;
	try {
		return JSON.stringify(JSON.parse(trimmed), null, 2);
	} catch {
		return text;
	}
}

export function describeTool(data, pending) {
	const status = Object.hasOwn(statuses, data.status) ? data.status : pending ? 'running' : data.error ? 'unfinished' : 'completed';
	const title = data.view?.title || (Object.hasOwn(labels, data.name) ? labels[data.name] : data.name) || '工具';
	const hint = data.view?.summary ? `「${data.view.summary}」` : '';
	const duration = formatDuration(data.elapsed_ms);
	const endpoint = data.view?.endpoint === 'server' ? '服务端' : data.view?.endpoint === 'local' ? '本地' : '';
	const body = status === 'running' ? '' : data.error || data.output || '';
	return {
		status,
		title,
		hint,
		// Meta travels as separate labels so the layout can place status, duration
		// and endpoint apart instead of gluing them into the title sentence.
		meta: [statuses[status], duration, endpoint].filter(Boolean),
		arguments: data.arguments || '',
		detail: status === 'running' ? '等待工具返回结果…' : body ? prettyJSON(body) : statuses[status],
		empty: status !== 'running' && !body
	};
}

export function searchResults(data) {
	if (data.view?.card !== 'search' || !data.output) return null;
	let parsed;
	try {
		parsed = JSON.parse(data.output);
	} catch {
		return null;
	}
	const items = Array.isArray(parsed?.results) ? parsed.results : Array.isArray(parsed) ? parsed : [];
	const links = items
		.map(item => ({
			title: String(item?.title || item?.name || '').trim(),
			url: String(item?.url || item?.link || '').trim()
		}))
		.filter(item => item.url.startsWith('https://') && item.title)
		.slice(0, 5);
	if (!links.length) return null;
	const list = document.createElement('ul');
	list.className = 'tool-results';
	for (const link of links) {
		const item = document.createElement('li');
		const anchor = document.createElement('a');
		anchor.href = link.url;
		anchor.target = '_blank';
		anchor.rel = 'noopener noreferrer';
		anchor.textContent = link.title;
		item.append(anchor);
		list.append(item);
	}
	return list;
}

export function coursewareResult(data) {
	if (!['courseware_list','courseware_read'].includes(data.name) || !data.output) return null;
	try {
		const value=JSON.parse(data.output), node=document.createElement('div');node.className='tool-courseware';
		if(data.name==='courseware_list'){
			const docs=Array.isArray(value.documents)?value.documents:[];if(!docs.length)return null;
			const list=document.createElement('ul');for(const doc of docs.slice(0,12)){const item=document.createElement('li');item.textContent=`${doc.name} · ${Math.max(1,Math.ceil(Number(doc.text_bytes||0)/1024))} KB 文本`;list.append(item);}node.append(list);return node;
		}
		if(typeof value.document!=='string'||typeof value.excerpt!=='string')return null;
		const source=document.createElement('strong');source.textContent=value.document;const excerpt=document.createElement('pre');excerpt.textContent=value.excerpt;node.append(source,excerpt);return node;
	}catch{return null;}
}

export function workspaceFileResult(data) {
	if (!['workspace_share','local_file_share'].includes(data.name) || !data.output) return null;
	try {
		const value = JSON.parse(data.output);
		if (data.name === 'local_file_share') {
			const file = value?.artifacts?.[0];
			if (!file || typeof file.name !== 'string' || !file.name || typeof file.download_url !== 'string' || !file.download_url.startsWith('/api/local-agent/artifacts/') || !Number.isSafeInteger(file.bytes) || file.bytes < 0 || file.bytes > 32 * 1024 * 1024 || typeof file.sha256 !== 'string' || !/^[a-f0-9]{64}$/.test(file.sha256)) return null;
			return file;
		}
		if (value?.kind !== 'workspace_file' || typeof value.name !== 'string' || !value.name ||
			typeof value.download_url !== 'string' || !value.download_url.startsWith('/api/sessions/') ||
			!Number.isSafeInteger(value.bytes) || value.bytes < 0 || value.bytes > 32 * 1024 * 1024 ||
			typeof value.sha256 !== 'string' || !/^[a-f0-9]{64}$/.test(value.sha256)) return null;
		return value;
	} catch { return null; }
}
