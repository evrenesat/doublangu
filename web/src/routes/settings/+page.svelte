<script lang="ts">
	import { onMount } from 'svelte';
	import {
		DoublanguAPIError,
		DoublanguNetworkError,
		getReaderSettings,
		saveReaderSettings
	} from '$lib/api/client';

	let pronounceOnHover = $state(true);
	let pronounceOnHoverLoading = $state(true);
	let readerError = $state('');
	let readerLoadFailed = $state(false);
	let readerSaving = $state(false);

	onMount(() => {
		void loadReaderSettings();
	});

	async function loadReaderSettings() {
		pronounceOnHoverLoading = true;
		readerError = '';
		readerLoadFailed = false;
		try {
			const settings = await getReaderSettings();
			pronounceOnHover = settings.pronounce_on_hover;
			try {
				localStorage.setItem('doublangu:reader:pronounce-on-hover', JSON.stringify({ pronounce_on_hover: settings.pronounce_on_hover }));
			} catch {
				// Local mirror only; the server remains authoritative.
			}
		} catch (cause) {
			readerError = errorMessage(cause, 'Could not load reader preferences.');
			readerLoadFailed = true;
		} finally {
			pronounceOnHoverLoading = false;
		}
	}

	async function togglePronounceOnHover() {
		const previous = pronounceOnHover;
		pronounceOnHover = !pronounceOnHover;
		readerSaving = true;
		readerError = '';
		try {
			const saved = await saveReaderSettings({ pronounce_on_hover: pronounceOnHover });
			pronounceOnHover = saved.pronounce_on_hover;
			try {
				localStorage.setItem('doublangu:reader:pronounce-on-hover', JSON.stringify({ pronounce_on_hover: saved.pronounce_on_hover }));
			} catch {
				// Ignore local storage failures; the server value stands.
			}
		} catch (cause) {
			pronounceOnHover = previous;
			readerError = errorMessage(cause, 'Could not save reader preferences.');
		} finally {
			readerSaving = false;
		}
	}

	function errorMessage(cause: unknown, fallback: string): string {
		if (cause instanceof DoublanguAPIError) return cause.message;
		if (cause instanceof DoublanguNetworkError) return 'Could not reach the server. Check your connection.';
		if (cause instanceof Error) return cause.message;
		return fallback;
	}
</script>

<svelte:head>
	<title>Settings — Doublangu</title>
</svelte:head>

<section class="panel reader-settings" aria-labelledby="reader-heading">
	<div class="section-heading">
		<div>
			<h2 id="reader-heading">Reader</h2>
			<p>Reader preferences are saved to your account and apply across browsers.</p>
		</div>
	</div>
	<h3>Pronunciation</h3>
	{#if pronounceOnHoverLoading}
		<p class="status" role="status">Loading reader preferences…</p>
	{:else}
		<label class="preference-row">
			<input type="checkbox" checked={pronounceOnHover} disabled={readerSaving} onchange={() => void togglePronounceOnHover()} />
			<span>
				<strong>Pronounce on hover</strong>
				<small>Play a word's pronunciation when the pointer hovers over it in the reader.</small>
			</span>
		</label>
	{/if}
	{#if readerError}
		<p class="status error" role="alert">{readerError}</p>
	{/if}
	{#if readerLoadFailed}
		<button type="button" class="secondary" onclick={() => void loadReaderSettings()}>Retry</button>
	{/if}
</section>

<style>
	.panel {
		padding: 1.25rem;
		border: 1px solid var(--color-border);
		border-radius: 0.75rem;
		background: var(--color-surface);
	}

	h2 {
		margin-bottom: 0.35rem;
	}

	h3 {
		margin: 1.25rem 0 0.75rem;
	}

	.section-heading p {
		margin: 0;
		color: var(--color-muted);
	}

	.status {
		color: var(--color-muted);
	}

	.secondary {
		margin-top: 0.6rem;
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		padding: 0.45rem 0.7rem;
		cursor: pointer;
		font: inherit;
		background: var(--color-surface-raised);
		color: var(--color-text);
	}

	.preference-row {
		display: flex;
		align-items: flex-start;
		gap: 0.6rem;
	}

	.preference-row small {
		display: block;
		color: var(--color-muted);
	}

	.status.error {
		color: var(--color-danger);
	}
</style>
