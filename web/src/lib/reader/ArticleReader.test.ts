import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import type { Article } from '$lib/api/client';
import ArticleReader from './ArticleReader.svelte';

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

function goodBinding(stage: string) {
	return { stage_id: stage, provider_id: 'codex-app-server', model_id: 'model-a', options: {} };
}

function badBinding(stage: string) {
	return { ...goodBinding(stage), valid: false, validity_reason: 'provider retired' };
}

const pipelineArticle: Article = {
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

const legacyArticle: Article = { ...pipelineArticle, id: 'legacy-id', analysis_pipeline: undefined };

function stubFetch(article: Article, profiles: () => Response) {
	const calls: string[] = [];
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		calls.push(`${init.method ?? 'GET'} ${input} body=${String(init.body ?? '')}`);
		if (input === '/api/v1/reader/settings') return json(200, { pronounce_on_hover: false, updated_at: '' });
		if (input === '/api/v1/analysis/profiles') return profiles();
		if (input === `/api/v1/articles/${article.id}/reanalyze` && init.method === 'POST') return json(200, article);
		throw new Error(`unexpected request ${init.method ?? 'GET'} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);
	return { fetchMock, calls };
}

const profileCalls = (calls: string[]) => calls.filter((call) => call.includes('/api/v1/analysis/profiles'));

afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
	for (const cookie of document.cookie.split(';')) {
		const name = cookie.split('=')[0]?.trim();
		if (name) document.cookie = `${name}=; Max-Age=0; Path=/`;
	}
});

it('loads profiles on demand and defaults fresh runs to the active usable profile', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const { calls } = stubFetch(pipelineArticle, () =>
		json(200, {
			profiles: [
				{ id: 'profile-a', name: 'A', is_active: false, bindings: [goodBinding('linguistic_analysis'), goodBinding('translation')] },
				{ id: 'profile-b', name: 'B', is_active: true, bindings: [goodBinding('linguistic_analysis'), goodBinding('translation')] },
				{ id: 'profile-c', name: 'C', is_active: false, bindings: [badBinding('linguistic_analysis'), goodBinding('translation')] }
			]
		})
	);

	render(ArticleReader, { props: { article: pipelineArticle, onArticleChange: () => {} } });
	await waitFor(() => expect(screen.getByRole('button', { name: 'Fresh analysis…' })).toBeTruthy());
	// Normal reading performs no profile request until the workflow opens.
	expect(profileCalls(calls)).toEqual([]);
	expect(screen.queryByRole('combobox', { name: 'Profile for a fresh analysis run' })).toBeNull();

	await fireEvent.click(screen.getByRole('button', { name: 'Fresh analysis…' }));
	const select = (await screen.findByRole('combobox', { name: 'Profile for a fresh analysis run' })) as HTMLSelectElement;
	expect(profileCalls(calls)).toHaveLength(1);
	await waitFor(() => expect(select.value).toBe('profile-b'));
	const options = [...select.querySelectorAll('option')].map((option) => option.textContent);
	expect(options).toEqual(['A', 'B (active)']);

	await fireEvent.click(screen.getByRole('button', { name: 'Run fresh analysis' }));
	await waitFor(() => expect(calls.some((call) => call.includes('/reanalyze'))).toBe(true));
	const run = calls.find((call) => call.includes('/reanalyze'));
	expect(run).toBe(`POST /api/v1/articles/article-id/reanalyze body=${JSON.stringify({ fresh: true, profile_id: 'profile-b' })}`);
});

it('falls back to a usable profile when the active one is invalid, and honors selection changes', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const { calls } = stubFetch(pipelineArticle, () =>
		json(200, {
			profiles: [
				{ id: 'profile-x', name: 'X', is_active: true, bindings: [badBinding('linguistic_analysis'), goodBinding('translation')] },
				{ id: 'profile-y', name: 'Y', is_active: false, bindings: [goodBinding('linguistic_analysis'), goodBinding('translation')] },
				{ id: 'profile-z', name: 'Z', is_active: false, bindings: [goodBinding('linguistic_analysis'), goodBinding('translation')] }
			]
		})
	);

	render(ArticleReader, { props: { article: pipelineArticle, onArticleChange: () => {} } });
	await waitFor(() => expect(screen.getByRole('button', { name: 'Fresh analysis…' })).toBeTruthy());
	await fireEvent.click(screen.getByRole('button', { name: 'Fresh analysis…' }));
	const select = (await screen.findByRole('combobox', { name: 'Profile for a fresh analysis run' })) as HTMLSelectElement;
	await waitFor(() => expect(select.value).toBe('profile-y'));

	await fireEvent.change(select, { target: { value: 'profile-z' } });
	await fireEvent.click(screen.getByRole('button', { name: 'Run fresh analysis' }));
	await waitFor(() => expect(calls.some((call) => call.includes('/reanalyze'))).toBe(true));
	const run = calls.find((call) => call.includes('/reanalyze'));
	expect(run).toBe(`POST /api/v1/articles/article-id/reanalyze body=${JSON.stringify({ fresh: true, profile_id: 'profile-z' })}`);
});

it('gives legacy articles the same profile-selecting fresh workflow', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const { calls } = stubFetch(legacyArticle, () =>
		json(200, {
			profiles: [
				{ id: 'profile-b', name: 'B', is_active: true, bindings: [goodBinding('linguistic_analysis'), goodBinding('translation')] }
			]
		})
	);

	render(ArticleReader, { props: { article: legacyArticle, onArticleChange: () => {} } });
	await waitFor(() => expect(screen.getByRole('button', { name: 'Fresh analysis…' })).toBeTruthy());
	expect(profileCalls(calls)).toEqual([]);
	await fireEvent.click(screen.getByRole('button', { name: 'Fresh analysis…' }));
	const select = (await screen.findByRole('combobox', { name: 'Profile for a fresh analysis run' })) as HTMLSelectElement;
	await waitFor(() => expect(select.value).toBe('profile-b'));

	await fireEvent.click(screen.getByRole('button', { name: 'Run fresh analysis' }));
	await waitFor(() => expect(calls.some((call) => call.includes('/reanalyze'))).toBe(true));
	const run = calls.find((call) => call.includes('/reanalyze'));
	expect(run).toBe(`POST /api/v1/articles/legacy-id/reanalyze body=${JSON.stringify({ fresh: true, profile_id: 'profile-b' })}`);
});

it('replaces the fresh Run action with a Settings link when no profile is usable', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	stubFetch(pipelineArticle, () =>
		json(200, {
			profiles: [
				{ id: 'profile-c', name: 'C', is_active: true, bindings: [badBinding('linguistic_analysis'), goodBinding('translation')] }
			]
		})
	);

	render(ArticleReader, { props: { article: pipelineArticle, onArticleChange: () => {} } });
	await waitFor(() => expect(screen.getByRole('button', { name: 'Fresh analysis…' })).toBeTruthy());
	await fireEvent.click(screen.getByRole('button', { name: 'Fresh analysis…' }));
	await waitFor(() => expect(screen.getByRole('link', { name: 'Choose a usable profile in Settings' })).toBeTruthy());
	expect(screen.queryByRole('button', { name: 'Run fresh analysis' })).toBeNull();
});

it('retries the profile load after a transient failure and issues a new request', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	let attempts = 0;
	const { calls } = stubFetch(pipelineArticle, () => {
		attempts += 1;
		if (attempts === 1) return json(500, { error: 'boom' });
		return json(200, {
			profiles: [
				{ id: 'profile-b', name: 'B', is_active: true, bindings: [goodBinding('linguistic_analysis'), goodBinding('translation')] }
			]
		});
	});

	render(ArticleReader, { props: { article: pipelineArticle, onArticleChange: () => {} } });
	await waitFor(() => expect(screen.getByRole('button', { name: 'Fresh analysis…' })).toBeTruthy());
	await fireEvent.click(screen.getByRole('button', { name: 'Fresh analysis…' }));
	await waitFor(() => expect(screen.getByRole('button', { name: 'Retry loading profiles' })).toBeTruthy());
	expect(screen.queryByRole('button', { name: 'Run fresh analysis' })).toBeNull();
	expect(profileCalls(calls)).toHaveLength(1);

	await fireEvent.click(screen.getByRole('button', { name: 'Retry loading profiles' }));
	await waitFor(() => expect(screen.getByRole('button', { name: 'Run fresh analysis' })).toBeTruthy());
	expect(profileCalls(calls)).toHaveLength(2);
	expect(screen.queryByRole('button', { name: 'Retry loading profiles' })).toBeNull();
});
