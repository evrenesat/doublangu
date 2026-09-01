<script lang="ts">
	import { onMount } from 'svelte';
	import { appPath } from '$lib/paths';
	import {
		DoublanguAPIError,
		DoublanguNetworkError,
		listArticles,
		type ArticleSummary
	} from '$lib/api/client';

	let articles = $state<ArticleSummary[]>([]);
	let loading = $state(true);
	let error = $state('');

	onMount(() => {
		void loadArticles();
	});

	async function loadArticles() {
		loading = true;
		error = '';
		try {
			articles = await listArticles();
		} catch (cause) {
			if (cause instanceof DoublanguAPIError) error = cause.message;
			else if (cause instanceof DoublanguNetworkError) error = 'Could not reach the server. Check your connection.';
			else error = 'Could not load reader articles.';
		} finally {
			loading = false;
		}
	}

	function articleUrl(id: string): string {
		return appPath(`/reader/${encodeURIComponent(id)}`);
	}

	function statusLabel(article: ArticleSummary): string {
		if (article.analysis_status === 'queued' || article.analysis_status === 'processing' || article.analysis_status === 'needs_analysis') return 'Preparing English hints…';
		if (article.analysis_status === 'failed') return 'Needs analysis retry';
		if (article.narration_status === 'queued' || article.narration_status === 'generating' || article.narration_status === 'partial') return 'Speech preparing…';
		if (article.narration_status === 'failed') return 'Speech needs retry';
		if (article.analysis_status === 'ready') return 'Ready to read';
		if (article.enrichment_status === 'ready') return 'Ready to read';
		if (article.enrichment_status === 'processing') return 'Enriching…';
		if (article.enrichment_status === 'failed') return 'Needs retry';
		return 'Draft';
	}
</script>

<svelte:head>
	<title>Reader — Doublangu</title>
</svelte:head>

<div class="reader-list-page">
	<div class="page-heading">
		<div>
			<h1>Reader</h1>
			<p>Read Dutch articles with quiet English hints when you need them.</p>
		</div>
		<a class="button" href={appPath('/reader/new')}>Paste an article</a>
	</div>

	{#if loading}
		<p class="status" role="status">Loading articles…</p>
	{:else if error}
		<div class="error" role="alert">
			<p>{error}</p>
			<button type="button" onclick={() => void loadArticles()}>Retry</button>
		</div>
	{:else if articles.length === 0}
		<div class="empty">
			<p>No articles yet.</p>
			<a class="button" href={appPath('/reader/new')}>Paste your first article</a>
		</div>
	{:else}
		<ul class="article-list" role="list">
			{#each articles as article (article.id)}
				<li>
					<a class="article-link" href={articleUrl(article.id)}>
						<strong>{article.title}</strong>
						<span>{article.source_language} → {article.target_language}</span>
						<span class="article-status">{statusLabel(article)}</span>
					</a>
				</li>
			{/each}
		</ul>
	{/if}
</div>

<style>
	.reader-list-page {
		max-width: 52rem;
		margin: 0 auto;
	}

	.page-heading {
		display: flex;
		align-items: end;
		justify-content: space-between;
		gap: 1rem;
		margin-bottom: 1.5rem;
	}

	h1 { margin-bottom: 0.35rem; }
	.page-heading p { margin: 0; color: var(--color-muted, #64748b); }

	.button {
		display: inline-block;
		padding: 0.55rem 0.8rem;
		border-radius: 0.45rem;
		background: var(--color-accent, #2563eb);
		color: #fff;
		font-weight: 600;
		text-decoration: none;
		white-space: nowrap;
	}

	.status { color: var(--color-muted, #64748b); }

	.error {
		padding: 1rem;
		border-radius: 0.5rem;
		background: #fee2e2;
		color: #991b1b;
	}

	.error button { padding: 0.4rem 0.7rem; font: inherit; }

	.empty {
		padding: 2rem;
		border: 2px dashed var(--color-border, #cbd5e1);
		border-radius: 0.6rem;
		text-align: center;
		color: var(--color-muted, #64748b);
	}

	.article-list {
		display: grid;
		gap: 0.65rem;
		padding: 0;
		margin: 0;
		list-style: none;
	}

	.article-link {
		display: grid;
		grid-template-columns: minmax(0, 1fr) auto auto;
		align-items: center;
		gap: 0.8rem;
		padding: 0.9rem 1rem;
		border: 1px solid var(--color-border, #cbd5e1);
		border-radius: 0.55rem;
		color: inherit;
		text-decoration: none;
	}

	.article-link:hover,
	.article-link:focus-visible {
		outline: 2px solid var(--color-accent, #2563eb);
		outline-offset: 1px;
		background: var(--color-surface-hover, #f0f4ff);
	}

	.article-link span { color: var(--color-muted, #64748b); font-size: 0.85rem; }
	.article-status { text-align: right; }

	@media (max-width: 600px) {
		.page-heading { align-items: start; flex-direction: column; }
		.article-link { grid-template-columns: 1fr auto; }
		.article-link span:nth-child(2) { grid-column: 1; }
		.article-status { grid-column: 2; grid-row: 1 / span 2; }
	}
</style>
