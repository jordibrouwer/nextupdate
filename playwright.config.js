// @ts-check
const { defineConfig } = require('@playwright/test');

module.exports = defineConfig({
    testDir: 'e2e',
    testMatch: /.*\.spec\.js$/,
    timeout: 30_000,
    fullyParallel: false,
    workers: Number(process.env.PW_WORKERS || 2),
    retries: 0,
    reporter: 'line',
    globalSetup: require.resolve('./e2e/global-setup.js'),
    use: { headless: true, viewport: { width: 1280, height: 800 }, trace: 'retain-on-failure' },
});
