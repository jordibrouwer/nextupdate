const { test, expect, signIn, PASSWORD } = require('./fixtures');

test('the first visit asks for an admin account, then signs in', async ({ page }) => {
    await page.goto('/');
    await expect(page.getByRole('heading', { name: 'Create your admin account' })).toBeVisible();

    await page.getByLabel('Name').fill('jordi');
    await page.getByLabel('Password').fill('short');
    await page.getByRole('button', { name: 'Create account' }).click();
    await expect(page.getByTestId('auth-error')).toContainText('at least 10 characters');

    await page.getByLabel('Password').fill(PASSWORD);
    await page.getByRole('button', { name: 'Create account' }).click();
    await expect(page.getByTestId('check-button')).toBeVisible();
    await expect(page).toHaveURL(/#\/updates/);
});

test('signing out returns to the sign-in form, and a wrong password says so', async ({ page }) => {
    await signIn(page);
    await page.goto('/');
    await expect(page.getByTestId('check-button')).toBeVisible();

    await page.getByRole('button', { name: 'Sign out' }).click();
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();

    await page.getByLabel('Name').fill('jordi');
    await page.getByLabel('Password').fill('not the password');
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByTestId('auth-error')).toContainText("don't match");

    await page.getByLabel('Password').fill(PASSWORD);
    await page.getByRole('button', { name: 'Sign in' }).click();
    await expect(page.getByTestId('check-button')).toBeVisible();
});

test('a session that ends while the app is open goes back to sign in', async ({ page }) => {
    await signIn(page);
    await page.goto('/');
    await expect(page.getByTestId('check-button')).toBeVisible();
    await page.context().clearCookies();
    // Navigate through the URL rather than clicking a link: the background poll may
    // notice the ended session first and replace the page, which is also a pass.
    await page.evaluate(() => { location.hash = '#/history'; });
    await expect(page.getByRole('heading', { name: 'Sign in' })).toBeVisible();
});

test('the theme button cycles and the choice survives a reload', async ({ page }) => {
    await signIn(page);
    await page.goto('/');
    const html = page.locator('html');
    await page.getByRole('button', { name: /Theme/ }).click(); // auto to light
    await expect(html).toHaveAttribute('data-theme', 'light');
    await page.getByRole('button', { name: /Theme/ }).click(); // light to dark
    await expect(html).toHaveAttribute('data-theme', 'dark');
    await page.reload();
    await expect(html).toHaveAttribute('data-theme', 'dark');
    await page.getByRole('button', { name: /Theme/ }).click(); // dark to auto
    await expect(html).not.toHaveAttribute('data-theme', /.+/);
});
