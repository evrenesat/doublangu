import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import ReaderSettingsPage from '../../routes/settings/+page.svelte';

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

function localMirror(): Record<string, unknown> {
	try {
		return JSON.parse(localStorage.getItem('doublangu:reader:pronounce-on-hover') ?? '{}') as Record<string, unknown>;
	} catch {
		return {};
	}
}

/** This jsdom URL has no working storage; stub it so the mirror logic is observable. */
function stubStorage(): void {
	const store = new Map<string, string>();
	vi.stubGlobal('localStorage', {
		getItem: (key: string) => store.get(key) ?? null,
		setItem: (key: string, value: string) => void store.set(key, value),
		removeItem: (key: string) => void store.delete(key),
		clear: () => void store.clear()
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

it('renders Reader content and requests only reader settings', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	const fetchMock = vi.fn(async (input: string): Promise<Response> => {
		if (input === '/api/v1/reader/settings') return json(200, { pronounce_on_hover: true, updated_at: '2026-01-01T00:00:00Z' });
		throw new Error(`unexpected request ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(ReaderSettingsPage);
	await waitFor(() => expect((screen.getByRole('checkbox', { name: /Pronounce on hover/ }) as HTMLInputElement).checked).toBe(true));
	expect(screen.getByRole('heading', { name: 'Reader' })).toBeTruthy();
	expect(screen.getByRole('heading', { name: 'Pronunciation' })).toBeTruthy();
	expect(screen.getByText("Play a word's pronunciation when the pointer hovers over it in the reader.")).toBeTruthy();

	// Settings must no longer touch analysis or diagnostics surfaces.
	for (const prefix of ['/api/v1/analysis/runs', '/api/v1/analysis/providers', '/api/v1/analysis/profiles', '/api/v1/analysis/settings', '/health']) {
		expect(fetchMock.mock.calls.filter(([url]) => String(url).startsWith(prefix))).toEqual([]);
	}
	// The removed Settings surfaces stay gone.
	expect(screen.queryByRole('heading', { name: 'Recent runs' })).toBeNull();
	expect(screen.queryByRole('link', { name: 'Plugins' })).toBeNull();
	expect(screen.queryByRole('link', { name: 'Library' })).toBeNull();
});

it('saves a checkbox mutation and mirrors it to the local copy', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	stubStorage();
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		if (input === '/api/v1/reader/settings' && (init.method ?? 'GET') === 'GET') {
			return json(200, { pronounce_on_hover: true, updated_at: '2026-01-01T00:00:00Z' });
		}
		if (input === '/api/v1/reader/settings' && init.method === 'PUT') {
			expect(JSON.parse(String(init.body))).toEqual({ pronounce_on_hover: false });
			return json(200, { pronounce_on_hover: false, updated_at: '2026-01-02T00:00:00Z' });
		}
		throw new Error(`unexpected request ${init.method ?? 'GET'} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(ReaderSettingsPage);
	const checkbox = (await screen.findByRole('checkbox', { name: /Pronounce on hover/ })) as HTMLInputElement;
	expect(checkbox.checked).toBe(true);
	await fireEvent.click(checkbox);
	await waitFor(() => expect(checkbox.checked).toBe(false));
	await waitFor(() => expect(localMirror()).toEqual({ pronounce_on_hover: false }));
});

it('rolls back a failed mutation with an inline error', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	stubStorage();
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		if (input === '/api/v1/reader/settings' && (init.method ?? 'GET') === 'GET') {
			return json(200, { pronounce_on_hover: true, updated_at: '2026-01-01T00:00:00Z' });
		}
		if (input === '/api/v1/reader/settings' && init.method === 'PUT') {
			return json(500, { error: 'save rejected', code: 'v1.internal' });
		}
		throw new Error(`unexpected request ${init.method ?? 'GET'} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(ReaderSettingsPage);
	const checkbox = (await screen.findByRole('checkbox', { name: /Pronounce on hover/ })) as HTMLInputElement;
	expect(checkbox.checked).toBe(true);
	await fireEvent.click(checkbox);
	await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
	expect(checkbox.checked).toBe(true);
	expect(localMirror()).toEqual({ pronounce_on_hover: true });
});

it('shows a retryable load error', async () => {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	let healthy = false;
	const fetchMock = vi.fn(async (input: string): Promise<Response> => {
		if (input === '/api/v1/reader/settings') {
			if (!healthy) return json(500, { error: 'unavailable', code: 'v1.internal' });
			return json(200, { pronounce_on_hover: false, updated_at: '2026-01-01T00:00:00Z' });
		}
		throw new Error(`unexpected request ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(ReaderSettingsPage);
	await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
	healthy = true;
	await fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
	await waitFor(() => expect((screen.getByRole('checkbox', { name: /Pronounce on hover/ }) as HTMLInputElement).checked).toBe(false));
});
