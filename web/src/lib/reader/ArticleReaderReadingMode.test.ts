import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import type { Article } from '$lib/api/client';
import ArticleReader from './ArticleReader.svelte';
import { READING_MODE_STORAGE_KEY } from './readingMode';

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

const article: Article = {
	id: 'article-id',
	title: 'Een dag',
	source_language: 'nl',
	target_language: 'en',
	enrichment_status: 'ready',
	enrichment_error_code: '',
	created_at: '2026-01-02T00:00:00Z',
	updated_at: '2026-01-02T00:00:00Z',
	blocks: [],
	content_hash: 'hash',
	analysis_status: 'ready',
	analysis_revision: 'rev-1',
	analysis_error_code: '',
	analysis_model: '',
	analysis_effort: '',
	narration_status: 'not_requested',
	narration_error_code: '',
	analysis_progress: { total_paragraphs: 1, completed_paragraphs: 1, current_block_index: 0, failed_block_index: -1 },
	sentences: [],
	occurrences: [],
	narration: { status: 'not_requested', error_code: '', sentence_count: 0, ready_count: 0, duration_ms: 0, size_bytes: 0, reclaimable_bytes: 0 },
	analysis_pipeline: { profile_id: 'profile-a', profile_name: 'A', snapshot_hash: 'hash' }
};

function stubFetch() {
	const fetchMock = vi.fn(async (input: string): Promise<Response> => {
		if (input === '/api/v1/reader/settings') return json(200, { pronounce_on_hover: true, updated_at: '' });
		throw new Error(`unexpected request GET ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);
}

beforeEach(() => {
	stubFetch();
	localStorage.clear();
});

afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
	localStorage.clear();
});

it('defaults to learning without an action row and remembers condensed', async () => {
	render(ArticleReader, { props: { article, onArticleChange: () => {} } });
	await waitFor(() => expect(screen.getByRole('button', { name: 'Condensed' })).toBeTruthy());
	expect(document.querySelector('.reader-shell')?.getAttribute('data-reading-mode')).toBe('learning');
	expect(screen.getByRole('button', { name: 'Learning' }).getAttribute('aria-pressed')).toBe('true');
	expect(document.querySelector('[data-condensed-action-row]')).toBeNull();

	await fireEvent.click(screen.getByRole('button', { name: 'Condensed' }));
	expect(document.querySelector('.reader-shell')?.getAttribute('data-reading-mode')).toBe('condensed');
	expect(screen.getByRole('button', { name: 'Condensed' }).getAttribute('aria-pressed')).toBe('true');
	expect(localStorage.getItem(READING_MODE_STORAGE_KEY)).toBe('condensed');
	const row = document.querySelector('[data-condensed-action-row]');
	expect(row?.textContent).toContain('No active sentence');
});

it('restores a stored condensed choice on mount', async () => {
	localStorage.setItem(READING_MODE_STORAGE_KEY, 'condensed');
	render(ArticleReader, { props: { article, onArticleChange: () => {} } });
	await waitFor(() =>
		expect(document.querySelector('.reader-shell')?.getAttribute('data-reading-mode')).toBe('condensed')
	);
	expect(document.querySelector('[data-condensed-action-row]')).not.toBeNull();
});
