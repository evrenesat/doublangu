import { expect, test, type Page } from '@playwright/test';
import { readerFixture, readerFixtureID } from '../../dev/readerFixture';

test.beforeEach(async ({ page }) => {
	await page.route('**/api/v1/auth/session', (route) => route.fulfill({ json: { authenticated: true } }));
	await page.route('**/api/v1/reader/settings', (route) => route.fulfill({ json: { pronounce_on_hover: false } }));
	await page.route(`**/api/v1/articles/${readerFixtureID}`, (route) => route.fulfill({ json: readerFixture }));
	await page.goto(`/reader/${readerFixtureID}`);
	await expect(page.locator('.text-occurrence')).toHaveCount(66);
});

async function condensed(page: Page) {
	await page.getByRole('button', { name: 'Condensed', exact: true }).click();
	await expect(page.locator('.reader-shell')).toHaveAttribute('data-reading-mode', 'condensed');
}

async function condensedGeometry(page: Page) {
	return page.evaluate(() => ({
		scroll: window.scrollY,
		overflow: document.documentElement.scrollWidth > window.innerWidth,
		paragraphs: [...document.querySelectorAll<HTMLElement>('.reader-paragraph')].map((item) => ({
			top: item.offsetTop,
			height: item.offsetHeight
		})),
		words: [...document.querySelectorAll<HTMLElement>('.text-occurrence')].map((item) => ({
			id: item.dataset.occurrenceId,
			x: item.offsetLeft,
			y: item.offsetTop,
			width: item.offsetWidth,
			height: item.offsetHeight
		}))
	}));
}

test('learning stays the default and condensed hides decorations without losing source text', async ({
	page
}) => {
	await expect(page.locator('.reader-shell')).toHaveAttribute('data-reading-mode', 'learning');
	await expect(page.locator('.translation-subtitle').first()).toBeVisible();
	await expect(page.locator('.sentence-footer').first()).toBeVisible();

	async function sentenceSources() {
		return page.evaluate(() =>
			[...document.querySelectorAll('.sentence-words')].map((element) => {
				const copy = element.cloneNode(true) as HTMLElement;
				copy.querySelectorAll('.translation-subtitle,svg').forEach((node) => node.remove());
				return copy.textContent?.replace(/\s+/g, ' ').trim();
			})
		);
	}
	const expectedSources = readerFixture.blocks.map((block) => block.source_text);
	expect(await sentenceSources()).toEqual(expectedSources);

	await condensed(page);

	// Learning decorations leave the layout entirely in condensed mode.
	await expect(page.locator('.translation-subtitle:visible')).toHaveCount(0);
	await expect(page.locator('.sentence-footer')).toHaveCount(0);
	await expect(page.locator('.construction-overlay')).toHaveCount(0);
	// The same semantic spans stay interactive as plain inline words.
	await expect(page.locator('.text-occurrence')).toHaveCount(66);

	// Condensed keeps the exact source spans, whitespace, and punctuation.
	expect(await sentenceSources()).toEqual(expectedSources);

	const facts = await page.evaluate(() => ({
		overflow: document.documentElement.scrollWidth > window.innerWidth,
		lineHeight: parseFloat(getComputedStyle(document.querySelector('.reader-paragraph')!).lineHeight),
		fontSize: parseFloat(getComputedStyle(document.querySelector('.reader-paragraph')!).fontSize)
	}));
	expect(facts.overflow).toBe(false);
	expect(facts.lineHeight / facts.fontSize).toBeCloseTo(1.65, 1);
});

test('the mode persists across reload and invalid storage falls back to learning', async ({
	page
}) => {
	await condensed(page);
	await page.reload();
	await expect(page.locator('.text-occurrence')).toHaveCount(66);
	await expect(page.locator('.reader-shell')).toHaveAttribute('data-reading-mode', 'condensed');
	await expect(page.locator('.translation-subtitle:visible')).toHaveCount(0);

	await page.evaluate(() => localStorage.setItem('doublangu.reader.readingMode.v1', 'compact'));
	await page.reload();
	await expect(page.locator('.text-occurrence')).toHaveCount(66);
	await expect(page.locator('.reader-shell')).toHaveAttribute('data-reading-mode', 'learning');
	await expect(page.locator('.translation-subtitle').first()).toBeVisible();
});

for (const width of [320, 375, 1280]) {
	test(`condensed focus never moves the article at ${width}px`, async ({ page }) => {
		await page.setViewportSize({ width, height: 1050 });
		await condensed(page);
		const before = await condensedGeometry(page);
		expect(before.overflow).toBe(false);

		for (const sentenceID of ['demo-sentence-1', 'demo-sentence-3']) {
			const sentence = page.locator(`[data-sentence-id="${sentenceID}"]`);
			await sentence.focus();
			await expect(sentence).toHaveClass(/focused/);
			// Focusing only enables the stable action row; the text must not rewrap.
			expect(await condensedGeometry(page)).toEqual(before);
			await page.keyboard.press('Escape');
			await expect(sentence).not.toHaveClass(/focused/);
			expect(await condensedGeometry(page)).toEqual(before);
		}
	});
}

test('the condensed action row tracks the active sentence without adding gaps', async ({ page }) => {
	await condensed(page);
	const row = page.locator('[data-condensed-action-row]');
	await expect(row).toBeVisible();
	await expect(row).toContainText('No active sentence');
	await expect(row.getByRole('button', { name: /Play sentence|Audio not ready/ })).toBeDisabled();
	await expect(row.getByRole('button', { name: /Translate sentence/ })).toBeDisabled();
	const idleBox = await row.boundingBox();

	const sentence = page.locator('[data-sentence-id="demo-sentence-1"]');
	await sentence.focus();
	await expect(row).toContainText('Sentence 2');
	await expect(row.getByRole('button', { name: /Translate sentence 2/ })).toBeEnabled();
	// The fixture carries no sentence audio, so Play stays disabled but stable.
	await expect(row.getByRole('button', { name: 'Audio not ready' })).toBeDisabled();
	expect(await row.boundingBox()).toEqual(idleBox);

	await page.keyboard.press('Escape');
	await expect(row).toContainText('No active sentence');
	expect(await row.boundingBox()).toEqual(idleBox);
});

test('condensed words keep hover and click interactions', async ({ page }) => {
	await condensed(page);
	await page.locator('[data-occurrence-id="demo-1-word-1"]').hover();
	await expect(page.getByRole('dialog')).toContainText('gave up');

	await page.keyboard.press('Escape');
	await page.locator('[data-occurrence-id="demo-0-word-11"]').click();
	await expect(page.getByRole('dialog')).toContainText('rest');
});

test('returning to learning restores subtitles and sentence tools', async ({ page }) => {
	await condensed(page);
	await expect(page.locator('.translation-subtitle:visible')).toHaveCount(0);

	await page.getByRole('button', { name: 'Learning', exact: true }).click();
	await expect(page.locator('.reader-shell')).toHaveAttribute('data-reading-mode', 'learning');
	await expect(page.locator('.translation-subtitle').first()).toBeVisible();
	await expect(page.locator('.sentence-footer').first()).toBeVisible();
	await expect(page.locator('[data-occurrence-id="demo-0-word-11"] .translation-subtitle')).toHaveText(
		'rest'
	);
});
