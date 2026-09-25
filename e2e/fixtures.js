const base = require('@playwright/test');
const { spawn } = require('node:child_process');
const path = require('node:path');

const BIN = path.resolve(__dirname, '.bin', 'uidemo');
const PASSWORD = 'long enough password';

async function waitFor(url) {
    for (let i = 0; i < 100; i++) {
        try {
            const res = await fetch(url + '/api/status');
            if (res.ok) return;
        } catch { /* not up yet */ }
        await new Promise((r) => setTimeout(r, 100));
    }
    throw new Error(`demo server did not start at ${url}`);
}

const test = base.test.extend({
    // One demo server per worker, on 18099 + worker index. Never 8080, and
    // not 8099 either: nextdash's own tests use it.
    demo: [async ({}, use, workerInfo) => {
        const port = 18099 + workerInfo.workerIndex;
        const url = `http://127.0.0.1:${port}`;
        const proc = spawn(BIN, ['-addr', `127.0.0.1:${port}`], { stdio: 'inherit' });
        try {
            await waitFor(url);
            await use({ url, port });
        } finally {
            proc.kill();
        }
    }, { scope: 'worker' }],

    baseURL: async ({ demo }, use) => { await use(demo.url); },

    // Fresh seeded state and no session before every test.
    page: async ({ page, demo }, use) => {
        const res = await fetch(`${demo.url}/__demo/reset`, { method: 'POST' });
        if (!res.ok) throw new Error(`reset failed: ${res.status}`);
        await use(page);
    },
});

/** Creates the admin account through the API; the cookie lands in the page's context. */
async function signIn(page) {
    const res = await page.request.post('/api/setup', {
        headers: { 'X-NextUpdate': '1' },
        data: { name: 'jordi', password: PASSWORD },
    });
    if (!res.ok()) throw new Error(`setup failed: ${res.status()} ${await res.text()}`);
}

module.exports = { test, expect: base.expect, signIn, PASSWORD };
