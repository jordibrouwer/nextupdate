const { test, expect, signIn } = require('./fixtures');

test.beforeEach(async ({ page }) => {
    await signIn(page);
});

async function addWebhook(page, demo, name = 'Sink') {
    await page.getByTestId('add-notifier').click();
    const form = page.getByTestId('notifier-form');
    await form.getByLabel('Name').fill(name);
    await form.getByLabel('Type').selectOption('webhook');
    await form.getByLabel('URL').fill(`${demo.url}/__demo/sink`);
    await form.getByRole('button', { name: 'Save' }).click();
    await expect(page.getByTestId('notifier-row').filter({ hasText: name })).toBeVisible();
}

test('adds a notifier, sends a test message and deletes it', async ({ page, demo }) => {
    await page.goto('/#/settings');
    await expect(page.getByTestId('notifier-row')).toHaveCount(0);
    await addWebhook(page, demo);

    const row = page.getByTestId('notifier-row').filter({ hasText: 'Sink' });
    await expect(row).toContainText('Webhook');
    await row.getByRole('button', { name: 'Test' }).click();
    await expect(page.getByTestId('toast').filter({ hasText: 'Test message sent to Sink' })).toBeVisible();
    const received = await (await fetch(`${demo.url}/__demo/sink`)).json();
    expect(received.some((b) => b.includes('nextupdate test'))).toBe(true);

    await row.getByRole('button', { name: 'Delete' }).click();
    await page.getByTestId('confirm-yes').click();
    await expect(page.getByTestId('notifier-row')).toHaveCount(0);
});

test('the form validates required fields with the server message', async ({ page }) => {
    await page.goto('/#/settings');
    await page.getByTestId('add-notifier').click();
    const form = page.getByTestId('notifier-form');
    await form.getByLabel('Name').fill('Broken');
    await form.getByLabel('Type').selectOption('ntfy');
    await form.getByLabel('Server URL').fill('https://ntfy.example');
    await form.getByLabel('Topic').fill('   ');
    await form.getByRole('button', { name: 'Save' }).click();
    await expect(form.getByTestId('form-error')).toContainText('Topic');
});

test('secrets are masked, kept when left empty and replaced when typed', async ({ page }) => {
    await page.goto('/#/settings');
    await page.getByTestId('add-notifier').click();
    const form = page.getByTestId('notifier-form');
    await form.getByLabel('Name').fill('Phone');
    await form.getByLabel('Type').selectOption('ntfy');
    await form.getByLabel('Server URL').fill('https://ntfy.example');
    await form.getByLabel('Topic').fill('updates');
    await form.getByLabel('Access token').fill('s3cret-token');
    await form.getByRole('button', { name: 'Save' }).click();

    const row = page.getByTestId('notifier-row').filter({ hasText: 'Phone' });
    await expect(row).toBeVisible();
    await expect(page.locator('body')).not.toContainText('s3cret-token');

    await row.getByRole('button', { name: 'Edit' }).click();
    const edit = page.getByTestId('notifier-form');
    await expect(edit.getByLabel('Access token')).toHaveValue('');
    await expect(edit.getByLabel('Access token')).toHaveAttribute('placeholder', 'Unchanged');
    await edit.getByLabel('Topic').fill('updates-2');
    await edit.getByRole('button', { name: 'Save' }).click();

    await expect(page.getByTestId('notifier-row').filter({ hasText: 'Phone' })).toBeVisible();
    const list = await (await page.request.get('/api/notifiers')).json();
    expect(list[0].config.topic).toBe('updates-2');
    expect(list[0].config.token).toBe('********'); // still set
});

test('the enabled checkbox saves on change', async ({ page, demo }) => {
    await page.goto('/#/settings');
    await addWebhook(page, demo);
    const row = page.getByTestId('notifier-row').filter({ hasText: 'Sink' });
    await row.getByLabel('Enabled').uncheck();
    await expect.poll(async () => (await (await page.request.get('/api/notifiers')).json())[0].enabled).toBe(false);
});

test('the widget card shows a masked token that can be revealed, copied and replaced', async ({ page, context }) => {
    await context.grantPermissions(['clipboard-read', 'clipboard-write']);
    await page.goto('/#/settings');
    const card = page.locator('section.card', { has: page.getByRole('heading', { name: 'nextdash widget' }) });
    await expect(card).toContainText('/api/widget');
    const token = page.getByTestId('widget-token');
    await expect(token).toHaveText(/^•+$/);
    await card.getByRole('button', { name: 'Show' }).click();
    const first = (await token.textContent()).trim();
    expect(first.length).toBeGreaterThan(20);

    await card.getByRole('button', { name: 'Copy token' }).click();
    await expect(page.getByTestId('toast').filter({ hasText: 'Token copied' })).toBeVisible();

    await card.getByRole('button', { name: 'Create a new token' }).click();
    await page.getByTestId('confirm-yes').click();
    await expect(page.getByTestId('toast').filter({ hasText: 'New widget token created' })).toBeVisible();
    await card.getByRole('button', { name: /Show|Hide/ }).first().click();
    await expect.poll(async () => (await token.textContent()).trim()).not.toBe(first);

    const old = await page.request.get(`/api/widget?token=${first}`);
    expect(old.status()).toBe(401);
});

test('the account card names the user and signs out', async ({ page }) => {
    await page.goto('/#/settings');
    await expect(page.locator('section.card', { hasText: 'Account' })).toContainText('Signed in as jordi');
    await page.locator('section.card', { hasText: 'Account' }).getByRole('button', { name: 'Sign out' }).click();
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
});
