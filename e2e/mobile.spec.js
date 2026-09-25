const { test, expect, signIn } = require('./fixtures');

test.use({ viewport: { width: 390, height: 800 } });

test.beforeEach(async ({ page }) => {
    await signIn(page);
});

test('the list shows first, tapping a container opens its details, back returns', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('a.row[data-name="immich"]')).toBeVisible();
    await expect(page.getByTestId('detail')).toBeHidden();
    await expect(page).toHaveURL(/#\/updates$/); // no automatic selection on a phone

    await page.locator('a.row[data-name="sonarr"]').click();
    await expect(page.getByTestId('detail')).toBeVisible();
    await expect(page.getByTestId('detail')).toContainText('sonarr');
    await expect(page.locator('.list-pane')).toBeHidden();

    await page.getByTestId('back').click();
    await expect(page.locator('.list-pane')).toBeVisible();
    await expect(page.getByTestId('detail')).toBeHidden();
});

test('nothing overflows the screen sideways', async ({ page }) => {
    for (const hash of ['#/updates', '#/updates/immich', '#/history', '#/settings']) {
        await page.goto(`/${hash}`);
        await expect(page.getByTestId('check-button')).toBeVisible();
        await page.waitForLoadState('networkidle');
        const overflow = await page.evaluate(() => document.documentElement.scrollWidth - window.innerWidth);
        expect(overflow, hash).toBeLessThanOrEqual(0);
    }
});

test('actions stay usable with a touch-sized target', async ({ page }) => {
    await page.goto('/#/updates/sonarr');
    const box = await page.getByTestId('update-button').boundingBox();
    expect(box.height).toBeGreaterThanOrEqual(36);
});
