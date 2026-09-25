const { test, expect, signIn } = require('./fixtures');

test.beforeEach(async ({ page }) => {
    await signIn(page);
});

const row = (page, name) => page.locator(`a.row[data-name="${name}"]`);

test('groups the containers and selects the first one', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByTestId('group-breaking').locator('a.row')).toHaveCount(1);
    await expect(page.getByTestId('group-updates').locator('a.row')).toHaveCount(3);
    await expect(page.getByTestId('group-current').locator('a.row')).toHaveCount(2);
    await expect(page.getByTestId('group-breaking').locator('.group-title')).toContainText('Breaking');
    await expect(row(page, 'immich')).toContainText('1.98.0 to 2.0.0');
    await expect(page).toHaveURL(/#\/updates\/immich$/);
    await expect(row(page, 'immich')).toHaveAttribute('aria-current', 'true');
    await expect(page.getByTestId('detail')).toContainText('immich');
});

test('shows the breaking reasons and the release notes, and keeps untrusted markup inert', async ({ page }) => {
    const dialogs = [];
    page.on('dialog', (d) => { dialogs.push(d.message()); d.dismiss(); });
    await page.goto('/#/updates/immich');

    await expect(page.getByTestId('breaking-box')).toContainText('Major version change from 1.98.0 to 2.0.0.');
    const notes = page.getByTestId('changelog');
    await expect(notes).toContainText('Database migration runs on first start.');
    await expect(notes.locator('.release')).toHaveCount(2); // 2.0.0 and 1.99.0, not 1.98.0

    // The script line is visible text, not a script; the javascript: link is not a link.
    await expect(notes).toContainText('<script>alert(1)</script>');
    await expect(notes.locator('script')).toHaveCount(0);
    await expect(notes.locator('a[href^="javascript:" i]')).toHaveCount(0);
    await expect(notes.getByText('click me')).toBeVisible();
    const docs = notes.getByRole('link', { name: 'docs' });
    await expect(docs).toHaveAttribute('href', 'https://example.com/docs');
    await expect(docs).toHaveAttribute('target', '_blank');
    await expect(docs).toHaveAttribute('rel', 'noopener noreferrer');
    await expect(notes.locator('pre code')).toContainText('DB_HOST=database');
    expect(dialogs).toEqual([]);
});

test('a container without a known repo says there are no release notes', async ({ page }) => {
    await page.goto('/#/updates/flaky');
    await expect(page.getByTestId('changelog')).toContainText('No release notes found.');
    await expect(page.getByTestId('changelog')).not.toContainText('null');
});

test('updating a patch release runs in the background and moves the container to up to date', async ({ page }) => {
    await page.goto('/#/updates/sonarr');
    await page.getByTestId('update-button').click();
    await expect(row(page, 'sonarr').locator('.spinner')).toBeVisible();
    await expect(page.getByTestId('update-button')).toHaveAttribute('aria-busy', 'true');
    await expect(page.getByTestId('toast').filter({ hasText: 'Updated sonarr' })).toBeVisible({ timeout: 15000 });
    await expect(page.getByTestId('group-current').locator('a.row[data-name="sonarr"]')).toBeVisible();
    await expect(page.getByTestId('group-updates').locator('a.row[data-name="sonarr"]')).toHaveCount(0);
});

test('a breaking update asks first, and cancelling changes nothing', async ({ page }) => {
    await page.goto('/#/updates/immich');
    await page.getByTestId('update-button').click();
    const dialog = page.getByTestId('confirm-dialog');
    await expect(dialog).toContainText('Update immich?');
    await expect(dialog).toContainText('Major version change');
    await dialog.getByRole('button', { name: 'Cancel' }).click();
    await expect(dialog).toHaveCount(0);
    await expect(page.getByTestId('group-breaking').locator('a.row[data-name="immich"]')).toBeVisible();
    await expect(row(page, 'immich').locator('.spinner')).toHaveCount(0);

    await page.getByTestId('update-button').click();
    await page.getByTestId('confirm-yes').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'Updated immich' })).toBeVisible({ timeout: 15000 });
    await expect(page.getByTestId('group-current').locator('a.row[data-name="immich"]')).toBeVisible();
    await expect(page.getByTestId('group-breaking')).toHaveCount(0); // an empty group is not drawn
    await expect(page.locator('.list')).not.toContainText('null');
});

test('an update that fails its health check is reported as rolled back and stays available', async ({ page }) => {
    await page.goto('/#/updates/flaky');
    await page.getByTestId('update-button').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'Update of flaky was rolled back: verify: healthcheck unhealthy' })).toBeVisible({ timeout: 15000 });
    await expect(page.getByTestId('group-updates').locator('a.row[data-name="flaky"]')).toBeVisible();
});

test('a container that was updated before can be rolled back', async ({ page }) => {
    await page.goto('/#/updates/jellyfin');
    await expect(page.getByTestId('detail')).toContainText('Up to date');
    await page.getByTestId('rollback-button').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'Rolled back jellyfin' })).toBeVisible({ timeout: 15000 });
    await expect(page.getByTestId('rollback-button')).toBeVisible();
    await expect(page.getByTestId('update-button')).toHaveCount(0); // jellyfin has no seeded update to offer again
});

test('a container with no earlier update has no rollback button', async ({ page }) => {
    await page.goto('/#/updates/postgres');
    await expect(page.getByTestId('detail')).toContainText('postgres');
    await expect(page.getByTestId('rollback-button')).toHaveCount(0);
});

test('the policy select saves on change and survives a reload', async ({ page }) => {
    await page.goto('/#/updates/vaultwarden');
    await expect(page.getByTestId('policy-select')).toHaveValue('notify');
    await page.getByTestId('policy-select').selectOption('auto');
    await expect(page.getByTestId('toast').filter({ hasText: 'Policy for vaultwarden is now auto' })).toBeVisible();
    await page.reload();
    await expect(page.getByTestId('policy-select')).toHaveValue('auto');
});

test('container settings validate and save', async ({ page }) => {
    await page.goto('/#/updates/vaultwarden');
    await page.getByText('Container settings').click();
    await page.getByLabel('Repository').fill('not a repo');
    await page.getByTestId('settings-save').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'owner/name' })).toBeVisible();

    await page.getByLabel('Repository').fill('dani-garcia/vaultwarden');
    await page.getByLabel('Health check URL').fill('http://vaultwarden:80/alive');
    await page.getByLabel('Verify window').fill('90');
    await page.getByTestId('settings-save').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'Settings for vaultwarden saved' })).toBeVisible();
    await page.reload();
    await page.getByText('Container settings').click();
    await expect(page.getByLabel('Health check URL')).toHaveValue('http://vaultwarden:80/alive');
    await expect(page.getByLabel('Verify window')).toHaveValue('90');
});

test('keyboard: j and k move, u updates, c checks', async ({ page }) => {
    await page.goto('/');
    await expect(page).toHaveURL(/#\/updates\/immich$/);
    await page.keyboard.press('j');
    await expect(page).toHaveURL(/#\/updates\/(flaky|sonarr|vaultwarden)$/);
    const second = page.url().split('/').pop();
    await page.keyboard.press('k');
    await expect(page).toHaveURL(/#\/updates\/immich$/);
    await page.keyboard.press('ArrowDown');
    await expect(page).toHaveURL(new RegExp(`#/updates/${second}$`));

    await page.keyboard.press('c');
    await expect(page.getByTestId('check-button')).toHaveAttribute('aria-busy', 'true');
    await expect(page.getByTestId('check-button')).toHaveAttribute('aria-busy', 'false', { timeout: 10000 });

    await page.goto('/#/updates/sonarr');
    await page.keyboard.press('u');
    await expect(row(page, 'sonarr').locator('.spinner')).toBeVisible();
    await expect(page.getByTestId('toast').filter({ hasText: 'Updated sonarr' })).toBeVisible({ timeout: 15000 });
});

test('typing in a field does not trigger shortcuts', async ({ page }) => {
    await page.goto('/#/updates/sonarr');
    await page.getByText('Container settings').click();
    await page.getByLabel('Repository').click();
    await page.keyboard.type('ju');
    await expect(row(page, 'sonarr').locator('.spinner')).toHaveCount(0);
    await expect(page).toHaveURL(/#\/updates\/sonarr$/);
});

test('the legend lists the shortcuts', async ({ page }) => {
    await page.goto('/');
    const legend = page.locator('.legend');
    for (const key of ['j', 'k', 'u', 'c']) await expect(legend.locator('kbd', { hasText: new RegExp(`^${key}$`) })).toBeVisible();
});
