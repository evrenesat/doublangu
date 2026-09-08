import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import PromptLibraryPanel from './PromptLibraryPanel.svelte';

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

const PROMPT_TYPES = ['linguistic_analysis', 'article_translation', 'explore', 'sentence_translation', 'correction'] as const;

afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
	for (const cookie of document.cookie.split(';')) {
		const name = cookie.split('=')[0]?.trim();
		if (name) document.cookie = `${name}=; Max-Age=0; Path=/`;
	}
});

function stubPromptFetch(options: { saveStatus?: number } = {}): ReturnType<typeof vi.fn> {
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		const method = init.method ?? 'GET';
		const match = input.match(/^\/api\/v1\/analysis\/prompts\/([a-z_]+)\/versions$/);
		if (match && method === 'GET') {
			const promptType = match[1];
			return json(200, {
				prompt_type: promptType,
				versions: [
					{ id: `v1-${promptType}`, prompt_type: promptType, version: 1, label: '', instruction_text: `Builtin ${promptType} instruction.`, content_hash: 'hash-1', created_at: '2026-01-01T00:00:00Z' }
				]
			});
		}
		if (match && method === 'POST') {
			const body = JSON.parse(String(init.body));
			if (options.saveStatus !== undefined && options.saveStatus !== 201) {
				return json(options.saveStatus, { error: 'instruction text must not be blank', code: 'v1.validation_error' });
			}
			return json(201, {
				prompt_type: match[1],
				version: { id: 'v2-new', prompt_type: match[1], version: 2, label: body.label ?? '', instruction_text: body.instruction_text, content_hash: 'hash-2', created_at: '2026-01-02T00:00:00Z' }
			});
		}
		throw new Error(`unexpected request ${method} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);
	return fetchMock;
}

it('saves a new immutable version without activating anything', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const fetchMock = stubPromptFetch();

	render(PromptLibraryPanel);
	await waitFor(() => expect(screen.getAllByRole('combobox')).toHaveLength(2));

	// The saved text is read-only and the newest version is selected.
	const selects = screen.getAllByRole('combobox') as HTMLSelectElement[];
	const typeSelect = selects[0]!;
	const versionSelect = selects[1]!;
	await fireEvent.change(typeSelect, { target: { value: 'article_translation' } });
	await waitFor(() => expect(screen.getByText(/Builtin article_translation instruction\./)).toBeTruthy());
	expect(versionSelect.value).toBe('v1-article_translation');

	// Edit as new version prefills the draft from the viewed version.
	await fireEvent.click(await screen.findByRole('button', { name: 'Edit as new version' }));
	const textarea = (await screen.findAllByRole('textbox')).find((element) => element.tagName === 'TEXTAREA') as HTMLTextAreaElement;
	expect(textarea.value).toBe('Builtin article_translation instruction.');
	await fireEvent.input(textarea, { target: { value: 'Sharper experiment wording.\nKeep the data rules.' } });
	await fireEvent.input(screen.getByPlaceholderText('e.g. More concise wording'), { target: { value: 'Sharper' } });

	const puts = () => fetchMock.mock.calls.filter(([url, init]) => url === '/api/v1/analysis/settings' && (init?.method ?? 'GET') === 'PUT');
	expect(puts()).toHaveLength(0);
	await fireEvent.click(screen.getByRole('button', { name: 'Save new version' }));
	await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Saved v2'));
	// Saving a version never activates it: no settings call, no profile write.
	expect(puts()).toHaveLength(0);
	expect(fetchMock.mock.calls.some(([url, init]) => String(url).includes('/analysis/profiles') && (init?.method ?? 'GET') === 'PUT')).toBe(false);
	// The new version is listed and selected; the editor closed.
	expect(versionSelect.value).toBe('v2-new');
	expect(screen.queryByRole('button', { name: 'Save new version' })).toBeNull();
});

it('keeps the draft on a save failure and shows the error in place', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	stubPromptFetch({ saveStatus: 400 });

	render(PromptLibraryPanel);
	await waitFor(() => expect(screen.getByRole('button', { name: 'Edit as new version' })).toBeTruthy());
	await fireEvent.click(screen.getByRole('button', { name: 'Edit as new version' }));
	const textarea = (await screen.findAllByRole('textbox')).find((element) => element.tagName === 'TEXTAREA') as HTMLTextAreaElement;
	await fireEvent.input(textarea, { target: { value: 'Draft that must survive the failure.' } });

	await fireEvent.click(screen.getByRole('button', { name: 'Save new version' }));
	await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('instruction text must not be blank'));
	// The unsaved draft is preserved for correction.
	expect(((await screen.findAllByRole('textbox')).find((element) => element.tagName === 'TEXTAREA') as HTMLTextAreaElement).value).toBe('Draft that must survive the failure.');
	expect(screen.getByRole('button', { name: 'Save new version' })).toBeTruthy();
});

it('a deferred save files the version under the submitted type and never corrupts another type', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	let releaseSaved!: (value: Response) => void;
	const savedGate = new Promise<Response>((resolve) => {
		releaseSaved = resolve;
	});
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		const method = init.method ?? 'GET';
		const match = input.match(/^\/api\/v1\/analysis\/prompts\/([a-z_]+)\/versions$/);
		if (match && method === 'GET') {
			const promptType = match[1] ?? '';
			return json(200, {
				prompt_type: promptType,
				versions: [{ id: `v1-${promptType}`, prompt_type: promptType, version: 1, label: '', instruction_text: `Builtin ${promptType}.`, content_hash: 'hash-1', created_at: '2026-01-01T00:00:00Z' }]
			});
		}
		if (match && method === 'POST') {
			const promptType = match[1] ?? '';
			const body = JSON.parse(String(init.body));
			const response = await savedGate;
			void body;
			return json(response.status, await response.json());
		}
		throw new Error(`unexpected request ${method} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(PromptLibraryPanel);
	await waitFor(() => expect(screen.getByRole('button', { name: 'Edit as new version' })).toBeTruthy());
	const selects = screen.getAllByRole('combobox') as HTMLSelectElement[];
	const typeSelect = selects[0]!;
	const versionSelect = selects[1]!;
	await fireEvent.change(typeSelect, { target: { value: 'explore' } });
	await fireEvent.click(screen.getByRole('button', { name: 'Edit as new version' }));
	const textarea = (await screen.findAllByRole('textbox')).find((element) => element.tagName === 'TEXTAREA') as HTMLTextAreaElement;
	await fireEvent.input(textarea, { target: { value: 'Explore experiment wording.' } });
	const labelInput = screen.getByPlaceholderText('e.g. More concise wording') as HTMLInputElement;
	await fireEvent.input(labelInput, { target: { value: 'Explore draft label' } });

	// Start the save; the POST is held open.
	await fireEvent.click(screen.getByRole('button', { name: 'Save new version' }));
	expect(screen.getByRole('button', { name: 'Saving…' })).toBeTruthy();

	// Neither draft field accepts edits while the POST is pending, and the
	// type/version navigation locks stay in place so a user cannot switch
	// away mid-flight; even a forced change cannot misfile the save.
	expect(textarea.disabled).toBe(true);
	expect(labelInput.disabled).toBe(true);
	expect(typeSelect.disabled).toBe(true);
	expect(versionSelect.disabled).toBe(true);
	expect(screen.getByRole('button', { name: 'Cancel' }).hasAttribute('disabled')).toBe(true);
	await fireEvent.change(typeSelect, { target: { value: 'correction' } });
	await fireEvent.change(versionSelect, { target: { value: 'v1-correction' } });

	// Complete the save: the version files under Explore, the success label
	// names Explore, and Correction's catalog stays untouched.
	releaseSaved(json(201, {
		prompt_type: 'explore',
		version: { id: 'v2-explore', prompt_type: 'explore', version: 2, label: '', instruction_text: 'Explore experiment wording.', content_hash: 'hash-2', created_at: '2026-01-02T00:00:00Z' }
	}));
	await waitFor(() => expect(screen.getByRole('status').textContent).toContain('Saved v2 for Explore'));
	expect(screen.getByRole('status').textContent).not.toContain('Correction');
	expect(fetchMock.mock.calls.some(([url, init]) => String(url).endsWith('/prompts/correction/versions') && (init?.method ?? 'GET') === 'POST')).toBe(false);
	// The exact submitted instruction and label reached the server.
	const saveCall = fetchMock.mock.calls.find(
		([url, init]) => String(url).endsWith('/prompts/explore/versions') && (init?.method ?? 'GET') === 'POST'
	);
	expect(JSON.parse(String(saveCall?.[1]?.body))).toEqual({
		instruction_text: 'Explore experiment wording.',
		label: 'Explore draft label'
	});
	// Navigating back to Explore shows the just-saved version selected and
	// listed; Correction keeps only its v1 and received no POST.
	await fireEvent.change(typeSelect, { target: { value: 'explore' } });
	await waitFor(() => expect(versionSelect.value).toBe('v2-explore'));
	expect(Array.from(versionSelect.querySelectorAll('option')).map((option) => option.value).sort()).toEqual(['v1-explore', 'v2-explore']);
	await fireEvent.change(typeSelect, { target: { value: 'correction' } });
	await waitFor(() => expect(versionSelect.value).toBe('v1-correction'));
	expect(Array.from(versionSelect.querySelectorAll('option')).map((option) => option.value)).toEqual(['v1-correction']);
});
