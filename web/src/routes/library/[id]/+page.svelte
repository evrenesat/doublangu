<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { goto } from '$app/navigation';
	import { appPath } from '$lib/paths';
	import {
		getLibrary,
		listWorks,
		listEditions,
		listChapters,
		deleteLibrary,
		deleteWork,
		deleteEdition,
		deleteChapter,
		DoublanguAPIError,
		DoublanguNetworkError,
		type Library,
		type Work,
		type Edition,
		type Chapter
	} from '$lib/api/client';

	let library = $state<Library | null>(null);
	let works = $state<Work[]>([]);
	let loading = $state(true);
	let error = $state('');

	// Expanded item tracking
	let expandedWork = $state<string | null>(null);
	let expandedEdition = $state<string | null>(null);

	// Edition/Chapter cache
	let editionsByWork = $state<Map<string, Edition[]>>(new Map());
	let chaptersByEdition = $state<Map<string, Chapter[]>>(new Map());
	let loadingEditions = $state<Set<string>>(new Set());
	let loadingChapters = $state<Set<string>>(new Set());
	let editionErrors = $state<Map<string, string>>(new Map());
	let chapterErrors = $state<Map<string, string>>(new Map());

	const libraryId = $derived($page.params.id ?? '');

	onMount(() => {
		if (!libraryId) {
			error = 'Invalid library ID.';
			loading = false;
			return;
		}
		void loadLibrary();
	});

	async function loadLibrary() {
		loading = true;
		error = '';
		try {
			library = await getLibrary(libraryId);
			works = await listWorks(libraryId);
		} catch (e) {
			if (e instanceof DoublanguAPIError) {
				if (e.status === 404) {
					error = 'Library not found.';
				} else if (e.status === 401) {
					error = 'Please sign in to view libraries.';
					void goto(appPath('/login'));
					return;
				} else {
					error = e.message;
				}
			} else if (e instanceof DoublanguNetworkError) {
				error = 'Could not reach the server. Check your connection.';
			} else {
				error = 'An unexpected error occurred.';
			}
		} finally {
			loading = false;
		}
	}

	async function toggleWork(workId: string) {
		if (expandedWork === workId) {
			expandedWork = null;
			expandedEdition = null;
			return;
		}
		expandedWork = workId;
		expandedEdition = null;

		if (!editionsByWork.has(workId)) await loadEditions(workId);
	}

	async function toggleEdition(editionId: string) {
		if (expandedEdition === editionId) {
			expandedEdition = null;
			return;
		}
		expandedEdition = editionId;

		if (!chaptersByEdition.has(editionId)) await loadChapters(editionId);
	}

	function nodeError(error: unknown, kind: string): string {
		if (error instanceof DoublanguAPIError) return error.message;
		if (error instanceof DoublanguNetworkError) return `Could not load ${kind}. Check your connection.`;
		return `Could not load ${kind}.`;
	}

	async function loadEditions(workId: string) {
		loadingEditions.add(workId);
		loadingEditions = new Set(loadingEditions);
		editionErrors.delete(workId);
		editionErrors = new Map(editionErrors);
		try {
			editionsByWork.set(workId, await listEditions(workId));
			editionsByWork = new Map(editionsByWork);
		} catch (err) {
			editionErrors.set(workId, nodeError(err, 'editions'));
			editionErrors = new Map(editionErrors);
		} finally {
			loadingEditions.delete(workId);
			loadingEditions = new Set(loadingEditions);
		}
	}

	async function loadChapters(editionId: string) {
		loadingChapters.add(editionId);
		loadingChapters = new Set(loadingChapters);
		chapterErrors.delete(editionId);
		chapterErrors = new Map(chapterErrors);
		try {
			chaptersByEdition.set(editionId, await listChapters(editionId));
			chaptersByEdition = new Map(chaptersByEdition);
		} catch (err) {
			chapterErrors.set(editionId, nodeError(err, 'chapters'));
			chapterErrors = new Map(chapterErrors);
		} finally {
			loadingChapters.delete(editionId);
			loadingChapters = new Set(loadingChapters);
		}
	}

	function getEditions(workId: string): Edition[] {
		return editionsByWork.get(workId) ?? [];
	}

	function getChapters(editionId: string): Chapter[] {
		return chaptersByEdition.get(editionId) ?? [];
	}

	async function handleDeleteLibrary() {
		if (!confirm('Delete this library and all its works?')) return;
		try {
			await deleteLibrary(libraryId);
			void goto(appPath('/library'));
		} catch (e) {
			if (e instanceof DoublanguAPIError) {
				error = e.message;
			} else {
				error = 'Failed to delete library.';
			}
		}
	}

	async function handleDeleteWork(workId: string, e: Event) {
		e.stopPropagation();
		if (!confirm('Delete this work?')) return;
		try {
		await deleteWork(workId);
			works = works.filter((w) => w.id !== workId);
			editionsByWork.delete(workId);
			editionsByWork = new Map(editionsByWork);
		} catch (err) {
			error = err instanceof DoublanguAPIError ? err.message : 'Failed to delete work.';
		}
	}

	async function handleDeleteEdition(workId: string, editionId: string, e: Event) {
		e.stopPropagation();
		if (!confirm('Delete this edition?')) return;
		try {
		await deleteEdition(editionId);
			const eds = editionsByWork.get(workId) ?? [];
			editionsByWork.set(workId, eds.filter((ed) => ed.id !== editionId));
			editionsByWork = new Map(editionsByWork);
			chaptersByEdition.delete(editionId);
			chaptersByEdition = new Map(chaptersByEdition);
		} catch (err) {
			error = err instanceof DoublanguAPIError ? err.message : 'Failed to delete edition.';
		}
	}

	async function handleDeleteChapter(editionId: string, chapterId: string, e: Event) {
		e.stopPropagation();
		if (!confirm('Delete this chapter?')) return;
		try {
		await deleteChapter(chapterId);
			const chs = chaptersByEdition.get(editionId) ?? [];
			chaptersByEdition.set(editionId, chs.filter((c) => c.id !== chapterId));
			chaptersByEdition = new Map(chaptersByEdition);
		} catch (err) {
			error = err instanceof DoublanguAPIError ? err.message : 'Failed to delete chapter.';
		}
	}

	function formatDate(iso: string): string {
		return new Date(iso).toLocaleDateString();
	}

	function panelID(kind: string, id: string): string {
		return `${kind}-${encodeURIComponent(id)}`;
	}
</script>

<svelte:head>
	<title>{library?.name ?? 'Library'} — Doublangu</title>
</svelte:head>

<div class="detail-page">
	<a href={appPath('/library')} class="back-link">← Back to library</a>

	{#if loading}
		<p class="status" role="status">Loading library…</p>
	{:else if error}
		<div class="error" role="alert">
			<p>{error}</p>
			<button onclick={() => void loadLibrary()}>Retry</button>
		</div>
	{:else if library}
		<div class="library-header">
			<div>
				<h1>{library.name}</h1>
				<p class="meta">
					{library.source_language} → {library.target_language}
					{#if library.description} · {library.description}{/if}
				</p>
				<p class="dates">Created {formatDate(library.created_at)} · Updated {formatDate(library.updated_at)}</p>
			</div>
			<button class="delete-btn" onclick={() => void handleDeleteLibrary()}>Delete library</button>
		</div>

		<section class="works-section">
			<h2>Works</h2>
			{#if works.length === 0}
				<p class="empty-hint">No works in this library yet.</p>
			{:else}
				<ul class="work-list" role="list">
					{#each works as work (work.id)}
						<li class="work-item" role="listitem">
							<button
								class="work-toggle"
								aria-expanded={expandedWork === work.id}
								aria-controls={panelID('editions', work.id)}
								onclick={() => void toggleWork(work.id)}
							>
								<span class="work-title">{work.title}</span>
								<span class="work-meta">{work.kind}{#if work.author} · by {work.author}{/if}</span>
							</button>
							<button
								class="delete-icon-btn"
								aria-label="Delete work {work.title}"
								onclick={(e) => void handleDeleteWork(work.id, e)}
							>×</button>

							{#if expandedWork === work.id}
								<div class="editions-panel" id={panelID('editions', work.id)}>
									<h3>Editions</h3>
									{#if loadingEditions.has(work.id)}
										<p class="status" role="status">Loading editions…</p>
									{:else if editionErrors.has(work.id)}
										<div class="node-error" role="alert">
											<p>{editionErrors.get(work.id)}</p>
											<button onclick={() => void loadEditions(work.id)}>Retry loading editions</button>
										</div>
									{:else if getEditions(work.id).length === 0}
										<p class="empty-hint">No editions.</p>
									{:else}
										<ul class="edition-list" role="list">
											{#each getEditions(work.id) as ed (ed.id)}
												<li class="edition-item" role="listitem">
													<button
														class="edition-toggle"
														aria-expanded={expandedEdition === ed.id}
														aria-controls={panelID('chapters', ed.id)}
														onclick={() => void toggleEdition(ed.id)}
													>
														<span class="edition-name">{ed.name}</span>
														<span class="edition-meta">{ed.language} · {ed.format}</span>
													</button>
													<button
														class="delete-icon-btn"
														aria-label="Delete edition {ed.name}"
														onclick={(e) => void handleDeleteEdition(work.id, ed.id, e)}
													>×</button>

													{#if expandedEdition === ed.id}
														<div class="chapters-panel" id={panelID('chapters', ed.id)}>
															<h4>Chapters</h4>
															{#if loadingChapters.has(ed.id)}
																<p class="status" role="status">Loading chapters…</p>
															{:else if chapterErrors.has(ed.id)}
																<div class="node-error" role="alert">
																	<p>{chapterErrors.get(ed.id)}</p>
																	<button onclick={() => void loadChapters(ed.id)}>Retry loading chapters</button>
																</div>
															{:else if getChapters(ed.id).length === 0}
																<p class="empty-hint">No chapters.</p>
															{:else}
																<ul class="chapter-list" role="list">
																	{#each getChapters(ed.id) as ch (ch.id)}
																		<li class="chapter-item" role="listitem">
																			<span class="chapter-num">Ch. {ch.chapter_number}</span>
																			<span class="chapter-title">{ch.title}</span>
																			<span class="chapter-time">{ch.start_ms}–{ch.end_ms} ms ({ch.duration_ms} ms)</span>
																			<button
																				class="delete-icon-btn"
																				aria-label="Delete chapter {ch.title}"
																				onclick={(e) => void handleDeleteChapter(ed.id, ch.id, e)}
																			>×</button>
																		</li>
																	{/each}
																</ul>
															{/if}
														</div>
													{/if}
												</li>
											{/each}
										</ul>
									{/if}
								</div>
							{/if}
						</li>
					{/each}
				</ul>
			{/if}
		</section>
	{:else}
		<p class="status">Library data unavailable.</p>
	{/if}
</div>

<style>
	.detail-page {
		max-width: 48rem;
		margin: 0 auto;
	}

	.back-link {
		display: inline-block;
		margin-bottom: 1rem;
		color: var(--color-accent, #2563eb);
		text-decoration: none;
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

	.library-header {
		display: flex;
		justify-content: space-between;
		align-items: flex-start;
		margin-bottom: 1.5rem;
	}

	.library-header h1 {
		margin: 0 0 0.25rem;
	}

	.meta {
		color: var(--color-muted, #666);
		margin: 0 0 0.25rem;
	}

	.dates {
		font-size: 0.8rem;
		color: var(--color-muted, #999);
		margin: 0;
	}

	.delete-btn {
		padding: 0.4rem 0.8rem;
		background: #dc2626;
		color: #fff;
		border: none;
		border-radius: 4px;
		cursor: pointer;
		font-size: 0.85rem;
	}

	.works-section h2 {
		margin-bottom: 0.75rem;
	}

	.empty-hint {
		color: var(--color-muted, #999);
		font-style: italic;
	}

	.work-list,
	.edition-list,
	.chapter-list {
		list-style: none;
		padding: 0;
		margin: 0;
	}

	.work-item,
	.edition-item,
	.chapter-item {
		border: 1px solid var(--color-border, #ddd);
		border-radius: 6px;
		margin-bottom: 0.5rem;
	}

	.work-toggle,
	.edition-toggle {
		display: flex;
		flex-direction: column;
		gap: 0.15rem;
		width: calc(100% - 2.5rem);
		padding: 0.6rem 0.8rem;
		background: none;
		border: none;
		text-align: left;
		cursor: pointer;
		font: inherit;
	}

	.work-toggle:focus,
	.edition-toggle:focus {
		outline: 2px solid var(--color-accent, #2563eb);
		outline-offset: -2px;
		border-radius: 5px;
	}

	.work-title {
		font-weight: 600;
	}

	.work-meta,
	.edition-meta {
		font-size: 0.8rem;
		color: var(--color-muted, #666);
	}

	.edition-name {
		font-weight: 500;
	}

	.delete-icon-btn {
		float: right;
		margin: 0.5rem 0.5rem 0 0;
		padding: 0.15rem 0.4rem;
		background: none;
		border: 1px solid var(--color-border, #ccc);
		border-radius: 3px;
		font-size: 1rem;
		cursor: pointer;
		color: var(--color-muted, #999);
	}

	.delete-icon-btn:hover {
		background: #fef2f2;
		color: #dc2626;
		border-color: #dc2626;
	}

	.editions-panel,
	.chapters-panel {
		padding: 0.5rem 0.8rem;
		border-top: 1px solid var(--color-border, #eee);
	}

	.editions-panel h3,
	.chapters-panel h4 {
		margin: 0 0 0.5rem;
		font-size: 0.9rem;
	}

	.chapter-item {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.5rem;
		padding: 0.4rem 0.6rem;
		font-size: 0.85rem;
	}

	.chapter-num {
		font-weight: 600;
		min-width: 3rem;
	}

	.chapter-title {
		flex: 1;
	}

	.chapter-time {
		color: var(--color-muted, #999);
		font-size: 0.8rem;
	}
</style>
