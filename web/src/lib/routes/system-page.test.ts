import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import SystemSettingsPage from '../../routes/settings/system/+page.svelte';

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
});

it('loads diagnostics with ready, not-ready, and unknown states', async () => {
	const fetchMock = vi.fn(async (input: string): Promise<Response> => {
		if (input === '/health') return json(200, { core_ready: true, loader_ready: false, registry_state: 'active', plugin_count: 2, plugin_ids: ['sample-plugin', 'other-plugin'] });
		throw new Error(`unexpected request ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(SystemSettingsPage);
	// Wait for data-driven content rather than the static heading.
	expect(await screen.findByText('Ready')).toBeTruthy();

	expect(screen.getByText('Not ready')).toBeTruthy();
	expect(screen.getByText('Unknown')).toBeTruthy(); // schema_available absent
	expect(screen.getByText('active')).toBeTruthy(); // registry state
	expect(screen.getByText('2')).toBeTruthy(); // plugin count
	expect(screen.getByRole('heading', { name: 'Loaded plugin IDs' })).toBeTruthy();
	expect(screen.getByText('sample-plugin')).toBeTruthy();
	expect(screen.getByText('other-plugin')).toBeTruthy();
});

it('renders the zero-plugin empty text', async () => {
	const fetchMock = vi.fn(async (input: string): Promise<Response> => {
		if (input === '/health') return json(200, { core_ready: true, loader_ready: true, schema_available: true, registry_state: 'empty', plugin_count: 0, plugin_ids: [] });
		throw new Error(`unexpected request ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(SystemSettingsPage);
	await waitFor(() => expect(screen.getByText('No plugins are loaded.')).toBeTruthy());
	expect(screen.queryByRole('heading', { name: 'Loaded plugin IDs' })).toBeNull();
});

it('retries after a failed diagnostics load', async () => {
	let healthy = false;
	const fetchMock = vi.fn(async (input: string): Promise<Response> => {
		if (input === '/health') {
			if (!healthy) return json(500, { error: 'boom', code: 'v1.internal' });
			return json(200, { core_ready: true, loader_ready: true, schema_available: true, registry_state: 'ready', plugin_count: 0, plugin_ids: [] });
		}
		throw new Error(`unexpected request ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);

	render(SystemSettingsPage);
	await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());
	healthy = true;
	await fireEvent.click(screen.getByRole('button', { name: 'Retry' }));
	await waitFor(() => expect(screen.getByText('No plugins are loaded.')).toBeTruthy());
	expect(fetchMock.mock.calls.filter(([url]) => url === '/health')).toHaveLength(2);
});
