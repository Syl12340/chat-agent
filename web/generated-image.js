// 生成图片的展示：image_generate 的结果是一段 JSON 文本，如果不解析，
// 用户只会看到 {"image_id":"...","url":"..."} 这种内容。
//
// 这里把它变成真正的图片。鉴权图片不能直接用 <img src>，必须走 request() 拿
// blob，所以复用 chat-input 已有的懒加载渲染器。
//
// 注意：这个模块不能直接引用 app.js 里的 chatInput —— 那是另一个模块作用域的
// 变量，引用会抛 "chatInput is not defined"。渲染器由调用方传进来。

const generatedImages = new WeakMap();
const jobStates = {
  running: '正在生成', completed: '图像已生成', cancel_requested: '已请求取消',
  discarded: '已取消交付', canceled: '已取消', unknown: '结果待核对', failed: '图像作业失败'
};

export function watchImageJob(log, data, request, onImage) {
  if (data.name !== 'image_generate' || log.imageJob) return;
  let initial;
  try { initial = JSON.parse(data.output); } catch { return; }
  if (!/^[a-f0-9]{32}$/.test(initial?.job_id || '') || !Object.hasOwn(jobStates, initial.status)) return;
  log.imageJob = initial.job_id;
  const cancel = document.createElement('button');
  cancel.type = 'button'; cancel.textContent = '取消图像作业';
  log.output.after(cancel);
  const render = job => {
    const status = jobStates[job.status] || '结果待核对';
    log.summary.textContent = '生成图像 · ' + status + ' · 本地渲染器';
    log.output.textContent = job.detail || status + ' · ' + job.job_id;
    cancel.hidden = job.status !== 'running';
    if (job.image_id) onImage({...data, output:JSON.stringify(job)});
    return job.status === 'running' || job.status === 'cancel_requested';
  };
  let timer, canceling = false;
  const poll = async () => {
    if (!log.node.isConnected) return;
    if (document.hidden || canceling) { timer = setTimeout(poll, 3000); return; }
    try {
      const job = await request(initial.job_id, false);
      if (!canceling && render(job)) timer = setTimeout(poll, 3000);
    } catch (error) {
      if (!canceling) {
        log.output.textContent = error.message || '暂时无法查询作业。';
        timer = setTimeout(poll, 8000);
      }
    }
  };
  cancel.addEventListener('click', async () => {
    canceling = true; cancel.disabled = true; clearTimeout(timer);
    try { render(await request(initial.job_id, true)); }
    catch { log.output.textContent = '取消请求未确认，正在核对状态。'; }
    finally { canceling = false; cancel.disabled = false; timer = setTimeout(poll, 1000); }
  });
  if (render(initial)) timer = setTimeout(poll, 1000);
}

function imageIdFromToolResult(data) {
  if (!data || data.name !== 'image_generate' || data.error) return null;
  const raw = data.output;
  if (typeof raw !== 'string' || !raw.trim()) return null;
  let payload;
  try {
    payload = JSON.parse(raw);
  } catch {
    return null;
  }
  const id = payload?.image_id;
  if (typeof id !== 'string' || !/^[A-Za-z0-9_-]{1,128}$/.test(id)) return null;
  return id;
}

// attachGeneratedImage renders the produced picture once per tool call, then dims
// the tool log so the JSON receipt does not compete with the image.
//
// renderImages is passed in rather than imported: it belongs to the composer module
// and carries its own auth and caching rules.
export function attachGeneratedImage(message, data, renderImages) {
  const host = message?.attachments;
  if (!host || typeof renderImages !== 'function') return;
  const id = imageIdFromToolResult(data);
  if (!id) return;
  let seen = generatedImages.get(host);
  if (!seen) {
    seen = new Set();
    generatedImages.set(host, seen);
  }
  if (seen.has(id)) return;
  seen.add(id);
  try {
    renderImages(host, id);
  } catch {
    // A failed render must not break the surrounding message; the tool log still
    // carries the image id, so nothing is lost.
    return;
  }
  const log = message.tools?.get(data.id);
  if (log?.node) log.node.classList.add('has-image');
}
