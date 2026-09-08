import { cleanup, fireEvent, render, screen, waitFor, within } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import AnalysisSettingsPage from '../../routes/settings/analysis/+page.svelte';

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

const PROMPT_TYPES = ['linguistic_analysis', 'article_translation', 'explore', 'sentence_translation', 'correction'] as const;

function pinnedVersion(promptType: string): { id: string; version: number; label: string } {
	return { id: `v1-${promptType}`, version: 1, label: '' };
}

function storedProfile(id: string, name: string) {
	return {
		id,
		name,
		is_active: false,
		bindings: [
			{ stage_id: 'linguistic_analysis', provider_id: 'codex-app-server', model_id: 'model-a', options: { reasoning_effort: 'low' } },
			{ stage_id: 'translation', provider_id: 'codex-app-server', model_id: 'model-a', options: { reasoning_effort: 'low' } }
		],
		explore_binding: {
			stage_id: 'translation',
			provider_id: 'codex-app-server',
			model_id: 'model-a',
			options: { reasoning_effort: 'low' }
		},
		prompt_versions: Object.fromEntries(PROMPT_TYPES.map((promptType) => [promptType, pinnedVersion(promptType)]))
	};
}

afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
	for (const cookie of document.cookie.split(';')) {
		const name = cookie.split('=')[0]?.trim();
		if (name) document.cookie = `${name}=; Max-Age=0; Path=/`;
	}
});

it('a just-saved prompt version can be pinned immediately without reload, and other profiles keep their pins', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const stored = [storedProfile('alpha', 'Alpha'), storedProfile('beta', 'Beta')];
	const profilePuts: Array<Record<string, unknown>> = [];
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		const method = init.method ?? 'GET';
		const versionsMatch = input.match(/^\/api\/v1\/analysis\/prompts\/([a-z_]+)\/versions$/);
		if (versionsMatch && method === 'GET') {
			const promptType = versionsMatch[1] ?? '';
			return json(200, {
				prompt_type: promptType,
				versions: [{ id: `v1-${promptType}`, prompt_type: promptType, version: 1, label: '', instruction_text: `Builtin ${promptType}.`, content_hash: 'hash-1', created_at: '2026-01-01T00:00:00Z' }]
			});
		}
		if (versionsMatch && method === 'POST') {
			const promptType = versionsMatch[1] ?? '';
			const body = JSON.parse(String(init.body));
			return json(201, {
				prompt_type: promptType,
				version: { id: `v2-${promptType}`, prompt_type: promptType, version: 2, label: body.label ?? '', instruction_text: body.instruction_text, content_hash: 'hash-2', created_at: '2026-01-02T00:00:00Z' }
			});
		}
		if (input === '/api/v1/analysis/providers' && method === 'GET') return json(200, { providers: [provider] });
		if (input === '/api/v1/analysis/profiles' && method === 'GET') return json(200, { profiles: stored });
		if (input === '/api/v1/analysis/settings' && method === 'GET') return json(200, { active_profile_id: 'alpha' });
		if (input.startsWith('/api/v1/analysis/profiles/') && method === 'PUT') {
			const body = JSON.parse(String(init.body));
			profilePuts.push(body);
			const profileId = input.split('/').pop() ?? '';
			const index = stored.findIndex((candidate) => candidate.id === profileId);
			if (index >= 0) stored[index] = { ...stored[index]!, name: body.name };
			return json(200, stored[index]);
		}
		throw new Error(`unexpected request ${method} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(AnalysisSettingsPage);
	await waitFor(() => expect(screen.getByRole('heading', { name: 'Prompts' })).toBeTruthy());
	await waitFor(() => expect(screen.getByRole('button', { name: 'New profile' })).toBeTruthy());

	// Open Alpha for editing and dirty the draft.
	const rows = document.querySelectorAll('ul.profile-list > li');
	const alphaRow = rows[0] as HTMLElement;
	expect(alphaRow.textContent).toContain('Alpha');
	await fireEvent.click(within(alphaRow).getByRole('button', { name: 'Edit' }));
	const editor = alphaRow.querySelector('.profile-editor') as HTMLElement;
	await fireEvent.input(editor.querySelector('input[type="text"]') as HTMLInputElement, { target: { value: 'Alpha renamed' } });

	// Save a new explore version in the Prompts section.
	const library = document.querySelector('.prompt-library') as HTMLElement;
	const librarySelects = within(library).getAllByRole('combobox') as HTMLSelectElement[];
	await fireEvent.change(librarySelects[0]!, { target: { value: 'explore' } });
	await fireEvent.click(await within(library).findByRole('button', { name: 'Edit as new version' }));
	const textarea = (await within(library).findAllByRole('textbox')).find((element) => element.tagName === 'TEXTAREA') as HTMLTextAreaElement;
	await fireEvent.input(textarea, { target: { value: 'Sharper explore wording.' } });
	await fireEvent.click(within(library).getByRole('button', { name: 'Save new version' }));
	await waitFor(() => expect(within(library).getByRole('status').textContent).toContain('Saved v2'));

	// The catalog update preserved the dirty open draft.
	expect(alphaRow.querySelector('.profile-editor')).toBe(editor);
	expect((editor.querySelector('input[type="text"]') as HTMLInputElement).value).toBe('Alpha renamed');

	// The just-saved v2 is selectable right away; pin it and save.
	const explorePromptSelect = (within(editor).getAllByRole('combobox') as HTMLSelectElement[]).find((select) =>
		Array.from(select.querySelectorAll('option')).some((option) => option.value === 'v2-explore')
	);
	expect(explorePromptSelect).toBeTruthy();
	await fireEvent.change(explorePromptSelect!, { target: { value: 'v2-explore' } });
	await waitFor(() => expect(screen.getByRole('button', { name: 'Save changes' }).hasAttribute('disabled')).toBe(false));
	await fireEvent.click(screen.getByRole('button', { name: 'Save changes' }));
	await waitFor(() => expect(profilePuts).toHaveLength(1));
	// Alpha pinned v2 for explore only; every other pin keeps v1.
	expect(profilePuts[0]!['prompt_versions']).toEqual({
		linguistic_analysis: 'v1-linguistic_analysis',
		article_translation: 'v1-article_translation',
		explore: 'v2-explore',
		sentence_translation: 'v1-sentence_translation',
		correction: 'v1-correction'
	});

	// Beta was never saved and keeps v1 everywhere.
	expect(profilePuts).toHaveLength(1);
	const betaPins = Object.fromEntries(
		Object.entries(stored[1]!.prompt_versions as Record<string, { id: string }>).map(([key, ref]) => [key, ref.id])
	);
	expect(betaPins).toEqual({
		linguistic_analysis: 'v1-linguistic_analysis',
		article_translation: 'v1-article_translation',
		explore: 'v1-explore',
		sentence_translation: 'v1-sentence_translation',
		correction: 'v1-correction'
	});
});
