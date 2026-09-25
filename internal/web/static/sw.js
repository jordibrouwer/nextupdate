// nextupdate service worker: shows push notifications. It never caches anything.
self.addEventListener('install', () => self.skipWaiting());

self.addEventListener('push', (event) => {
    let payload = {};
    try {
        payload = event.data ? event.data.json() : {};
    } catch (e) {
        payload = { body: event.data ? String(event.data) : '' };
    }
    const title = payload.title || 'nextupdate';
    event.waitUntil(self.registration.showNotification(title, {
        body: payload.body || '',
        icon: '/icons/icon-192.png',
        badge: '/icons/icon-192.png',
        tag: payload.container ? `nextupdate-${payload.container}` : 'nextupdate',
        data: { url: payload.url || '/' },
    }));
});

self.addEventListener('notificationclick', (event) => {
    event.notification.close();
    const url = (event.notification.data && event.notification.data.url) || '/';
    event.waitUntil((async () => {
        const open = await self.clients.matchAll({ type: 'window', includeUncontrolled: true });
        for (const c of open) {
            if ('focus' in c) return c.focus();
        }
        return self.clients.openWindow(url);
    })());
});

// A fetch handler keeps the app installable. It passes every request through.
self.addEventListener('fetch', () => {});
