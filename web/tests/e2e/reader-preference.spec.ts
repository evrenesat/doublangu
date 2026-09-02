import { expect, test } from '@playwright/test';

const articleID = 'preference-article-id';

function response(body: unknown, status = 200) {
	return { status, contentType: 'application/json', body: JSON.stringify(body) };
}

function readyArticle() {
	return {
		id: articleID,
		title: 'Voorkeuren',
		source_language: 'nl',
		target_language: 'en',
		enrichment_status: 'ready',
		enrichment_error_code: '',
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-02T00:00:00Z',
		blocks: [{
			id: 'block-id',
			article_id: articleID,
			block_index: 0,
			kind: 'paragraph',
			source_text: 'Hoi.',
			annotations: [],
			sentences: [],
			occurrences: [],
			analysis_status: 'ready',
			analysis_error_code: '',
			has_analysis: false,
			analysis_is_current: false,
			published_analysis_revision: '',
			published_analysis_model: '',
			published_analysis_effort: '',
			published_at: ''
		}],
		content_hash: 'content-hash',
		analysis_status: 'ready',
		analysis_revision: 'reader.analysis.v3',
		analysis_error_code: '',
		analysis_model: 'model-a',
		analysis_effort: 'low',
		analysis_progress: { total_paragraphs: 1, completed_paragraphs: 0, current_block_index: -1, failed_block_index: -1 },
		narration_status: 'not_requested',
		narration_error_code: '',
		sentences: [],
		occurrences: [],
		narration: { status: 'not_requested', error_code: '', sentence_count: 0, ready_count: 0, duration_ms: 0, size_bytes: 0, reclaimable_bytes: 0 }
	};
}

test.beforeEach(async ({ page }) => {
	await page.route('**/api/v1/auth/session', (route) => route.fulfill(response({ authenticated: true })));
	await page.route(`**/api/v1/articles/${articleID}`, (route) => route.fulfill(response(readyArticle())));
});

test('pronounce on hover defaults on and a failed save rolls back with an inline error', async ({ page }) => {
	let serverEnabled = true;
	await page.route('**/api/v1/reader/settings', async (route) => {
		if (route.request().method() === 'PUT') {
			serverEnabled = true; // The server rejects the attempted change.
			return route.fulfill(response({ error: { code: 'v1.internal' } }, 500));
		}
		return route.fulfill(response({ pronounce_on_hover: serverEnabled, updated_at: '2026-01-01T00:00:00Z' }));
	});
	await page.goto(`/reader/${articleID}`);
	const toggle = page.locator('.hover-toggle input');
	await expect(toggle).toBeChecked();
	await toggle.uncheck();
	await expect(toggle).toBeChecked({ timeout: 5000 }); // rolled back
	await expect(page.locator('.reader-error[role="alert"]')).toBeVisible();
});

test('a saved off value survives a reload through the server setting', async ({ page }) => {
	let serverEnabled = true;
	let updatedAt = '2026-01-01T00:00:00Z';
	await page.route('**/api/v1/reader/settings', async (route) => {
		if (route.request().method() === 'PUT') {
			serverEnabled = false;
			updatedAt = '2026-01-02T00:00:00Z';
		}
		return route.fulfill(response({ pronounce_on_hover: serverEnabled, updated_at: updatedAt }));
	});
	await page.goto(`/reader/${articleID}`);
	const toggle = page.locator('.hover-toggle input');
	await expect(toggle).toBeChecked();
	await toggle.uncheck();
	await expect(toggle).not.toBeChecked();
	await page.reload();
	await expect(toggle).not.toBeChecked({ timeout: 5000 });
});

test('the settings page mirrors the same owner preference', async ({ page }) => {
	let serverEnabled = true;
	await page.route('**/api/v1/reader/settings', async (route) => {
		if (route.request().method() === 'PUT') {
			serverEnabled = !serverEnabled;
		}
		return route.fulfill(response({ pronounce_on_hover: serverEnabled, updated_at: '2026-01-01T00:00:00Z' }));
	});
	await page.route('**/api/v1/analysis/models', (route) => route.fulfill(response({ models: [], retrieved_at: '', stale: false, last_error: '' })));
	await page.route('**/api/v1/analysis/settings', (route) => route.fulfill(response({ model: '', effort: 'medium', updated_at: '2026-01-01T00:00:00Z' })));
	await page.route('**/api/v1/analysis/runs*', (route) => route.fulfill(response({ runs: [], next_cursor: '' })));
	await page.route('**/health', (route) => route.fulfill(response({ core_ready: true, loader_ready: true, schema_available: true, registry_state: 'ok', plugin_count: 0, plugin_ids: [] })));
	await page.goto('/settings');
	const checkbox = page.locator('.reader-settings .preference-row input');
	await expect(checkbox).toBeChecked();
	await checkbox.uncheck();
	await expect(checkbox).not.toBeChecked();
});

test('a slow initial settings load cannot revert a newer toggle', async ({ page }) => {
	// The initial GET is held open behind an explicit gate; the toggle PUT
	// settles first with the disabled value, and only then is the stale
	// enabled response released. The final assertion runs strictly after the
	// stale GET has been delivered, so it genuinely proves the load cannot
	// revert the newer save.
	let putSettledResolve: () => void = () => {};
	const putSettled = new Promise<void>((resolve) => { putSettledResolve = resolve; });
	let staleDeliveredResolve: () => void = () => {};
	const staleDelivered = new Promise<void>((resolve) => { staleDeliveredResolve = resolve; });
	let releaseStaleResolve: () => void = () => {};
	const releaseStale = new Promise<void>((resolve) => { releaseStaleResolve = resolve; });

	let firstGET = true;
	let putSawDisabled = false;
	await page.route('**/api/v1/reader/settings', async (route) => {
		if (route.request().method() === 'GET') {
			if (firstGET) {
				firstGET = false;
				await releaseStale;
			}
			await route.fulfill(response({ pronounce_on_hover: true, updated_at: '2026-01-01T00:00:00Z' }));
			staleDeliveredResolve();
			return;
		}
		putSawDisabled = true;
		await route.fulfill(response({ pronounce_on_hover: false, updated_at: '2026-01-02T00:00:00Z' }));
		putSettledResolve();
	});
	await page.goto(`/reader/${articleID}`);
	const toggle = page.locator('.hover-toggle input');
	await expect(toggle).toBeChecked();
	await toggle.uncheck();
	await expect(toggle).not.toBeChecked();
	await putSettled; // the successful PUT has been applied by the reader
	expect(putSawDisabled).toBe(true);
	releaseStaleResolve(); // now deliver the stale enabled snapshot
	await staleDelivered; // ...and wait until the stale GET is fully handled
	await expect(toggle).not.toBeChecked({ timeout: 2000 });
});
