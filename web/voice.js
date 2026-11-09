import { Features, describeGap, inNativeShell, supports } from '/native-capabilities.js';
// Audio is requested only by an explicit click. Keep at most one clip in memory;
// neither audio nor recognition results enter persistent browser storage.
export function createVoiceUI({ state, request, notice, restoreDraft, onError }) {
  const input = document.getElementById('voice-input');
  const panel = document.getElementById('voice-player');
  const audio = document.getElementById('voice-audio');
  const status = document.getElementById('voice-status');
  const native = inNativeShell;
  let generation = 0, scopeEpoch = 0, recognition = 0, controller = null, clip = null, recognizing = false;
  const scoped = () => `${state.current || ''}:${state.roleID}`;
  function stop() {
    generation++;
    controller?.abort(); controller = null;
    audio.pause();
    if (audio.src) audio.currentTime = 0;
    status.textContent = clip ? '已停止，可用播放控件重听。' : '已停止；合成请求可能已产生用量。';
  }
  function clear() {
    scopeEpoch++; recognition++; recognizing = false;
    stop();
    if (clip) URL.revokeObjectURL(clip.url);
    clip = null; audio.removeAttribute('src'); audio.load(); panel.hidden = true;
  }
  function lock() {
    // Hide the control when this shell has no recogniser instead of offering
    // a button that can only fail.
    input.hidden = !native || !supports(Features.voice);
    input.disabled = !state.token || state.loading || state.compacting || recognizing || document.getElementById('prompt').disabled;
  }
  async function recognize() {
    if (input.disabled) return;
    if (!supports(Features.voice)) { notice(describeGap(Features.voice)); return; }
    stop();
    const auth = state.token, scope = scoped(), epoch = scopeEpoch, attempt = ++recognition;
    recognizing = true; lock();
    notice('由手机系统识别语音，可能使用系统服务的网络连接；识别文字确认发送后才会提交给聊天服务。');
    try {
      const result = await window.ChatAndroidVoice.recognize();
      if (state.token !== auth || scoped() !== scope || epoch !== scopeEpoch) return;
      if (!result.available) notice('当前手机没有可用的语音识别服务，可使用键盘的语音输入。');
      else if (result.busy) notice('请先完成系统中的语音输入。');
      else if (!result.canceled && result.text) restoreDraft(result.text);
      else notice('已取消语音输入。');
    } catch (error) { if (state.token === auth && scoped() === scope && epoch === scopeEpoch) notice(error.message); }
    finally { if (attempt === recognition) { recognizing = false; lock(); } }
  }
  async function speak(text) {
    if (!state.token || !state.config?.speech_enabled || state.loading || document.hidden) return;
    if (controller) { notice('已有朗读正在合成，可先点击停止朗读。'); return; }
    const auth = state.token, scope = scoped();
    const cached = clip?.text === text && clip.scope === scope;
    stop();
    const attempt = generation;
    panel.hidden = false;
    if (!cached) {
      if (clip) URL.revokeObjectURL(clip.url);
      clip = null; audio.removeAttribute('src'); audio.load();
      status.textContent = '正在生成朗读…';
      controller = new AbortController();
      try {
        const response = await request('/api/voice/speech', { method: 'POST', body: JSON.stringify({input: text}), signal: controller.signal });
        const blob = await response.blob();
        if (generation !== attempt || state.token !== auth || scoped() !== scope || document.hidden) return;
        clip = { text, scope, url: URL.createObjectURL(blob) }; audio.src = clip.url;
      } catch (error) {
        if (generation === attempt) {
          panel.hidden = true;
          if (error.status === 401) onError(error);
          else if (error.name !== 'AbortError') notice(`${error.message}；没有自动重试。`);
        }
        return;
      } finally { if (generation === attempt) controller = null; }
    }
    status.textContent = '语音已就绪，可播放或重听。';
    try { await audio.play(); }
    catch { if (generation === attempt) status.textContent = '语音已就绪，请点击播放。'; }
  }
  function attach(message) {
    if (!state.config?.speech_enabled || !message.text.trim() || message.node.querySelector('.voice-read')) return;
    const button = document.createElement('button'); button.className = 'voice-read'; button.type = 'button';
    button.textContent = '朗读';
    const tooLong = Array.from(message.text).length > (state.config.speech_max_chars || 1200);
    button.disabled = tooLong;
    button.title = tooLong ? '这段回答超过单次朗读上限（1200 字）' : '点击后生成语音，会使用语音合成额度；不会自动朗读';
    button.addEventListener('click', () => speak(message.text));
    message.node.querySelector('.message-content').append(button);
  }
  input.addEventListener('click', recognize);
  document.getElementById('voice-stop').addEventListener('click', stop);
  audio.addEventListener('error', () => { if (clip) status.textContent = '播放失败，可尝试播放控件；不会自动重新合成。'; });
  document.addEventListener('visibilitychange', () => { if (document.hidden) stop(); });
  window.addEventListener('pagehide', clear);
  return { attach, lock, clear, stop };
}
