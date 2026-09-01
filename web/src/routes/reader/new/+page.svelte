<script lang="ts">
	import { goto } from '$app/navigation';
	import { appPath } from '$lib/paths';
	import { onMount } from 'svelte';
	import {
		createArticle,
		DoublanguAPIError,
		DoublanguNetworkError
	} from '$lib/api/client';

	let title = $state('');
	let body = $state('');
	let pending = $state(false);
	let ready = $state(false);
	let error = $state('');

	onMount(() => {
		ready = true;
	});

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		error = '';
		const form = event.currentTarget as HTMLFormElement;
		const values = new FormData(form);
		const articleTitle = String(values.get('title') ?? '').trim();
		const articleBody = String(values.get('body') ?? '');
		if (!articleTitle || !articleBody.trim()) {
			error = 'Title and article text are required.';
			return;
		}

		pending = true;
		try {
			const article = await createArticle({
				title: articleTitle,
				body: articleBody,
				source_language: 'nl',
				target_language: 'en'
			});
			// The v2 server stores the source and queues analysis in one transaction.
			// Legacy servers retain the old compatibility choreography until they are
			// migrated, without making the new background path wait on enrichment.
			const path = appPath(`/reader/${encodeURIComponent(article.id)}`);
			await goto(article.analysis_status === undefined ? `${path}?enrich=1` : path);
		} catch (cause) {
			if (cause instanceof DoublanguAPIError) error = cause.message;
			else if (cause instanceof DoublanguNetworkError) error = 'Could not reach the server. Check your connection.';
			else error = 'Could not save the article.';
			pending = false;
		}
	}
</script>

<svelte:head>
	<title>Paste article — Doublangu</title>
</svelte:head>

<div class="new-reader-page">
	<a href={appPath('/reader')}>← Back to reader</a>
	<h1>Paste an article</h1>
	<p class="intro">Save the Dutch source first. Doublangu adds quiet English hints after it is stored.</p>

	{#if error}
		<p id="article-form-error" class="error" role="alert">{error}</p>
	{/if}

	<form novalidate onsubmit={submit} aria-describedby={error ? 'article-form-error' : undefined}>
		<label>
			<span>Title</span>
			<input name="title" bind:value={title} maxlength="200" required disabled={pending} />
		</label>
		<label>
			<span>Article text</span>
			<textarea name="body" bind:value={body} required disabled={pending} placeholder="Plak hier een Nederlands artikel…"></textarea>
		</label>
		<div class="language-direction" aria-label="Translation direction">
			<span><strong>Dutch</strong> source</span>
			<span aria-hidden="true">→</span>
			<span><strong>English</strong> hints</span>
		</div>
	<button type="submit" disabled={pending || !ready}>{pending ? 'Saving…' : 'Save article'}</button>
	</form>
</div>

<style>
	.new-reader-page { max-width: 46rem; margin: 0 auto; }
	h1 { margin: 1rem 0 0.35rem; }
	.intro { margin: 0 0 1.4rem; color: var(--color-muted, #64748b); }
	form { display: grid; gap: 1rem; }
	label { display: grid; gap: 0.35rem; font-weight: 600; }
	input, textarea, button { box-sizing: border-box; width: 100%; padding: 0.65rem 0.75rem; font: inherit; }
	input, textarea { border: 1px solid var(--color-border, #cbd5e1); border-radius: 0.45rem; background: var(--color-bg, #fff); color: inherit; }
	textarea { min-height: 16rem; resize: vertical; line-height: 1.5; }
	input:focus-visible, textarea:focus-visible, button:focus-visible { outline: 2px solid var(--color-accent, #2563eb); outline-offset: 1px; }
	.language-direction { display: flex; align-items: center; gap: 0.65rem; color: var(--color-muted); }
	.language-direction strong { color: var(--color-text); }
	button { border: 0; border-radius: 0.45rem; background: var(--color-accent, #2563eb); color: #fff; font-weight: 700; cursor: pointer; }
	button:disabled { cursor: wait; opacity: 0.65; }
	.error { padding: 0.75rem; border-radius: 0.45rem; background: #fee2e2; color: #991b1b; }
	@media (max-width: 480px) { textarea { min-height: 12rem; } }
</style>
