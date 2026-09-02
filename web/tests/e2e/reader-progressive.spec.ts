import { expect, test, type Page } from '@playwright/test';

const articleID = 'progressive-article-id';
const blockA = 'block-a';
const blockB = 'block-b';

function response(body: unknown, status = 200) {
	return { status, contentType: 'application/json', body: JSON.stringify(body) };
}

const occurrence = (overrides: Record<string, unknown> = {}) => ({
	id: 'occ-a',
	article_block_id: blockA,
	article_sentence_id: null,
	semantic_sense_id: null,
	kind: 'word',
	role: 'token',
	shadow_policy: 'token',
	shadow_text: 'sofa',
	subtitle_suppression_reason: 'none',
	canonical_pronunciation_text: '',
	context_pronunciation_key: '',
	confidence_milli: 900,
	sense: null,
	learning_state: null,
	show_shadow: true,
	member_occurrence_ids: [],
	pronunciation: null,
	spans: [{ id: 'occ-span-a', article_occurrence_id: 'occ-a', span_index: 0, start_utf16: 3, end_utf16: 7, source_text: 'bank' }],
	...overrides
});

function paragraphBlock(id: string, index: number, source: string, published: boolean, status: string, progress: number) {
	return {
		id,
		article_id: articleID,
		block_index: index,
		kind: 'paragraph',
		source_text: source,
		annotations: [],
		sentences: published
			? [{ id: `sentence-${id}`, article_block_id: id, sentence_index: 0, start_utf16: 0, end_utf16: source.length, source_text: source, source_hash: 'h', audio: null }]
			: [{ id: `sentence-${id}`, article_block_id: id, sentence_index: 0, start_utf16: 0, end_utf16: source.length, source_text: source, source_hash: 'h', audio: null }],
		occurrences: published
			? index === 0
				? [occurrence({ article_block_id: id, id: `occ-${id}`, shadow_text: 'sofa', spans: [{ id: `span-${id}`, article_occurrence_id: `occ-${id}`, span_index: 0, start_utf16: 3, end_utf16: 7, source_text: 'bank' }] })]
				: [occurrence({ article_block_id: id, id: `occ-${id}`, shadow_text: 'pretty', spans: [{ id: `span-${id}`, article_occurrence_id: `occ-${id}`, span_index: 0, start_utf16: 4, end_utf16: 9, source_text: 'mooie' }] })]
			: [],
		analysis_status: status,
		analysis_error_code: '',
		has_analysis: published,
		analysis_is_current: published,
		published_analysis_revision: published ? 'reader.analysis.v3' : '',
		published_analysis_model: published ? 'model-a' : '',
		published_analysis_effort: published ? 'low' : '',
		published_at: published ? '2026-01-01T00:00:00Z' : ''
	};
}

function buildArticle(publishedFirst: boolean, publishedSecond: boolean, status: string): unknown {
	const first = publishedFirst ? 'ready' : status === 'processing' ? 'pending' : 'pending';
	const second = publishedSecond ? 'ready' : 'pending';
	return {
		id: articleID,
		title: 'Progressief lezen',
		source_language: 'nl',
		target_language: 'en',
		enrichment_status: 'ready',
		enrichment_error_code: '',
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-02T00:00:00Z',
		blocks: [
			paragraphBlock(blockA, 0, 'De bank staat.', publishedFirst, first, publishedFirst ? 1 : 0),
			paragraphBlock(blockB, 1, 'Een mooie stoel.', publishedSecond, second, publishedSecond ? 2 : 1)
		],
		content_hash: 'content-hash',
		analysis_status: status,
		analysis_revision: status === 'ready' ? 'reader.analysis.v3' : '',
		analysis_error_code: status === 'failed' ? 'v1.enrichment_provider_failure' : '',
		analysis_model: 'model-a',
		analysis_effort: 'low',
		analysis_progress: {
			total_paragraphs: 2,
			completed_paragraphs: (publishedFirst ? 1 : 0) + (publishedSecond ? 1 : 0),
			current_block_index: status === 'processing' ? (publishedFirst ? 1 : 0) : -1,
			failed_block_index: -1
		},
		narration_status: 'not_requested',
		narration_error_code: '',
		sentences: [],
		occurrences: [],
		narration: { status: 'not_requested', error_code: '', sentence_count: 2, ready_count: 0, duration_ms: 0, size_bytes: 0, reclaimable_bytes: 0 }
	};
}

function subtitleBoxes(page: Page) {
	return page.locator('[data-occurrence-id] .translation-subtitle').evaluateAll((items) =>
		items.map((item) => {
			const rect = item.getBoundingClientRect();
			return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom };
		})
	);
}

test('paragraphs appear progressively without reload or overlapping subtitles', async ({ page }) => {
		page.setDefaultTimeout(8000);
	let publishedFirst = false;
	let publishedSecond = false;
	await page.route('**/api/v1/auth/session', (route) => route.fulfill(response({ authenticated: true })));
	await page.route(`**/api/v1/articles/${articleID}`, (route) => {
		const status = publishedSecond ? 'ready' : 'processing';
		return route.fulfill(response(buildArticle(publishedFirst, publishedSecond, status)));
	});
	await page.goto(`/reader/${articleID}`);

	// Queued/processing start: raw Dutch for both paragraphs, no subtitles.
	await expect(page.getByText('Analyzing paragraph 1 of 2', { exact: false }).first()).toBeVisible();
	await expect(page.locator('[data-analysis-note]').first()).toBeVisible();
	expect(await subtitleBoxes(page)).toHaveLength(0);
	const rawText = await page.locator('.reader-paragraph').first().innerText();
	expect(rawText).toContain('De bank staat.');

	// Release paragraph 1: it gains a visible subtitle without reloading;
	// paragraph 2 remains raw and pending.
	publishedFirst = true;
	await expect(page.getByText('sofa', { exact: true }).first()).toBeVisible({ timeout: 5000 });
	await expect(page.getByText('Analyzing paragraph 2 of 2', { exact: false }).first()).toBeVisible();
	await expect(page.locator(`[data-occurrence-id="occ-${blockB}"]`)).toHaveCount(0);

	// Release paragraph 2: analysis collapses to the compact ready status and
	// both subtitles are laid out without intersecting boxes.
	publishedSecond = true;
	await expect(page.getByText('1 complete', { exact: false })).toHaveCount(0);
	await expect(page.getByText('Ready', { exact: true }).first()).toBeVisible({ timeout: 5000 });
	const boxes = await subtitleBoxes(page);
	expect(boxes).toHaveLength(2);
	for (let left = 0; left < boxes.length; left += 1) {
		for (let right = left + 1; right < boxes.length; right += 1) {
			const a = boxes[left] as { left: number; right: number; top: number; bottom: number };
			const b = boxes[right] as { left: number; right: number; top: number; bottom: number };
			const intersects = a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom;
			expect(intersects, `subtitle boxes ${left} and ${right} intersect`).toBe(false);
		}
	}
	const overflow = await page.evaluate(() => {
		const body = document.querySelector('.reader-body');
		if (!body) return false;
		return body.scrollWidth > body.clientWidth + 1;
	});
	expect(overflow).toBe(false);
});

test('wait-state text and ARIA values advance once per paragraph', async ({ page }) => {
		page.setDefaultTimeout(8000);
	let publishedFirst = false;
	await page.route('**/api/v1/auth/session', (route) => route.fulfill(response({ authenticated: true })));
	await page.route(`**/api/v1/articles/${articleID}`, (route) => {
		return route.fulfill(response(buildArticle(publishedFirst, false, publishedFirst ? 'processing' : 'processing')));
	});
	await page.goto(`/reader/${articleID}`);
	const bar = page.locator('.analysis-progress');
	await expect(bar).toBeVisible();
	await expect(bar.locator('[role="progressbar"]')).toHaveAttribute('aria-valuenow', '0');
	publishedFirst = true;
	await expect(bar.locator('[role="progressbar"]')).toHaveAttribute('aria-valuenow', '50', { timeout: 5000 });
	await expect(bar).toContainText('1 complete');
});

const longArticleID = 'long-construction-article-id';
const longSource = 'Hij gooide gisteren bijna het bijltje erbij neer.';
const longGroupText = 'gooide gisteren bijna het bijltje erbij neer';
const longSubtitle = 'threw the axe in the towel yesterday afternoon almost entirely';

function longGroupArticle(): unknown {
	const groupSpan = { id: 'group-span', article_occurrence_id: 'group', span_index: 0, start_utf16: 4, end_utf16: 4 + longGroupText.length, source_text: longGroupText };
	return {
		id: longArticleID,
		title: 'Lange constructie',
		source_language: 'nl',
		target_language: 'en',
		enrichment_status: 'ready',
		enrichment_error_code: '',
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-02T00:00:00Z',
		blocks: [{
			id: 'long-block',
			article_id: longArticleID,
			block_index: 0,
			kind: 'paragraph',
			source_text: longSource,
			annotations: [],
			sentences: [{ id: 'long-sentence', article_block_id: 'long-block', sentence_index: 0, start_utf16: 0, end_utf16: longSource.length, source_text: longSource, source_hash: 'h', audio: null }],
			occurrences: [{
				id: 'group',
				article_block_id: 'long-block',
				article_sentence_id: null,
				semantic_sense_id: null,
				kind: 'expression',
				role: 'contiguous_construction',
				shadow_policy: 'group',
				shadow_text: longSubtitle,
				subtitle_suppression_reason: 'none',
				canonical_pronunciation_text: '',
				context_pronunciation_key: '',
				confidence_milli: 900,
				sense: null,
				learning_state: null,
				show_shadow: true,
				member_occurrence_ids: [],
				pronunciation: null,
				spans: [groupSpan]
			}],
			analysis_status: 'ready',
			analysis_error_code: '',
			has_analysis: true,
			analysis_is_current: true,
			published_analysis_revision: 'reader.analysis.v3',
			published_analysis_model: 'model-a',
			published_analysis_effort: 'low',
			published_at: '2026-01-01T00:00:00Z'
		}],
		content_hash: 'content-hash',
		analysis_status: 'ready',
		analysis_revision: 'reader.analysis.v3',
		analysis_error_code: '',
		analysis_model: 'model-a',
		analysis_effort: 'low',
		analysis_progress: { total_paragraphs: 1, completed_paragraphs: 1, current_block_index: -1, failed_block_index: -1 },
		narration_status: 'not_requested',
		narration_error_code: '',
		sentences: [],
		occurrences: [],
		narration: { status: 'not_requested', error_code: '', sentence_count: 1, ready_count: 0, duration_ms: 0, size_bytes: 0, reclaimable_bytes: 0 }
	};
}

test('long group subtitles wrap without overlapping or overflowing at 320, 768, and 1440 px', async ({ page }) => {
	page.setDefaultTimeout(8000);
	await page.route('**/api/v1/auth/session', (route) => route.fulfill(response({ authenticated: true })));
	await page.route(`**/api/v1/articles/${longArticleID}`, (route) => route.fulfill(response(longGroupArticle())));
	await page.goto(`/reader/${longArticleID}`);
	await expect(page.getByText(longSubtitle, { exact: true }).first()).toBeVisible();

	for (const width of [320, 768, 1440]) {
		await page.setViewportSize({ width, height: 900 });
		const boxes = await page.locator('[data-occurrence-id] .translation-subtitle').evaluateAll((items) =>
			items.map((item) => {
				const rect = item.getBoundingClientRect();
				return { left: rect.left, right: rect.right, top: rect.top, bottom: rect.bottom, width: rect.width };
			})
		);
		expect(boxes.length).toBeGreaterThan(0);
		const body = await page.locator('.reader-body').evaluate((element) => ({
			clientWidth: element.clientWidth,
			scrollWidth: element.scrollWidth
		}));
		expect(body.scrollWidth, `horizontal overflow at ${width}px`).toBeLessThanOrEqual(body.clientWidth + 1);
		for (const box of boxes) {
			expect(box.right, `subtitle exceeds viewport at ${width}px`).toBeLessThanOrEqual(width + 1);
			expect(box.left, `subtitle starts off viewport at ${width}px`).toBeGreaterThanOrEqual(-1);
		}
		for (let left = 0; left < boxes.length; left += 1) {
			for (let right = left + 1; right < boxes.length; right += 1) {
				const a = boxes[left] as { left: number; right: number; top: number; bottom: number };
				const b = boxes[right] as { left: number; right: number; top: number; bottom: number };
				const intersects = a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom;
				expect(intersects, `subtitle boxes ${left}/${right} intersect at ${width}px`).toBe(false);
			}
		}
	}
});
