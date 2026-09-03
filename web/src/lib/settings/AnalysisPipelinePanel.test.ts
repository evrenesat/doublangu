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

function stubStandardFetch(options: { providers?: unknown; profiles?: unknown; activeProfileID?: string } = {}): ReturnType<typeof vi.fn> {
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		const method = init.method ?? 'GET';
		if (input === '/api/v1/analysis/providers' && method === 'GET') return json(200, { providers: options.providers ?? [provider] });
		if (input === '/api/v1/analysis/profiles' && method === 'GET') return json(200, { profiles: options.profiles ?? [] });
		if (input === '/api/v1/analysis/settings' && method === 'GET') return json(200, { active_profile_id: options.activeProfileID ?? '' });
		throw new Error(`unexpected request ${method} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);
	return fetchMock;
}

it('shows the active profile above collapsed provider test controls', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const staleProvider = { ...provider, stale: true, last_error: 'catalog refresh failed earlier' };
	const profiles = [
		{
			id: 'profile-1',
			name: 'Main',
			is_active: true,
			bindings: [
				{ stage_id: 'linguistic_analysis', provider_id: 'codex-app-server', model_id: 'model-a', options: { reasoning_effort: 'low' } },
				{ stage_id: 'translation', provider_id: 'codex-app-server', model_id: 'model-a', options: { reasoning_effort: 'low' } }
			]
		}
	];
	stubStandardFetch({ providers: [staleProvider], profiles, activeProfileID: 'profile-1' });

	render(AnalysisPipelinePanel);
	await waitFor(() => expect(screen.getByRole('heading', { name: 'Active profile' })).toBeTruthy());

	// Information order: active profile, then profiles, then providers.
	const activeHeading = screen.getByRole('heading', { name: 'Active profile' });
	const profilesHeading = screen.getByRole('heading', { name: 'Profiles' });
	const providersHeading = screen.getByRole('heading', { name: 'Providers' });
	expect(activeHeading.compareDocumentPosition(profilesHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();
	expect(profilesHeading.compareDocumentPosition(providersHeading) & Node.DOCUMENT_POSITION_FOLLOWING).toBeTruthy();

	// The active profile card carries the selection and both stage rows.
	const activeCard = document.querySelector('.active-card');
	expect(activeCard?.textContent).toContain('Main');
	expect((activeCard?.textContent ?? '').match(/Codex · model-a/g)).toHaveLength(2);
	const activeRadio = screen.getByRole('radio', { name: /Main/ }) as HTMLInputElement;
	expect(activeRadio.checked).toBe(true);
	expect(screen.getByRole('button', { name: 'Delete' }).hasAttribute('disabled')).toBe(true);

	// Stale-catalog state stays visible in the collapsed provider presentation.
	expect(screen.getByText(/stale catalog/)).toBeTruthy();
	expect(screen.getByText('catalog refresh failed earlier')).toBeTruthy();

	// Detailed test controls live inside native details, collapsed by default.
	const details = document.querySelector('details.provider-test') as HTMLDetailsElement | null;
	expect(details).toBeTruthy();
	expect(details!.open).toBe(false);
	expect(details!.querySelector('button')!.textContent!.trim()).toBe('Test Linguistic analysis');
});

it('expands a provider to run a stage conformance test and refresh its catalog', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const refreshedProvider = { ...provider, retrieved_at: '2 Sep 2026 15:42' };
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		const method = init.method ?? 'GET';
		if (input.startsWith('/api/v1/analysis/providers?refresh=true') && method === 'GET') {
			return json(200, { providers: [refreshedProvider] });
		}
		if (input === '/api/v1/analysis/providers' && method === 'GET') {
			return json(200, { providers: [provider] });
		}
		if (input === '/api/v1/analysis/providers/codex-app-server/test' && method === 'POST') {
			expect(JSON.parse(String(init.body))).toEqual({
				stage_id: 'linguistic_analysis',
				model_id: 'model-a',
				options: { reasoning_effort: 'low' }
			});
			return json(200, { status: 'healthy', duration_ms: 120 });
		}
		if (input === '/api/v1/analysis/profiles' && method === 'GET') return json(200, { profiles: [] });
		if (input === '/api/v1/analysis/settings' && method === 'GET') return json(200, { active_profile_id: '' });
		throw new Error(`unexpected request ${method} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(AnalysisPipelinePanel);
	await waitFor(() => expect(screen.getByRole('button', { name: 'Refresh catalog' })).toBeTruthy());

	// Catalog refresh hits the forced-refresh query for exactly this provider.
	await fireEvent.click(screen.getByRole('button', { name: 'Refresh catalog' }));
	await waitFor(() =>
		expect(
			fetchMock.mock.calls.some(([url]) => String(url) === '/api/v1/analysis/providers?refresh=true&provider_id=codex-app-server')
		).toBe(true)
	);
	await waitFor(() => expect(screen.getByText('Catalog retrieved: 2 Sep 2026 15:42')).toBeTruthy());

	// Expand the provider to reach the tuple tests.
	const details = document.querySelector('details.provider-test') as HTMLDetailsElement;
	expect(details).toBeTruthy();
	await fireEvent.click(screen.getByText('Test provider'));
	details.open = true;
	const testButton = await screen.findByRole('button', { name: 'Test Linguistic analysis' });
	await fireEvent.click(testButton);
	await waitFor(() => expect(screen.getByText(/Conformance fixture passed in 120 ms/)).toBeTruthy());
	const posts = fetchMock.mock.calls.filter(
		([url, init]) => url === '/api/v1/analysis/providers/codex-app-server/test' && (init?.method ?? 'GET') === 'POST'
	);
	expect(posts).toHaveLength(1);
});

it('treats mac_relay like openai_compatible for numeric stage options', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const relayProvider = {
		id: 'mac-relay',
		label: 'Mac relay',
		type: 'mac_relay',
		enabled: true,
		stale: false,
		health: 'healthy',
		models: [{ id: 'qwen-mlx', display_name: 'Qwen MLX' }]
	};
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		const method = init.method ?? 'GET';
		if (input === '/api/v1/analysis/providers' && method === 'GET') return json(200, { providers: [relayProvider] });
		if (input === '/api/v1/analysis/providers/mac-relay/test' && method === 'POST') {
			expect(JSON.parse(String(init.body))).toEqual({
				stage_id: 'translation',
				model_id: 'qwen-mlx',
				options: { temperature_milli: 0, max_output_tokens: 16384 }
			});
			return json(200, { status: 'healthy', duration_ms: 90 });
		}
		if (input === '/api/v1/analysis/profiles' && method === 'GET') return json(200, { profiles: [] });
		if (input === '/api/v1/analysis/settings' && method === 'GET') return json(200, { active_profile_id: '' });
		throw new Error(`unexpected request ${method} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(AnalysisPipelinePanel);
	await waitFor(() => expect(screen.getByRole('button', { name: 'Refresh catalog' })).toBeTruthy());

	// The collapsed test area exposes numeric controls for mac_relay.
	const details = document.querySelector('details.provider-test') as HTMLDetailsElement;
	await fireEvent.click(screen.getByText('Test provider'));
	details.open = true;
	expect((await screen.findAllByText('Temperature (milli)')).length).toBe(2); // one per stage
	expect(screen.getAllByText('Max output tokens')).toHaveLength(2);
	expect(screen.queryByText('Reasoning effort')).toBeNull();

	// Running a tuple test posts the numeric options the server requires.
	await fireEvent.click(await screen.findByRole('button', { name: 'Test Translation' }));
	await waitFor(() => expect(screen.getByText(/Conformance fixture passed in 90 ms/)).toBeTruthy());
	const posts = fetchMock.mock.calls.filter(([url, init]) => url === '/api/v1/analysis/providers/mac-relay/test' && (init?.method ?? 'GET') === 'POST');
	expect(posts).toHaveLength(1);

	// The profile editor renders numeric binding fields for mac_relay too.
	await fireEvent.click(screen.getByRole('button', { name: 'New profile' }));
	await fireEvent.input(screen.getByPlaceholderText('e.g. Mixed codex + omlx'), { target: { value: 'Relay only' } });
	const providerSelects = screen
		.getAllByRole('combobox')
		.filter((select) => select.querySelector('option')?.textContent === 'Select a provider');
	expect(providerSelects).toHaveLength(2);
	for (const select of providerSelects) {
		await fireEvent.change(select, { target: { value: 'mac-relay' } });
	}
	const editor = document.querySelector('.profile-editor');
	expect(editor).toBeTruthy();
	// Two stages x (temperature + max tokens), and no effort control.
	expect(editor!.querySelectorAll('input[type="number"]')).toHaveLength(4);
	expect(editor!.textContent).not.toContain('Reasoning effort');
	// Catalog models render as a select, not the old free-text input:
	// two provider selects plus two model selects.
	expect(editor!.querySelectorAll('select')).toHaveLength(4);
	expect(editor!.textContent).toContain('qwen-mlx');
	await waitFor(() => expect(screen.getByRole('button', { name: 'Create profile' }).hasAttribute('disabled')).toBe(false));
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
