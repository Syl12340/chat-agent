// Native shells can offer a bounded text-export channel. Normal browsers retain
// the same Blob download behavior; no credentials cross this interface.
export function saveMarkdown(content, filename) {
  try { if (window.ChatAndroidExports?.saveText(content, filename)) return; } catch {}
  const url = URL.createObjectURL(new Blob([content], { type: 'text/markdown;charset=utf-8' }));
  const link = document.createElement('a'); link.href = url; link.download = filename; link.click();
  setTimeout(() => URL.revokeObjectURL(url), 1000);
}
