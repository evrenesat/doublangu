<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { listLibraries, DoublanguAPIError, DoublanguNetworkError, type Library } from '$lib/api/client';

	let libraries = $state<Library[]>([]);
	let loading = $state(true);
	let error = $state('');

	onMount(() => {
		void loadLibraries();
	});

	async function loadLibraries() {
		loading = true;
		error = '';
		try {
			libraries = await listLibraries();
		} catch (e) {
			if (e instanceof DoublanguAPIError) {
				error = e.message;
			} else if (e instanceof DoublanguNetworkError) {
				error = 'Could not reach the server. Check your connection.';
			} else {
				error = 'An unexpected error occurred.';
			}
		} finally {
			loading = false;
		}
	}

	function detailUrl(id: string): string {
		return `/library/${encodeURIComponent(id)}`;
	}
</script>

<svelte:head>
	<title>Library — Doublangu</title>
</svelte:head>

<div class="library-page">
	<h1>Library</h1>

	{#if loading}
		<p class="status" role="status">Loading libraries…</p>
	{:else if error}
		<div class="error" role="alert">
			<p>{error}</p>
			<button onclick={() => void loadLibraries()}>Retry</button>
		</div>
	{:else if libraries.length === 0}
		<div class="empty">
			<p>No libraries yet.</p>
			<a href="/library/new" class="button">Create your first library</a>
		</div>
	{:else}
		<a href="/library/new" class="button create-link">Create library</a>
		<ul class="library-list" role="list">
			{#each libraries as lib (lib.id)}
				<li role="listitem" class="library-item">
					<a href={detailUrl(lib.id)} class="library-link">
						<span class="lib-name">{lib.name}</span>
						<span class="lib-languages">{lib.source_language} → {lib.target_language}</span>
						{#if lib.description}
							<span class="lib-description">{lib.description}</span>
						{/if}
					</a>
				</li>
			{/each}
		</ul>
	{/if}
</div>

<style>
	.library-page {
		max-width: 48rem;
		margin: 0 auto;
	}

	h1 {
		margin-bottom: 1rem;
	}

	.status {
		color: var(--color-muted, #666);
	}

	.error {
		background: #fee2e2;
		color: #991b1b;
		padding: 1rem;
		border-radius: 6px;
		margin-bottom: 1rem;
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

	.empty {
		text-align: center;
		padding: 2rem;
		border: 2px dashed var(--color-border, #ccc);
		border-radius: 8px;
		color: var(--color-muted, #666);
	}

	.button {
		display: inline-block;
		padding: 0.5rem 1rem;
		background: var(--color-accent, #2563eb);
		color: #fff;
		border-radius: 4px;
		text-decoration: none;
		font-size: 0.9rem;
		margin-top: 0.5rem;
	}

	.create-link {
		margin-bottom: 1rem;
	}

	.library-list {
		list-style: none;
		padding: 0;
		margin: 0;
		display: flex;
		flex-direction: column;
		gap: 0.5rem;
	}

	.library-item {
		padding: 0;
	}

	.library-link {
		display: flex;
		flex-direction: column;
		gap: 0.25rem;
		padding: 0.75rem 1rem;
		border: 1px solid var(--color-border, #ddd);
		border-radius: 6px;
		text-decoration: none;
		color: inherit;
		transition: background 0.15s;
		outline-offset: -1px;
	}

	.library-link:hover,
	.library-link:focus {
		background: var(--color-surface-hover, #f0f4ff);
		outline: 2px solid var(--color-accent, #2563eb);
	}

	.lib-name {
		font-weight: 600;
		font-size: 1.05rem;
	}

	.lib-languages {
		font-size: 0.8rem;
		color: var(--color-muted, #666);
	}

	.lib-description {
		font-size: 0.85rem;
		color: var(--color-muted, #666);
	}
</style>
