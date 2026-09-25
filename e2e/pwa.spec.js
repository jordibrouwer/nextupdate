const { test, expect } = require('./fixtures');

test('the demo serves the app shell', async ({ page }) => {
    const res = await page.goto('/');
    expect(res.status()).toBe(200);
    await expect(page).toHaveTitle('nextupdate');
    expect(res.headers()['content-security-policy']).toContain("script-src 'self'");
});
