import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { SentenceTranslationEnvelope } from '$lib/api/client';
import SentenceTranslationPopover from './SentenceTranslationPopover.svelte';

vi.mock('$lib/api/client', () => ({
	getSentenceTranslation: vi.fn(async () => ({ status: 'missing' }) as never),
	startSentenceTranslation: vi.fn(async () => ({ status: 'missing' }) as never)
}));

vi.mock('$lib/paths', () => ({
	appPath: (path: string) => path
}));

afterEach(() => cleanup());

type Envelope = SentenceTranslationEnvelope;

function testBackend(
	lookupImpl: () => Promise<Envelope>,
	startImpl?: (_mode: string) => Promise<Envelope>
) {
	const lookup = vi.fn(async (_articleId: string, _sentenceId: string) => lookupImpl());
	const start = vi.fn(
		async (_articleId: string, _sentenceId: string, input: { mode: 'ensure' | 'regenerate' }) =>
			startImpl ? startImpl(input.mode) : ({ status: 'missing' } as Envelope)
	);
	const poll = vi.fn(async (_articleId: string, _sentenceId: string) => lookupImpl());
	return { lookup, start, poll, pollIntervalMs: 1 };
}

function testProps(overrides: Record<string, unknown> = {}) {
	return {
		articleId: 'article-1',
		sentenceId: 'sentence-1',
		sentenceLabel: 'Zij zit op een bank.',
		anchor: document.createElement('button'),
		autoEnsure: false,
		onEnter: () => {},
		onLeave: () => {},
		onClose: () => {},
		...overrides
	};
}

describe('SentenceTranslationPopover', () => {
	it('renders a saved translation without generating', async () => {
		const seam = testBackend(async () => ({
			status: 'ready',
			sentence_id: 'sentence-1',
			translation: 'She is sitting on a bench.',
			run_id: 'run-1'
		}));
		render(SentenceTranslationPopover, { props: { ...testProps({ autoEnsure: true }), _backend: seam } });
		await waitFor(() => expect(screen.getByText('She is sitting on a bench.')).toBeTruthy());
		expect(seam.start).not.toHaveBeenCalled();
		expect(screen.getByRole('button', { name: 'Regenerate' })).toBeTruthy();
		expect(screen.getByRole('link', { name: 'View run' }).getAttribute('href')).toBe(
			'/analysis-runs/run-1'
		);
	});

	it('opens read-only until the dwell signal fires', async () => {
		const seam = testBackend(
			async () => ({ status: 'missing' }),
			async () => ({ status: 'ready', sentence_id: 'sentence-1', translation: 'Fresh translation.' })
		);
		render(SentenceTranslationPopover, { props: { ...testProps(), _backend: seam } });
		// Opened but dwell has not fired: lookup only, no generation.
		await waitFor(() => expect(screen.getByText('Preparing translation…')).toBeTruthy());
		expect(seam.start).not.toHaveBeenCalled();
		expect(seam.poll).not.toHaveBeenCalled();
	});

	it('autoEnsure starts one ensure and shows the result', async () => {
		const seam = testBackend(
			async () => ({ status: 'missing' }),
			async () => ({ status: 'ready', sentence_id: 'sentence-1', translation: 'Fresh translation.' })
		);
		render(SentenceTranslationPopover, { props: { ...testProps({ autoEnsure: true }), _backend: seam } });
		await waitFor(() => expect(screen.getByText('Fresh translation.')).toBeTruthy());
		expect(seam.start).toHaveBeenCalledTimes(1);
		expect(seam.start).toHaveBeenCalledWith('article-1', 'sentence-1', { mode: 'ensure' });
	});

	it('failed generation offers Retry and View run without looping', async () => {
		const seam = testBackend(async () => ({
			status: 'failed',
			sentence_id: 'sentence-1',
			error_code: 'v1.st_invalid',
			error_summary: 'Provider returned invalid output.',
			run_id: 'run-9'
		}));
		render(SentenceTranslationPopover, { props: { ...testProps({ autoEnsure: true }), _backend: seam } });
		await waitFor(() => expect(screen.getByText('Provider returned invalid output.')).toBeTruthy());
		// The failure is retained, never auto-retried.
		expect(seam.start).not.toHaveBeenCalled();
		expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy();
		expect(screen.getByRole('link', { name: 'View run' })).toBeTruthy();
	});

	it('failed replacement keeps the old text with a regeneration retry', async () => {
		const seam = testBackend(
			async () => ({
				status: 'ready',
				sentence_id: 'sentence-1',
				translation: 'Old translation.'
			}),
			async () => ({
				status: 'failed',
				sentence_id: 'sentence-1',
				error_code: 'v1.st_invalid',
				run_id: 'run-2'
			})
		);
		render(SentenceTranslationPopover, { props: { ...testProps(), _backend: seam } });
		await waitFor(() => expect(screen.getByText('Old translation.')).toBeTruthy());
		await fireEvent.click(screen.getByRole('button', { name: 'Regenerate' }));
		await waitFor(() =>
			expect(screen.getByRole('button', { name: 'Retry regeneration' })).toBeTruthy()
		);
		expect(screen.getByText('Old translation.')).toBeTruthy();
		expect(seam.start).toHaveBeenCalledWith('article-1', 'sentence-1', { mode: 'regenerate' });
	});

	it('shows no active sentence without a subject', async () => {
		render(SentenceTranslationPopover, { props: testProps({ sentenceId: '', sentenceLabel: '' }) });
		await waitFor(() => expect(screen.getByText('No active sentence')).toBeTruthy());
	});
});
