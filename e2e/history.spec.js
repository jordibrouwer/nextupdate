const { test, expect, signIn } = require('./fixtures');

test.beforeEach(async ({ page }) => {
    await signIn(page);
});

test('lists the earlier update with its steps', async ({ page }) => {
    await page.goto('/#/history');
    const entries = page.getByTestId('history-entry');
    await expect(entries).toHaveCount(1);
    const e = entries.first();
    await expect(e).toContainText('jellyfin');
    await expect(e).toContainText('Updated');
    await expect(e).toContainText('2 days ago');
    await e.getByText('Steps').click();
    await expect(e).toContainText('verify jellyfin');
});

test('shows new entries, newest first, with the reason of a rollback', async ({ page }) => {
    await page.goto('/#/updates/flaky');
    await page.getByTestId('update-button').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'was rolled back' })).toBeVisible({ timeout: 15000 });
    await page.getByRole('link', { name: 'History' }).click();
    const entries = page.getByTestId('history-entry');
    await expect(entries).toHaveCount(2);
    await expect(entries.first()).toContainText('flaky');
    await expect(entries.first()).toContainText('Rolled back');
    await expect(entries.first()).toContainText('verify: healthcheck unhealthy');
    await expect(entries.nth(1)).toContainText('jellyfin');
});

test('the container filter narrows the list', async ({ page }) => {
    await page.goto('/#/updates/sonarr');
    await page.getByTestId('update-button').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'Updated sonarr' })).toBeVisible({ timeout: 15000 });
    await page.getByRole('link', { name: 'History' }).click();
    await expect(page.getByTestId('history-entry')).toHaveCount(2);
    await page.getByLabel('Container').selectOption('sonarr');
    await expect(page.getByTestId('history-entry')).toHaveCount(1);
    await expect(page.getByTestId('history-entry').first()).toContainText('sonarr');
});
