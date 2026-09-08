import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
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
	models: [
		{ id: 'model-a', display_name: 'Model A', supported_reasoning_efforts: [{ value: 'low' }] },
		{ id: 'model-b', display_name: 'Model B', supported_reasoning_efforts: [{ value: 'low' }] }
	]
};

const PROMPT_TYPES = ['linguistic_analysis', 'article_translation', 'explore', 'sentence_translation', 'correction'] as const;

function promptVersionId(promptType: string, version: number): string {
	return `prompt-v${version}-${promptType}`;
}

/** Answers the prompt-library endpoints every settings render fetches. */
function promptLibraryResponse(input: string): Response | undefined {
	const match = input.match(/^\/api\/v1\/analysis\/prompts\/([a-z_]+)\/versions$/);
	if (!match || !match[1]) return undefined;
	const promptType = match[1];
	return json(200, {
		prompt_type: promptType,
		versions: [1, 2].map((version) => ({
			id: promptVersionId(promptType, version),
			prompt_type: promptType,
			version,
			label: version === 2 ? 'Experiment' : '',
			instruction_text: `Instruction v${version} for ${promptType}.`,
			content_hash: `hash-v${version}-${promptType}`,
			created_at: '2026-01-01T00:00:00Z'
		}))
	});
}

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
		const promptResponse = promptLibraryResponse(input);
		if (promptResponse) return promptResponse;
		if (input === '/api/v1/analysis/providers' && method === 'GET') return json(200, { providers: options.providers ?? [provider] });
		if (input === '/api/v1/analysis/profiles' && method === 'GET') return json(200, { profiles: options.profiles ?? [] });
		if (input === '/api/v1/analysis/settings' && method === 'GET') return json(200, { active_profile_id: options.activeProfileID ?? '' });
		throw new Error(`unexpected request ${method} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);
	return fetchMock;
}

function codexBindings(): Array<{ stage_id: string; provider_id: string; model_id: string; options: Record<string, string> }> {
	return [
		{ stage_id: 'linguistic_analysis', provider_id: 'codex-app-server', model_id: 'model-a', options: { reasoning_effort: 'low' } },
		{ stage_id: 'translation', provider_id: 'codex-app-server', model_id: 'model-a', options: { reasoning_effort: 'low' } }
	];
}

function rowEditors(): NodeListOf<Element> {
	return document.querySelectorAll('ul.profile-list > li .profile-editor');
}

/** The profile list row at a position, re-queried so it is never stale. */
function listRow(index: number): HTMLElement {
	return document.querySelectorAll('ul.profile-list > li')[index] as HTMLElement;
}

/** Pins every prompt selector of the open editor to its first saved version. */
async function pinPromptVersions(editor: HTMLElement): Promise<void> {
	const promptSelects = within(editor)
		.getAllByRole('combobox')
		.filter((select) => (select.querySelector('option') as HTMLOptionElement | null)?.textContent === 'Pin a saved version…');
	expect(promptSelects.length).toBe(5);
	for (const select of promptSelects) {
		const option = Array.from(select.querySelectorAll('option')).find((candidate) => candidate.value !== '');
		await fireEvent.change(select, { target: { value: option!.value } });
	}
}

it('shows the active profile above collapsed provider test controls', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const staleProvider = { ...provider, stale: true, last_error: 'catalog refresh failed earlier' };
	const profiles = [
		{
			id: 'profile-1',
			name: 'Main',
			is_active: true,
			bindings: codexBindings()
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
		const promptResponse = promptLibraryResponse(input);
		if (promptResponse) return promptResponse;
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
		const promptResponse = promptLibraryResponse(input);
		if (promptResponse) return promptResponse;
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
	expect((await screen.findAllByText('Temperature (milli)')).length).toBeGreaterThanOrEqual(2);
	expect(screen.getAllByText('Max output tokens').length).toBeGreaterThanOrEqual(2);

	// Running a tuple test posts the numeric options the server requires.
	await fireEvent.click(await screen.findByRole('button', { name: 'Test Translation' }));
	await waitFor(() => expect(screen.getByText(/Conformance fixture passed in 90 ms/)).toBeTruthy());
	const posts = fetchMock.mock.calls.filter(([url, init]) => url === '/api/v1/analysis/providers/mac-relay/test' && (init?.method ?? 'GET') === 'POST');
	expect(posts).toHaveLength(1);

	// The profile editor renders numeric binding fields for mac_relay across
	// both stage bindings and the Explore binding.
	await fireEvent.click(screen.getByRole('button', { name: 'New profile' }));
	await fireEvent.input(screen.getByPlaceholderText('e.g. Mixed codex + omlx'), { target: { value: 'Relay only' } });
	const editor = document.querySelector('.profile-editor') as HTMLElement;
	const providerSelects = within(editor)
		.getAllByRole('combobox')
		.filter((select) => select.querySelector('option')?.textContent === 'Select a provider');
	// Two stage bindings plus the independent Explore binding.
	expect(providerSelects).toHaveLength(3);
	for (const select of providerSelects) {
		await fireEvent.change(select, { target: { value: 'mac-relay' } });
	}
	await pinPromptVersions(editor);
	// Two stages plus Explore, each with temperature and max output tokens.
	expect(editor.querySelectorAll('input[type="number"]')).toHaveLength(6);
	expect(editor.textContent).not.toContain('Reasoning effort');
	expect(editor.textContent).toContain('qwen-mlx');
	await waitFor(() => expect(screen.getByRole('button', { name: 'Create profile' }).hasAttribute('disabled')).toBe(false));
});

it('creating a profile does not activate it, and manual activation saves explicitly', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const stored: unknown[] = [];
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		const method = init.method ?? 'GET';
		const promptResponse = promptLibraryResponse(input);
		if (promptResponse) return promptResponse;
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
	const editor = document.querySelector('.profile-editor') as HTMLElement;
	const providerSelects = within(editor)
		.getAllByRole('combobox')
		.filter((select) => select.querySelector('option')?.textContent === 'Select a provider');
	expect(providerSelects).toHaveLength(3);
	for (const select of providerSelects) {
		await fireEvent.change(select, { target: { value: 'codex-app-server' } });
	}
	await pinPromptVersions(editor);

	await fireEvent.click(screen.getByRole('button', { name: 'Create profile' }));
	await waitFor(() => expect(screen.getByText('Mixed')).toBeTruthy());

	// Creation persisted the profile but did not touch the active-selection setting.
	const posts = fetchMock.mock.calls.filter(([url, init]) => url === '/api/v1/analysis/profiles' && (init?.method ?? 'GET') === 'POST');
	expect(posts).toHaveLength(1);
	const createdBody = JSON.parse(String(posts[0]?.[1]?.body));
	expect(createdBody.name).toBe('Mixed');
	expect(createdBody.explore_binding.model_id).toBe('model-a');
	expect(Object.keys(createdBody.prompt_versions)).toHaveLength(5);
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

function profileWithPromptPins(name: string, version: 1 | 2, exploreModel: string) {
	const promptVersions: Record<string, { id: string; version: number; label: string }> = {};
	for (const promptType of PROMPT_TYPES) {
		promptVersions[promptType] = {
			id: promptVersionId(promptType, version),
			version,
			label: version === 2 ? 'Experiment' : ''
		};
	}
	return {
		id: `profile-${name.toLowerCase()}`,
		name,
		is_active: false,
		bindings: codexBindings(),
		explore_binding: {
			stage_id: 'translation',
			provider_id: 'codex-app-server',
			model_id: exploreModel,
			options: { reasoning_effort: 'low' }
		},
		prompt_versions: promptVersions
	};
}

it('pins prompt versions per profile so two profiles can differ, with Explore independent of Translation', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const alpha = profileWithPromptPins('Alpha', 1, 'model-b');
	const beta = profileWithPromptPins('Beta', 2, 'model-a');
	stubStandardFetch({ providers: [provider], profiles: [alpha, beta], activeProfileID: 'profile-alpha' });

	render(AnalysisPipelinePanel);
	await waitFor(() => expect(screen.getByRole('heading', { name: 'Profiles' })).toBeTruthy());
	const rows = document.querySelectorAll('ul.profile-list > li');

	// Alpha pins v1 and its Explore model differs from its Translation model.
	await fireEvent.click(within(listRow(0)).getByRole('button', { name: 'Edit' }));
	let editor = listRow(0).querySelector('.profile-editor') as HTMLElement;
	let promptSelects = within(editor).getAllByRole('combobox').filter((select) => {
		const options = Array.from(select.querySelectorAll('option')).map((option) => option.value);
		return options.some((value) => value.startsWith('prompt-v'));
	});
	expect(promptSelects).toHaveLength(5);
	for (const select of promptSelects) {
		// The select only lists its own type's versions, so its first option
		// identifies the type; Alpha pinned v1 of every type.
		const ownType = (Array.from(select.querySelectorAll('option')).map((option) => option.value).find((value) => value.startsWith('prompt-v')) ?? '').split('-(?:v\d+)-');
		const versionPrefix = 'prompt-v1-';
		const type = (Array.from(select.querySelectorAll('option')).map((option) => option.value).find((value) => value.startsWith('prompt-v')) ?? '').slice(versionPrefix.length);
		expect((select as HTMLSelectElement).value).toBe(promptVersionId(type, 1));
		expect(ownType.length).toBeGreaterThan(0);
	}
	const modelSelectValues = within(editor)
		.getAllByRole('combobox')
		.map((select) => (select as HTMLSelectElement).value);
	expect(modelSelectValues).toContain('model-b');
	expect(modelSelectValues).toContain('model-a');
	// Explore renders as its own fieldset.
	expect(within(editor).getByText('Explore (on-demand)')).toBeTruthy();
	expect(within(editor).getByText('Prompt versions')).toBeTruthy();

	// Beta pins v2 everywhere: switching profiles shows Beta's own pins.
	await fireEvent.click(within(listRow(0)).getByRole('button', { name: 'Cancel' }));
	await fireEvent.click(within(listRow(1)).getByRole('button', { name: 'Edit' }));
	editor = listRow(1).querySelector('.profile-editor') as HTMLElement;
	promptSelects = within(editor).getAllByRole('combobox').filter((select) => {
		const options = Array.from(select.querySelectorAll('option')).map((option) => option.value);
		return options.some((value) => value.startsWith('prompt-v'));
	});
	for (const select of promptSelects) {
		expect((select as HTMLSelectElement).value).toContain('-v2-');
	}
});

it('opens the profile editor directly below the edited card, wherever that card sits', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const profiles = ['Alpha', 'Beta', 'Gamma'].map((name, index) => ({
		id: `profile-${index + 1}`,
		name,
		is_active: index === 0,
		bindings: codexBindings()
	}));
	stubStandardFetch({ providers: [provider], profiles, activeProfileID: 'profile-1' });

	render(AnalysisPipelinePanel);
	await waitFor(() => expect(screen.getByRole('heading', { name: 'Profiles' })).toBeTruthy());

	const rows = document.querySelectorAll('ul.profile-list > li');
	expect(rows).toHaveLength(3);
	for (const [index, row] of [...rows].entries()) {
		await fireEvent.click(within(row as HTMLElement).getByRole('button', { name: 'Edit' }));

		// Exactly one editor exists, inside this profile's row, right after its card.
		expect(row.querySelector('.profile-editor')).toBeTruthy();
		expect(rowEditors()).toHaveLength(1);
		expect(row.querySelector('.profile-card-line + .profile-editor')).toBeTruthy();

		// The form is pre-filled with the profile and the name field is focused.
		const nameInput = row.querySelector<HTMLInputElement>('input[placeholder="e.g. Mixed codex + omlx"]');
		expect(nameInput?.value).toBe(['Alpha', 'Beta', 'Gamma'][index]);
		expect(document.activeElement).toBe(nameInput);

		await fireEvent.click(within(row.querySelector('.profile-editor') as HTMLElement).getByRole('button', { name: 'Cancel' }));
		expect(row.querySelector('.profile-editor')).toBeNull();
	}
});

it('asks before discarding a dirty draft when switching profiles or to creation', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const profiles = ['Alpha', 'Beta'].map((name, index) => ({
		id: `profile-${index + 1}`,
		name,
		is_active: index === 0,
		bindings: codexBindings()
	}));
	stubStandardFetch({ providers: [provider], profiles, activeProfileID: 'profile-1' });

	render(AnalysisPipelinePanel);
	await waitFor(() => expect(screen.getByRole('heading', { name: 'Profiles' })).toBeTruthy());
	expect(document.querySelectorAll('ul.profile-list > li')).toHaveLength(2);

	await fireEvent.click(within(listRow(0)).getByRole('button', { name: 'Edit' }));
	const alphaEditor = listRow(0).querySelector('.profile-editor') as HTMLElement;
	await fireEvent.input(alphaEditor.querySelector('input[type="text"]') as HTMLInputElement, { target: { value: 'Alpha rewritten' } });

	// Clicking Edit on another profile keeps the draft and asks first.
	await fireEvent.click(within(listRow(1)).getByRole('button', { name: 'Edit' }));
	expect(listRow(1).querySelector('.profile-editor')).toBeNull();
	expect(within(alphaEditor).getByRole('alert').textContent).toContain('unsaved changes');

	// Keep editing preserves the unsaved draft in place.
	await fireEvent.click(within(alphaEditor).getByRole('button', { name: 'Keep editing' }));
	expect(within(alphaEditor).queryByRole('button', { name: 'Discard changes' })).toBeNull();
	expect((alphaEditor.querySelector('input[type="text"]') as HTMLInputElement).value).toBe('Alpha rewritten');

	// Creation also goes through the same explicit discard choice.
	await fireEvent.click(screen.getByRole('button', { name: 'New profile' }));
	expect(within(alphaEditor).getByRole('alert').textContent).toContain('unsaved changes');
	await fireEvent.click(within(alphaEditor).getByRole('button', { name: 'Discard changes' }));

	// The discarded switch opens the creation editor below the New profile button.
	const section = document.querySelector('.profiles-section') as HTMLElement;
	const creationEditor = section.querySelector(':scope > .profile-editor') as HTMLElement;
	expect(creationEditor).toBeTruthy();
	expect(creationEditor.querySelector('h3')?.textContent).toBe('New profile');
	expect(rowEditors()).toHaveLength(0);

	// An untouched editor still switches immediately without asking.
	await fireEvent.click(within(listRow(0)).getByRole('button', { name: 'Edit' }));
	const reopened = listRow(0).querySelector('.profile-editor') as HTMLElement;
	expect(within(reopened).queryByRole('alert')).toBeNull();
	expect((reopened.querySelector('input[type="text"]') as HTMLInputElement).value).toBe('Alpha');
});

it('shows profile save failures in place, keeps the editor open, and restores focus on cancel', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		const method = init.method ?? 'GET';
		const promptResponse = promptLibraryResponse(input);
		if (promptResponse) return promptResponse;
		if (input === '/api/v1/analysis/providers' && method === 'GET') return json(200, { providers: [provider] });
		if (input === '/api/v1/analysis/profiles' && method === 'GET') return json(200, { profiles: [] });
		if (input === '/api/v1/analysis/settings' && method === 'GET') return json(200, { active_profile_id: '' });
		if (input === '/api/v1/analysis/profiles' && method === 'POST') {
			return json(500, { error: 'Profile name already exists', code: 'v1.conflict' });
		}
		throw new Error(`unexpected request ${method} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(AnalysisPipelinePanel);
	await waitFor(() => expect(screen.getByRole('button', { name: 'New profile' })).toBeTruthy());
	const newButton = screen.getByRole('button', { name: 'New profile' });
	await fireEvent.click(newButton);

	// Creation renders below the New profile button, not at the page bottom.
	const section = document.querySelector('.profiles-section') as HTMLElement;
	const editor = section.querySelector(':scope > .profile-editor') as HTMLElement;
	expect(editor).toBeTruthy();
	expect(editor.previousElementSibling?.classList.contains('profiles-heading')).toBe(true);

	await fireEvent.input(editor.querySelector('input[type="text"]') as HTMLInputElement, { target: { value: 'Mixed' } });
	const providerSelects = within(editor)
		.getAllByRole('combobox')
		.filter((select) => select.querySelector('option')?.textContent === 'Select a provider');
	for (const select of providerSelects) {
		await fireEvent.change(select, { target: { value: 'codex-app-server' } });
	}
	await pinPromptVersions(editor);
	await waitFor(() => expect(screen.getByRole('button', { name: 'Create profile' }).hasAttribute('disabled')).toBe(false));

	await fireEvent.click(screen.getByRole('button', { name: 'Create profile' }));
	// The server failure appears inside the editor and the draft survives.
	await waitFor(() => expect(within(editor).getByRole('alert').textContent).toBe('Profile name already exists'));
	expect(section.querySelector(':scope > .profile-editor')).toBe(editor);
	expect((editor.querySelector('input[type="text"]') as HTMLInputElement).value).toBe('Mixed');

	// Cancel closes the editor and returns focus to the trigger that opened it.
	await fireEvent.click(within(editor).getByRole('button', { name: 'Cancel' }));
	expect(section.querySelector(':scope > .profile-editor')).toBeNull();
	expect(document.activeElement).toBe(newButton);
});

it('initializes Explore from a completed Translation once, then the bindings stay independent', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	stubStandardFetch({ providers: [provider], profiles: [], activeProfileID: '' });

	render(AnalysisPipelinePanel);
	await waitFor(() => expect(screen.getByRole('button', { name: 'New profile' })).toBeTruthy());
	await fireEvent.click(screen.getByRole('button', { name: 'New profile' }));
	const editor = document.querySelector('.profile-editor') as HTMLElement;
	// Fieldset-scoped lookups: editor control counts change as providers are
	// assigned, but the legends are stable.
	const fieldsetFor = (legend: string): HTMLElement =>
		Array.from(editor.querySelectorAll('fieldset')).find(
			(candidate) => candidate.querySelector('legend')?.textContent === legend
		) as HTMLElement;
	const translationFieldset = fieldsetFor('Translation');
	const exploreFieldset = fieldsetFor('Explore (on-demand)');
	const providerSelectIn = (fieldset: HTMLElement): HTMLSelectElement =>
		(within(fieldset).getAllByRole('combobox') as HTMLSelectElement[]).find((select) =>
			Array.from(select.querySelectorAll('option')).some((option) => option.textContent === 'Select a provider')
		)!;
	const modelSelectIn = (fieldset: HTMLElement): HTMLSelectElement =>
		(within(fieldset).getAllByRole('combobox') as HTMLSelectElement[]).find((select) =>
			Array.from(select.querySelectorAll('option')).some((option) => option.value === 'model-a')
		)!;
	const translationProviderSelect = providerSelectIn(translationFieldset);
	const exploreProviderSelect = providerSelectIn(exploreFieldset);
	// Model controls become selects once a provider with a catalog is chosen,
	// so they are looked up lazily after assignment.

	// Nothing selected yet: Explore is empty.
	expect(exploreProviderSelect.value).toBe('');

	// Catalog-selected path: choosing the translation provider completes the
	// binding and seeds Explore exactly once.
	await fireEvent.change(translationProviderSelect, { target: { value: 'codex-app-server' } });
	expect(exploreProviderSelect.value).toBe('codex-app-server');
	const translationModelSelect = modelSelectIn(translationFieldset);
	const exploreModelSelect = modelSelectIn(exploreFieldset);
	expect(exploreModelSelect.value).toBe('model-a');

	// A later translation model change must not move Explore.
	await fireEvent.change(translationModelSelect, { target: { value: 'model-b' } });
	expect(translationModelSelect.value).toBe('model-b');
	expect(exploreModelSelect.value).toBe('model-a');
});
