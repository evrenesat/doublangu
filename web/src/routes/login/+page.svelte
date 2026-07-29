<script lang="ts">
	import { goto } from '$app/navigation';
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
			const response = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin' });
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
			const resp = await fetch('/api/v1/auth/login', {
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

			await goto('/');
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
		<h1>Doublangu</h1>
		<p class="subtitle">Sign in to continue</p>

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
		background: var(--color-surface, #f5f5f5);
	}

	.login-form {
		width: 100%;
		max-width: 360px;
		padding: 2rem;
		background: var(--color-bg, #fff);
		border-radius: 8px;
		box-shadow: 0 2px 8px rgba(0, 0, 0, 0.1);
	}

	h1 {
		margin: 0 0 0.25rem;
		font-size: 1.5rem;
		text-align: center;
	}

	.subtitle {
		margin: 0 0 1.5rem;
		text-align: center;
		color: var(--color-muted, #666);
		font-size: 0.9rem;
	}

	.error {
		background: #fee2e2;
		color: #991b1b;
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
		border: 1px solid var(--color-border, #ccc);
		border-radius: 4px;
		font-size: 1rem;
	}

	input:focus {
		outline: 2px solid var(--color-accent, #2563eb);
		outline-offset: -1px;
	}

	button {
		width: 100%;
		padding: 0.6rem;
		background: var(--color-accent, #2563eb);
		color: #fff;
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
