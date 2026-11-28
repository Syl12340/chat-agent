// The render-reuse path only runs when the same session is re-rendered, which in
// the real app happens when a turn produces messages. So this sends two messages
// and reads window.__renderReuse between them. The judge is that counter being
// present: cards that were simply never rebuilt would also carry stale stamps.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE);

const BASE = process.env.BASE;
const TOKEN = process.env.POCKET_ACCESS_TOKEN;
const NAME = 'reuse-live-' + Date.now();

const api = async (path, init = {}) => {
  const response = await fetch(process.env.SERVER + path, {
    ...init,
    headers: { Authorization: 'Bearer ' + TOKEN, 'Content-Type': 'application/json', ...(init.headers || {}) },
  });
  const text = await response.text();
  let body = null;
  try { body = JSON.parse(text); } catch { body = text; }
  return { status: response.status, body };
};

const waitForTurn = async (page, previous) => {
  const deadline = Date.now() + 180000;
  while (Date.now() < deadline) {
    const state = await page.evaluate(() => {
      const pending = document.querySelectorAll('.message.pending').length;
      const assistant = document.querySelectorAll('#messages > .message.assistant').length;
      return { pending, assistant };
    });
    if (state.pending === 0 && state.assistant > previous) return state;
    await page.waitForTimeout(1500);
  }
  return null;
};

(async () => {
  const browser = await chromium.launch();
  const errors = [];
  try {
    const page = await browser.newPage();
    page.on('pageerror', e => errors.push(e.message));

    await page.goto(BASE + '/', { waitUntil: 'domcontentloaded', timeout: 120000 });
    await page.waitForSelector('#login-form', { timeout: 60000 });
    const clicked = await page.evaluate(name => {
      const field = document.getElementById('device-name');
      if (field) { field.value = name; field.dispatchEvent(new Event('input', { bubbles: true })); }
      const button = document.getElementById('login-button');
      if (!button) return false;
      button.click();
      return true;
    }, NAME);
    if (!clicked) throw new Error('login form had no submit control');

    await page.waitForTimeout(4000);
    const listed = await api('/api/devices');
    const devices = Array.isArray(listed.body?.devices) ? listed.body.devices : [];
    const mine = devices.find(d => d.name === NAME);
    if (!mine) { console.log(JSON.stringify({ stage: 'enroll', errors: errors.slice(0, 2) })); console.log('RESULT: blocked'); process.exitCode = 2; return; }
    const approved = await api('/api/devices/' + mine.id, { method: 'PATCH', body: JSON.stringify({ revision: mine.revision, status: 'approved' }) });
    if (approved.status !== 200) { console.log(JSON.stringify({ stage: 'approve', status: approved.status })); console.log('RESULT: blocked'); process.exitCode = 2; return; }

    await page.reload({ waitUntil: 'domcontentloaded', timeout: 120000 });
    await page.waitForTimeout(5000);
    const signedIn = await page.evaluate(() => {
      const overlay = document.getElementById('login-overlay');
      return !overlay || overlay.hidden;
    });
    if (!signedIn) { console.log(JSON.stringify({ stage: 'signin', errors: errors.slice(0, 2) })); console.log('RESULT: blocked'); process.exitCode = 2; return; }

    // A fresh session keeps this out of the user's real conversation.
    await page.click('#new-chat');
    await page.waitForTimeout(1500);

    const send = async text => {
      await page.fill('#prompt', text);
      await page.press('#prompt', 'Enter');
    };

    await send('只回一个字：好');
    const first = await waitForTurn(page, 0);
    if (!first) { console.log(JSON.stringify({ stage: 'turn1', errors: errors.slice(0, 2) })); console.log('RESULT: blocked'); process.exitCode = 2; return; }
    const afterFirst = await page.evaluate(() => ({ cards: document.querySelectorAll('#messages > *').length, stats: window.__renderReuse || null }));

    // Mark every card, then let the second turn re-render the transcript.
    await page.evaluate(() => {
      window.__stamps = new Map();
      for (const node of document.querySelectorAll('#messages > [data-message-id]')) {
        node.__reuseStamp = Math.random();
        window.__stamps.set(node.dataset.messageId, node.__reuseStamp);
      }
    });

    await send('再回一个字：行');
    const second = await waitForTurn(page, first.assistant);
    if (!second) { console.log(JSON.stringify({ stage: 'turn2', errors: errors.slice(0, 2) })); console.log('RESULT: failed'); process.exitCode = 1; return; }

    const afterSecond = await page.evaluate(() => {
      let kept = 0, total = 0;
      for (const node of document.querySelectorAll('#messages > [data-message-id]')) {
        total++;
        const stamp = window.__stamps.get(node.dataset.messageId);
        if (stamp !== undefined && node.__reuseStamp === stamp) kept++;
      }
      return { cards: document.querySelectorAll('#messages > *').length, marked: total, kept, stats: window.__renderReuse || null };
    });

    console.log(JSON.stringify({ afterFirst, afterSecond, errors: errors.slice(0, 2) }));
    // The counter only exists if the reuse block ran, and reused counts the nodes
    // it actually took from the previous render. kept is reported for interest but
    // is not judged: stamping JS properties on nodes that get replaced anyway says
    // nothing about whether reuse happened.
    const ok = afterSecond.stats !== null && afterSecond.stats.reused > 0 && afterSecond.stats.considered > 0;
    console.log(ok ? 'RESULT: ok' : 'RESULT: failed');
    process.exitCode = ok ? 0 : 1;
  } finally {
    await browser.close();
  }
})().catch(e => { console.error('ERROR', e.message); process.exitCode = 1; });
