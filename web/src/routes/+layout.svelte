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

	// The single owner menu shared by article and non-article headers. It is a
	// disclosure pattern: plain links/buttons, closed on Escape (with focus
	// return), outside pointer-down, and every route change.
	let menuOpen = $state(false);
	let menuArea = $state<HTMLElement | null>(null);
	let menuButton = $state<HTMLButtonElement | null>(null);

	const isLoginPage = $derived($page.route.id === '/login');
	const isArticlePage = $derived($page.route.id === '/reader/[id]');

	afterNavigate(() => {
		menuOpen = false;
		void synchronizeSession();
	});

	function toggleMenu(): void {
		menuOpen = !menuOpen;
	}

	function handleMenuEscape(event: KeyboardEvent): void {
		if (!menuOpen || event.key !== 'Escape') return;
		menuOpen = false;
		menuButton?.focus();
	}

	function closeMenuOnOutsidePointerDown(event: PointerEvent): void {
		if (!menuOpen || !menuArea) return;
		if (!menuArea.contains(event.target as Node)) menuOpen = false;
	}

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

<svelte:window onkeydown={handleMenuEscape} />
<svelte:document onpointerdown={closeMenuOnOutsidePointerDown} />

<UIHostProvider>
	{#if !isLoginPage && authenticated}
		<header class:reader-header={isArticlePage}>
			<nav aria-label="Main navigation">
				<a class="brand" href={appPath('/reader')} aria-label="Doublangu reader">Doublangu</a>
				{#if isArticlePage}
					<span class="article-label">Article reader</span>
				{:else}
					<a href={appPath('/reader')}>Articles</a>
					<a class="new-article" href={appPath('/reader/new')}>Paste article</a>
				{/if}
				<div class="menu-area" bind:this={menuArea}>
					<button
						class="menu-button"
						type="button"
						aria-label="Menu"
						aria-expanded={menuOpen}
						aria-controls="owner-menu"
						bind:this={menuButton}
						onclick={toggleMenu}
					>
						<!-- Lucide "menu" glyph inlined: the installed lucide-svelte ships
							legacy components that cannot compile in this runes-only app. -->
						<svg width="20" height="20" viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
							<line x1="4" x2="20" y1="6" y2="6" />
							<line x1="4" x2="20" y1="12" y2="12" />
							<line x1="4" x2="20" y1="18" y2="18" />
						</svg>
					</button>
					{#if menuOpen}
						<div class="menu-panel" id="owner-menu">
							<a href={appPath('/analysis-runs')}>Analysis runs</a>
							<a href={appPath('/settings')}>Settings</a>
							<button class="menu-logout" type="button" onclick={() => void signOut()}>Logout</button>
						</div>
					{/if}
				</div>
			</nav>
		</header>
	{/if}

	<main class:login-main={isLoginPage} class:reader-main={isArticlePage}>
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
	.article-label { color: var(--color-muted); }

	.new-article {
		margin-left: auto;
		padding: 0.42rem 0.7rem;
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
	}

	/* On the article header nothing else pushes right, so the menu does. */
	.reader-header .menu-area {
		margin-left: auto;
	}

	.menu-area {
		position: relative;
	}

	.menu-button {
		display: inline-flex;
		align-items: center;
		justify-content: center;
		width: 2.4rem;
		height: 2.4rem;
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		background: transparent;
		color: var(--color-text);
		cursor: pointer;
	}

	.menu-panel {
		position: absolute;
		top: calc(100% + 0.5rem);
		right: 0;
		z-index: 25;
		min-width: 11rem;
		display: grid;
		padding: 0.4rem;
		border: 1px solid var(--color-border);
		border-radius: 0.6rem;
		background: var(--color-surface-raised);
		box-shadow: 0 14px 34px rgb(0 0 0 / 0.35);
	}

	.menu-panel a,
	.menu-logout {
		padding: 0.5rem 0.65rem;
		border: 0;
		border-radius: 0.45rem;
		background: transparent;
		color: var(--color-text);
		font: inherit;
		font-weight: 650;
		text-align: left;
		text-decoration: none;
		white-space: nowrap;
		cursor: pointer;
	}

	.menu-logout {
		color: var(--color-muted);
	}

	.menu-panel a:hover,
	.menu-panel a:focus-visible,
	.menu-logout:hover,
	.menu-logout:focus-visible {
		background: var(--color-surface-hover);
		color: var(--color-text);
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
		main.reader-main { padding: 0.65rem 0.6rem; }
		.reader-header nav { min-height: 3rem; padding-block: 0.45rem; gap: 0.6rem; }
		.reader-header .brand { font-size: 1.05rem; }
		.reader-header .article-label { font-size: 0.8rem; }
		nav {
			gap: 0.75rem;
			padding-inline: 0.85rem;
		}

		.new-article {
			padding: 0.35rem 0.5rem;
		}

		.menu-button {
			width: 2.1rem;
			height: 2.1rem;
		}

		.menu-panel {
			min-width: 9.5rem;
		}
	}
	@media (max-width: 350px) { .reader-header .article-label { display: none; } }
</style>
