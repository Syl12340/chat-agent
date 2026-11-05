const CACHE = 'pocket-shell-v25';
const SHELL = ['/', '/app.js', '/theme.js', '/tasks.js', '/companion.js', '/life.js', '/turns.js', '/activities.js', '/native.js', '/native-care.js', '/voice.js', '/device-auth.js', '/role-proposals.js', '/topics.js', '/chat-input.js', '/chat-ui.css', '/composer-preferences.js', '/markdown.js', '/math-render.js', '/richtext.css', '/vendor/katex/katex.mjs', '/vendor/katex/LICENSE.txt', '/ui/structures.js', '/desktop-workspace.js', '/desktop-workspace.css', '/ui/protocol.js', '/ui/state.js', '/ui/render.js', '/ui/quiz.js', '/ui/charts.js', '/ui/vectors.js', '/ui/ui.css', '/style.css', '/sw.js', '/manifest.webmanifest', '/icon.svg', '/icon-192.png', '/icon-512.png'];
self.addEventListener('install', event => {
  event.waitUntil(caches.open(CACHE).then(cache => cache.addAll(SHELL)));
  self.skipWaiting();
});
self.addEventListener('activate', event => {
  event.waitUntil(caches.keys().then(keys => Promise.all(keys.filter(key => key.startsWith('pocket-shell-') && key !== CACHE).map(key => caches.delete(key)))).then(() => self.clients.claim()));
});
self.addEventListener('fetch', event => {
  const request = event.request;
  const url = new URL(request.url);
  // Never store authenticated requests, API responses, or conversation history.
  if (request.method !== 'GET' || url.origin !== self.location.origin || request.headers.has('Authorization') || !SHELL.includes(url.pathname)) return;
  event.respondWith(fetch(request).then(response => {
    if (response.ok) {
      const copy = response.clone();
      event.waitUntil(caches.open(CACHE).then(cache => cache.put(url.pathname, copy)));
    }
    return response;
  }).catch(async () => (await caches.match(url.pathname)) || Response.error()));
});
