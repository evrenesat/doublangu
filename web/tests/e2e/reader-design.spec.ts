import { expect, test, type Page } from '@playwright/test';
import { readerFixture, readerFixtureID } from '../../dev/readerFixture';
import { longReaderFixture, longReaderFixtureID } from '../../dev/longReaderFixture';

test.beforeEach(async ({ page }) => {
	await page.route('**/api/v1/auth/session', route => route.fulfill({ json: { authenticated: true } }));
	await page.route('**/api/v1/reader/settings', route => route.fulfill({ json: { pronounce_on_hover: false } }));
	await page.route(`**/api/v1/articles/${readerFixtureID}`, route => route.fulfill({ json: readerFixture }));
	await page.goto(`/reader/${readerFixtureID}`);
	await expect(page.locator('.text-occurrence')).toHaveCount(66);
});

async function geometry(page: Page) {
	return page.evaluate(() => ({
		scroll: window.scrollY,
		cards: [...document.querySelectorAll<HTMLElement>('.reader-sentence')].map(item => ({ top: item.offsetTop, height: item.offsetHeight })),
		words: [...document.querySelectorAll<HTMLElement>('.text-occurrence')].map(item => ({ id: item.dataset.occurrenceId, x: item.offsetLeft, y: item.offsetTop, width: item.offsetWidth, height: item.offsetHeight }))
	}));
}

for (const width of [375, 1360]) {
	test(`focus enlarges without rewrapping or scrolling at ${width}px`, async ({ page }) => {
		await page.setViewportSize({ width, height: 1050 });
		for (const mode of ['enlarge', 'highlight']) {
			await page.getByRole('combobox', { name: 'Sentence focus style' }).selectOption(mode);
			const sentence = page.locator('[data-sentence-id="demo-sentence-1"]');
			await sentence.scrollIntoViewIfNeeded();
			await page.mouse.move(2, 2);
			await page.keyboard.press('Escape');
			await expect(sentence).not.toHaveClass(/focused/);
			const before = await geometry(page);
			await sentence.hover();
			await expect(sentence).toHaveClass(/focused/);
			await expect.poll(async () => sentence.locator('.sentence-words').evaluate(e => getComputedStyle(e).transform)).toBe('matrix(1, 0, 0, 1, 0, 0)');
			expect(await geometry(page)).toEqual(before);
			await page.mouse.move(2, 2);
			await page.keyboard.press('Escape');
			await page.keyboard.press('Escape');
			await expect.poll(async () => sentence.locator('.sentence-words').evaluate(e => getComputedStyle(e).transform)).toBe(mode === 'enlarge' ? 'matrix(0.94, 0, 0, 0.94, 0, 0)' : 'matrix(1, 0, 0, 1, 0, 0)');
		}
	});
}

test('word subtitles, exact membership, punctuation and wrapped connections stay readable', async ({ page }) => {
	for (const width of [320, 768, 1360]) {
		await page.setViewportSize({ width, height: 1050 });
		await expect(page.locator('.construction-overlay path').first()).toBeAttached();
		const facts = await page.evaluate(() => {
			const subtitles = [...document.querySelectorAll<HTMLElement>('.translation-subtitle')];
			const boxes = subtitles.map(e => e.getBoundingClientRect());
			return {
				missing: subtitles.filter(e => !e.textContent?.trim() || e.textContent === '·').length,
				overflow: document.documentElement.scrollWidth > window.innerWidth,
				overlaps: boxes.some((a,i) => boxes.some((b,j) => i < j && a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom)),
				sources: [...document.querySelectorAll('.sentence-words')].map(e => {
					const copy = e.cloneNode(true) as HTMLElement; copy.querySelectorAll('.translation-subtitle,svg').forEach(node => node.remove());
					return copy.textContent?.replace(/\s+/g, ' ').trim();
				})
			};
		});
		expect(facts).toEqual({ missing: 0, overflow: false, overlaps: false, sources: readerFixture.blocks.map(b => b.source_text) });
		await expect(page.locator('[data-occurrence-id="demo-3-word-2"]')).not.toHaveClass(/construction-member/);
		await expect(page.locator('[data-occurrence-id="demo-3-word-3"]')).not.toHaveClass(/construction-member/);
		await expect(page.locator('[data-occurrence-id="demo-0-word-11"] .translation-subtitle')).toHaveText('rest');
	}
	await page.locator('[data-occurrence-id="demo-1-word-1"]').hover();
	await expect(page.getByRole('dialog')).toContainText('gave up');
	await page.locator('[data-occurrence-id="demo-1-word-5"]').hover();
	await expect(page.locator('[data-occurrence-id="demo-1-word-1"]')).toHaveClass(/construction-active/);
});

test('each reading theme keeps source and subtitles legible on both surfaces', async ({ page }) => {
	for (const theme of ['Ink','Paper','Sepia','Contrast']) {
		await page.getByRole('button', { name: theme, exact: true }).click();
		const ratios = await page.locator('.reader-shell').evaluate(element => {
			const style = getComputedStyle(element);
			const luminance = (hex: string) => {
				let h = hex.trim().replace('#',''); if (h.length === 3) h = h.split('').map(v=>v+v).join('');
				return [0,2,4].map(i => parseInt(h.slice(i,i+2),16)/255).map(v=>v<=0.04045?v/12.92:((v+0.055)/1.055)**2.4).reduce((sum,v,i)=>sum+v*[0.2126,0.7152,0.0722][i]!,0);
			};
			const values: number[] = [];
			for (const foreground of ['--reader-text','--reader-subtitle','--reader-accent','--reader-construction']) for (const background of ['--reader-bg','--reader-surface']) {
				const a=luminance(style.getPropertyValue(foreground));const b=luminance(style.getPropertyValue(background));values.push((Math.max(a,b)+0.05)/(Math.min(a,b)+0.05));
			}
			return values;
		});
		for (const ratio of ratios) expect(ratio, theme).toBeGreaterThanOrEqual(4.5);
	}
});

test('a full-length article stays dense, complete and stable deep into mobile reading', async ({ page }) => {
	await page.route(`**/api/v1/articles/${longReaderFixtureID}`, route => route.fulfill({ json: longReaderFixture }));
	await page.setViewportSize({ width: 375, height: 812 });
	await page.goto(`/reader/${longReaderFixtureID}`);
	await expect(page.locator('.text-occurrence')).toHaveCount(871);
	await expect(page.locator('.paragraph-zone')).toHaveCount(12);
	await expect(page.locator('.reader-sentence')).toHaveCount(48);
	await expect(page.locator('[data-occurrence-id="demo-28-word-5"]')).not.toHaveClass(/construction-member/);
	await expect(page.locator('[data-occurrence-id="demo-28-word-12"]')).toHaveClass(/construction-member/);
	const density = await page.evaluate(() => {
		const words = document.querySelectorAll<HTMLElement>('.text-occurrence');
		const body = document.querySelector('.reader-body')!.getBoundingClientRect();
		return { wordsPerThousandPixels: words.length / body.height * 1000, start: body.top,
			subtitlePixels: parseFloat(getComputedStyle(document.querySelector('.translation-subtitle')!).fontSize) * 0.94,
			sourcePixels: parseFloat(getComputedStyle(document.querySelector('.source-text')!).fontSize) * 0.94 };
	});
	expect(density.wordsPerThousandPixels).toBeGreaterThan(60);
	expect(density.start).toBeLessThan(370);
	expect(density.subtitlePixels).toBeGreaterThanOrEqual(12);
	expect(density.sourcePixels).toBeGreaterThanOrEqual(20);
	for (const width of [320, 375, 1360]) {
		await page.setViewportSize({ width, height: 812 });
		const facts = await page.evaluate(() => {
			const subtitles = [...document.querySelectorAll<HTMLElement>('.translation-subtitle')];
			const boxes = subtitles.map(element => element.getBoundingClientRect());
			return { missing: subtitles.filter(element => !element.textContent?.trim() || element.textContent === '·').length,
				overflow: document.documentElement.scrollWidth > innerWidth,
				overlaps: boxes.some((a, i) => boxes.some((b, j) => i < j && a.left < b.right && b.left < a.right && a.top < b.bottom && b.top < a.bottom)),
				sources: [...document.querySelectorAll('.sentence-words')].map(element => {
					const copy = element.cloneNode(true) as HTMLElement;
					copy.querySelectorAll('.translation-subtitle,svg').forEach(node => node.remove());
					return copy.textContent?.replace(/\s+/g, ' ').trim();
				}) };
		});
		expect(facts).toEqual({ missing: 0, overflow: false, overlaps: false, sources: longReaderFixture.sentences.map(sentence => sentence.source_text) });
	}
	await page.setViewportSize({ width: 375, height: 812 });
	for (const mode of ['enlarge', 'highlight']) {
		await page.getByRole('combobox', { name: 'Sentence focus style' }).selectOption(mode);
		for (const index of [26, 44, 47]) {
			const sentence = page.locator(`[data-sentence-id="demo-sentence-${index}"]`);
			await sentence.scrollIntoViewIfNeeded();
			await page.mouse.move(2, 2);
			await page.keyboard.press('Escape');
			await page.keyboard.press('Escape');
			await expect(page.getByRole('dialog')).toHaveCount(0);
			await expect(sentence).not.toHaveClass(/focused/);
			const before = await geometry(page);
			await sentence.hover({ position: { x: 3, y: 3 } });
			await expect(sentence).toHaveClass(/focused/);
			await expect.poll(async () => sentence.locator('.sentence-words').evaluate(element => getComputedStyle(element).transform)).toBe('matrix(1, 0, 0, 1, 0, 0)');
			expect(await geometry(page)).toEqual(before);
		}
	}
	await page.getByRole('button', { name: 'Reading appearance', exact: true }).click();
	await page.getByRole('button', { name: 'Sepia', exact: true }).click();
	await expect(page.getByRole('button', { name: 'Sepia', exact: true })).toHaveAttribute('aria-pressed', 'true');
});
