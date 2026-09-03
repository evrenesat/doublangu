<script lang="ts">
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
			const response = await fetch(appPath('/health'), { credentials: 'same-origin' });
			if (!response.ok) throw new Error(`Server returned ${response.status}`);
			const report = await response.json();
			coreReady = report.core_ready ?? null;
			loaderReady = report.loader_ready ?? null;
			schemaAvailable = report.schema_available ?? null;
			registryState = report.registry_state ?? null;
			pluginCount = report.plugin_count ?? null;
			pluginIds = report.plugin_ids ?? [];
		} catch (cause) {
			error = cause instanceof DoublanguNetworkError
				? 'Could not retrieve server diagnostics.'
				: 'Could not reach the server. Check your connection.';
		} finally {
			loading = false;
		}
	}

	function serverStatus(value: boolean | null): string {
		if (value === true) return 'Ready';
		if (value === false) return 'Not ready';
		return 'Unknown';
	}

	function serverStatusClass(value: boolean | null): string {
		if (value === true) return 'status-ok';
		if (value === false) return 'status-warn';
		return 'status-unknown';
	}
</script>

<svelte:head>
	<title>System — Doublangu</title>
</svelte:head>

<section class="system-page" aria-labelledby="system-heading">
	<h2 id="system-heading">System</h2>
	<p class="intro">Read-only server information used for troubleshooting.</p>

	<div class="panel">
		<h3>Server status</h3>
		{#if loading}
			<p class="status" role="status">Loading diagnostics…</p>
		{:else if error}
			<div class="error-box" role="alert">
				<p>{error}</p>
				<button type="button" class="secondary" onclick={() => void loadDiagnostics()}>Retry</button>
			</div>
		{:else}
			<dl class="diag-list">
				<div><dt>Core</dt><dd><span class={serverStatusClass(coreReady)}>{serverStatus(coreReady)}</span></dd></div>
				<div><dt>Plugin loader</dt><dd><span class={serverStatusClass(loaderReady)}>{serverStatus(loaderReady)}</span></dd></div>
				<div><dt>Schema</dt><dd><span class={serverStatusClass(schemaAvailable)}>{serverStatus(schemaAvailable)}</span></dd></div>
				<div><dt>Registry state</dt><dd>{registryState ?? '—'}</dd></div>
				<div><dt>Plugins loaded</dt><dd>{pluginCount ?? '—'}</dd></div>
			</dl>
			{#if pluginIds.length > 0}
				<h3>Loaded plugin IDs</h3>
				<ul class="plugin-id-list" role="list">
					{#each pluginIds as pid (pid)}<li>{pid}</li>{/each}
				</ul>
			{:else}<p class="muted">No plugins are loaded.</p>{/if}
		{/if}
	</div>
</section>

<style>
	h2 {
		margin-bottom: 0.35rem;
	}

	h3 {
		margin: 0 0 0.65rem;
	}

	.intro {
		margin: 0 0 1.25rem;
		color: var(--color-muted);
	}

	.panel {
		padding: 1.25rem;
		border: 1px solid var(--color-border);
		border-radius: 0.75rem;
		background: var(--color-surface);
	}

	.status,
	.muted {
		color: var(--color-muted);
	}

	.error-box {
		padding: 0.85rem;
		border-radius: 0.55rem;
		background: var(--color-danger-bg);
		color: var(--color-danger);
	}

	.error-box p {
		margin-bottom: 0.7rem;
	}

	.secondary {
		border: 1px solid currentColor;
		border-radius: 0.5rem;
		padding: 0.5rem 0.75rem;
		cursor: pointer;
		background: transparent;
		color: inherit;
		font: inherit;
	}

	.status-ok {
		color: #7ee2a8 !important;
		font-weight: 650;
	}

	.status-warn {
		color: var(--color-warning) !important;
		font-weight: 650;
	}

	.status-unknown {
		color: var(--color-muted) !important;
	}

	.diag-list {
		margin: 0;
	}

	.diag-list > div {
		display: flex;
		gap: 1rem;
		padding: 0.5rem 0;
		border-bottom: 1px solid var(--color-border);
	}

	.diag-list dt {
		min-width: 8rem;
		font-weight: 650;
	}

	.diag-list dd {
		margin: 0;
	}

	.plugin-id-list {
		padding-left: 1.25rem;
		font-size: 0.9rem;
	}
</style>
