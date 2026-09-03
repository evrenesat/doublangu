import { cleanup, render, screen, waitFor } from '@testing-library/svelte';
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

it('renders recent runs without any legacy model editor when no providers exist', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const run = {
		id: 'run-id', article_id: 'article-id', article_title: 'Een dag', attempt_count: 1,
		requested_model: 'visible-model', requested_effort: 'medium', status: 'succeeded',
		total_paragraphs: 2, completed_paragraphs: 2, failed_block_index: -1, duration_ms: 123,
		started_at: '2026-01-02T00:00:00Z', completed_at: '2026-01-02T00:00:01Z', error_code: ''
	};
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		if (input === '/health') return json(200, { core_ready: true, loader_ready: true, schema_available: true, registry_state: 'ready', plugin_count: 0, plugin_ids: [] });
		if (input === '/api/v1/analysis/runs?limit=10') return json(200, { runs: [run] });
		if (input === '/api/v1/analysis/providers') return json(200, { providers: [] });
		if (input === '/api/v1/analysis/profiles') return json(200, { profiles: [] });
		if (input === '/api/v1/analysis/settings') return json(200, { active_profile_id: '' });
		throw new Error(`unexpected request ${init.method ?? 'GET'} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(SettingsPage);
	await waitFor(() => expect(screen.getByText('Een dag')).toBeTruthy());
	// No legacy surface remains: no model editor, no legacy settings calls.
	expect(screen.queryByRole('heading', { name: 'Article analysis' })).toBeNull();
	expect(screen.queryByRole('button', { name: 'Save selection' })).toBeNull();
	expect(screen.queryByRole('button', { name: 'Refresh models' })).toBeNull();
	expect(screen.getByText('Choose the provider profile used for new article analysis.')).toBeTruthy();
	expect(screen.getByText('Een dag')).toBeTruthy();
	expect(screen.getByText('visible-model · medium')).toBeTruthy();
	expect(screen.getByText(/new articles cannot be analyzed until a provider is configured/)).toBeTruthy();
	const legacy = fetchMock.mock.calls.filter(([url]) => url === '/api/v1/analysis/models' || url === '/api/v1/analysis/pipeline-settings');
	expect(legacy).toEqual([]);
});

it('shows tuple tests and profile-bound runs when pipeline providers are configured', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const providers = {
		providers: [
			{
				id: 'codex-app-server',
				label: 'Codex',
				type: 'codex_app_server',
				enabled: true,
				stale: false,
				health: 'healthy',
				models: [{ id: 'model-a', display_name: 'Model A', supported_reasoning_efforts: [{ value: 'low' }] }]
			}
		]
	};
	const run = {
		id: 'run-id', article_id: 'article-id', article_title: 'Een dag', attempt_count: 1,
		requested_model: '', requested_effort: '', status: 'succeeded',
		profile_name: 'Mixed', profile_snapshot_hash: 'hash',
		bindings: [
			{ stage_id: 'linguistic_analysis', provider_id: 'codex-app-server', model_id: 'model-a' },
			{ stage_id: 'translation', provider_id: 'mac-omlx', model_id: 'model-b' }
		],
		total_paragraphs: 2, completed_paragraphs: 2, failed_block_index: -1, duration_ms: 123,
		started_at: '2026-01-02T00:00:00Z', completed_at: '2026-01-02T00:00:01Z', error_code: ''
	};
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		if (input === '/health') return json(200, { core_ready: true, loader_ready: true, schema_available: true, registry_state: 'ready', plugin_count: 0, plugin_ids: [] });
		if (input === '/api/v1/analysis/runs?limit=10') return json(200, { runs: [run] });
		if (input === '/api/v1/analysis/providers') return json(200, providers);
		if (input === '/api/v1/analysis/profiles') return json(200, { profiles: [] });
		if (input === '/api/v1/analysis/settings') return json(200, { active_profile_id: '' });
		throw new Error(`unexpected request ${init.method ?? 'GET'} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(SettingsPage);
	await waitFor(() => expect(screen.getByRole('heading', { name: 'Analysis providers & profiles' })).toBeTruthy());
	expect(screen.queryByRole('heading', { name: 'Article analysis' })).toBeNull();
	// Per-stage tuple tests and the per-provider refresh replace the old single test button.
	await waitFor(() => expect(screen.getByRole('button', { name: 'Refresh catalog' })).toBeTruthy());
	expect(screen.getByRole('button', { name: 'Test Linguistic analysis' })).toBeTruthy();
	expect(screen.getByRole('button', { name: 'Test Translation' })).toBeTruthy();
	// Recent runs show the profile plus both compact bindings.
	expect(screen.getByRole('heading', { name: 'Recent runs' })).toBeTruthy();
	expect(screen.getByText('Mixed · codex-app-server · model-a · mac-omlx · model-b')).toBeTruthy();
});
