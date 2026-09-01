	<script lang="ts">
	import { onMount } from 'svelte';
	import { page } from '$app/stores';
	import { DoublanguAPIError, DoublanguNetworkError, getAnalysisRun, type AnalysisRun } from '$lib/api/client';
	import { appPath } from '$lib/paths';

	let run = $state<AnalysisRun | null>(null);
	let loading = $state(true);
	let error = $state('');
	const runID = $derived($page.params.id ?? '');

	onMount(() => {
		if (!runID) {
			error = 'Invalid analysis run ID.';
			loading = false;
			return;
		}
		void loadRun();
	});

	async function loadRun() {
		loading = true;
		error = '';
		try {
			run = await getAnalysisRun(runID);
		} catch (cause) {
			if (cause instanceof DoublanguAPIError && cause.status === 404) error = 'Analysis run not found.';
			else if (cause instanceof DoublanguNetworkError) error = 'Could not reach the server. Check your connection.';
			else if (cause instanceof Error) error = cause.message;
			else error = 'Could not load the analysis run.';
		} finally {
			loading = false;
		}
	}

	function statusLabel(status: string): string {
		if (status === 'succeeded') return 'Succeeded';
		if (status === 'failed') return 'Failed';
		return 'Running';
	}

	function statusClass(status: string): string {
		if (status === 'succeeded') return 'ok';
		if (status === 'failed') return 'failed';
		return 'running';
	}
</script>

<svelte:head>
	<title>{run?.article_title ?? 'Analysis run'} — Doublangu</title>
</svelte:head>

<div class="run-page">
	<a class="back-link" href={appPath('/settings')}>← Back to settings</a>
	{#if loading}
		<p class="status" role="status">Loading analysis run…</p>
	{:else if error}
		<div class="error" role="alert">
			<p>{error}</p>
			<button type="button" onclick={() => void loadRun()}>Retry</button>
		</div>
	{:else if run}
		<header class="page-heading">
			<div>
				<p class="eyebrow">Analysis run</p>
				<h1>{run.article_title}</h1>
				<p class="muted"><a href={appPath(`/reader/${encodeURIComponent(run.article_id)}`)}>Open article</a></p>
			</div>
			<span class={`status-badge ${statusClass(run.status)}`}>{statusLabel(run.status)}</span>
		</header>

		<section class="panel" aria-labelledby="summary-heading">
			<h2 id="summary-heading">Run summary</h2>
			<dl class="summary-grid">
				<div><dt>Requested model</dt><dd>{run.requested_model || 'No model selected'}</dd></div>
				<div><dt>Reasoning effort</dt><dd>{run.requested_effort}</dd></div>
				<div><dt>Reported model</dt><dd>{run.reported_model || '—'}</dd></div>
				<div><dt>Provider</dt><dd>{run.provider_id}</dd></div>
				<div><dt>CLI version</dt><dd>{run.codex_cli_version || '—'}</dd></div>
				<div><dt>Progress</dt><dd>{run.completed_paragraphs}/{run.total_paragraphs} paragraphs{run.failed_block_index >= 0 ? ` · failed at paragraph ${run.failed_block_index + 1}` : ''}</dd></div>
				<div><dt>Duration</dt><dd>{run.duration_ms} ms</dd></div>
				<div><dt>Started</dt><dd>{run.started_at}</dd></div>
				<div><dt>Completed</dt><dd>{run.completed_at || '—'}</dd></div>
				<div><dt>Contract</dt><dd>{run.contract_version}</dd></div>
				<div><dt>Prompt</dt><dd>{run.prompt_version}</dd></div>
			</dl>
			{#if run.error_code}
				<div class="failure-detail">
					<strong>{run.error_code}</strong>
					{#if run.error_detail}<pre>{run.error_detail}</pre>{/if}
				</div>
			{/if}
		</section>

		<section class="panel" aria-labelledby="turns-heading">
			<h2 id="turns-heading">Turn artifacts</h2>
			{#if run.turns.length === 0}
				<p class="muted">No provider turns were needed; the accepted result came from the exact cache.</p>
			{:else}
				<div class="turn-list">
					{#each run.turns as turn (turn.id)}
						<details class="turn" open={turn.status === 'failed'}>
							<summary>
								<span>Paragraph {turn.block_index} · {turn.turn_kind}</span>
								<span class={`turn-status ${turn.status === 'failed' ? 'failed' : 'ok'}`}>{turn.status}</span>
							</summary>
							<div class="turn-content">
								<p class="muted">Started {turn.started_at} · {turn.duration_ms} ms</p>
								<h3>Prompt</h3>
								<pre>{turn.prompt}</pre>
								<h3>Output schema</h3>
								<pre>{turn.output_schema}</pre>
								<h3>Completed response</h3>
								<pre>{turn.completed_response || '—'}</pre>
								{#if turn.validation_error}<h3>Validation error</h3><pre>{turn.validation_error}</pre>{/if}
								{#if turn.provider_error}<h3>Provider error</h3><pre>{turn.provider_error}</pre>{/if}
								<h3>Completion metadata</h3>
								<pre>{turn.completion_metadata_json}</pre>
								{#if turn.provider_stderr_excerpt}<h3>Provider stderr excerpt</h3><pre>{turn.provider_stderr_excerpt}</pre>{/if}
							</div>
						</details>
					{/each}
				</div>
				{/if}
			</section>
		{#if run.stderr_excerpt}
			<section class="panel" aria-labelledby="stderr-heading">
				<h2 id="stderr-heading">Provider stderr excerpt</h2>
				<pre>{run.stderr_excerpt}</pre>
			</section>
		{/if}
		{/if}
	</div>

<style>
	.run-page { max-width: 66rem; margin: 0 auto; }
	.back-link { display: inline-block; margin-bottom: 1.2rem; }
	.page-heading { display: flex; align-items: start; justify-content: space-between; gap: 1rem; margin-bottom: 1.25rem; }
	.eyebrow { margin: 0; color: var(--color-muted); font-size: 0.8rem; text-transform: uppercase; letter-spacing: 0.08em; }
	h1 { margin: 0.15rem 0 0.25rem; }
	h2 { margin-bottom: 0.8rem; }
	h3 { margin: 1rem 0 0.35rem; font-size: 0.9rem; }
	.muted, .status { color: var(--color-muted); }
	.panel { padding: 1.25rem; margin-bottom: 1.25rem; border: 1px solid var(--color-border); border-radius: 0.75rem; background: var(--color-surface); }
	.summary-grid { display: grid; grid-template-columns: repeat(2, minmax(0, 1fr)); gap: 0.8rem 1.5rem; margin: 0; }
	.summary-grid div { min-width: 0; }
	.summary-grid dt { color: var(--color-muted); font-size: 0.82rem; }
	.summary-grid dd { margin: 0.15rem 0 0; overflow-wrap: anywhere; }
	.status-badge, .turn-status { font-weight: 700; }
	.status-badge { padding: 0.35rem 0.6rem; border-radius: 999px; background: var(--color-surface-raised); }
	.ok { color: #7ee2a8; }
	.failed { color: var(--color-danger); }
	.running { color: var(--color-warning); }
	.failure-detail { margin-top: 1rem; padding: 0.8rem; background: var(--color-danger-bg); border-radius: 0.5rem; color: var(--color-danger); }
	.failure-detail pre { color: inherit; }
	.turn-list { display: grid; gap: 0.7rem; }
	.turn { border: 1px solid var(--color-border); border-radius: 0.55rem; overflow: hidden; }
	.turn summary { display: flex; justify-content: space-between; gap: 1rem; padding: 0.75rem 0.9rem; cursor: pointer; background: var(--color-surface-raised); }
	.turn-content { padding: 0 0.9rem 1rem; }
	pre { max-height: 26rem; overflow: auto; margin: 0; padding: 0.75rem; border: 1px solid var(--color-border); border-radius: 0.4rem; background: #10121b; color: #e4e7f2; white-space: pre-wrap; overflow-wrap: anywhere; font: 0.78rem/1.45 ui-monospace, SFMono-Regular, Menlo, monospace; }
	.error { padding: 1rem; background: var(--color-danger-bg); color: var(--color-danger); border-radius: 0.5rem; }
	.error button { padding: 0.4rem 0.7rem; font: inherit; }
	@media (max-width: 600px) { .page-heading { flex-direction: column; } .summary-grid { grid-template-columns: 1fr; } }
</style>
