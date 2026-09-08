import { expect, test, type Page } from '@playwright/test';

// The burger menu is the single owner surface for Analysis runs, Settings,
// and Logout on every authenticated route, while Articles/back-to-library and
// Paste article stay outside it. These cases use the same mocked-session
// pattern as the other specs; unmocked reads hit the throwaway backend.
function response(body: unknown, status = 200) {
	return { status, contentType: 'application/json', body: JSON.stringify(body) };
}

const article = {
	id: 'article-id',
	title: 'Een rustige dag',
	source_language: 'nl',
	target_language: 'en',
	enrichment_status: 'ready',
	enrichment_error_code: '',
	created_at: '2026-01-01T00:00:00Z',
	updated_at: '2026-01-02T00:00:00Z',
	blocks: []
};

test.beforeEach(async ({ page }) => {
	await page.route('**/api/v1/auth/session', (route) => route.fulfill(response({ authenticated: true })));
});

async function mockArticlePage(page: Page): Promise<void> {
	await page.route('**/api/v1/articles/article-id', (route) => route.fulfill(response(article)));
}

function navigation(page: Page) {
	return page.getByRole('navigation', { name: 'Main navigation' });
}

function menuButton(page: Page) {
	return navigation(page).getByRole('button', { name: 'Menu' });
}

function menuPanel(page: Page) {
	return page.locator('#owner-menu');
}

async function openMenu(page: Page): Promise<void> {
	await menuButton(page).click();
	await expect(menuButton(page)).toHaveAttribute('aria-expanded', 'true');
	await expect(menuPanel(page)).toBeVisible();
}

async function expectOwnerItems(page: Page): Promise<void> {
	// Requested order: Analysis runs, Settings, Logout — real links/buttons.
	const labels = await menuPanel(page)
		.locator('a, button')
		.evaluateAll((elements) => elements.map((element) => element.textContent?.trim()));
	expect(labels).toEqual(['Analysis runs', 'Settings', 'Logout']);
	await expect(menuPanel(page).getByRole('link', { name: 'Analysis runs' })).toBeVisible();
	await expect(menuPanel(page).getByRole('link', { name: 'Settings' })).toBeVisible();
	await expect(menuPanel(page).getByRole('button', { name: 'Logout' })).toBeVisible();
}

test('keeps Articles and Paste article outside one owner menu on non-article routes', async ({ page }) => {
	await page.goto('/reader');
	const bar = navigation(page);
	await expect(menuButton(page)).toHaveAttribute('aria-expanded', 'false');
	await expect(bar.getByRole('link', { name: 'Articles' })).toBeVisible();
	await expect(bar.getByRole('link', { name: 'Paste article' })).toBeVisible();
	// The menu items only exist while the menu is open.
	await expect(bar.getByRole('link', { name: 'Analysis runs' })).toHaveCount(0);
	await expect(bar.getByRole('button', { name: 'Logout' })).toHaveCount(0);

	await openMenu(page);
	await expectOwnerItems(page);
});

test('offers the same owner menu on an article route above the reader content', async ({ page }) => {
	await mockArticlePage(page);
	await page.goto('/reader/article-id');
	await expect(page.getByText('Article reader')).toBeVisible();

	await openMenu(page);
	await expectOwnerItems(page);
	// Layering: the open panel sits above the article body and stays clickable.
	const panelBox = await menuPanel(page).boundingBox();
	expect(panelBox).not.toBeNull();
	expect(panelBox!.y).toBeGreaterThanOrEqual(0);
	await menuPanel(page).getByRole('link', { name: 'Settings' }).click();
	await expect(page).toHaveURL('/settings');
});

test('menu actions navigate and the menu closes on route change', async ({ page }) => {
	await page.goto('/reader');
	await openMenu(page);
	await menuPanel(page).getByRole('link', { name: 'Analysis runs' }).click();
	await expect(page).toHaveURL('/analysis-runs');
	await expect(menuButton(page)).toHaveAttribute('aria-expanded', 'false');
	await expect(menuPanel(page)).toHaveCount(0);

	await openMenu(page);
	await menuPanel(page).getByRole('link', { name: 'Settings' }).click();
	await expect(page).toHaveURL('/settings');
	await expect(menuButton(page)).toHaveAttribute('aria-expanded', 'false');
	await expect(menuPanel(page)).toHaveCount(0);
});

test('Escape closes the menu and returns focus; an outside click closes without focus return', async ({ page }) => {
	await page.goto('/reader');
	const button = menuButton(page);

	await openMenu(page);
	await page.keyboard.press('Escape');
	await expect(button).toHaveAttribute('aria-expanded', 'false');
	await expect(menuPanel(page)).toHaveCount(0);
	await expect(button).toBeFocused();

	await openMenu(page);
	await page.locator('main').click({ position: { x: 10, y: 10 } });
	await expect(button).toHaveAttribute('aria-expanded', 'false');
	await expect(menuPanel(page)).toHaveCount(0);
});

test('menu items are reachable with Tab in DOM order once the menu is open', async ({ page }) => {
	await page.goto('/reader');
	await openMenu(page);
	await menuButton(page).focus();
	await page.keyboard.press('Tab');
	await expect(menuPanel(page).getByRole('link', { name: 'Analysis runs' })).toBeFocused();
	await page.keyboard.press('Tab');
	await expect(menuPanel(page).getByRole('link', { name: 'Settings' })).toBeFocused();
	await page.keyboard.press('Tab');
	await expect(menuPanel(page).getByRole('button', { name: 'Logout' })).toBeFocused();
});

test('logout through the menu returns to the login page', async ({ page }) => {
	await page.context().addCookies([{ name: 'csrf_token', value: 'test-csrf-token', domain: 'localhost', path: '/' }]);
	await page.route('**/api/v1/auth/logout', (route) => route.fulfill(response({ ok: true })));
	await page.goto('/reader');
	await openMenu(page);
	await menuPanel(page).getByRole('button', { name: 'Logout' }).click();
	await expect(page).toHaveURL('/login');
	await expect(menuButton(page)).toHaveCount(0);
});

test('the login page never renders the owner menu', async ({ page }) => {
	await page.unroute('**/api/v1/auth/session');
	await page.route('**/api/v1/auth/session', (route) => route.fulfill(response({ authenticated: false })));
	await page.goto('/settings');
	await expect(page).toHaveURL(/\/login/);
	await expect(menuButton(page)).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Logout' })).toHaveCount(0);
});

test('the header and the open menu fit a 320px viewport on both route kinds', async ({ page }) => {
	await page.setViewportSize({ width: 320, height: 700 });
	await mockArticlePage(page);

	for (const path of ['/reader', '/reader/article-id']) {
		await page.goto(path);
		const overflow = await page.evaluate(() => document.documentElement.scrollWidth);
		expect(overflow).toBeLessThanOrEqual(320);

		await openMenu(page);
		await expectOwnerItems(page);
		const box = await menuPanel(page).boundingBox();
		expect(box).not.toBeNull();
		expect(box!.x).toBeGreaterThanOrEqual(0);
		expect(box!.x + box!.width).toBeLessThanOrEqual(320);
		expect(box!.y).toBeGreaterThanOrEqual(0);
		expect(box!.y + box!.height).toBeLessThanOrEqual(700);
	}
});
