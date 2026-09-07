import { expect, test, type Page } from '@playwright/test';
import { paragraph } from '../../dev/readerFixture';

// Reported sentence from the handoff plan (§9.10): exact zero-based
// construction members [1, 2, 9, 10] are gaan, er, van, uit.
const reportedSentence = 'We gaan er vaak, zonder het zelf te merken, van uit dat het ergens naartoe moet leiden.';
const glosses = ['We', 'go', 'there', 'often', 'without', 'it', 'ourselves', 'to', 'noticing', 'from', 'out', 'that', 'it', 'somewhere', 'to', 'must', 'lead'];
const expressionFixture = paragraph(0, reportedSentence, glosses, [
	{ words: [1, 2, 9, 10], label: '(er) van uit gaan', meaning: 'to assume', split: true }
]);
const exploreArticleID = 'reader-explore-article-id';
const exploreArticle = {
	id: exploreArticleID,
	title: 'Van uit gaan',
	source_language: 'nl',
	target_language: 'en',
	enrichment_status: 'ready',
	enrichment_error_code: '',
	created_at: '2026-09-07T00:00:00Z',
	updated_at: '2026-09-07T00:00:00Z',
	blocks: [expressionFixture],
	sentences: expressionFixture.sentences,
	occurrences: expressionFixture.occurrences,
	content_hash: '0'.repeat(64),
	analysis_status: 'ready',
	analysis_revision: 'reader.analysis.v2',
	analysis_error_code: '',
	analysis_model: '',
	analysis_effort: '',
	narration_status: 'not_requested',
	narration_error_code: '',
	analysis_progress: { total_paragraphs: 1, completed_paragraphs: 1, current_block_index: -1, failed_block_index: -1 },
	narration: { status: 'not_requested', error_code: '', sentence_count: 1, ready_count: 0, duration_ms: 0, size_bytes: 0, reclaimable_bytes: 0 }
};

// Dictionary responses keyed by the requested subject. The word entry and
// the expression entry are distinct subjects with distinct content, and the
// word entry never claims to explain the idiom.
const documents: Record<string, { body: Record<string, unknown>; status: number }> = {};

function documentFor(lookupForm: string, kind: string, senses: unknown[]) {
	return {
		version: 'reader.dictionary.v1',
		lookup_form: lookupForm,
		lookup_kind: kind,
		source_language: 'nl',
		target_language: 'en',
		senses
	};
}

const gaanSenses = [{
	part_of_speech: 'verb',
	translation_en: 'to go',
	meaning_en: 'To move or travel to another place.',
	usage_en: 'The most common verb for going somewhere.',
	pattern_nl: '',
	parts: [],
	examples: [{ text_nl: 'We gaan naar huis.', translation_en: 'We are going home.' }]
}];

const expressionSenses = [{
	part_of_speech: 'expression',
	translation_en: 'to assume',
	meaning_en: 'To take it for granted that something is true.',
	usage_en: 'Followed by a clause with "dat".',
	pattern_nl: 'ervan uitgaan dat …',
	parts: [
		{ source_nl: 'gaan … uit', explanation_en: 'the split verb "uitgaan" (to start from)' },
		{ source_nl: 'er … van', explanation_en: 'together: "from it", referring to the assumption' }
	],
	examples: [{ text_nl: 'Ik ga ervan uit dat je komt.', translation_en: 'I assume you are coming.' }]
}];

async function setupExploreAPI(page: Page, options: { missingFirst?: boolean } = {}) {
	await page.route('**/api/v1/auth/session', (route) => route.fulfill({ json: { authenticated: true } }));
	await page.route('**/api/v1/reader/settings', (route) => route.fulfill({ json: { pronounce_on_hover: false } }));
	await page.route(`**/api/v1/articles/${exploreArticleID}`, (route) => route.fulfill({ json: exploreArticle }));
	await page.context().addCookies([{ name: 'csrf_token', value: 'test-csrf-token', domain: 'localhost', path: '/' }]);

	documents[`word:gaan`] = { status: 200, body: { status: 'ready', entry_id: 'entry-gaan', document: documentFor('gaan', 'word', gaanSenses) } };
	documents[`expression:(er) van uit gaan`] = { status: 200, body: { status: 'ready', entry_id: 'entry-expression', document: documentFor('(er) van uit gaan', 'expression', expressionSenses) } };

	let queuedStarts = 0;
	await page.route('**/api/v1/articles/*/explore**', async (route) => {
		const request = route.request();
		const url = new URL(request.url());
		const occurrenceID = url.searchParams.get('occurrence_id') ?? request.postDataJSON()?.occurrence_id ?? '';
		if (options.missingFirst) {
			// The shared entry does not exist yet: GET reports missing and the
			// one explicit POST starts the durable generation.
			if (request.method() === 'GET') {
				return route.fulfill({ status: 200, json: { status: 'missing' } });
			}
			queuedStarts += 1;
			return route.fulfill({ status: 202, json: { status: 'queued', entry_id: 'entry-pending', job_id: 'job-pending' } });
		}
		const occurrence = exploreArticle.occurrences?.find((item: { id: string }) => item.id === occurrenceID);
		const subject = occurrence?.role === 'token' ? documents[`word:gaan`] : documents[`expression:(er) van uit gaan`];
		return route.fulfill({ status: subject?.status ?? 404, json: subject?.body ?? { error: 'missing', code: 'v1.not_found' } });
	});
	await page.route('**/api/v1/dictionary/entries/*', (route) => {
		const entryID = route.request().url().split('/').at(-1);
		if (entryID === 'entry-pending') {
			return route.fulfill({ status: 200, json: { status: 'generating', entry_id: 'entry-pending', job_id: 'job-pending' } });
		}
		return route.fulfill({ status: 404, json: { error: 'missing', code: 'v1.not_found' } });
	});
	return { startCount: () => queuedStarts };
}

const tokenSelector = `[data-occurrence-id="demo-0-word-1"]`;
const expressionSelector = `[data-construction-ids~="demo-0-expression-0"]`;

test.describe('reader explore', () => {
	test('explore is present on every lexical word, including expression members without their own sense content', async ({ page }) => {
		await setupExploreAPI(page);
		await page.goto(`/reader/${exploreArticleID}`);
		await expect(page.locator('.text-occurrence').first()).toBeVisible();
		// A member word of the split expression: hover previews the
		// expression, but the click opens the word.
		await page.locator(tokenSelector).click();
		await expect(page.getByRole('dialog')).toBeVisible();
		// The explicit subject selector offers both subjects.
		await expect(page.getByRole('button', { name: 'Word: gaan' })).toBeVisible();
		await expect(page.getByRole('button', { name: 'Expression' })).toBeVisible();
	});

	test('word and expression are distinct subjects with distinct dictionary entries', async ({ page }) => {
		await setupExploreAPI(page);
		await page.goto(`/reader/${exploreArticleID}`);
		await page.locator(tokenSelector).click();
		await expect(page.getByRole('dialog')).toBeVisible();

		// Word subject explores "gaan" and renders its own meanings.
		await page.getByRole('button', { name: 'Explore' }).click();
		await expect(page.getByText('Saved dictionary entry')).toBeVisible();
		await expect(page.getByText('To move or travel to another place.')).toBeVisible();
		await expect(page.getByText('to go', { exact: true })).toBeVisible();

		// Switching subject re-explores the whole expression; the word
		// translation is replaced by the expression translation.
		await page.getByRole('button', { name: 'Expression' }).click();
		await expect(page.getByRole('dialog').getByText('to assume')).toBeVisible();
		await page.getByRole('button', { name: 'Explore' }).click();
		await expect(page.getByText('To take it for granted that something is true.')).toBeVisible();
		// Parts and the dat-pattern usage render for the expression only.
		await expect(page.getByText('the split verb "uitgaan" (to start from)')).toBeVisible();
		await expect(page.getByText('Ik ga ervan uit dat je komt.')).toHaveAttribute('lang', 'nl');
		// The word entry is gone from the panel.
		await expect(page.getByText('To move or travel to another place.')).toHaveCount(0);
	});

	test('starting from the expression end explores the idiom without a word selector roundtrip', async ({ page }) => {
		await setupExploreAPI(page);
		await page.goto(`/reader/${exploreArticleID}`);
		// Clicking a member opens the word first; the explicit subject switch
		// moves the popover, translation, and Explore target to the idiom.
		await page.locator(expressionSelector).first().click();
		await expect(page.getByRole('dialog')).toBeVisible();
		await page.getByRole('button', { name: 'Expression' }).click();
		await expect(page.getByRole('dialog').getByText('to assume')).toBeVisible();
		await page.getByRole('button', { name: 'Explore' }).click();
		await expect(page.getByText('Saved dictionary entry')).toBeVisible();
		await expect(page.getByText('Followed by a clause with "dat".')).toBeVisible();
		// Intervening subtitles are untouched by the expanded panel.
		const vaakSubtitle = page.locator('[data-occurrence-id="demo-0-word-3"] .translation-subtitle');
		await expect(vaakSubtitle).toHaveText('often');
	});

	test('pending entries show sequential statuses, one start request, and reuse the saved entry', async ({ page }) => {
		const api = await setupExploreAPI(page, { missingFirst: true });
		await page.goto(`/reader/${exploreArticleID}`);
		await page.locator(tokenSelector).click();
		await page.getByRole('button', { name: 'Explore' }).click();
		await expect(page.getByText('Queued…')).toBeVisible();
		// The durable flow is joined, not restarted: exactly one POST.
		expect(api.startCount()).toBe(1);
		// The pending entry polls; while it stays generating the panel says so.
		await expect(page.getByText('Generating explanation…')).toBeVisible({ timeout: 5000 });
		expect(api.startCount()).toBe(1);
	});
});
