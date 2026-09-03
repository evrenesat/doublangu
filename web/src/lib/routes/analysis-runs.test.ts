import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import AnalysisRunsPage from '../../routes/analysis-runs/+page.svelte';

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

const bindingRun = {
	id: 'run-ok', article_id: 'article-1', article_title: 'Heel het dorp', attempt_count: 1,
	requested_model: '', requested_effort: '', status: 'succeeded',
	profile_name: 'Imported Codex', profile_snapshot_hash: 'hash',
	bindings: [
		{ stage_id: 'linguistic_analysis', provider_id: 'codex-app-server', model_id: 'model-a' },
		{ stage_id: 'translation', provider_id: 'codex-app-server', model_id: 'model-b' }
	],
	total_paragraphs: 3, completed_paragraphs: 3, failed_block_index: -1, duration_ms: 35300,
	started_at: '2026-01-02T18:21:00Z', completed_at: '2026-01-02T18:21:35Z', error_code: ''
};

const failedRun = {
	id: 'run-bad', article_id: 'article-1', article_title: 'Heel het dorp', attempt_count: 2,
	requested_model: '', requested_effort: '', status: 'failed',
	profile_name: 'Imported Codex',
	bindings: [{ stage_id: 'linguistic_analysis', provider_id: 'codex-app-server', model_id: 'model-a' }],
	total_paragraphs: 4, completed_paragraphs: 3, failed_block_index: 3, duration_ms: 25900,
	started_at: '2026-01-02T17:54:00Z', completed_at: '2026-01-02T17:54:26Z', error_code: 'v1.analysis_stage_failed'
};

const legacyRun = {
	id: 'run-old', article_id: 'article-2', article_title: 'Een dag', attempt_count: 1,
	requested_model: 'model-x', requested_effort: 'medium', status: 'running',
	total_paragraphs: 5, completed_paragraphs: 2, failed_block_index: -1, duration_ms: 400,
	started_at: '2026-01-02T10:00:00Z', completed_at: '', error_code: ''
};

afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
});

it('loads the first 25 runs with status, provenance, and detail links', async () => {
	const fetchMock = vi.fn(async (input: string): Promise<Response> => {
		if (input === '/api/v1/analysis/runs?limit=25') return json(200, { runs: [bindingRun, failedRun, legacyRun], next_cursor: '' });
		throw new Error(`unexpected request ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(AnalysisRunsPage);
	// Wait for data-driven content rather than the static heading.
	expect(await screen.findByText('Succeeded')).toBeTruthy();

	// All three lifecycle states render.
	expect(screen.getByText('Failed')).toBeTruthy();
	expect(screen.getByText('Running')).toBeTruthy();

	// Provenance: profile + bindings for pipeline runs, legacy model/effort otherwise.
	expect(screen.getByText('Imported Codex · codex-app-server · model-a · codex-app-server · model-b')).toBeTruthy();
	expect(screen.getByText('model-x · medium')).toBeTruthy();

	// Progress, failed paragraph, error code, and duration are exposed.
	expect(screen.getByText('failed at paragraph 4')).toBeTruthy();
	expect(screen.getByText('v1.analysis_stage_failed')).toBeTruthy();
	expect(screen.getByText('35.3 s')).toBeTruthy();
	expect(screen.getByText('25.9 s')).toBeTruthy();

	// Every run links to the top-level detail route.
	expect((document.querySelector('a[href="/analysis-runs/run-ok"]'))).toBeTruthy();
	expect((document.querySelector('a[href="/analysis-runs/run-bad"]'))).toBeTruthy();
	expect((document.querySelector('a[href="/analysis-runs/run-old"]'))).toBeTruthy();

	// No cursor, no Load more.
	expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull();
});

it('appends the next page instead of replacing loaded runs', async () => {
	const fetchMock = vi.fn(async (input: string): Promise<Response> => {
		if (input === '/api/v1/analysis/runs?limit=25') return json(200, { runs: [bindingRun], next_cursor: 'cursor-1' });
		if (input === '/api/v1/analysis/runs?limit=25&cursor=cursor-1') return json(200, { runs: [failedRun], next_cursor: '' });
		throw new Error(`unexpected request ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(AnalysisRunsPage);
	const loadMore = await screen.findByRole('button', { name: 'Load more' });
	expect(screen.queryByText('Heel het dorp')).toBeTruthy();
	expect(screen.queryByText('Een dag')).toBeNull();

	await fireEvent.click(loadMore);
	await waitFor(() => expect(screen.getByText('v1.analysis_stage_failed')).toBeTruthy());
	// Previously loaded runs stay visible.
	expect(screen.getByText('Imported Codex · codex-app-server · model-a · codex-app-server · model-b')).toBeTruthy();
	// The cursor is exhausted, so the control disappears.
	expect(screen.queryByRole('button', { name: 'Load more' })).toBeNull();
	expect(fetchMock).toHaveBeenCalledTimes(2);
});

it('shows an empty state, and a retryable error when the API fails', async () => {
	let healthy = false;
	const fetchMock = vi.fn(async (input: string): Promise<Response> => {
		if (input === '/api/v1/analysis/runs?limit=25') {
			if (!healthy) return json(500, { error: 'unavailable', code: 'v1.internal' });
			return json(200, { runs: [legacyRun], next_cursor: '' });
		}
		throw new Error(`unexpected request ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(AnalysisRunsPage);
	await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
	expect(screen.queryByText('Een dag')).toBeNull();

	healthy = true;
	await fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
	await waitFor(() => expect(screen.getByText('Een dag')).toBeTruthy());

	cleanup();
	vi.clearAllMocks();
	const emptyMock = vi.fn(async (input: string): Promise<Response> => {
		if (input === '/api/v1/analysis/runs?limit=25') return json(200, { runs: [], next_cursor: '' });
		throw new Error(`unexpected request ${input}`);
	});
	vi.stubGlobal('fetch', emptyMock);
	render(AnalysisRunsPage);
	await waitFor(() => expect(screen.getByText('No analysis runs have been recorded yet.')).toBeTruthy());
});
