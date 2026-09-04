import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import SpeechWorkersPage from '../../routes/settings/workers/+page.svelte';

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

const minutesAgo = (minutes: number): string => new Date(Date.now() - minutes * 60_000).toISOString();

const worker = {
	id: 'worker-1',
	name: 'MacBook Air',
	protocol_version: 'speech-worker.v1',
	revoked_at: '',
	last_seen_at: minutesAgo(2),
	capabilities: [{ engine: 'avspeech', languages: ['nl'], unit_kinds: ['word', 'sentence'], max_bytes: 10, max_duration_ms: 20 }],
	llm_relay_capabilities: [{ max_completion_bytes: 2097152 }],
	relay_last_seen_at: minutesAgo(1),
	software_version: '0.2.1',
	created_at: '2026-08-28T09:00:00Z',
	updated_at: minutesAgo(1)
};

/** Same day formatting the page uses, so assertions follow the test locale. */
function formatDay(iso: string): string {
	return new Date(iso).toLocaleDateString(undefined, { day: 'numeric', month: 'short', year: 'numeric' });
}

/** Same expiry formatting the page uses. */
function formatExpiry(iso: string): string {
	return new Date(iso).toLocaleString(undefined, { day: 'numeric', month: 'short', year: 'numeric', hour: '2-digit', minute: '2-digit' });
}

const enrollment = { id: 'enrollment-1', token: 'one-time-enrollment-token', expires_at: '2026-09-03T12:30:00Z' };

afterEach(() => {
	cleanup();
	vi.unstubAllGlobals();
	delete (navigator as unknown as { clipboard?: unknown }).clipboard;
	for (const cookie of document.cookie.split(';')) {
		const name = cookie.split('=')[0]?.trim();
		if (name) document.cookie = `${name}=; Max-Age=0; Path=/`;
	}
});

/** Mutations need the CSRF cookie; set it before any test that posts or deletes. */
function useCsrfCookie(): void {
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
}

function stubWorkersFetch(handlers: {
	workers: () => unknown;
	enrollment?: () => Response;
	revoke?: () => Response;
}): ReturnType<typeof vi.fn> {
	const fetchMock = vi.fn(async (input: string, init: RequestInit = {}): Promise<Response> => {
		const method = init.method ?? 'GET';
		if (input === '/api/v1/speech-workers' && method === 'GET') return json(200, handlers.workers());
		if (input === '/api/v1/speech-workers/enrollments' && method === 'POST') return handlers.enrollment?.() ?? json(201, enrollment);
		if (input === '/api/v1/speech-workers/worker-1' && method === 'DELETE') return handlers.revoke?.() ?? json(200, { ok: true });
		throw new Error(`unexpected request ${method} ${input}`);
	});
	vi.stubGlobal('fetch', fetchMock);
	return fetchMock;
}

it('lists enrolled workers with capabilities, relay state, and revoke control', async () => {
	stubWorkersFetch({ workers: () => [worker] });

	render(SpeechWorkersPage);
	// Wait for data-driven content rather than the static heading.
	expect(await screen.findByText('MacBook Air')).toBeTruthy();
	expect(screen.getByText('Version 0.2.1 · speech-worker.v1')).toBeTruthy();
	expect(screen.getByText('Capabilities: TTS avspeech (nl) · LLM relay')).toBeTruthy();
	expect(screen.getByText(/Last seen: \d+ minutes? ago/)).toBeTruthy();
	expect(screen.getByText(/Relay last seen: \d+ minutes? ago/)).toBeTruthy();
	expect(screen.getByText(`Enrolled: ${formatDay(worker.created_at)}`)).toBeTruthy();
	expect(screen.getByRole('button', { name: 'Revoke' })).toBeTruthy();
	expect(screen.getByRole('heading', { name: 'Enroll a new worker' })).toBeTruthy();
	expect(screen.getByRole('button', { name: 'Generate enrollment token' })).toBeTruthy();
});

it('renders the empty state when no workers are enrolled', async () => {
	stubWorkersFetch({ workers: () => [] });

	render(SpeechWorkersPage);
	await waitFor(() => expect(screen.getByText('No workers are enrolled yet.')).toBeTruthy());
	expect(screen.queryByRole('button', { name: 'Revoke' })).toBeNull();
});

it('generates a token, shows it with its expiry, and copies it via the clipboard', async () => {
	useCsrfCookie();
	const writeText = vi.fn().mockResolvedValue(undefined);
	Object.defineProperty(navigator, 'clipboard', { value: { writeText }, configurable: true });
	let generationCount = 0;
	stubWorkersFetch({
		workers: () => [],
		enrollment: () => {
			generationCount += 1;
			return json(201, {
				id: `enrollment-${generationCount}`,
				token: generationCount === 1 ? 'one-time-enrollment-token' : 'one-time-enrollment-token-2',
				expires_at: '2026-09-03T12:30:00Z'
			});
		}
	});

	render(SpeechWorkersPage);
	await fireEvent.click(screen.getByRole('button', { name: 'Generate enrollment token' }));
	await waitFor(() => expect(screen.getByRole('heading', { name: 'Enrollment token' })).toBeTruthy());
	expect(screen.getByText('one-time-enrollment-token')).toBeTruthy();
	expect(screen.getByText(`Expires: ${formatExpiry(enrollment.expires_at)}`)).toBeTruthy();
	expect(screen.getByText('Use this token in the worker setup. It can be used once.')).toBeTruthy();

	await fireEvent.click(screen.getByRole('button', { name: 'Copy token' }));
	await waitFor(() => expect(writeText).toHaveBeenCalledWith('one-time-enrollment-token'));
	expect(screen.getByText('Copied.')).toBeTruthy();

	// Generating again replaces the shown token; nothing else received it.
	await fireEvent.click(screen.getByRole('button', { name: 'Generate enrollment token' }));
	await waitFor(() => expect(screen.getByText('one-time-enrollment-token-2')).toBeTruthy());
	expect(screen.queryByText('one-time-enrollment-token')).toBeNull();
	expect(writeText).toHaveBeenCalledTimes(1);
});

it('recovers when enrollment generation fails once', async () => {
	useCsrfCookie();
	let healthy = false;
	const fetchMock = stubWorkersFetch({
		workers: () => [],
		enrollment: () => (healthy ? json(201, enrollment) : json(500, { error: 'generation failed', code: 'v1.internal' }))
	});

	render(SpeechWorkersPage);
	await waitFor(() => expect(screen.getByRole('button', { name: 'Generate enrollment token' })).toBeTruthy());
	await fireEvent.click(screen.getByRole('button', { name: 'Generate enrollment token' }));
	await waitFor(() => expect(screen.getByRole('alert')).toBeTruthy());

	// The failure must not leave the control permanently disabled.
	const generate = screen.getByRole('button', { name: 'Generate enrollment token' }) as HTMLButtonElement;
	expect(generate.disabled).toBe(false);
	healthy = true;
	await fireEvent.click(generate);
	await waitFor(() => expect(screen.getByText('one-time-enrollment-token')).toBeTruthy());
	expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'POST')).toHaveLength(2);
});

it('revokes only after an inline confirmation and reloads the list', async () => {
	useCsrfCookie();
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	let revoked = false;
	const fetchMock = stubWorkersFetch({ workers: () => (revoked ? [] : [worker]) });

	render(SpeechWorkersPage);
	await waitFor(() => expect(screen.getByText('MacBook Air')).toBeTruthy());

	// Confirmation names the worker and Cancel never calls DELETE.
	await fireEvent.click(screen.getByRole('button', { name: 'Revoke' }));
	expect(screen.getByText(/Revoke MacBook Air\?/)).toBeTruthy();
	await fireEvent.click(screen.getByRole('button', { name: 'Cancel' }));
	expect(screen.queryByText(/Revoke MacBook Air\?/)).toBeNull();
	expect(fetchMock.mock.calls.filter(([, init]) => init?.method === 'DELETE')).toHaveLength(0);

	// Confirming calls DELETE with CSRF and then reloads the (now empty) list.
	await fireEvent.click(screen.getByRole('button', { name: 'Revoke' }));
	revoked = true;
	await fireEvent.click(screen.getByRole('button', { name: 'Revoke worker' }));
	await waitFor(() => expect(screen.getByText('No workers are enrolled yet.')).toBeTruthy());
	const deletes = fetchMock.mock.calls.filter(([url, init]) => url === '/api/v1/speech-workers/worker-1' && init?.method === 'DELETE');
	expect(deletes).toHaveLength(1);
	const headers = new Headers(deletes[0]![1]!.headers);
	expect(headers.get('x-csrf-token')).toBe('test-csrf-token');
});

it('shows revoke failures inline and stays usable', async () => {
	useCsrfCookie();
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
	stubWorkersFetch({ workers: () => [worker], revoke: () => json(403, { error: 'revocation denied', code: 'v1.forbidden' }) });

	render(SpeechWorkersPage);
	await waitFor(() => expect(screen.getByText('MacBook Air')).toBeTruthy());
	await fireEvent.click(screen.getByRole('button', { name: 'Revoke' }));
	await fireEvent.click(screen.getByRole('button', { name: 'Revoke worker' }));
	await waitFor(() => expect(screen.getByText('revocation denied')).toBeTruthy());
	expect(screen.getByRole('button', { name: 'Revoke worker' }).hasAttribute('disabled')).toBe(false);
});

it('marks a revoked worker without offering revoke again', async () => {
	stubWorkersFetch({ workers: () => [{ ...worker, revoked_at: '2026-09-01T00:00:00Z' }] });

	render(SpeechWorkersPage);
	await waitFor(() => expect(screen.getByText('MacBook Air')).toBeTruthy());
	expect(screen.getByText('Revoked')).toBeTruthy();
	expect(screen.queryByRole('button', { name: 'Revoke' })).toBeNull();
});
