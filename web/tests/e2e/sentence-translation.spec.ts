import { expect, test, type Page } from '@playwright/test';
import { paragraph } from '../../dev/readerFixture';

const articleID = 'sentence-translation-article-id';
const blockOne = { ...paragraph(0, 'Noor zit op de bank.', ['Noor', 'sits', 'on', 'the', 'sofa'], []), article_id: articleID };
const blockTwo = { ...paragraph(1, 'Zij leest een boek.', ['She', 'reads', 'a', 'book'], []), article_id: articleID };
const sentenceID = 'demo-sentence-0';
const sentenceTwoID = 'demo-sentence-1';
const article = {
	id: articleID,
	title: 'Bankzitten',
	source_language: 'nl',
	target_language: 'en',
	enrichment_status: 'ready',
	enrichment_error_code: '',
	created_at: '2026-09-07T00:00:00Z',
	updated_at: '2026-09-07T00:00:00Z',
	blocks: [blockOne, blockTwo],
	sentences: [...blockOne.sentences, ...blockTwo.sentences],
	occurrences: [...blockOne.occurrences, ...blockTwo.occurrences],
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

type SentenceConfig = {
	initial: 'missing' | 'ready' | 'failed';
	readyText?: string;
	failSummary?: string;
	regenerateFinal: 'ready' | 'failed';
};

type SentenceRuntime = { status: string; translation: string | null; polls: number };
type PostTally = { ensure: number; regenerate: number };

function envelope(runtime: SentenceRuntime, sid: string, runId: string, failSummary: string) {
	return {
		status: runtime.status,
		sentence_id: sid,
		translation: runtime.translation,
		job_id: 'job-st-1',
		run_id: runId,
		generation_status: runtime.status === 'ready' || runtime.status === 'missing' ? 'idle' : runtime.status === 'failed' ? 'failed' : runtime.status,
		error_code: runtime.status === 'failed' ? 'v1.st_invalid' : null,
		error_summary: runtime.status === 'failed' ? failSummary : null
	};
}

async function setupSentenceAPI(page: Page, config: SentenceConfig) {
	await page.route('**/api/v1/auth/session', (route) => route.fulfill({ json: { authenticated: true } }));
	await page.route('**/api/v1/reader/settings', (route) => route.fulfill({ json: { pronounce_on_hover: false } }));
	await page.route(`**/api/v1/articles/${articleID}`, (route) => route.fulfill({ json: article }));
	await page.context().addCookies([{ name: 'csrf_token', value: 'test-csrf-token', domain: 'localhost', path: '/' }]);

	const runtimes = new Map<string, SentenceRuntime>();
	function runtimeFor(sid: string): SentenceRuntime {
		let runtime = runtimes.get(sid);
		if (!runtime) {
			runtime =
				config.initial === 'ready'
					? { status: 'ready', translation: config.readyText ?? 'Noor is sitting on the sofa.', polls: 0 }
					: config.initial === 'failed'
						? { status: 'failed', translation: null, polls: 0 }
						: { status: 'missing', translation: null, polls: 0 };
			runtimes.set(sid, runtime);
		}
		return runtime;
	}
	function textFor(sid: string): string {
		if (sid === sentenceTwoID) return 'She is reading a book.';
		return config.readyText ?? 'Noor is sitting on the sofa.';
	}
	const posts: PostTally = { ensure: 0, regenerate: 0 };
	const failSummary = config.failSummary ?? 'Provider returned invalid output.';

	await page.route('**/sentences/*/translation', async (route) => {
		const request = route.request();
		const sid = request.url().match(/sentences\/([^/]+)\/translation/)?.[1] ?? sentenceID;
		const runtime = runtimeFor(sid);
		if (request.method() === 'POST') {
			const body = request.postDataJSON() as { mode: string };
			if (body.mode === 'ensure') {
				posts.ensure += 1;
				if (runtime.status !== 'missing') {
					return route.fulfill({ status: 200, json: envelope(runtime, sid, 'run-st-1', failSummary) });
				}
				runtime.status = 'queued';
				runtime.polls = 0;
				return route.fulfill({ status: 202, json: envelope(runtime, sid, 'run-st-1', failSummary) });
			}
			posts.regenerate += 1;
			if (config.regenerateFinal === 'failed') {
				runtime.status = 'failed';
				return route.fulfill({ status: 200, json: envelope(runtime, sid, 'run-st-1', failSummary) });
			}
			runtime.status = 'queued';
			runtime.polls = 0;
			return route.fulfill({ status: 202, json: envelope(runtime, sid, 'run-st-2', failSummary) });
		}
		// Read-only GET: advance one pending generation step per poll.
		if (runtime.status === 'queued') {
			runtime.status = 'running';
			runtime.polls += 1;
			return route.fulfill({ status: 200, json: { ...envelope(runtime, sid, 'run-st-1', failSummary), status: 'queued' } });
		}
		if (runtime.status === 'running') {
			runtime.status = 'ready';
			runtime.translation = textFor(sid);
			runtime.polls += 1;
			return route.fulfill({ status: 200, json: { ...envelope(runtime, sid, 'run-st-2', failSummary), status: 'running' } });
		}
		return route.fulfill({ status: 200, json: envelope(runtime, sid, 'run-st-1', failSummary) });
	});
	return { posts };
}

const translateButton = (page: Page) => page.getByRole('button', { name: 'Translate sentence 1' });
const sentenceDialog = (page: Page) => page.getByRole('dialog', { name: /Sentence translation/ });

test.describe('sentence translation', () => {
	test('missing translation hover generates once after the dwell', async ({ page }) => {
		const api = await setupSentenceAPI(page, { initial: 'missing', regenerateFinal: 'ready' });
		await page.goto(`/reader/${articleID}`);
		await translateButton(page).hover();
		await expect(sentenceDialog(page)).toBeVisible();
		await expect(sentenceDialog(page).getByText('Noor is sitting on the sofa.')).toBeVisible({ timeout: 10000 });
		expect(api.posts.ensure).toBe(1);
		expect(api.posts.regenerate).toBe(0);
	});

	test('leaving before the dwell sends no generation', async ({ page }) => {
		const api = await setupSentenceAPI(page, { initial: 'missing', regenerateFinal: 'ready' });
		await page.goto(`/reader/${articleID}`);
		await translateButton(page).hover();
		await page.mouse.move(4, 4);
		// Longer than the 350ms dwell: nothing may have been sent.
		await page.waitForTimeout(700);
		expect(api.posts.ensure).toBe(0);
		expect(api.posts.regenerate).toBe(0);
		await expect(sentenceDialog(page)).toHaveCount(0);
	});

	test('saved hover never calls the LLM', async ({ page }) => {
		const api = await setupSentenceAPI(page, { initial: 'ready', regenerateFinal: 'ready' });
		await page.goto(`/reader/${articleID}`);
		await translateButton(page).hover();
		await expect(sentenceDialog(page).getByText('Noor is sitting on the sofa.')).toBeVisible();
		expect(api.posts.ensure).toBe(0);
		expect(api.posts.regenerate).toBe(0);
		await expect(sentenceDialog(page).getByRole('button', { name: 'Regenerate' })).toBeVisible();
	});

	test('failed translation offers Retry and View run without hover loops', async ({ page }) => {
		const api = await setupSentenceAPI(page, { initial: 'failed', regenerateFinal: 'ready' });
		await page.goto(`/reader/${articleID}`);
		await translateButton(page).click();
		await expect(sentenceDialog(page).getByText('Provider returned invalid output.')).toBeVisible();
		await expect(sentenceDialog(page).getByRole('button', { name: 'Retry' })).toBeVisible();
		await expect(sentenceDialog(page).getByRole('link', { name: 'View run' })).toBeVisible();
		// Opening a retained failure never retries on its own.
		expect(api.posts.ensure).toBe(0);
		// Move off first: the click left the pointer over the trigger, and
		// a hover without pointer movement fires no pointerenter.
		await page.mouse.move(4, 4);
		// Focus inside the dialog so Escape must return focus without the
		// focus handler reopening the popover on arrival.
		await sentenceDialog(page).getByRole('button', { name: 'Retry' }).focus();
		await page.keyboard.press('Escape');
		await expect(sentenceDialog(page)).toHaveCount(0);
		await translateButton(page).hover();
		await expect(sentenceDialog(page).getByText('Provider returned invalid output.')).toBeVisible();
		await page.waitForTimeout(600);
		expect(api.posts.ensure).toBe(0);
		expect(api.posts.regenerate).toBe(0);
		// The explicit Retry regenerates and shows the result.
		await sentenceDialog(page).getByRole('button', { name: 'Retry' }).click();
		await expect(sentenceDialog(page).getByText('Noor is sitting on the sofa.')).toBeVisible({ timeout: 10000 });
		expect(api.posts.regenerate).toBe(1);
	});

	test('failed regeneration keeps the old text with a retry', async ({ page }) => {
		const api = await setupSentenceAPI(page, {
			initial: 'ready',
			readyText: 'Oude tekst.',
			regenerateFinal: 'failed'
		});
		await page.goto(`/reader/${articleID}`);
		await translateButton(page).click();
		await expect(sentenceDialog(page).getByText('Oude tekst.')).toBeVisible();
		expect(api.posts.ensure).toBe(0);
		await sentenceDialog(page).getByRole('button', { name: 'Regenerate' }).click();
		await expect(sentenceDialog(page).getByText('Retry regeneration')).toBeVisible();
		await expect(sentenceDialog(page).getByText('Oude tekst.')).toBeVisible();
		expect(api.posts.regenerate).toBe(1);
	});

	test('switching sentences never mislabels a response', async ({ page }) => {
		const api = await setupSentenceAPI(page, { initial: 'missing', regenerateFinal: 'ready' });
		await page.goto(`/reader/${articleID}`);
		const secondButton = page.getByRole('button', { name: 'Translate sentence 2' });
		const firstDialog = page.getByRole('dialog', { name: /Sentence translation: Noor/ });
		const secondDialog = page.getByRole('dialog', { name: /Sentence translation: Zij/ });
		// Generate the first sentence, then open the second: the first
		// popover closes instead of being reused, so a stale response can
		// never appear under the new label.
		await translateButton(page).click();
		await expect(firstDialog.getByText('Noor is sitting on the sofa.')).toBeVisible({ timeout: 10000 });
		await secondButton.click();
		await expect(secondDialog.getByText('She is reading a book.')).toBeVisible({ timeout: 10000 });
		await expect(firstDialog).toHaveCount(0);
		expect(api.posts.ensure).toBe(2);
	});
});

test.describe('explore regenerate', () => {
	test('regeneration keeps the old entry with progress and a duplicate guard', async ({ page }) => {
		await page.route('**/api/v1/auth/session', (route) => route.fulfill({ json: { authenticated: true } }));
		await page.route('**/api/v1/reader/settings', (route) => route.fulfill({ json: { pronounce_on_hover: false } }));
		await page.route(`**/api/v1/articles/${articleID}`, (route) => route.fulfill({ json: article }));
		await page.context().addCookies([{ name: 'csrf_token', value: 'test-csrf-token', domain: 'localhost', path: '/' }]);

		const document = {
			version: 'reader.dictionary.v1',
			lookup_form: 'bank',
			lookup_kind: 'word',
			source_language: 'nl',
			target_language: 'en',
			senses: [
				{
					part_of_speech: 'noun',
					translation_en: 'sofa',
					meaning_en: 'A seat to sit on.',
					usage_en: '',
					pattern_nl: '',
					parts: [],
					examples: [{ text_nl: 'Noor zit op de bank.', translation_en: 'Noor is sitting on the sofa.' }]
				}
			]
		};
		let regeneratePosts = 0;
		await page.route('**/api/v1/articles/*/explore**', async (route) => {
			const request = route.request();
			if (request.method() === 'GET') {
				return route.fulfill({ status: 200, json: { status: 'ready', entry_id: 'entry-bank', document } });
			}
			const body = request.postDataJSON() as { regenerate?: boolean };
			if (body.regenerate) {
				regeneratePosts += 1;
				return route.fulfill({ status: 202, json: { status: 'queued', entry_id: 'entry-bank', job_id: 'job-bank-2', run_id: 'run-bank-2' } });
			}
			return route.fulfill({ status: 200, json: { status: 'ready', entry_id: 'entry-bank', document } });
		});
		await page.route('**/api/v1/dictionary/entries/*', (route) =>
			route.fulfill({ status: 200, json: { status: 'generating', entry_id: 'entry-bank', job_id: 'job-bank-2' } })
		);

		await page.goto(`/reader/${articleID}`);
		await page.locator('[data-occurrence-id="demo-0-word-4"]').click();
		await expect(page.getByRole('dialog')).toBeVisible();
		await page.getByRole('button', { name: 'Explore' }).click();
		await expect(page.getByText('Saved dictionary entry')).toBeVisible();
		await page.getByRole('button', { name: 'Regenerate', exact: true }).click();
		expect(regeneratePosts).toBe(1);
		// The old entry stays readable with replacement progress.
		await expect(page.getByText('Regenerating… previous entry kept')).toBeVisible();
		await expect(page.getByText('A seat to sit on.')).toBeVisible();
		// The pending control is disabled and no second request goes out.
		await expect(page.getByRole('button', { name: 'Regenerating…' })).toBeDisabled();
		await page.waitForTimeout(300);
		expect(regeneratePosts).toBe(1);
	});
});
