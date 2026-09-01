<script lang="ts">
	import { goto } from '$app/navigation';
	import { appPath } from '$lib/paths';
	import { onMount } from 'svelte';

	let password = $state('');
	let error = $state('');
	let loading = $state(false);
	let csrfToken = $state('');

	onMount(() => {
		void bootstrapCSRF();
	});

	async function bootstrapCSRF() {
		loading = true;
		error = '';
		try {
			const response = await fetch(appPath('/api/v1/auth/csrf'), { credentials: 'same-origin' });
			if (!response.ok) {
				throw new Error('bootstrap rejected');
			}
			csrfToken = getCSRFToken();
			if (!csrfToken) {
				throw new Error('bootstrap cookie missing');
			}
		} catch {
			error = 'Could not prepare sign-in. Please try again.';
		} finally {
			loading = false;
		}
	}

	async function handleSubmit(e: SubmitEvent) {
		e.preventDefault();
		error = '';
		loading = true;

		try {
			if (!csrfToken) {
				await bootstrapCSRF();
				if (!csrfToken) return;
			}
			const resp = await fetch(appPath('/api/v1/auth/login'), {
				method: 'POST',
				credentials: 'same-origin',
				headers: {
					'Content-Type': 'application/json',
					'X-CSRF-Token': csrfToken
				},
				body: JSON.stringify({ password })
			});

			if (!resp.ok) {
				const body = await resp.json().catch(() => ({}));
				error = body.error || 'Login failed';
				loading = false;
				return;
			}

			const requested = new URLSearchParams(window.location.search).get('next');
			const destination = requested?.startsWith('/') && !requested.startsWith('//') ? requested : '/reader';
			await goto(appPath(destination as `/${string}`));
		} catch {
			error = 'Network error';
			loading = false;
		}
	}

	function getCSRFToken(): string {
		const match = document.cookie.match(/(?:^|;\s*)csrf_token=([^;]*)/);
		return match?.[1] ?? '';
	}
</script>

<svelte:head>
	<title>Login — Doublangu</title>
</svelte:head>

<div class="login-page">
		<form class="login-form" onsubmit={handleSubmit}>
			<p class="eyebrow">Dutch → English reader</p>
			<h1>Welcome back</h1>
			<p class="subtitle">This unlocks your private Doublangu data. The browser’s earlier password prompt protects the beta site itself.</p>

		{#if error}
			<div class="error" role="alert">{error}</div>
		{/if}

		<label>
			<span>Password</span>
			<input
				type="password"
				bind:value={password}
				autocomplete="current-password"
				required
				disabled={loading}
				placeholder="Enter your password"
			/>
		</label>

		<button type="submit" disabled={loading || !password || !csrfToken}>
			{loading ? 'Signing in…' : 'Sign in'}
		</button>
	</form>
</div>

<style>
	.login-page {
		display: flex;
		align-items: center;
		justify-content: center;
		min-height: 100vh;
		padding: 1.25rem;
	}

	.login-form {
		width: 100%;
		max-width: 360px;
		padding: clamp(1.5rem, 5vw, 2.5rem);
		border: 1px solid var(--color-border);
		background: var(--color-surface);
		border-radius: 0.9rem;
		box-shadow: 0 24px 70px rgba(0, 0, 0, 0.28);
	}

	h1 {
		margin: 0 0 0.25rem;
		font-size: 2rem;
		text-align: center;
		letter-spacing: -0.035em;
	}

	.subtitle {
		margin: 0 0 1.5rem;
		text-align: center;
		color: var(--color-muted);
		font-size: 0.9rem;
	}

	.eyebrow {
		margin: 0 0 0.55rem;
		color: var(--color-accent-strong);
		font-size: 0.75rem;
		font-weight: 800;
		letter-spacing: 0.12em;
		text-align: center;
		text-transform: uppercase;
	}

	.error {
		background: var(--color-danger-bg);
		color: var(--color-danger);
		padding: 0.5rem 0.75rem;
		border-radius: 4px;
		margin-bottom: 1rem;
		font-size: 0.85rem;
	}

	label {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		margin-bottom: 1rem;
	}

	label span {
		font-size: 0.85rem;
		font-weight: 500;
	}

	input {
		padding: 0.5rem 0.75rem;
		border: 1px solid var(--color-border);
		border-radius: 4px;
		font-size: 1rem;
		background: var(--color-bg);
	}

	input:focus {
		outline: 2px solid var(--color-accent-strong);
		outline-offset: -1px;
	}

	button {
		width: 100%;
		padding: 0.6rem;
		background: var(--color-accent);
		color: #11131d;
		font-weight: 750;
		border: none;
		border-radius: 4px;
		font-size: 1rem;
		cursor: pointer;
	}

	button:disabled {
		opacity: 0.6;
		cursor: not-allowed;
	}
</style>
