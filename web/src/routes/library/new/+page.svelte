<script lang="ts">
	import { onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { appPath } from '$lib/paths';
	import { createLibrary, DoublanguAPIError, DoublanguNetworkError } from '$lib/api/client';

	let name = $state('');
	let sourceLanguage = $state('nl');
	let targetLanguage = $state('en');
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
			await goto(appPath(`/library/${encodeURIComponent(library.id)}`));
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
		<a href={appPath('/library')}>← Back to library</a>
		<h1>Create library</h1>
		<p class="scope-note">Experimental media catalog. You do not need a library to use the article reader.</p>
	{#if error}<p id="form-error" role="alert">{error}</p>{/if}
	<form novalidate onsubmit={submit} aria-describedby={error ? 'form-error' : undefined}>
		<label>Name <input name="name" bind:value={name} aria-required="true" disabled={pending} /></label>
		<label>Source language <select name="source_language" bind:value={sourceLanguage} aria-required="true" disabled={pending}><option value="nl">Dutch (nl)</option></select></label>
		<label>Target language <select name="target_language" bind:value={targetLanguage} aria-required="true" disabled={pending}><option value="en">English (en)</option></select></label>
		<label>Description (optional) <textarea name="description" bind:value={description} disabled={pending}></textarea></label>
		<button type="submit" disabled={pending || !ready}>{pending ? 'Creating…' : 'Create library'}</button>
	</form>
</div>

<style>
	.new-library { max-width: 36rem; margin: 0 auto; }
	form { display: grid; gap: .85rem; }
	label { display: grid; gap: .3rem; font-weight: 600; }
	input, select, textarea, button { font: inherit; padding: .5rem; }
	textarea { min-height: 5rem; }
	.scope-note { color: var(--color-muted); }
	[role='alert'] { padding: .7rem; border-radius: .45rem; background: var(--color-danger-bg); color: var(--color-danger); }
</style>
