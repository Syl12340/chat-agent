// Optional development check; uses an already installed Playwright package.
// PLAYWRIGHT_MODULE may point to an existing package. No app dependency added.
const { chromium } = require(process.env.PLAYWRIGHT_MODULE || 'playwright');
const assert = require('node:assert/strict');
const fs = require('node:fs/promises');
const path = require('node:path');

async function main() {
  const base = process.env.CHAT_PREVIEW_URL || 'http://127.0.0.1:18080';
  const token = process.env.CHAT_ACCESS_TOKEN;
  assert(token, 'CHAT_ACCESS_TOKEN is required');
  const config = await fetch(`${base}/api/status`, { headers: { Authorization: `Bearer ${token}` } }).then(r => r.json());
  assert.equal(config.demo, true, 'Browser smoke check only runs against explicit local demo mode');
  await fs.mkdir('.cache', { recursive: true });
  const browser = await chromium.launch({ headless: true });
  const errors = [];
  const login = async page => {
    page.on('pageerror', error => errors.push(error.message));
    await page.goto(base);
    await page.locator('#access-token').fill(token);
    await page.locator('#login-button').click();
    await page.locator('#login-overlay').waitFor({ state: 'hidden' });
    await page.waitForFunction(() => !document.getElementById('new-chat').disabled);
  };
  const settled = async page => {
    await page.waitForFunction(() => document.getElementById('stop').hidden && !document.getElementById('new-chat').disabled);
  };
  try {
    const desktop = await browser.newContext({ viewport: { width: 1440, height: 1000 }, reducedMotion: 'reduce' });
    desktop.setDefaultTimeout(10000);
    const page = await desktop.newPage();
    await login(page);
    await page.locator('#new-chat').click();
    await page.locator('#welcome').waitFor({ state: 'visible' });
    await page.screenshot({ path: path.resolve('.cache/p1-desktop.png') });
    await page.locator('[data-prompt]').first().click();
    await page.waitForSelector('.message.assistant');
    await settled(page);
    assert.match(await page.locator('#messages').innerText(), /UTC/);
    assert.match(await page.locator('.tool-log summary').innerText(), /get_current_time.*已完成/);
    const timeTitle = await page.locator('#chat-title').innerText();
    await page.reload();
    await page.locator('#login-overlay').waitFor({ state: 'hidden' });
    await page.waitForFunction(() => document.querySelector('.message.assistant .message-body')?.textContent.includes('UTC'));
    await page.screenshot({ path: path.resolve('.cache/p1-desktop-chat.png') });

    await page.locator('#new-chat').click();
    await page.waitForFunction(() => !document.getElementById('prompt').disabled);
    const attack = '<img src=x onerror=window.chatXSS=true> [unsafe](javascript:alert(1))';
    await page.locator('#prompt').fill(attack);
    await page.locator('#send').click();
    await page.waitForSelector('.message.assistant');
    await settled(page);
    assert.equal(await page.locator('#messages img').count(), 0);
    assert.equal(await page.locator('#messages a[href^="javascript:"]').count(), 0);
    assert.equal(await page.evaluate(() => Boolean(window.chatXSS)), false);

    await page.locator('#prompt').fill('测试停止流式回复');
    await page.locator('#send').click();
    await page.waitForFunction(() => document.querySelector('.message.pending .message-body')?.textContent.length > 0);
    await page.locator('#stop').click();
    await settled(page);
    assert.match(await page.locator('.message-status').last().innerText(), /已停止/);
    console.log('Desktop flow passed: tools, refresh, safe Markdown and stop.');

    const talk = async text => {
      await page.locator('#prompt').fill(text); await page.locator('#send').click();
      await settled(page);
    };
    await page.locator('#new-chat').click();
    await page.waitForFunction(() => !document.getElementById('prompt').disabled);
    await talk('记住：我喜欢乌龙茶');
    assert.match(await page.locator('#messages').innerText(), /待审核记忆/);
    await page.locator('#memory-open').click();
    await page.locator('.memory-entry').first().waitFor();
    await page.locator('.memory-entry .approve').first().click();
    await page.waitForFunction(() => document.querySelector('[data-memory-filter="approved"]').classList.contains('selected') && !document.getElementById('memory-save').disabled);
    await page.locator('.memory-entry .edit').first().click();
    await page.locator('#memory-content').fill('我喜欢红茶');
    await page.locator('#memory-save').click();
    await page.waitForFunction(() => document.querySelector('.memory-entry-content')?.textContent === '我喜欢红茶' && !document.getElementById('memory-save').disabled);
    await page.screenshot({ path: path.resolve('.cache/p2-memory.png') });
    const downloadReady = page.waitForEvent('download'); await page.locator('#memory-export').click();
    const download = await downloadReady; assert.equal(download.suggestedFilename(), 'companion-memory.md');
    assert.match(await fs.readFile(await download.path(), 'utf8'), /我喜欢红茶/);
    await page.locator('#memory-close').click();
    await page.locator('#new-chat').click(); await page.waitForFunction(() => !document.getElementById('prompt').disabled);
    await talk('你还记得我喜欢什么吗');
    assert.match(await page.locator('.message.assistant .message-body').last().innerText(), /我喜欢红茶/);

    await page.locator('#role-open').click();
    await page.waitForFunction(() => !document.getElementById('role-save').disabled && document.getElementById('role-content').value.length > 0);
    await page.locator('#role-file').selectOption('PROFILE.md');
    await page.locator('#role-content').fill('# 演示风格\n\n保持简洁、具体，先回应用户的问题。');
    await page.locator('#role-save').click();
    await page.waitForFunction(() => document.getElementById('role-message').textContent.includes('已保存'));
    assert(await page.locator('.role-skill').count() > 0);
    await page.screenshot({ path: path.resolve('.cache/p2-roles.png') });
    await page.locator('#role-close').click();
    await page.locator('#role-picker').selectOption('planner');
    await page.waitForFunction(() => !document.getElementById('new-chat').disabled && document.getElementById('role-picker').value === 'planner');
    await page.locator('#memory-open').click();
    await page.waitForFunction(() => !document.getElementById('memory-save').disabled);
    await page.locator('[data-memory-filter="approved"]').click();
    assert.equal(await page.locator('.memory-entry').count(), 0, 'New role must not show companion memory');
    await page.locator('#memory-close').click();
    await talk('你还记得我喜欢什么吗');
    assert.match(await page.locator('.message.assistant .message-body').last().innerText(), /没有匹配/);

    await page.locator('#role-picker').selectOption('companion');
    await page.waitForFunction(() => !document.getElementById('new-chat').disabled && document.getElementById('role-picker').value === 'companion');
    await page.locator('#memory-open').click();
    await page.waitForFunction(() => !document.getElementById('memory-save').disabled);
    await page.locator('[data-memory-filter="approved"]').click();
    await page.locator('.memory-entry').first().waitFor();
    page.once('dialog', dialog => dialog.accept());
    await page.locator('.memory-entry .delete').first().click();
    await page.waitForFunction(() => document.querySelectorAll('.memory-entry').length === 0 && !document.getElementById('memory-save').disabled);
    await page.locator('#memory-close').click();
    await talk('你还记得我喜欢什么吗');
    assert.match(await page.locator('.message.assistant .message-body').last().innerText(), /没有匹配/);
    for (let i = 1; i <= 4; i++) await talk(`演示对话轮次 ${i}`);
    const fullCount = await page.locator('.message.user').count();
    await page.locator('#settings').click(); await page.locator('#compact-context').click();
    await page.waitForFunction(() => document.getElementById('compact-message').textContent.includes('已整理'));
    assert.equal(await page.locator('.message.user').count(), fullCount, 'Compaction must retain full visible history');
    await page.locator('#close-settings').click();
    await page.reload(); await page.locator('#login-overlay').waitFor({ state: 'hidden' });
    await page.waitForFunction(count => document.querySelectorAll('.message.user').length === count && !document.getElementById('new-chat').disabled, fullCount);
    await page.locator('#settings').click(); await page.locator('#summary-details').waitFor({ state: 'visible' }); await page.locator('#close-settings').click();
    await page.locator('#memory-open').click(); await page.waitForFunction(() => !document.getElementById('memory-save').disabled);
    await page.locator('#memory-content').fill('我偏好简洁、具体的回答。'); await page.locator('#memory-save').click();
    await page.waitForFunction(() => document.querySelector('.memory-entry-content')?.textContent === '我偏好简洁、具体的回答。' && !document.getElementById('memory-save').disabled);
    await page.locator('#memory-close').click();
    console.log('P2 flow passed: review, correction, export, role isolation, role settings, deletion and durable context compaction.');

    await page.locator('#new-chat').click(); await page.waitForFunction(() => !document.getElementById('prompt').disabled);
    await talk('待办：整理 P3 演示');
    assert.match(await page.locator('.message.assistant .message-body').last().innerText(), /已保存待办/);
    await talk('完成第一条待办');
    assert.match(await page.locator('.message.assistant .message-body').last().innerText(), /待办已完成/);
    await page.locator('#tasks-open').click();
    await page.waitForFunction(() => !document.getElementById('tasks-save').disabled && document.querySelector('.task-done'));
    await page.locator('.task-entry .task-complete').first().click();
    await page.waitForFunction(() => !document.getElementById('tasks-save').disabled && document.querySelector('.task-open'));
    await page.locator('#tasks-title').fill('整理 P3 演示'); await page.locator('#tasks-save').click();
    await page.waitForFunction(() => !document.getElementById('tasks-save').disabled);
    assert.equal(await page.locator('.task-entry').count(), 1, 'Duplicate active todo should not be created');
    await page.locator('#tasks-title').fill('核对手机布局'); await page.locator('#tasks-save').click();
    await page.waitForFunction(() => !document.getElementById('tasks-save').disabled && document.querySelectorAll('.task-entry').length === 2);
    await page.locator('.task-entry', { hasText: '核对手机布局' }).locator('.task-complete').click();
    await page.waitForFunction(() => !document.getElementById('tasks-save').disabled);
    await page.screenshot({ path: path.resolve('.cache/p3-tasks.png') });
    await page.locator('[data-task-tab="reminder"]').click();
    await page.locator('#tasks-title').fill('检查 P3 到期提醒');
    const localDue = await page.evaluate(() => {
      const date = new Date(Date.now() - 10000); const pad = value => String(value).padStart(2, '0');
      return `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())}T${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`;
    });
    await page.locator('#tasks-due').fill(localDue); await page.locator('#tasks-save').click();
    await page.waitForFunction(() => !document.getElementById('tasks-save').disabled && document.querySelector('.task-entry'));
    for (let attempt = 0; attempt < 10; attempt++) {
      await page.locator('#tasks-refresh').click(); await page.waitForFunction(() => !document.getElementById('tasks-save').disabled);
      if (await page.locator('.task-due').count()) break;
      await page.waitForTimeout(200);
    }
    assert.equal(await page.locator('.task-due').count(), 1, 'Reminder must reach the inbox');
    await page.screenshot({ path: path.resolve('.cache/p3-reminders.png') });
    await page.locator('.task-acknowledge').click();
    await page.waitForFunction(() => !document.getElementById('tasks-save').disabled && document.querySelector('.task-acknowledged'));
    await page.locator('#tasks-close').click();
    await talk('一分钟后提醒我喝水');
    assert.match(await page.locator('.message.assistant .message-body').last().innerText(), /提醒已保存/);
    await page.locator('#tasks-open').click(); await page.waitForFunction(() => !document.getElementById('tasks-save').disabled);
    await page.locator('[data-task-tab="reminder"]').click(); await page.locator('.task-cancel').first().click();
    await page.waitForFunction(() => !document.getElementById('tasks-save').disabled && document.querySelector('.task-canceled'));
    await page.locator('#tasks-close').click();
    await page.locator('#role-picker').selectOption('planner'); await page.waitForFunction(() => !document.getElementById('new-chat').disabled);
    await page.locator('#tasks-open').click(); await page.waitForFunction(() => !document.getElementById('tasks-save').disabled);
    assert.equal(await page.locator('.task-entry').count(), 0, 'Planner must not see companion todos');
    await page.locator('#tasks-close').click();
    await page.locator('#role-picker').selectOption('companion'); await page.waitForFunction(() => !document.getElementById('new-chat').disabled);
    console.log('P3 flow passed: task tools, manual completion/reopen, deduplication, due inbox, acknowledgment, cancellation and role isolation.');

    const mobile = await browser.newContext({ viewport: { width: 390, height: 844 }, isMobile: true, hasTouch: true, deviceScaleFactor: 1, reducedMotion: 'reduce' });
    mobile.setDefaultTimeout(10000);
    const phone = await mobile.newPage();
    await login(phone);
    await phone.locator('#menu').click();
    await phone.locator('#sidebar.open').waitFor({ state: 'visible' });
    await phone.locator('.session-item', { hasText: timeTitle }).first().click();
    await phone.waitForFunction(() => !document.getElementById('sidebar').classList.contains('open') && document.querySelector('.message.assistant .message-body')?.textContent.includes('UTC'));
    const fits = await phone.evaluate(() => document.documentElement.scrollWidth <= innerWidth);
    assert.equal(fits, true, 'Mobile page must not overflow horizontally');
    await phone.screenshot({ path: path.resolve('.cache/p1-mobile.png') });
    await phone.locator('#menu').click(); await phone.locator('#memory-open').click();
    await phone.waitForFunction(() => !document.getElementById('memory-save').disabled && document.querySelector('.memory-entry-content')?.textContent.includes('简洁'));
    await phone.screenshot({ path: path.resolve('.cache/p2-mobile-memory.png') });
    assert.equal(await phone.evaluate(() => document.getElementById('memory-dialog').scrollWidth <= document.getElementById('memory-dialog').clientWidth), true, 'Mobile memory dialog must not overflow');
    await phone.locator('#memory-close').click();
    await phone.locator('#menu').click(); await phone.locator('#tasks-open').click();
    await phone.waitForFunction(() => !document.getElementById('tasks-save').disabled && document.querySelector('.task-entry'));
    await phone.screenshot({ path: path.resolve('.cache/p3-mobile-tasks.png') });
    await phone.locator('[data-task-tab="reminder"]').click();
    await phone.screenshot({ path: path.resolve('.cache/p3-mobile-reminders.png') });
    assert.equal(await phone.evaluate(() => document.getElementById('tasks-dialog').scrollWidth <= document.getElementById('tasks-dialog').clientWidth), true, 'Mobile task panel must not overflow');
    await phone.locator('#tasks-close').click();

    await phone.evaluate(() => navigator.serviceWorker.ready);
    await phone.waitForFunction(() => Boolean(navigator.serviceWorker.controller));
    const cached = await phone.evaluate(async () => {
      const output = [];
      for (const name of await caches.keys()) for (const req of await (await caches.open(name)).keys()) output.push(new URL(req.url).pathname);
      return output;
    });
    assert(cached.includes('/'), 'PWA shell must be cached');
    assert(!cached.some(url => url.startsWith('/api/')), 'Private APIs must never be cached');
    await mobile.setOffline(true);
    await phone.reload();
    await phone.locator('#offline-banner').waitFor({ state: 'visible' });
    await mobile.setOffline(false);
    assert.deepEqual(errors, [], 'Browser JavaScript must not throw');
    console.log('Browser smoke passed: login, streaming tools, refresh, safe Markdown, stop, mobile drawer, PWA shell and private-cache boundary.');
    console.log('Screenshots: .cache/p1-*.png, .cache/p2-*.png, .cache/p3-*.png');
  } finally { await browser.close(); }
}
main().catch(error => { console.error(error.message); process.exitCode = 1; });
