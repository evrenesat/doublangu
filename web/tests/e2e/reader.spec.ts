import { expect, test, type Page } from '@playwright/test';

const articleID = 'article-id';
const blockID = 'block-id';
const annotationID = 'annotation-id';

function response(body: unknown, status = 200) {
	return { status, contentType: 'application/json', body: JSON.stringify(body) };
}

test.beforeEach(async ({ page }) => {
	await page.route('**/api/v1/auth/session', (route) => route.fulfill(response({ authenticated: true })));
});

function article(status: 'draft' | 'processing' | 'ready' | 'failed' = 'ready', learned = false) {
	return {
		id: articleID,
		title: 'Een rustige dag',
		source_language: 'nl',
		target_language: 'en',
		enrichment_status: status,
		enrichment_error_code: '',
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-02T00:00:00Z',
		blocks: [{
			id: blockID,
			article_id: articleID,
			block_index: 0,
			kind: 'paragraph',
			source_text: 'Ik wil tot rust komen.',
			annotations: [{
				id: 'word-annotation-id',
				article_block_id: blockID,
				start_utf16: 3,
				end_utf16: 6,
				source_text: 'wil',
				kind: 'word',
				learning_key: 'wil',
				primary_translation: 'to want',
				alternatives: ['will'],
				literal_translation: '',
				meaning_note: 'To want something.',
				usage_note: '',
				parts_note: '',
				suggest_shadow: true,
				learning_state: learned ? { source_language: 'nl', kind: 'word', learning_key: 'wil', status: 'learned', updated_at: '2026-01-03T00:00:00Z' } : null,
				show_shadow: !learned
			}, {
				id: annotationID,
				article_block_id: blockID,
				start_utf16: 7,
				end_utf16: 21,
				source_text: 'tot rust komen',
				kind: 'expression',
				learning_key: 'tot rust komen',
				primary_translation: 'to calm down',
				alternatives: ['to settle down', 'to unwind'],
				literal_translation: 'to come to rest',
				meaning_note: 'To become mentally or physically calm.',
				usage_note: 'Use after activity, stress, or strong emotion.',
				parts_note: 'tot rust + komen',
				suggest_shadow: true,
				learning_state: learned ? { source_language: 'nl', kind: 'expression', learning_key: 'tot rust komen', status: 'learned', updated_at: '2026-01-03T00:00:00Z' } : null,
				show_shadow: !learned
			}]
		}]
	};
}

async function setupArticleAPI(page: Page, initial = article()): Promise<{ getCurrent: () => ReturnType<typeof article>; setCurrent: (next: ReturnType<typeof article>) => void; failNextLearning: () => void }> {
	await page.context().addCookies([{ name: 'csrf_token', value: 'test-csrf-token', domain: 'localhost', path: '/' }]);
	let current = initial;
	let shouldFailNextLearning = false;
	await page.route('**/api/v1/articles', (route) => {
		if (route.request().method() === 'GET') return route.fulfill(response([{ ...current, blocks: undefined }]));
		current = article('draft');
		return route.fulfill(response(current, 201));
	});
	await page.route(`**/api/v1/articles/${articleID}/enrich`, (route) => {
		current = article('ready', false);
		return route.fulfill(response(current));
	});
	await page.route(`**/api/v1/articles/${articleID}`, (route) => route.fulfill(response(current)));
	await page.route('**/api/v1/learning-state', (route) => {
		if (shouldFailNextLearning) {
			shouldFailNextLearning = false;
			return route.fulfill(response({ error: 'Learning state unavailable', code: 'v1.internal_error' }, 500));
		}
		current = article('ready', true);
		return route.fulfill(response({ source_language: 'nl', kind: 'expression', learning_key: 'tot rust komen', status: 'learned', updated_at: '2026-01-04T00:00:00Z' }));
	});
	return {
		getCurrent: () => current,
		setCurrent: (next) => { current = next; },
		failNextLearning: () => { shouldFailNextLearning = true; }
	};
}

const semanticSense = {
	id: 'sense-give-up',
	semantic_item_id: 'item-give-up',
	kind: 'expression',
	canonical_form: 'opgeven',
	sense_discriminator: 'give-up',
	primary_translation: 'give up',
	alternatives: ['quit'],
	literal_translation: 'give up',
	meaning_note: 'To stop trying or to surrender.',
	usage_note: 'Used when an effort is abandoned.',
	parts_note: 'geef + op',
	canonical_pronunciation_text: 'opgeven'
};

function semanticArticle(status: 'queued' | 'ready' = 'ready') {
	const sentenceID = 'sentence-id';
	const block = {
		id: blockID,
		article_id: articleID,
		block_index: 0,
		kind: 'paragraph',
		source_text: 'Ik geef het boek op.',
		annotations: [],
		sentences: status === 'ready' ? [{ id: sentenceID, article_block_id: blockID, sentence_index: 0, start_utf16: 0, end_utf16: 20, source_text: 'Ik geef het boek op.', source_hash: 'sentence-hash', audio: { render_id: 'sentence-render', url: '/api/v1/audio/sentence-render', ready: true, duration_ms: 900, size_bytes: 1200, error_code: '' } }] : [],
		occurrences: status === 'ready' ? [
			{ id: 'token-ik', article_block_id: blockID, article_sentence_id: sentenceID, semantic_sense_id: null, kind: 'word', role: 'token', shadow_policy: 'none', shadow_text: '', confidence_milli: 1000, sense: null, learning_state: null, show_shadow: false, pronunciation: null, spans: [{ id: 'span-ik', article_occurrence_id: 'token-ik', span_index: 0, start_utf16: 0, end_utf16: 2, source_text: 'Ik' }] },
			{ id: 'token-geef', article_block_id: blockID, article_sentence_id: sentenceID, semantic_sense_id: semanticSense.id, kind: 'word', role: 'token', shadow_policy: 'token', shadow_text: 'give', confidence_milli: 940, sense: { ...semanticSense, kind: 'word', primary_translation: 'give', sense_discriminator: 'give' }, learning_state: null, show_shadow: true, pronunciation: { render_id: 'geef-render', url: '/api/v1/audio/geef-render', ready: true, duration_ms: 300, size_bytes: 200, error_code: '' }, spans: [{ id: 'span-geef', article_occurrence_id: 'token-geef', span_index: 0, start_utf16: 3, end_utf16: 7, source_text: 'geef' }] },
			{ id: 'token-het', article_block_id: blockID, article_sentence_id: sentenceID, semantic_sense_id: null, kind: 'word', role: 'token', shadow_policy: 'token', shadow_text: 'the', confidence_milli: 920, sense: null, learning_state: null, show_shadow: true, pronunciation: { render_id: 'het-render', url: '/api/v1/audio/het-render', ready: true, duration_ms: 220, size_bytes: 180, error_code: '' }, spans: [{ id: 'span-het', article_occurrence_id: 'token-het', span_index: 0, start_utf16: 8, end_utf16: 11, source_text: 'het' }] },
			{ id: 'token-boek', article_block_id: blockID, article_sentence_id: sentenceID, semantic_sense_id: null, kind: 'word', role: 'token', shadow_policy: 'token', shadow_text: 'book', confidence_milli: 920, sense: null, learning_state: null, show_shadow: true, pronunciation: { render_id: 'boek-render', url: '/api/v1/audio/boek-render', ready: true, duration_ms: 240, size_bytes: 190, error_code: '' }, spans: [{ id: 'span-boek', article_occurrence_id: 'token-boek', span_index: 0, start_utf16: 12, end_utf16: 16, source_text: 'boek' }] },
			{ id: 'token-op', article_block_id: blockID, article_sentence_id: sentenceID, semantic_sense_id: semanticSense.id, kind: 'word', role: 'token', shadow_policy: 'none', shadow_text: '', confidence_milli: 940, sense: { ...semanticSense, kind: 'word', primary_translation: 'up', sense_discriminator: 'up' }, learning_state: null, show_shadow: false, pronunciation: { render_id: 'op-render', url: '/api/v1/audio/op-render', ready: true, duration_ms: 180, size_bytes: 160, error_code: '' }, spans: [{ id: 'span-op', article_occurrence_id: 'token-op', span_index: 0, start_utf16: 17, end_utf16: 19, source_text: 'op' }] },
			{ id: 'construction-give-up', article_block_id: blockID, article_sentence_id: sentenceID, semantic_sense_id: semanticSense.id, kind: 'expression', role: 'discontinuous_construction', shadow_policy: 'marker', shadow_text: 'give up', confidence_milli: 950, sense: semanticSense, learning_state: null, show_shadow: false, pronunciation: null, spans: [{ id: 'span-construction-a', article_occurrence_id: 'construction-give-up', span_index: 0, start_utf16: 3, end_utf16: 7, source_text: 'geef' }, { id: 'span-construction-b', article_occurrence_id: 'construction-give-up', span_index: 1, start_utf16: 17, end_utf16: 19, source_text: 'op' }] }
		] : []
	};
	return {
		id: articleID,
		title: 'Een hoorbare dag',
		source_language: 'nl',
		target_language: 'en',
		enrichment_status: 'draft',
		enrichment_error_code: '',
		created_at: '2026-01-01T00:00:00Z',
		updated_at: '2026-01-02T00:00:00Z',
		content_hash: 'article-hash',
		analysis_status: status,
		analysis_revision: status === 'ready' ? 'reader.analysis.v2' : '',
		analysis_error_code: '',
		narration_status: status === 'ready' ? 'ready' : 'not_requested',
		narration_error_code: '',
		blocks: [block],
		sentences: block.sentences,
		occurrences: block.occurrences,
		narration: { status: status === 'ready' ? 'ready' : 'not_requested', error_code: '', sentence_count: status === 'ready' ? 1 : 0, ready_count: status === 'ready' ? 1 : 0, duration_ms: status === 'ready' ? 900 : 0, size_bytes: status === 'ready' ? 1200 : 0, reclaimable_bytes: status === 'ready' ? 1200 : 0 }
	};
}

async function setupSemanticArticleAPI(page: Page, initial = semanticArticle(), ready = semanticArticle('ready')): Promise<void> {
	await page.context().addCookies([{ name: 'csrf_token', value: 'test-csrf-token', domain: 'localhost', path: '/' }]);
	let current = initial;
	let articleReads = 0;
	const narrationAudio = ready.blocks[0]?.sentences?.[0]?.audio ?? null;
	await page.route(`**/api/v1/articles/${articleID}`, (route) => {
		articleReads += 1;
		if (current.analysis_status === 'queued' && articleReads > 1) {
			return new Promise<void>((resolve) => setTimeout(() => { current = ready; void route.fulfill(response(current)).then(() => resolve()); }, 1000));
		}
		return route.fulfill(response(current));
	});
	await page.route(`**/api/v1/articles/${articleID}/narration`, (route) => route.fulfill(response({ ...ready.narration, article_id: articleID, clips: [{ sentence_id: 'sentence-id', sequence_index: 0, audio: narrationAudio }] })));
	await page.route('**/api/v1/learning-state', async (route) => {
		const body = route.request().postDataJSON() as { semantic_sense_id: string; status: 'learned' | 'unlearned' };
		return route.fulfill(response({ semantic_sense_id: body.semantic_sense_id, status: body.status, updated_at: '2026-01-04T00:00:00Z' }));
	});
	await page.route('**/api/v1/audio/**', (route) => route.fulfill({ status: 200, contentType: 'audio/mp4', body: 'm4a' }));
}

test('routes a direct protected URL through app sign-in before rendering it', async ({ page }) => {
	await page.unroute('**/api/v1/auth/session');
	await page.route('**/api/v1/auth/session', (route) => route.fulfill(response({ authenticated: false })));
	await page.goto('/reader/new');
	await expect(page).toHaveURL('/login?next=%2Freader%2Fnew');
	await expect(page.getByRole('heading', { name: 'Welcome back' })).toBeVisible();
	await expect(page.getByRole('heading', { name: 'Paste an article' })).toHaveCount(0);
});

test('explains a persisted enrichment timeout and confirms the article is safe', async ({ page }) => {
	const failed = { ...article('failed'), enrichment_error_code: 'v1.enrichment_timeout' };
	await setupArticleAPI(page, failed);
	await page.goto(`/reader/${articleID}`);
	await expect(page.getByText('Codex took too long to process this article.')).toBeVisible();
	await expect(page.getByText('Your article is saved. Retry once; if it times out again, split it into shorter parts.')).toBeVisible();
	await expect(page.getByRole('button', { name: 'Retry enrichment' })).toBeVisible();
});

test('keeps implementation scaffolds out of learner navigation', async ({ page }) => {
	await setupArticleAPI(page);
	await page.goto('/reader');
	const navigation = page.getByRole('navigation', { name: 'Main navigation' });
	await expect(navigation.getByRole('link', { name: 'Articles' })).toBeVisible();
	await expect(navigation.getByRole('link', { name: 'Paste article' })).toBeVisible();
	await expect(navigation.getByRole('link', { name: 'Settings' })).toBeVisible();
	await expect(navigation.getByRole('link', { name: /Library|Plugins/ })).toHaveCount(0);
});

test('saves the pasted article first, enriches it, and renders contextual English subtitles', async ({ page }) => {
	await setupArticleAPI(page, article('draft'));
	await page.goto('/reader/new');
	await page.getByLabel('Title').fill('Een rustige dag');
	await page.getByLabel('Article text').fill('Ik wil tot rust komen.');
	await page.getByRole('button', { name: 'Save article' }).click();

	await expect(page).toHaveURL(/\/reader\/article-id$/);
	await expect(page.getByRole('heading', { name: 'Een rustige dag' })).toBeVisible();
	await expect(page.getByText('Ik wil')).toBeVisible();
	await expect(page.getByText('to calm down')).toBeVisible();
	await expect(page.getByText('to want')).toBeVisible();
	await expect(page.getByRole('button', { name: 'Hear' })).toHaveCount(0);
});

test('opens, pins, explores, and closes an expression popover with keyboard input', async ({ page }) => {
	await setupArticleAPI(page);
	await page.goto(`/reader/${articleID}`);
	const trigger = page.getByRole('button', { name: 'tot rust komen: to calm down' });
	await trigger.hover();
	await expect(page.getByRole('dialog')).toBeVisible();
	await expect(page.getByText('Also: to settle down · to unwind')).toBeVisible();

	await trigger.click();
	await page.getByRole('button', { name: 'wil: to want' }).hover();
	await expect(page.getByRole('dialog')).toHaveAttribute('aria-label', 'Translation for tot rust komen');
	await expect(page.getByRole('dialog').getByText('to calm down')).toBeVisible();
	await page.getByRole('button', { name: 'Explore' }).click();
	await expect(page.getByRole('button', { name: 'Meaning' })).toBeVisible();
	await page.getByRole('button', { name: 'Usage' }).click();
	await expect(page.getByText('Use after activity, stress, or strong emotion.')).toBeVisible();
	await expect(page.getByText('To become mentally or physically calm.')).toHaveCount(0);
	await page.keyboard.press('Escape');
	await expect(page.getByRole('dialog')).toHaveCount(0);
});

test('persists learned suppression, rolls back a failed save, and works at 320px', async ({ page }) => {
	const api = await setupArticleAPI(page);
	await page.goto(`/reader/${articleID}`);
	const trigger = page.getByRole('button', { name: 'tot rust komen: to calm down' });
	await trigger.click();
	await page.getByRole('button', { name: 'Mark learned' }).click();
	await expect(page.getByText('Marked learned. Subtitle hidden.')).toBeVisible();
	await expect(page.locator(`[data-annotation-id="${annotationID}"] .translation-subtitle`)).toHaveCount(0);

	await page.reload();
	await expect(page.locator('.translation-subtitle')).toHaveCount(0);
	await page.getByRole('button', { name: 'tot rust komen: to calm down' }).click();
	await expect(page.getByRole('button', { name: 'Mark unlearned' })).toBeVisible();

	await page.setViewportSize({ width: 320, height: 720 });
	api.failNextLearning();
	await page.keyboard.press('Escape');
	await page.getByRole('button', { name: 'tot rust komen: to calm down' }).click();
	await page.getByRole('button', { name: 'Mark unlearned' }).click();
	await expect(page.getByRole('alert')).toContainText('Learning state unavailable');
	await expect(page.locator('.translation-subtitle')).toHaveCount(0);
	await page.getByRole('button', { name: 'Explore' }).click();
	await page.getByRole('button', { name: 'Usage' }).click();
	await expect(page.getByText('Use after activity, stress, or strong emotion.')).toBeVisible();
	await page.evaluate(() => new Promise<void>((resolve) => requestAnimationFrame(() => requestAnimationFrame(() => resolve()))));
	const popover = page.getByRole('dialog');
	const box = await popover.boundingBox();
	expect(box).not.toBeNull();
	expect(box!.x).toBeGreaterThanOrEqual(0);
	expect(box!.x + box!.width).toBeLessThanOrEqual(320);
	expect(box!.y).toBeGreaterThanOrEqual(0);
	expect(box!.y + box!.height).toBeLessThanOrEqual(720);
});

test('renders the v2 source immediately, then layers subtitles and shared construction markers', async ({ page }) => {
	await setupSemanticArticleAPI(page, semanticArticle('queued'));
	await page.goto(`/reader/${articleID}`);
	await expect(page.getByRole('heading', { name: 'Een hoorbare dag' })).toBeVisible();
	await expect(page.locator('.reader-body')).toBeVisible();
	const source = await page.locator('.reader-body').evaluate((element) => {
		const clone = element.cloneNode(true) as HTMLElement;
		clone.querySelectorAll('.translation-subtitle').forEach((subtitle) => subtitle.remove());
		return clone.textContent?.replace(/\s+/g, ' ').trim();
	});
	expect(source).toContain('Ik geef het boek op.');
	await expect(page.getByText('Preparing English subtitles…')).toBeVisible();
	await expect(page.locator('.translation-subtitle').filter({ hasText: 'give' })).toBeVisible();
	await expect(page.locator('.translation-subtitle').filter({ hasText: 'the' })).toBeVisible();
	const ordinaryWord = page.locator('[data-occurrence-id="token-boek"] .source-text');
	await expect(ordinaryWord).toHaveCSS('text-decoration-line', 'none');
	const members = page.locator('[data-construction-ids~="construction-give-up"]');
	await expect(members).toHaveCount(2);
	await expect(members.first().locator('.source-text')).toHaveCSS('text-decoration-style', 'wavy');
	await members.first().hover();
	await expect(members.first()).toHaveClass(/construction-active/);
	await expect(members.nth(1)).toHaveClass(/construction-active/);
});

test('uses sense-keyed learning, explicit hover audio, and lazy narration playback', async ({ page }) => {
	await setupSemanticArticleAPI(page);
	await page.goto(`/reader/${articleID}`);
	await page.getByLabel('Pronounce on hover').check();
	await page.getByRole('button', { name: /geef: give/ }).click();
	await expect(page.getByRole('dialog')).toBeVisible();
	await page.getByRole('button', { name: 'Mark learned' }).click();
	await expect(page.getByText('Marked learned. Subtitle hidden.')).toBeVisible();
	await expect(page.getByRole('button', { name: /geef: give/ })).toBeVisible();
	await page.getByRole('button', { name: 'Play' }).click();
	await expect(page.getByRole('button', { name: 'Pause' })).toBeVisible();
	await expect(page.getByText('Sentence 1 of 1')).toBeVisible();
});
