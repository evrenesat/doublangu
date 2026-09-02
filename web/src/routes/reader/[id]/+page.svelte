<script lang="ts">
	import { onDestroy, onMount } from 'svelte';
	import { goto } from '$app/navigation';
	import { page } from '$app/stores';
	import { appPath } from '$lib/paths';
	import {
		DoublanguAPIError,
		DoublanguNetworkError,
		enrichArticle,
		getArticle,
		updateLearningState,
		type Article,
		type ArticleAnnotation,
		type LearningStatus
	} from '$lib/api/client';
	import ArticleReader from '$lib/reader/ArticleReader.svelte';
	import ArticleBlock from '$lib/reader/ArticleBlock.svelte';
	import { enrichmentMessage } from '$lib/reader/enrichment';

	let article = $state<Article | null>(null);
	let loading = $state(true);
	let enriching = $state(false);
	let error = $state('');
	let pollTimer: ReturnType<typeof setTimeout> | undefined;
	let destroyed = false;

	const articleID = $derived($page.params.id ?? '');
	const enrichAfterLoad = $derived($page.url.searchParams.get('enrich') === '1');

	// While analysis is processing, poll every second so paragraph commits
	// appear promptly; while merely queued (or only speech is pending), poll
	// every two seconds. The eight-second delay is reserved for failures.
	function pollDelayFor(next: Article): number {
		return next.analysis_status === 'processing' ? 1000 : 2000;
	}

	onMount(() => {
		if (!articleID) {
			error = 'Invalid article ID.';
			loading = false;
			return;
		}
		const visibility = () => {
			if (document.hidden) clearPoll();
			else if (article && shouldPoll(article)) schedulePoll(0);
		};
		document.addEventListener('visibilitychange', visibility);
		void loadArticle();
		return () => document.removeEventListener('visibilitychange', visibility);
	});

	onDestroy(() => {
		destroyed = true;
		clearPoll();
	});

	function isV2(next: Article): boolean {
		return next.analysis_status !== undefined;
	}

	function shouldPoll(next: Article): boolean {
		if (!isV2(next)) return false;
		const analysisPending = next.analysis_status === 'needs_analysis' || next.analysis_status === 'queued' || next.analysis_status === 'processing';
		const speechPending = next.narration_status === 'queued' || next.narration_status === 'generating' || next.narration_status === 'partial';
		return analysisPending || speechPending;
	}

	function clearPoll(): void {
		if (pollTimer) clearTimeout(pollTimer);
		pollTimer = undefined;
	}

	function schedulePoll(delay: number): void {
		clearPoll();
		if (destroyed || document.hidden || !article || !shouldPoll(article)) return;
		pollTimer = setTimeout(() => {
			pollTimer = undefined;
			void pollArticle();
		}, delay);
	}

	async function pollArticle(): Promise<void> {
		if (destroyed || document.hidden || !article || !shouldPoll(article)) return;
		try {
			const next = await getArticle(articleID);
			if (destroyed) return;
			article = next;
			if (shouldPoll(next)) schedulePoll(pollDelayFor(next));
		} catch (cause) {
			if (!destroyed) {
				error = errorMessage(cause, 'Could not refresh the article.');
				// Back off after request failures only; a later success resets
				// to the state-based cadence above.
				schedulePoll(8000);
			}
		}
	}

	async function loadArticle(): Promise<void> {
		loading = true;
		error = '';
		try {
			const loaded = await getArticle(articleID);
			article = loaded;
			if (!isV2(loaded) && enrichAfterLoad && loaded.enrichment_status !== 'ready') {
				await runLegacyEnrichment();
				await goto(appPath(`/reader/${encodeURIComponent(articleID)}`), { replaceState: true, noScroll: true });
			} else if (shouldPoll(loaded)) {
				schedulePoll(0);
			}
		} catch (cause) {
			handleError(cause);
		} finally {
			loading = false;
		}
	}

	async function runLegacyEnrichment(): Promise<void> {
		enriching = true;
		error = '';
		try {
			article = await enrichArticle(articleID);
		} catch (cause) {
			handleError(cause);
			try {
				article = await getArticle(articleID);
			} catch {
				// Keep the original provider error visible if a refresh also fails.
			}
		} finally {
			enriching = false;
		}
	}

	function handleError(cause: unknown): void {
		if (cause instanceof DoublanguAPIError) {
			if (cause.status === 401) {
				void goto(appPath('/login'));
				return;
			}
			if (cause.status === 404) {
				error = 'Article not found.';
				return;
			}
			if (cause.code.startsWith('v1.enrichment_') || cause.code === 'v1.annotator_unavailable') {
				error = enrichmentMessage(cause.code).detail;
				return;
			}
			error = cause.message;
		} else {
			error = errorMessage(cause, 'Could not load the article.');
		}
	}

	function errorMessage(cause: unknown, fallback: string): string {
		if (cause instanceof DoublanguAPIError) return cause.message;
		if (cause instanceof DoublanguNetworkError) return 'Could not reach the server. Check your connection.';
		if (cause instanceof Error) return cause.message;
		return fallback;
	}

	function updateFromReader(next: Article): void {
		article = next;
		if (shouldPoll(next) && !pollTimer) schedulePoll(0);
	}

	function legacyStatusMessage(): string {
		if (!article) return '';
		if (article.enrichment_status === 'processing') return 'Adding English hints…';
		if (article.enrichment_status === 'failed') return enrichmentMessage(article.enrichment_error_code).title;
		if (article.enrichment_status === 'draft') return 'This article has not been enriched yet.';
		return 'English hints are ready.';
	}

	function replaceAnnotation(annotationID: string, update: (annotation: ArticleAnnotation) => ArticleAnnotation): void {
		if (!article) return;
		article = {
			...article,
			blocks: article.blocks.map((block) => ({
				...block,
				annotations: block.annotations.map((annotation) =>
					annotation.id === annotationID ? update(annotation) : annotation
				)
			}))
		};
	}

	async function setLearningStatus(annotation: ArticleAnnotation, status: LearningStatus): Promise<void> {
		if (!article) return;
		const previous = annotation;
		const optimisticState = {
			source_language: article.source_language,
			kind: annotation.kind,
			learning_key: annotation.learning_key,
			status,
			updated_at: new Date().toISOString()
		};
		replaceAnnotation(annotation.id, (current) => ({
			...current,
			learning_state: optimisticState,
			show_shadow: status !== 'learned'
		}));
		try {
			const saved = await updateLearningState({
				source_language: article.source_language,
				kind: annotation.kind,
				learning_key: annotation.learning_key,
				status
			});
			replaceAnnotation(annotation.id, (current) => ({
				...current,
				learning_state: saved,
				show_shadow: saved.status !== 'learned'
			}));
		} catch (cause) {
			replaceAnnotation(annotation.id, () => previous);
			throw cause;
		}
	}
</script>

<svelte:head>
	<title>{article?.title ?? 'Article'} — Doublangu</title>
</svelte:head>

<div class="article-page">
	<a href={appPath('/reader')} class="back-link">← Back to reader</a>

	{#if loading}
		<p class="status" role="status">Loading article…</p>
	{:else if error && !article}
		<div class="error" role="alert">
			<p>{error}</p>
			<button type="button" onclick={() => void loadArticle()}>Retry</button>
		</div>
	{:else if article}
		<header class="article-header">
			<h1>{article.title}</h1>
			<p class="meta">{article.source_language} → {article.target_language}</p>
		</header>

		{#if isV2(article)}
			<ArticleReader article={article} onArticleChange={updateFromReader} />
		{:else}
			<div class="enrichment-status" class:status-error={article.enrichment_status === 'failed'}>
				<span role={article.enrichment_status === 'processing' ? 'status' : undefined}>{legacyStatusMessage()}</span>
				{#if article.enrichment_status === 'failed' || article.enrichment_status === 'draft'}
					<button type="button" disabled={enriching} onclick={() => void runLegacyEnrichment()}>
						{enriching ? 'Enriching…' : 'Retry enrichment'}
					</button>
				{/if}
				{#if error}
					<span class="status-detail" role="alert">{error}</span>
				{:else if article.enrichment_status === 'failed'}
					<span class="status-detail">{enrichmentMessage(article.enrichment_error_code).detail}</span>
				{/if}
			</div>

			<article class="reading-surface" aria-label={article.title}>
				{#each article.blocks as block (block.id)}
					<ArticleBlock block={block} onLearningStatus={setLearningStatus} />
				{/each}
			</article>
		{/if}
	{/if}
</div>

<style>
	.article-page { max-width: 56rem; margin: 0 auto; }
	.back-link { display: inline-block; margin-bottom: 1.25rem; }
	.status { color: var(--color-muted, #64748b); }
	.error { padding: 1rem; border-radius: 0.5rem; background: var(--color-danger-bg, #351c24); color: var(--color-danger, #ffb4c3); }
	.error button { padding: 0.4rem 0.7rem; font: inherit; }
	.article-header { margin-bottom: 1rem; }
	.article-header h1 { margin: 0 0 0.25rem; font-size: clamp(1.6rem, 1.3rem + 1vw, 2.35rem); }
	.meta { margin: 0; color: var(--color-muted, #64748b); }
	.enrichment-status {
		display: flex;
		align-items: center;
		flex-wrap: wrap;
		gap: 0.6rem;
		margin: 0 0 1.5rem;
		padding: 0.65rem 0.75rem;
		border-radius: 0.45rem;
		background: var(--color-surface, #191c29);
		color: var(--color-muted, #64748b);
		font-size: 0.85rem;
	}
	.enrichment-status.status-error { background: var(--color-warning-bg, #342819); color: var(--color-warning, #ffd18a); }
	.enrichment-status button { padding: 0.35rem 0.6rem; border: 1px solid currentColor; border-radius: 999px; background: transparent; color: inherit; font: inherit; cursor: pointer; }
	.enrichment-status button:disabled { opacity: 0.6; cursor: wait; }
	.status-detail { flex-basis: 100%; color: var(--color-danger, #ffb4c3); }
	.reading-surface { padding: clamp(1rem, 3vw, 2.5rem); border: 1px solid var(--color-border, #353b52); border-radius: 0.8rem; background: var(--color-bg, #11131d); }
	@media (max-width: 420px) { .reading-surface { padding: 0.75rem; } }
</style>
