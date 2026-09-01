<script lang="ts">
	/**
	 * Settings page for the Doublangu owner.
	 * Displays core server and plugin diagnostics fetched from /health.
	 */
	import { onMount } from 'svelte';
	import { DoublanguNetworkError } from '$lib/api/client';
	import { appPath } from '$lib/paths';

	let coreReady = $state<boolean | null>(null);
	let loaderReady = $state<boolean | null>(null);
	let schemaAvailable = $state<boolean | null>(null);
	let registryState = $state<string | null>(null);
	let pluginCount = $state<number | null>(null);
	let pluginIds = $state<string[]>([]);

	let loading = $state(true);
	let error = $state('');

	onMount(() => {
		void loadDiagnostics();
	});

	async function loadDiagnostics() {
		loading = true;
		error = '';
		try {
			const resp = await fetch(appPath('/health'), { credentials: 'same-origin' });
			if (!resp.ok) {
				throw new DoublanguNetworkError(`Server returned ${resp.status}`);
			}
			const report = await resp.json();
			coreReady = report.core_ready ?? null;
			loaderReady = report.loader_ready ?? null;
			schemaAvailable = report.schema_available ?? null;
			registryState = report.registry_state ?? null;
			pluginCount = report.plugin_count ?? null;
			pluginIds = report.plugin_ids ?? [];
		} catch (e) {
			if (e instanceof DoublanguNetworkError) {
				error = 'Could not retrieve server diagnostics.';
			} else {
				error = 'Could not reach the server. Check your connection.';
			}
		} finally {
			loading = false;
		}
	}

	function statusLabel(value: boolean | null): string {
		if (value === true) return 'Ready';
		if (value === false) return 'Not ready';
		return 'Unknown';
	}

	function statusClass(value: boolean | null): string {
		if (value === true) return 'status-ok';
		if (value === false) return 'status-warn';
		return 'status-unknown';
	}
</script>

<svelte:head>
	<title>Settings — Doublangu</title>
</svelte:head>

<div class="settings-page">
	<h1>Settings</h1>

	{#if loading}
		<p class="status" role="status">Loading diagnostics…</p>
	{:else if error}
		<div class="error" role="alert">
			<p>{error}</p>
			<button onclick={() => void loadDiagnostics()}>Retry</button>
		</div>
	{:else}
		<section class="diagnostics">
			<h2>Server Diagnostics</h2>
			<dl class="diag-list">
				<div class="diag-row">
					<dt>Core</dt>
					<dd><span class={statusClass(coreReady)}>{statusLabel(coreReady)}</span></dd>
				</div>
				<div class="diag-row">
					<dt>Plugin loader</dt>
					<dd><span class={statusClass(loaderReady)}>{statusLabel(loaderReady)}</span></dd>
				</div>
				<div class="diag-row">
					<dt>Schema</dt>
					<dd><span class={statusClass(schemaAvailable)}>{statusLabel(schemaAvailable)}</span></dd>
				</div>
				<div class="diag-row">
					<dt>Registry state</dt>
					<dd>{registryState ?? '—'}</dd>
				</div>
				<div class="diag-row">
					<dt>Plugins loaded</dt>
					<dd>{pluginCount ?? '—'}</dd>
				</div>
			</dl>

			{#if pluginIds.length > 0}
				<h3>Loaded plugin IDs</h3>
				<ul class="plugin-id-list" role="list">
					{#each pluginIds as pid (pid)}
						<li role="listitem">{pid}</li>
					{/each}
				</ul>
			{:else}
				<p class="empty-hint">No plugins are loaded.</p>
			{/if}
		</section>

		<section class="navigation">
			<h2>Navigation</h2>
			<nav aria-label="Settings navigation">
				<ul class="nav-links">
					<li><a href={appPath('/library')}>Library</a></li>
					<li><a href={appPath('/plugins')}>Plugins</a></li>
				</ul>
			</nav>
		</section>
	{/if}
</div>

<style>
	.settings-page {
		max-width: 36rem;
		margin: 0 auto;
	}

	h1 {
		margin-bottom: 1.5rem;
	}

	.status {
		color: var(--color-muted, #666);
	}

	.error {
		background: #fee2e2;
		color: #991b1b;
		padding: 1rem;
		border-radius: 6px;
	}

	.error button {
		margin-top: 0.5rem;
		padding: 0.4rem 0.8rem;
		background: #991b1b;
		color: #fff;
		border: none;
		border-radius: 4px;
		cursor: pointer;
	}

	.diagnostics {
		margin-bottom: 2rem;
	}

	.diag-list {
		margin: 0;
	}

	.diag-row {
		display: flex;
		gap: 1rem;
		padding: 0.5rem 0;
		border-bottom: 1px solid var(--color-border, #eee);
	}

	.diag-row dt {
		font-weight: 600;
		min-width: 8rem;
	}

	.diag-row dd {
		margin: 0;
	}

	.status-ok {
		color: #16a34a;
		font-weight: 600;
	}

	.status-warn {
		color: #dc2626;
		font-weight: 600;
	}

	.status-unknown {
		color: var(--color-muted, #999);
	}

	.plugin-id-list {
		list-style: disc;
		padding-left: 1.25rem;
		margin: 0.5rem 0;
		font-size: 0.9rem;
	}

	.empty-hint {
		color: var(--color-muted, #666);
	}

	.navigation h2 {
		margin-bottom: 0.5rem;
	}

	.nav-links {
		list-style: none;
		padding: 0;
		display: flex;
		gap: 1rem;
	}

	.nav-links a {
		color: var(--color-accent, #2563eb);
	}
</style>
