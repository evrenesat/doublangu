<script lang="ts">
	import { onMount } from 'svelte';
	import {
		DoublanguAPIError,
		DoublanguNetworkError,
		listAnalysisRuns,
		type AnalysisRunBinding,
		type AnalysisRunSummary
	} from '$lib/api/client';
	import { appPath } from '$lib/paths';

	const PAGE_SIZE = 25;

	let runs = $state<AnalysisRunSummary[]>([]);
	let nextCursor = $state('');
	let loading = $state(true);
	let loadingMore = $state(false);
	let error = $state('');

	onMount(() => {
		void loadRuns();
	});

	async function loadRuns() {
		loading = true;
		error = '';
		try {
			const page = await listAnalysisRuns({ limit: PAGE_SIZE });
			runs = page.runs;
			nextCursor = page.next_cursor ?? '';
		} catch (cause) {
			error = errorMessage(cause, 'Analysis history is unavailable.');
		} finally {
			loading = false;
		}
	}

	/** Append the next page; previously loaded runs must stay visible. */
	async function loadMore() {
		if (!nextCursor || loadingMore) return;
		loadingMore = true;
		error = '';
		try {
			const page = await listAnalysisRuns({ limit: PAGE_SIZE, cursor: nextCursor });
			runs = [...runs, ...page.runs];
			nextCursor = page.next_cursor ?? '';
		} catch (cause) {
			error = errorMessage(cause, 'Could not load more analysis runs.');
		} finally {
			loadingMore = false;
		}
	}

	function statusLabel(status: string): string {
		if (status === 'succeeded') return 'Succeeded';
		if (status === 'failed') return 'Failed';
		return 'Running';
	}

	/** Provenance line: profile plus both compact bindings for pipeline runs, legacy model/effort otherwise. */
	function runProvenance(run: AnalysisRunSummary): string {
		const bindings: AnalysisRunBinding[] = run.bindings ?? [];
		if (bindings.length > 0) {
			const name = run.profile_name || 'Pipeline profile';
			const compact = bindings.map((binding) => `${binding.provider_id} · ${binding.model_id}`).join(' · ');
			return `${name} · ${compact}`;
		}
		return `${run.requested_model || 'No model'} · ${run.requested_effort}`;
	}

	function statusClass(status: string): string {
		if (status === 'succeeded') return 'status-ok';
		if (status === 'failed') return 'status-warn';
		return 'status-unknown';
	}

	function formatDuration(ms: number): string {
		if (!ms || ms < 1000) return `${ms ?? 0} ms`;
		const seconds = ms / 1000;
		if (seconds < 60) return `${seconds.toFixed(1)} s`;
		const minutes = Math.floor(seconds / 60);
		const rest = Math.round(seconds % 60);
		return `${minutes} m ${String(rest).padStart(2, '0')} s`;
	}

	function formatStarted(iso: string): string {
		if (!iso) return '—';
		const date = new Date(iso);
		if (Number.isNaN(date.getTime())) return iso;
		return date.toLocaleString(undefined, { month: 'short', day: 'numeric', hour: '2-digit', minute: '2-digit' });
	}

	function errorMessage(cause: unknown, fallback: string): string {
		if (cause instanceof DoublanguAPIError) return cause.message;
		if (cause instanceof DoublanguNetworkError) return 'Could not reach the server. Check your connection.';
		if (cause instanceof Error) return cause.message;
		return fallback;
	}
</script>

<svelte:head>
	<title>Analysis runs — Doublangu</title>
</svelte:head>

<div class="runs-page">
	<div class="page-heading">
		<h1>Analysis runs</h1>
		<p>Analysis jobs are retained for troubleshooting. Open a run to inspect providers, timing, outputs, and failures.</p>
	</div>

	{#if loading}
		<p class="status" role="status">Loading analysis runs…</p>
	{:else if error && runs.length === 0}
		<div class="error-box" role="alert">
			<p>{error}</p>
			<button type="button" class="secondary" onclick={() => void loadRuns()}>Retry</button>
		</div>
	{:else if runs.length === 0}
		<p class="muted">No analysis runs have been recorded yet.</p>
	{:else}
		<table class="run-table">
			<thead>
				<tr>
					<th scope="col">Status</th>
					<th scope="col">Article</th>
					<th scope="col">Profile / models</th>
					<th scope="col" class="num">Progress</th>
					<th scope="col" class="num">Duration</th>
					<th scope="col">Started</th>
				</tr>
			</thead>
			<tbody>
				{#each runs as run (run.id)}
					<tr>
						<td data-label="Status">
							<span class={statusClass(run.status)}>{statusLabel(run.status)}</span>
							{#if run.error_code}<span class="error-code">{run.error_code}</span>{/if}
						</td>
						<td data-label="Article">
							<a href={appPath(`/analysis-runs/${encodeURIComponent(run.id)}`)}>{run.article_title}</a>
						</td>
						<td data-label="Profile / models">{runProvenance(run)}</td>
						<td data-label="Progress" class="num">
							{run.completed_paragraphs} / {run.total_paragraphs}
							{#if run.failed_block_index >= 0}
								<span class="failed-note">failed at paragraph {run.failed_block_index + 1}</span>
							{/if}
						</td>
						<td data-label="Duration" class="num">{formatDuration(run.duration_ms)}</td>
						<td data-label="Started">{formatStarted(run.started_at)}</td>
					</tr>
				{/each}
			</tbody>
		</table>
		{#if error}
			<p class="error-text" role="alert">{error}</p>
		{/if}
		{#if nextCursor}
			<button type="button" class="secondary load-more" disabled={loadingMore} onclick={() => void loadMore()}>
				{loadingMore ? 'Loading…' : 'Load more'}
			</button>
		{/if}
	{/if}
</div>

<style>
	.runs-page {
		max-width: 66rem;
		margin: 0 auto;
	}

	.page-heading h1 {
		margin-bottom: 0.35rem;
	}

	.page-heading p {
		margin: 0 0 1.4rem;
		color: var(--color-muted);
		max-width: 44rem;
	}

	.status {
		color: var(--color-muted);
	}

	.muted {
		color: var(--color-muted);
	}

	.error-text {
		color: var(--color-danger);
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
		border: 1px solid var(--color-border);
		border-radius: 0.5rem;
		padding: 0.5rem 0.75rem;
		cursor: pointer;
		font: inherit;
		background: var(--color-surface-raised);
		color: var(--color-text);
	}

	button:disabled {
		opacity: 0.5;
		cursor: not-allowed;
	}

	.load-more {
		margin-top: 0.9rem;
	}

	.run-table {
		width: 100%;
		border-collapse: collapse;
		font-size: 0.92rem;
	}

	.run-table th {
		text-align: left;
		padding: 0.45rem 0.6rem;
		border-bottom: 1px solid var(--color-border);
		color: var(--color-muted);
		font-size: 0.82rem;
		text-transform: uppercase;
		letter-spacing: 0.05em;
	}

	.run-table td {
		padding: 0.55rem 0.6rem;
		border-bottom: 1px solid var(--color-border);
		vertical-align: top;
	}

	.run-table tbody tr:hover {
		background: var(--color-surface-hover);
	}

	.run-table a {
		color: var(--color-text);
		font-weight: 650;
	}

	.num {
		text-align: right;
		white-space: nowrap;
	}

	.status-ok {
		color: #7ee2a8;
		font-weight: 650;
	}

	.status-warn {
		color: var(--color-danger);
		font-weight: 650;
	}

	.status-unknown {
		color: var(--color-warning);
		font-weight: 650;
	}

	.error-code {
		display: block;
		color: var(--color-muted);
		font-size: 0.82rem;
		overflow-wrap: anywhere;
	}

	.failed-note {
		display: block;
		color: var(--color-danger);
		font-size: 0.82rem;
	}

	@media (max-width: 720px) {
		.run-table thead {
			display: none;
		}

		.run-table,
		.run-table tbody,
		.run-table tr,
		.run-table td {
			display: block;
		}

		.run-table tbody tr {
			padding: 0.6rem 0.7rem;
			margin-bottom: 0.55rem;
			border: 1px solid var(--color-border);
			border-radius: 0.55rem;
		}

		.run-table td {
			padding: 0.14rem 0;
			border-bottom: 0;
		}

		.run-table td::before {
			content: attr(data-label) ': ';
			color: var(--color-muted);
			font-weight: 650;
		}

		.num {
			text-align: left;
		}
	}
</style>
