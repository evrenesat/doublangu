<script lang="ts">
	import { page } from '$app/stores';
	import { appPath } from '$lib/paths';

	let { children } = $props();

	const sections = [
		{ href: '/settings', label: 'Reader' },
		{ href: '/settings/analysis', label: 'Analysis' },
		{ href: '/settings/workers', label: 'Workers' },
		{ href: '/settings/system', label: 'System' }
	];

	const currentRoute = $derived($page.route.id ?? '');
	const activeHref = $derived.by(() => {
		const matches = sections.filter((section) => currentRoute === section.href || currentRoute.startsWith(`${section.href}/`));
		// The longest match wins so /settings/analysis never highlights Reader.
		return matches.sort((a, b) => b.href.length - a.href.length)[0]?.href ?? '';
	});
</script>

<div class="settings-page">
	<div class="settings-heading">
		<h1>Settings</h1>
		<p>Configure reading behavior, analysis models, workers, and server diagnostics.</p>
	</div>

	<div class="settings-body">
		<nav class="settings-nav" aria-label="Settings sections">
			{#each sections as section (section.href)}
				<a
					href={appPath(section.href as `/${string}`)}
					class:active={section.href === activeHref}
					aria-current={section.href === activeHref ? 'page' : undefined}
				>
					{section.label}
				</a>
			{/each}
		</nav>

		<div class="settings-content">
			{@render children()}
		</div>
	</div>
</div>

<style>
	.settings-page {
		max-width: 54rem;
		margin: 0 auto;
	}

	.settings-heading h1 {
		margin-bottom: 0.35rem;
	}

	.settings-heading p {
		margin: 0 0 1.5rem;
		color: var(--color-muted);
	}

	.settings-body {
		display: grid;
		grid-template-columns: 12rem minmax(0, 1fr);
		gap: 1.75rem;
		align-items: start;
	}

	.settings-nav {
		display: grid;
		gap: 0.2rem;
	}

	.settings-nav a {
		padding: 0.42rem 0.6rem;
		border-radius: 0.45rem;
		color: var(--color-muted);
		text-decoration: none;
		font-weight: 650;
	}

	.settings-nav a:hover,
	.settings-nav a:focus-visible {
		color: var(--color-text);
		background: var(--color-surface-hover);
	}

	.settings-nav a.active {
		color: var(--color-text);
		background: var(--color-surface-raised);
	}

	@media (max-width: 600px) {
		.settings-body {
			grid-template-columns: 1fr;
			gap: 1rem;
		}

		.settings-nav {
			display: flex;
			flex-wrap: wrap;
			gap: 0.3rem;
		}

		.settings-nav a {
			white-space: nowrap;
		}
	}
</style>
