import { expect, test, type Locator, type Page } from '@playwright/test';

const library = { id: 'library-id', name: 'Dutch Library', source_language: 'nl', target_language: 'en', description: 'Learn Dutch', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-02T00:00:00Z' };
const work = { id: 'work-id', library_id: library.id, title: 'De Avonden', author: 'Gerard Reve', kind: 'audiobook', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-02T00:00:00Z' };
const edition = { id: 'edition-id', work_id: work.id, name: 'Original', language: 'nl', format: 'audiobook', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-02T00:00:00Z' };
const chapter = { id: 'chapter-id', edition_id: edition.id, title: 'Chapter 1', chapter_number: 1, start_ms: 0, end_ms: 120000, duration_ms: 120000, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-02T00:00:00Z' };
const createdLibrary = { ...library, id: 'created library' };
const diagnostics = { core_ready: true, loader_ready: true, schema_available: true, registry_state: 'active', plugin_count: 2, registration_count: 5, plugin_ids: ['sample-plugin'] };

function response(body: unknown, status = 200) {
	return { status, contentType: 'application/json', body: JSON.stringify(body) };
}

test.beforeEach(async ({ page }) => {
	await page.route('**/api/v1/auth/session', (route) => route.fulfill(response({ authenticated: true })));
});

async function mockLibraryAPI(page: Page): Promise<void> {
	await page.context().addCookies([{ name: 'csrf_token', value: 'test-csrf-token', domain: 'localhost', path: '/' }]);
	await page.route('**/api/v1/libraries', (route) => route.fulfill(
		route.request().method() === 'GET' ? response([library]) : response(createdLibrary, 201)
	));
	await page.route(`**/api/v1/libraries/${library.id}`, (route) => route.fulfill(response(library)));
	await page.route(`**/api/v1/libraries/${library.id}/works`, (route) => route.fulfill(response([work])));
	await page.route('**/api/v1/libraries/created%20library', (route) => route.fulfill(response(createdLibrary)));
	await page.route('**/api/v1/libraries/created%20library/works', (route) => route.fulfill(response([])));
	await page.route(`**/api/v1/works/${work.id}/editions`, (route) => route.fulfill(response([edition])));
	await page.route(`**/api/v1/editions/${edition.id}/chapters`, (route) => route.fulfill(response([chapter])));
	await page.route('**/health', (route) => route.fulfill(response(diagnostics)));
}

async function tabTo(page: Page, target: Locator): Promise<void> {
	for (let count = 0; count < 12; count += 1) {
		await page.keyboard.press('Tab');
		if (await target.evaluate((element) => element === document.activeElement)) return;
	}
	await expect(target).toBeFocused();
}

test('shows library data and the creation route target', async ({ page }) => {
	await mockLibraryAPI(page);
	await page.goto('/library');
	await expect(page.getByRole('heading', { name: 'Library' })).toBeVisible();
	await expect(page.getByRole('link', { name: 'Dutch Library' })).toHaveAttribute('href', `/library/${library.id}`);
	await expect(page.getByRole('link', { name: 'Create library' })).toHaveAttribute('href', '/library/new');
});

test('uses real Tab input to reach a library link', async ({ page }) => {
	await mockLibraryAPI(page);
	await page.goto('/library');
	await tabTo(page, page.getByRole('link', { name: 'Dutch Library' }));
});

test('shows an empty state that links to the new-library route', async ({ page }) => {
	await mockLibraryAPI(page);
	await page.route('**/api/v1/libraries', (route) => route.fulfill(response([])));
	await page.goto('/library');
	await expect(page.getByText('No libraries yet.')).toBeVisible();
	await expect(page.getByRole('link', { name: 'Create your first library' })).toHaveAttribute('href', '/library/new');
});

test('shows a retryable library-list error', async ({ page }) => {
	await mockLibraryAPI(page);
	await page.route('**/api/v1/libraries', (route) => route.fulfill(response({ error: 'Server error', code: 'v1.internal' }, 500)));
	await page.goto('/library');
	await expect(page.getByRole('alert')).toContainText('Server error');
	await expect(page.getByRole('button', { name: 'Retry' })).toBeVisible();
});

test('shows client-side required-field validation when creating a library', async ({ page }) => {
	await mockLibraryAPI(page);
	await page.goto('/library/new');
	await page.getByLabel('Name').fill('');
	await page.getByLabel('Source language').selectOption('nl');
	await page.getByLabel('Target language').selectOption('en');
	await page.getByRole('button', { name: 'Create library' }).click();
	await expect(page.getByRole('alert')).toContainText('required');
});

test('submits a library and navigates to its encoded detail target', async ({ page }) => {
	await mockLibraryAPI(page);
	await page.goto('/library/new');
	await page.getByLabel('Name').fill('New library');
	await page.getByLabel('Source language').selectOption('nl');
	await page.getByLabel('Target language').selectOption('en');
	await page.getByRole('button', { name: 'Create library' }).click();
	await expect(page).toHaveURL('/library/created%20library');
});

test('shows library detail metadata and the back-link target', async ({ page }) => {
	await mockLibraryAPI(page);
	await page.goto(`/library/${library.id}`);
	await expect(page.getByRole('heading', { name: 'Dutch Library' })).toBeVisible();
	await expect(page.getByRole('link', { name: '← Back to library' })).toHaveAttribute('href', '/library');
});

test('uses Enter and Space to expand the work and edition tree', async ({ page }) => {
	await mockLibraryAPI(page);
	await page.goto(`/library/${library.id}`);
	const workToggle = page.locator('.work-toggle');
	await tabTo(page, workToggle);
	await page.keyboard.press('Enter');
	await expect(workToggle).toHaveAttribute('aria-expanded', 'true');
	const editionToggle = page.locator('.edition-toggle');
	await tabTo(page, editionToggle);
	await page.keyboard.press('Space');
	await expect(editionToggle).toHaveAttribute('aria-expanded', 'true');
	await expect(page.getByText('Chapter 1')).toBeVisible();
});

test('shows a loading state while editions are pending', async ({ page }) => {
	await mockLibraryAPI(page);
	let release!: () => void;
	const pending = new Promise<void>((resolve) => { release = resolve; });
	await page.route(`**/api/v1/works/${work.id}/editions`, async (route) => { await pending; await route.fulfill(response([edition])); });
	await page.goto(`/library/${library.id}`);
	await page.locator('.work-toggle').click();
	await expect(page.getByRole('status')).toContainText('Loading editions');
	release();
});

test('shows an empty editions state', async ({ page }) => {
	await mockLibraryAPI(page);
	await page.route(`**/api/v1/works/${work.id}/editions`, (route) => route.fulfill(response([])));
	await page.goto(`/library/${library.id}`);
	await page.locator('.work-toggle').click();
	await expect(page.getByText('No editions.')).toBeVisible();
});

test('shows a retryable editions error instead of an empty state', async ({ page }) => {
	await mockLibraryAPI(page);
	await page.route(`**/api/v1/works/${work.id}/editions`, (route) => route.fulfill(response({ error: 'Editions unavailable', code: 'v1.internal' }, 500)));
	await page.goto(`/library/${library.id}`);
	await page.locator('.work-toggle').click();
	await expect(page.getByRole('alert')).toContainText('Editions unavailable');
	await expect(page.getByRole('button', { name: 'Retry loading editions' })).toBeVisible();
	await expect(page.getByText('No editions.')).toHaveCount(0);
});

test('shows a loading state while chapters are pending', async ({ page }) => {
	await mockLibraryAPI(page);
	let release!: () => void;
	const pending = new Promise<void>((resolve) => { release = resolve; });
	await page.route(`**/api/v1/editions/${edition.id}/chapters`, async (route) => { await pending; await route.fulfill(response([chapter])); });
	await page.goto(`/library/${library.id}`);
	await page.locator('.work-toggle').click();
	await page.locator('.edition-toggle').click();
	await expect(page.getByRole('status')).toContainText('Loading chapters');
	release();
});

test('shows an empty chapters state', async ({ page }) => {
	await mockLibraryAPI(page);
	await page.route(`**/api/v1/editions/${edition.id}/chapters`, (route) => route.fulfill(response([])));
	await page.goto(`/library/${library.id}`);
	await page.locator('.work-toggle').click();
	await page.locator('.edition-toggle').click();
	await expect(page.getByText('No chapters.')).toBeVisible();
});

test('shows a retryable chapters error instead of an empty state', async ({ page }) => {
	await mockLibraryAPI(page);
	await page.route(`**/api/v1/editions/${edition.id}/chapters`, (route) => route.fulfill(response({ error: 'Chapters unavailable', code: 'v1.internal' }, 500)));
	await page.goto(`/library/${library.id}`);
	await page.locator('.work-toggle').click();
	await page.locator('.edition-toggle').click();
	await expect(page.getByRole('alert')).toContainText('Chapters unavailable');
	await expect(page.getByRole('button', { name: 'Retry loading chapters' })).toBeVisible();
	await expect(page.getByText('No chapters.')).toHaveCount(0);
});

test('keeps developer diagnostics out of the learner navigation', async ({ page }) => {
	await mockLibraryAPI(page);
	await page.route('**/health', (route) => route.fulfill(response({ ...diagnostics, plugin_count: 0, plugin_ids: [] })));
	await page.goto('/settings');
	await expect(page.getByText('No plugins are loaded.')).toBeVisible();
	await expect(page.getByRole('link', { name: 'Doublangu reader' })).toHaveAttribute('href', '/reader');
	await expect(page.getByRole('banner').getByRole('link', { name: 'Plugins' })).toHaveCount(0);
	await expect(page.getByRole('button', { name: 'Sign out' })).toBeVisible();
});
