<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { createLibrary, DoublanguAPIError, DoublanguNetworkError } from '$lib/api/client';

	let name = $state('');
	let sourceLanguage = $state('');
	let targetLanguage = $state('');
	let description = $state('');
	let pending = $state(false);
	let ready = $state(false);
	let error = $state('');

	onMount(() => {
		ready = true;
	});

	async function submit(event: SubmitEvent) {
		event.preventDefault();
		error = '';
		const fields = new FormData(event.currentTarget as HTMLFormElement);
		const libraryName = String(fields.get('name') ?? '').trim();
		const source = String(fields.get('source_language') ?? '').trim();
		const target = String(fields.get('target_language') ?? '').trim();
		const summary = String(fields.get('description') ?? '').trim();
		if (!libraryName || !source || !target) {
			error = 'Name, source language, and target language are required.';
			return;
		}
		pending = true;
		try {
			const library = await createLibrary({
				name: libraryName,
				source_language: source,
				target_language: target,
				...(summary ? { description: summary } : {})
			});
			await goto(`/library/${encodeURIComponent(library.id)}`);
		} catch (cause) {
			if (cause instanceof DoublanguAPIError) error = cause.message;
			else if (cause instanceof DoublanguNetworkError) error = 'Could not reach the server. Check your connection.';
			else error = 'Could not create the library.';
		} finally {
			pending = false;
		}
	}
</script>

<svelte:head><title>Create library — Doublangu</title></svelte:head>

<div class="new-library">
	<a href="/library">← Back to library</a>
	<h1>Create library</h1>
	{#if error}<p id="form-error" role="alert">{error}</p>{/if}
	<form novalidate onsubmit={submit} aria-describedby={error ? 'form-error' : undefined}>
		<label>Name <input name="name" bind:value={name} aria-required="true" disabled={pending} /></label>
		<label>Source language <input name="source_language" bind:value={sourceLanguage} aria-required="true" disabled={pending} /></label>
		<label>Target language <input name="target_language" bind:value={targetLanguage} aria-required="true" disabled={pending} /></label>
		<label>Description (optional) <textarea name="description" bind:value={description} disabled={pending}></textarea></label>
		<button type="submit" disabled={pending || !ready}>{pending ? 'Creating…' : 'Create library'}</button>
	</form>
</div>

<style>
	.new-library { max-width: 36rem; margin: 0 auto; }
	form { display: grid; gap: .85rem; }
	label { display: grid; gap: .3rem; font-weight: 600; }
	input, textarea, button { font: inherit; padding: .5rem; }
	textarea { min-height: 5rem; }
	[role='alert'] { color: #991b1b; }
</style>
