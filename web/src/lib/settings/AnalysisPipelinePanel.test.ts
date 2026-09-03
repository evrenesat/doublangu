import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import AnalysisPipelinePanel from './AnalysisPipelinePanel.svelte';

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

const provider = {
	id: 'codex-app-server',
	label: 'Codex',
	type: 'codex_app_server',
	enabled: true,
	stale: false,
	health: 'healthy',
	models: [{ id: 'model-a', display_name: 'Model A', supported_reasoning_efforts: [{ value: 'low' }] }]
};

afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
	for (const cookie of document.cookie.split(';')) {
		const name = cookie.split('=')[0]?.trim();
		if (name) document.cookie = `${name}=; Max-Age=0; Path=/`;
	}
});

it('creating a profile does not activate it, and manual activation saves explicitly', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const stored: unknown[] = [];
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		const method = init.method ?? 'GET';
		if (input === '/api/v1/analysis/providers') return json(200, { providers: [provider] });
		if (input === '/api/v1/analysis/profiles' && method === 'GET') return json(200, { profiles: stored });
		if (input === '/api/v1/analysis/settings' && method === 'GET') return json(200, { active_profile_id: '' });
		if (input === '/api/v1/analysis/profiles' && method === 'POST') {
			const body = JSON.parse(String(init.body));
			const created = { id: 'profile-1', name: body.name, bindings: body.bindings, options: body.options };
			stored.push(created);
			return json(200, created);
		}
		if (input === '/api/v1/analysis/settings' && method === 'PUT') return json(200, { active_profile_id: 'profile-1' });
		throw new Error(`unexpected request ${method} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(AnalysisPipelinePanel);
	await waitFor(() => expect(screen.getByRole('button', { name: 'New profile' })).toBeTruthy());

	await fireEvent.click(screen.getByRole('button', { name: 'New profile' }));
	await fireEvent.input(screen.getByPlaceholderText('e.g. Mixed codex + omlx'), { target: { value: 'Mixed' } });
	// Assign the only provider to both stage slots; models and efforts default from the catalog.
	const providerSelects = screen
		.getAllByRole('combobox')
		.filter((select) => select.querySelector('option')?.textContent === 'Select a provider');
	expect(providerSelects).toHaveLength(2);
	for (const select of providerSelects) {
		await fireEvent.change(select, { target: { value: 'codex-app-server' } });
	}

	await fireEvent.click(screen.getByRole('button', { name: 'Create profile' }));
	await waitFor(() => expect(screen.getByText('Mixed')).toBeTruthy());

	// Creation persisted the profile but did not touch the active-selection setting.
	const posts = fetchMock.mock.calls.filter(([url, init]) => url === '/api/v1/analysis/profiles' && (init?.method ?? 'GET') === 'POST');
	expect(posts).toHaveLength(1);
	expect(JSON.parse(String(posts[0]?.[1]?.body)).name).toBe('Mixed');
	const puts = fetchMock.mock.calls.filter(([url, init]) => url === '/api/v1/analysis/settings' && (init?.method ?? 'GET') === 'PUT');
	expect(puts).toEqual([]);
	expect((screen.getByRole('radio', { name: /Mixed/ }) as HTMLInputElement).checked).toBe(false);

	// Manual activation through the profile radio saves explicitly.
	await fireEvent.click(screen.getByRole('radio', { name: /Mixed/ }));
	await waitFor(() =>
		expect(
			fetchMock.mock.calls.some(
				([url, init]) => url === '/api/v1/analysis/settings' && (init?.method ?? 'GET') === 'PUT'
			)
		).toBe(true)
	);
	const save = fetchMock.mock.calls.find(
		([url, init]) => url === '/api/v1/analysis/settings' && (init?.method ?? 'GET') === 'PUT'
	);
	expect(save?.[1]?.body).toBe(JSON.stringify({ active_profile_id: 'profile-1' }));
});
