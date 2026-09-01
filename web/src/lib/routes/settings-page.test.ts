import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import SettingsPage from '../../routes/settings/+page.svelte';

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
	for (const cookie of document.cookie.split(';')) {
		const name = cookie.split('=')[0]?.trim();
		if (name) document.cookie = `${name}=; Max-Age=0; Path=/`;
	}
});

it('loads runtime models, filters efforts, saves explicitly, and reports stale refreshes', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const models = {
		models: [
			{ id: 'visible-model', display_name: 'Visible', is_default: true, hidden: false, supported_reasoning_efforts: [{ value: 'low', description: 'Fast' }, { value: 'medium', description: 'Balanced' }] },
			{ id: 'hidden-model', display_name: 'Hidden', is_default: false, hidden: true, supported_reasoning_efforts: [{ value: 'high', description: 'Careful' }] }
		],
		retrieved_at: '2026-01-02T00:00:00Z',
		stale: false
	};
	const run = {
		id: 'run-id', article_id: 'article-id', article_title: 'Een dag', attempt_count: 1,
		requested_model: 'visible-model', requested_effort: 'medium', status: 'succeeded',
		total_paragraphs: 2, completed_paragraphs: 2, failed_block_index: -1, duration_ms: 123,
		started_at: '2026-01-02T00:00:00Z', completed_at: '2026-01-02T00:00:01Z', error_code: ''
	};
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		if (input === '/health') return json(200, { core_ready: true, loader_ready: true, schema_available: true, registry_state: 'ready', plugin_count: 0, plugin_ids: [] });
		if (input === '/api/v1/analysis/models?refresh=true') return json(200, { ...models, stale: true, last_error: 'refresh failed' });
		if (input === '/api/v1/analysis/models') return json(200, models);
		if (input === '/api/v1/analysis/settings' && init.method === 'PUT') return json(200, { model: 'hidden-model', effort: 'high', updated_at: '2026-01-02T00:00:02Z' });
		if (input === '/api/v1/analysis/settings') return json(200, { model: 'visible-model', effort: 'medium', updated_at: '2026-01-02T00:00:00Z' });
		if (input === '/api/v1/analysis/runs?limit=10') return json(200, { runs: [run] });
		throw new Error(`unexpected request ${init.method ?? 'GET'} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(SettingsPage);
	await waitFor(() => expect(screen.getByRole('option', { name: /Visible/ })).toBeTruthy());
	expect(screen.getByRole('option', { name: /Hidden · hidden/ })).toBeTruthy();
	expect(screen.getByText('Saved: visible-model · medium')).toBeTruthy();
	expect(screen.getByText('Een dag')).toBeTruthy();

	await fireEvent.change(screen.getByRole('combobox', { name: 'Model' }), { target: { value: 'hidden-model' } });
	await waitFor(() => expect(screen.getByRole('option', { name: /high/i })).toBeTruthy());
	expect(screen.queryByRole('option', { name: /low/i })).toBeNull();
	expect(screen.getByText('Unsaved selection')).toBeTruthy();
	await fireEvent.change(screen.getByRole('combobox', { name: 'Reasoning effort' }), { target: { value: 'high' } });

	await fireEvent.click(screen.getByRole('button', { name: 'Save selection' }));
	await waitFor(() => expect(screen.getByText('Saved: hidden-model · high')).toBeTruthy());
	const saveCall = fetchMock.mock.calls.find(([input, init]) => input === '/api/v1/analysis/settings' && init?.method === 'PUT');
	expect(saveCall?.[1]?.body).toBe(JSON.stringify({ model: 'hidden-model', effort: 'high' }));

	await fireEvent.click(screen.getByRole('button', { name: 'Refresh models' }));
	await waitFor(() => expect(screen.getByText('Using the last known model catalog. refresh failed')).toBeTruthy());
});
