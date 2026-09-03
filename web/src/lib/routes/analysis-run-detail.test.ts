import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import RunDetailPage from '../../routes/analysis-runs/[id]/+page.svelte';

function json(body: unknown, status = 200): Response {
	return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
	delete (globalThis as typeof globalThis & { __doublanguPage?: unknown }).__doublanguPage;
});

it('renders retained run artifacts as escaped diagnostic text', async () => {
	(globalThis as typeof globalThis & { __doublanguPage?: unknown }).__doublanguPage = { params: { id: 'run /?' } };
	const run = {
		id: 'run /?', article_id: 'article-id', article_title: 'Een dag', job_id: 'job-id', attempt_count: 2,
		content_hash: 'hash', contract_version: 'reader.analysis.v2', prompt_version: 'reader-analysis-prompt.v2',
		requested_model: 'model-a', requested_effort: 'low', provider_id: 'codex.appserver', codex_cli_version: 'codex 1',
		reported_model: 'model-a', started_at: '2026-01-02T00:00:00Z', completed_at: '2026-01-02T00:00:01Z',
		duration_ms: 1000, status: 'failed', total_paragraphs: 2, completed_paragraphs: 1, failed_block_index: 1,
		error_code: 'v1.annotator_invalid_output', error_detail: 'validation <detail>', turns: [{
			id: 'turn-id', run_id: 'run /?', block_index: 1, turn_index: 0, turn_kind: 'initial',
			prompt: 'prompt <script>alert(1)</script>', output_schema: '{"type":"object"}',
			completed_response: 'response <script>alert(2)</script>', response_hash: 'hash',
			validation_error: 'bad <field>', provider_error: 'provider <error>', completion_metadata_json: '{"model":"model-a"}',
			provider_stderr_excerpt: '', started_at: '2026-01-02T00:00:00Z', completed_at: '2026-01-02T00:00:01Z',
			duration_ms: 1000, status: 'failed'
		}]
	};
	const fetchMock = vi.fn().mockResolvedValue(json(run));
	vi.stubGlobal('fetch', fetchMock);

	render(RunDetailPage);
	await waitFor(() => expect(screen.getByText('Turn artifacts')).toBeTruthy());
	// The back link points at the top-level run list, not settings.
	const backLink = document.querySelector('a.back-link') as HTMLAnchorElement | null;
	expect(backLink?.getAttribute('href')).toBe('/analysis-runs');
	expect(backLink?.textContent).toBe('← Analysis runs');
	expect(screen.getAllByText('model-a')).toHaveLength(2);
	expect(screen.getByText('v1.annotator_invalid_output')).toBeTruthy();
	expect(screen.getByText('prompt <script>alert(1)</script>')).toBeTruthy();
	expect(screen.getByText('response <script>alert(2)</script>')).toBeTruthy();
	expect(screen.getByText('bad <field>')).toBeTruthy();
	expect(document.querySelector('script')).toBeNull();
	expect(fetchMock).toHaveBeenCalledWith('/api/v1/analysis/runs/run%20%2F%3F', expect.objectContaining({ credentials: 'same-origin' }));
});

it('shows a retryable not-found state for a missing run', async () => {
	(globalThis as typeof globalThis & { __doublanguPage?: unknown }).__doublanguPage = { params: { id: 'missing' } };
	let healthy = false;
	const fetchMock = vi.fn(async (): Promise<Response> => {
		if (!healthy) return json({ error: 'Not found', code: 'v1.not_found' }, 404);
		return json({
			id: 'missing', article_id: 'a', article_title: 'Een dag', requested_model: 'm', requested_effort: 'low',
			provider_id: 'p', started_at: '', duration_ms: 0, status: 'succeeded', total_paragraphs: 1,
			completed_paragraphs: 1, failed_block_index: -1, error_code: '', turns: []
		});
	});
	vi.stubGlobal('fetch', fetchMock);

	render(RunDetailPage);
	await waitFor(() => expect(screen.getByText('Analysis run not found.')).toBeTruthy());
	const retry = screen.getByRole('button', { name: 'Retry' });
	expect(retry).toBeTruthy();
	healthy = true;
	await fireEvent.click(retry);
	await waitFor(() => expect(screen.getByText('Een dag')).toBeTruthy());
});
