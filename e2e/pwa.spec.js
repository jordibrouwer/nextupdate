const vm = require('node:vm');
const { test, expect, signIn } = require('./fixtures');

test('the demo serves the app shell', async ({ page }) => {
    const res = await page.goto('/');
    expect(res.status()).toBe(200);
    await expect(page).toHaveTitle('nextupdate');
    expect(res.headers()['content-security-policy']).toContain("script-src 'self'");
});

test('the manifest describes an installable app with real icons', async ({ page, request }) => {
    await page.goto('/');
    const href = await page.locator('link[rel="manifest"]').getAttribute('href');
    const res = await request.get(href);
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('application/manifest+json');
    const m = await res.json();
    expect(m).toMatchObject({ name: 'nextupdate', start_url: '/', scope: '/', display: 'standalone', theme_color: '#2563eb' });
    const plain = (size) => m.icons.some((i) => i.sizes === size && i.type === 'image/png' && !i.purpose);
    expect(plain('192x192'), 'a plain 192 icon').toBe(true);
    expect(plain('512x512'), 'a plain 512 icon').toBe(true);
    expect(m.icons.some((i) => i.sizes === 'any' && i.type === 'image/svg+xml')).toBe(true);
    expect(m.icons.some((i) => i.purpose === 'maskable' && i.sizes === '512x512')).toBe(true);
    for (const icon of m.icons) {
        const r = await request.get(icon.src);
        expect(r.status(), icon.src).toBe(200);
        expect(r.headers()['content-type'], icon.src).toContain(icon.type);
    }
});

test('the icons are valid PNGs of the right size', async ({ request }) => {
    for (const [file, size] of [['icon-192.png', 192], ['icon-512.png', 512], ['icon-maskable-512.png', 512], ['apple-touch-icon.png', 180]]) {
        const buf = await (await request.get(`/icons/${file}`)).body();
        expect(buf.subarray(1, 4).toString(), file).toBe('PNG');
        expect(buf.readUInt32BE(16), `${file} width`).toBe(size);
        expect(buf.readUInt32BE(20), `${file} height`).toBe(size);
    }
});

test('the service worker is served from the root as JavaScript and registers', async ({ page, request }) => {
    const res = await request.get('/sw.js');
    expect(res.status()).toBe(200);
    expect(res.headers()['content-type']).toContain('text/javascript');
    await signIn(page);
    await page.goto('/');
    await expect(page.getByTestId('check-button')).toBeVisible();
    const scope = await page.evaluate(async () => (await navigator.serviceWorker.ready).scope);
    expect(scope).toMatch(/\/$/);
});

test('the service worker shows a notification for a push message and opens the app on click', async ({ request }) => {
    // Run the worker's own code in Node with the globals it gets in a worker.
    // (The page's CSP rightly forbids evaluating strings, so this is not done in the browser.)
    const src = await (await request.get('/sw.js')).text();
    const listeners = {};
    const shown = [];
    const opened = [];
    let closed = false;
    const self = {
        addEventListener: (t, fn) => { listeners[t] = fn; },
        registration: { showNotification: (title, opts) => { shown.push({ title, opts }); return Promise.resolve(); } },
        clients: { matchAll: async () => [], openWindow: async (u) => { opened.push(u); return null; } },
        skipWaiting: () => {},
    };
    vm.runInNewContext(src, { self });

    let pending;
    listeners.push({ data: { json: () => ({ title: 'Update available: app', body: 'Details', url: 'https://nu.example', container: 'app' }) }, waitUntil: (p) => { pending = p; } });
    await pending;
    expect(shown).toHaveLength(1);
    expect(shown[0].title).toBe('Update available: app');
    expect(shown[0].opts.body).toBe('Details');
    expect(shown[0].opts.data.url).toBe('https://nu.example');

    listeners.push({ data: { json: () => { throw new Error('not json'); } }, waitUntil: (p) => { pending = p; } });
    await pending;
    expect(shown[1].title).toBe('nextupdate'); // a broken payload still shows something

    listeners.notificationclick({ notification: { close: () => { closed = true; }, data: { url: 'https://nu.example' } }, waitUntil: (p) => { pending = p; } });
    await pending;
    expect(closed).toBe(true);
    expect(opened).toEqual(['https://nu.example']);
});
