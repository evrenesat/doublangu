<script lang="ts">
	import '../app.css';
	import favicon from '$lib/assets/favicon.svg';
	import { afterNavigate, goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { appPath, appRelativePath } from '$lib/paths';
	import UIHostProvider from '$lib/plugins/UIHostContext.svelte';
	import { logoutSession } from '$lib/api/client';

	let { children } = $props();
	let checkingSession = $state(true);
	let authenticated = $state(false);
	let sessionError = $state('');
	let requestSequence = 0;

	const isLoginPage = $derived($page.route.id === '/login');

	afterNavigate(() => {
		void synchronizeSession();
	});

	async function synchronizeSession() {
		const sequence = ++requestSequence;
		sessionError = '';
		if (isLoginPage) {
			checkingSession = false;
			return;
		}
		checkingSession = true;
		try {
			const response = await fetch(appPath('/api/v1/auth/session'), { credentials: 'same-origin' });
			if (!response.ok) throw new Error(`session check returned ${response.status}`);
			const result = (await response.json()) as { authenticated?: boolean };
			if (sequence !== requestSequence) return;
			authenticated = result.authenticated === true;
			if (!authenticated) {
				const next = `${appRelativePath($page.url.pathname)}${$page.url.search}`;
				await goto(appPath(`/login?next=${encodeURIComponent(next)}`), { replaceState: true });
				return;
			}
		} catch {
			if (sequence === requestSequence) {
				authenticated = false;
				sessionError = 'Doublangu could not verify your app session.';
			}
		} finally {
			if (sequence === requestSequence) checkingSession = false;
		}
	}

	async function signOut() {
		try {
			await logoutSession();
		} finally {
			authenticated = false;
			await goto(appPath('/login'));
		}
	}
</script>

<svelte:head>
  <link rel="icon" href={favicon} />
  <meta name="viewport" content="width=device-width, initial-scale=1" />
</svelte:head>

<UIHostProvider>
	{#if !isLoginPage && authenticated}
		<header>
			<nav aria-label="Main navigation">
				<a class="brand" href={appPath('/reader')} aria-label="Doublangu reader">Doublangu</a>
				<a href={appPath('/reader')}>Articles</a>
				<a class="new-article" href={appPath('/reader/new')}>Paste article</a>
				<button class="sign-out" type="button" onclick={() => void signOut()}>Sign out</button>
			</nav>
		</header>
	{/if}

	<main class:login-main={isLoginPage}>
		{#if isLoginPage}
			{@render children()}
		{:else if checkingSession}
			<div class="session-state" role="status">Opening your reader…</div>
		{:else if sessionError}
			<div class="session-state session-error" role="alert">
				<p>{sessionError}</p>
				<button type="button" onclick={() => void synchronizeSession()}>Try again</button>
			</div>
		{:else if authenticated}
			{@render children()}
		{/if}
	</main>
</UIHostProvider>

<style>
	header {
		position: sticky;
		top: 0;
		z-index: 20;
		border-bottom: 1px solid var(--color-border);
		background: var(--color-bg-header);
		backdrop-filter: blur(14px);
	}

	nav {
		max-width: 74rem;
		min-height: 4rem;
		margin: 0 auto;
		padding: 0.65rem 1.25rem;
		display: flex;
		gap: 1.2rem;
		align-items: center;
	}

	nav a {
		font-weight: 650;
		text-decoration: none;
	}

	.brand {
		margin-right: 0.35rem;
		font-size: 1.2rem;
		letter-spacing: -0.025em;
		color: var(--color-text);
	}

	.new-article {
		margin-left: auto;
		padding: 0.42rem 0.7rem;
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
	}

	.sign-out {
		border: 0;
		background: transparent;
		color: var(--color-muted);
		cursor: pointer;
	}

	main {
		max-width: 74rem;
		margin: 0 auto;
		padding: clamp(1.25rem, 4vw, 3rem) 1.25rem;
	}

	main.login-main {
		max-width: none;
		padding: 0;
	}

	.session-state {
		max-width: 34rem;
		margin: 18vh auto 0;
		padding: 1.25rem;
		border: 1px solid var(--color-border);
		border-radius: 0.75rem;
		background: var(--color-surface);
		text-align: center;
		color: var(--color-muted);
	}

	.session-error {
		color: var(--color-danger);
	}

	.session-error p {
		margin-bottom: 0.75rem;
	}

	.session-error button {
		padding: 0.45rem 0.75rem;
		border: 1px solid currentColor;
		border-radius: 0.45rem;
		background: transparent;
		color: inherit;
	}

	@media (max-width: 540px) {
		nav {
			gap: 0.75rem;
			padding-inline: 0.85rem;
		}

		.new-article {
			padding: 0.35rem 0.5rem;
		}

		.sign-out {
			font-size: 0.9rem;
		}
	}
</style>
