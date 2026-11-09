// 生成图片的展示：image_generate 的结果是一段 JSON 文本，如果不解析，
// 用户只会看到 {"image_id":"...","url":"..."} 这种内容。
//
// 这里把它变成真正的图片。鉴权图片不能直接用 <img src>，必须走 request() 拿
// blob，所以复用 chat-input 已有的懒加载渲染器。
//
// 注意：这个模块不能直接引用 app.js 里的 chatInput —— 那是另一个模块作用域的
// 变量，引用会抛 "chatInput is not defined"。渲染器由调用方传进来。

const generatedImages = new WeakMap();

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
