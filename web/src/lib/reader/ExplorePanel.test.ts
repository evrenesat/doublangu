import { cleanup, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import ExplorePanel from './ExplorePanel.svelte';

vi.mock('$lib/api/client', () => {
	const readyDocument = {
		version: 'reader.dictionary.v1',
		lookup_form: 'bank',
		lookup_kind: 'word',
		source_language: 'nl',
		target_language: 'en',
		senses: [
			{
				part_of_speech: 'noun',
				translation_en: 'bench',
				meaning_en: 'A long seat for several people.',
				usage_en: 'In a park this is usually a bench.',
				pattern_nl: '',
				parts: [],
				examples: [{ text_nl: 'Zij zit op een bank.', translation_en: 'She is sitting on a bench.' }]
			},
			{
				part_of_speech: 'noun',
				translation_en: 'bank',
				meaning_en: 'A financial institution.',
				usage_en: '',
				pattern_nl: '',
				parts: [],
				examples: [{ text_nl: 'Ik zet geld op de bank.', translation_en: 'I put money in the bank.' }]
			}
		]
	};
	return {
		getDictionaryEntry: vi.fn(async (_articleId: string, ref: { occurrenceId?: string; annotationId?: string }) => {
			if (ref.occurrenceId === 'w-missing') return { status: 'missing' } as never;
			return { status: 'ready', entry_id: 'entry-1', document: readyDocument } as never;
		}),
		startDictionaryExplore: vi.fn(async () => ({ status: 'queued', entry_id: 'entry-2', job_id: 'job-2' }) as never),
		getDictionaryEntryById: vi.fn(async () => ({ status: 'ready', entry_id: 'entry-2', document: readyDocument }) as never)
	};
});

afterEach(() => cleanup());

describe('ExplorePanel', () => {
	it('renders a saved entry in sequential numbered sections', async () => {
		render(ExplorePanel, { articleId: 'article-1', occurrenceId: 'w-ready' });
		await waitFor(() => expect(screen.getByText('Saved dictionary entry')).toBeTruthy());
		expect(screen.getByText('bench')).toBeTruthy();
		expect(screen.getByText('A long seat for several people.')).toBeTruthy();
		expect(screen.getByText('In a park this is usually a bench.')).toBeTruthy();
		expect(screen.getByText('A financial institution.')).toBeTruthy();
		// Dutch examples carry lang="nl"; English fields lang="en".
		const dutch = screen.getByText('Zij zit op een bank.');
		expect(dutch.getAttribute('lang')).toBe('nl');
		const english = screen.getByText('She is sitting on a bench.');
		expect(english.getAttribute('lang')).toBe('en');
		// Two numbered meanings rendered in order.
		expect(screen.getByText('1')).toBeTruthy();
		expect(screen.getByText('2')).toBeTruthy();
	});

	it('starts generation for a missing entry and shows the queued state', async () => {
		const { getDictionaryEntry, startDictionaryExplore } = await import('$lib/api/client');
		render(ExplorePanel, { articleId: 'article-1', occurrenceId: 'w-missing' });
		await waitFor(() => expect(screen.getByText('Queued…')).toBeTruthy());
		expect(getDictionaryEntry).toHaveBeenCalled();
		expect(startDictionaryExplore).toHaveBeenCalledWith('article-1', {
			occurrence_id: 'w-missing',
			annotation_id: undefined,
			retry: false
		});
	});

	it('surfaces failure with Retry and never invents content', async () => {
		const { getDictionaryEntry } = await import('$lib/api/client');
		(getDictionaryEntry as ReturnType<typeof vi.fn>).mockResolvedValueOnce({
			status: 'failed',
			entry_id: 'entry-3',
			error_code: 'v1.dictionary_invalid_output'
		});
		render(ExplorePanel, { articleId: 'article-1', occurrenceId: 'w-ready' });
		await waitFor(() => expect(screen.getByText('Could not generate an explanation.')).toBeTruthy());
		expect(screen.getByRole('button', { name: 'Retry' })).toBeTruthy();
		expect(screen.queryByText('Saved dictionary entry')).toBeNull();
	});
});
