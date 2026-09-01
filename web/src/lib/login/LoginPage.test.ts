import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import LoginPage from '../../routes/login/+page.svelte';

function clearCookies() {
	for (const item of document.cookie.split(';')) {
		const name = item.split('=')[0]?.trim();
		if (name) document.cookie = `${name}=; Max-Age=0; Path=/`;
	}
}

function jsonResponse(status: number, body: unknown) {
	return new Response(JSON.stringify(body), {
		status,
		headers: { 'Content-Type': 'application/json' }
	});
}

async function fillPassword(password = 'correct-password') {
	await fireEvent.input(screen.getByLabelText('Password'), { target: { value: password } });
}

afterEach(() => {
	cleanup();
	clearCookies();
	vi.unstubAllGlobals();
	delete (globalThis as Record<string, unknown>).__doublanguLastNavigation;
});

describe('login page', () => {
	it('bootstraps CSRF from a clean cookie jar, forwards the cookie/header, and redirects after login', async () => {
		const fetchMock = vi.fn(async (input: RequestInfo | URL, init?: RequestInit) => {
			if (String(input) === '/api/v1/auth/csrf') {
				document.cookie = 'csrf_token=bootstrapped-token; Path=/';
				return jsonResponse(200, { ok: true });
			}
			expect(String(input)).toBe('/api/v1/auth/login');
			expect(init?.credentials).toBe('same-origin');
			expect(new Headers(init?.headers).get('X-CSRF-Token')).toBe('bootstrapped-token');
			return jsonResponse(200, { ok: true });
		});
		vi.stubGlobal('fetch', fetchMock);

		render(LoginPage);
		await waitFor(() => expect(fetchMock).toHaveBeenCalledWith('/api/v1/auth/csrf', { credentials: 'same-origin' }));
		await fillPassword();
		await waitFor(() => expect((screen.getByRole('button', { name: 'Sign in' }) as HTMLButtonElement).disabled).toBe(false));
		await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
		await waitFor(() => expect((globalThis as Record<string, unknown>).__doublanguLastNavigation).toBe('/reader'));
	});

	it('shows the structured backend failure without navigating', async () => {
		vi.stubGlobal('fetch', async (input: RequestInfo | URL) => {
			if (String(input) === '/api/v1/auth/csrf') {
				document.cookie = 'csrf_token=structured-token; Path=/';
				return jsonResponse(200, { ok: true });
			}
			return jsonResponse(401, { error: 'Invalid password', code: 'v1.authentication_error' });
		});
		render(LoginPage);
		await fillPassword('wrong-password');
		await waitFor(() => expect((screen.getByRole('button', { name: 'Sign in' }) as HTMLButtonElement).disabled).toBe(false));
		await fireEvent.click(screen.getByRole('button', { name: 'Sign in' }));
		await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('Invalid password'));
		expect((globalThis as Record<string, unknown>).__doublanguLastNavigation).toBeUndefined();
	});

	it('shows bootstrap/network failure and keeps submission disabled', async () => {
		vi.stubGlobal('fetch', async () => { throw new Error('offline'); });
		render(LoginPage);
		await fillPassword();
		await waitFor(() => expect(screen.getByRole('alert').textContent).toContain('Could not prepare sign-in'));
		expect((screen.getByRole('button', { name: 'Sign in' }) as HTMLButtonElement).disabled).toBe(true);
	});

	it('keeps controls disabled while the initial bootstrap is loading', async () => {
		let resolveBootstrap!: (response: Response) => void;
		const pending = new Promise<Response>((resolve) => { resolveBootstrap = resolve; });
		vi.stubGlobal('fetch', () => pending);
		render(LoginPage);
		await fillPassword();
		expect((screen.getByRole('button', { name: 'Signing in…' }) as HTMLButtonElement).disabled).toBe(true);
		document.cookie = 'csrf_token=late-token; Path=/';
		resolveBootstrap(jsonResponse(200, { ok: true }));
		await waitFor(() => expect((screen.getByRole('button', { name: 'Sign in' }) as HTMLButtonElement).disabled).toBe(false));
	});
});
