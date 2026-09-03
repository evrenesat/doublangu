<script lang="ts">
	import { onMount } from 'svelte';
	import {
		DoublanguAPIError,
		DoublanguNetworkError,
		getReaderSettings,
		listAnalysisRuns,
		saveReaderSettings,
		type AnalysisRunBinding,
		type AnalysisRunSummary
	} from '$lib/api/client';
	import { appPath } from '$lib/paths';
	import AnalysisPipelinePanel from '$lib/settings/AnalysisPipelinePanel.svelte';

	let coreReady = $state<boolean | null>(null);
	let loaderReady = $state<boolean | null>(null);
	let schemaAvailable = $state<boolean | null>(null);
	let registryState = $state<string | null>(null);
	let pluginCount = $state<number | null>(null);
	let pluginIds = $state<string[]>([]);

	let analysisError = $state('');
	let runs = $state<AnalysisRunSummary[]>([]);
	let pronounceOnHover = $state(true);
	let pronounceOnHoverLoading = $state(true);
	let readerError = $state('');
	let readerSaving = $state(false);
	let analysisLoading = $state(true);
	let diagnosticsLoading = $state(true);
	let diagnosticsError = $state('');

	onMount(() => {
		void loadDiagnostics();
		void loadRuns();
		void loadReaderSettings();
	});

	async function loadReaderSettings() {
		pronounceOnHoverLoading = true;
		readerError = '';
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

	async function loadDiagnostics() {
		diagnosticsLoading = true;
		diagnosticsError = '';
		try {
			const response = await fetch(appPath('/health'), { credentials: 'same-origin' });
			if (!response.ok) throw new Error(`Server returned ${response.status}`);
			const report = await response.json();
			coreReady = report.core_ready ?? null;
			loaderReady = report.loader_ready ?? null;
			schemaAvailable = report.schema_available ?? null;
			registryState = report.registry_state ?? null;
			pluginCount = report.plugin_count ?? null;
			pluginIds = report.plugin_ids ?? [];
		} catch (cause) {
			diagnosticsError = cause instanceof DoublanguNetworkError
				? 'Could not retrieve server diagnostics.'
				: 'Could not reach the server. Check your connection.';
		} finally {
			diagnosticsLoading = false;
		}
	}

	async function loadRuns() {
		analysisLoading = true;
		analysisError = '';
		try {
			runs = (await listAnalysisRuns({ limit: 10 })).runs;
		} catch (cause) {
			analysisError = errorMessage(cause, 'Analysis history is unavailable.');
		} finally {
			analysisLoading = false;
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

	function serverStatus(value: boolean | null): string {
		if (value === true) return 'Ready';
		if (value === false) return 'Not ready';
		return 'Unknown';
	}

	function serverStatusClass(value: boolean | null): string {
		if (value === true) return 'status-ok';
		if (value === false) return 'status-warn';
		return 'status-unknown';
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

<div class="settings-page">
	<div class="page-heading">
		<div>
			<h1>Settings</h1>
			<p>Choose the provider profile used for new article analysis.</p>
		</div>
		<a href={appPath('/reader')}>Back to articles</a>
	</div>

	<section class="panel recent-runs" aria-labelledby="runs-heading">
		<div class="section-heading">
			<div>
				<h2 id="runs-heading">Recent runs</h2>
				<p class="muted">Each run is isolated per paragraph and retained here for troubleshooting.</p>
			</div>
		</div>
		{#if analysisLoading}
			<p class="status" role="status">Loading recent runs…</p>
		{:else}
			{#if analysisError}<p class="error-text" role="alert">{analysisError}</p>{/if}
			{#if runs.length === 0}
				<p class="muted">No analysis runs have been recorded yet.</p>
			{:else}
				<ul class="run-list" role="list">
					{#each runs as run (run.id)}
						<li>
							<a href={appPath(`/settings/analysis-runs/${encodeURIComponent(run.id)}`)}>
								<strong>{run.article_title}</strong>
								<span>{runProvenance(run)}</span>
								<span class={statusClass(run.status)}>{statusLabel(run.status)} · {run.completed_paragraphs}/{run.total_paragraphs} paragraphs</span>
								<span>{run.duration_ms} ms{run.failed_block_index >= 0 ? ` · failed at paragraph ${run.failed_block_index + 1}` : ''}</span>
								{#if run.error_code}<span>{run.error_code}</span>{/if}
							</a>
						</li>
					{/each}
				</ul>
			{/if}
		{/if}
	</section>

	<AnalysisPipelinePanel />

	<section class="panel reader-settings" aria-labelledby="reader-heading">
		<div class="section-heading">
			<div>
				<h2 id="reader-heading">Reader</h2>
				<p>Settings that follow you across browsers as the owner.</p>
			</div>
		</div>
		{#if pronounceOnHoverLoading}
			<p class="status" role="status">Loading reader preferences…</p>
		{:else}
			<label class="preference-row">
				<input type="checkbox" checked={pronounceOnHover} disabled={readerSaving} onchange={() => void togglePronounceOnHover()} />
				<span>
					<strong>Pronounce on hover</strong>
					<small>Play a word's pronunciation when the pointer hovers it in the reader.</small>
				</span>
			</label>
		{/if}
		{#if readerError}<p class="status error" role="alert">{readerError}</p>{/if}
	</section>

	<section class="panel diagnostics" aria-labelledby="diagnostics-heading">
		<h2 id="diagnostics-heading">Server diagnostics</h2>
		{#if diagnosticsLoading}
			<p class="status" role="status">Loading diagnostics…</p>
		{:else if diagnosticsError}
			<div class="error-box" role="alert">
				<p>{diagnosticsError}</p>
				<button type="button" class="secondary" onclick={() => void loadDiagnostics()}>Retry</button>
			</div>
		{:else}
			<dl class="diag-list">
				<div><dt>Core</dt><dd><span class={serverStatusClass(coreReady)}>{serverStatus(coreReady)}</span></dd></div>
				<div><dt>Plugin loader</dt><dd><span class={serverStatusClass(loaderReady)}>{serverStatus(loaderReady)}</span></dd></div>
				<div><dt>Schema</dt><dd><span class={serverStatusClass(schemaAvailable)}>{serverStatus(schemaAvailable)}</span></dd></div>
				<div><dt>Registry state</dt><dd>{registryState ?? '—'}</dd></div>
				<div><dt>Plugins loaded</dt><dd>{pluginCount ?? '—'}</dd></div>
			</dl>
			{#if pluginIds.length > 0}
				<h3>Loaded plugin IDs</h3>
				<ul class="plugin-id-list" role="list">
					{#each pluginIds as pid (pid)}<li>{pid}</li>{/each}
				</ul>
			{:else}<p class="muted">No plugins are loaded.</p>{/if}
		{/if}
	</section>

	<section class="navigation" aria-labelledby="navigation-heading">
		<h2 id="navigation-heading">More</h2>
		<nav aria-label="Settings navigation">
			<a href={appPath('/library')}>Library</a>
			<a href={appPath('/plugins')}>Plugins</a>
		</nav>
	</section>
</div>

<style>
	.settings-page { max-width: 54rem; margin: 0 auto; }
	.page-heading, .section-heading { display: flex; align-items: start; justify-content: space-between; gap: 1rem; }
	h1 { margin-bottom: 0.35rem; }
	h2 { margin-bottom: 0.55rem; }
	h3 { margin: 1.5rem 0 0.65rem; }
	.page-heading p, .muted { color: var(--color-muted); }
	.page-heading > a { white-space: nowrap; }
	.panel { padding: 1.25rem; margin-bottom: 1.25rem; border: 1px solid var(--color-border); border-radius: 0.75rem; background: var(--color-surface); }
	.section-heading p { margin: 0; }
	.status { color: var(--color-muted); }
	.error-text { color: var(--color-danger); }
	.error-box { padding: 0.85rem; border-radius: 0.55rem; background: var(--color-danger-bg); color: var(--color-danger); }
	.error-box p { margin-bottom: 0.7rem; }
	.secondary { border: 1px solid var(--color-border); border-radius: 0.5rem; padding: 0.5rem 0.75rem; cursor: pointer; background: var(--color-surface-raised); color: var(--color-text); }
	button:disabled { opacity: 0.5; cursor: not-allowed; }
	.run-list { list-style: none; padding: 0; margin: 0; display: grid; gap: 0.55rem; }
	.run-list a { display: grid; gap: 0.15rem; padding: 0.75rem; border: 1px solid var(--color-border); border-radius: 0.5rem; color: inherit; text-decoration: none; }
	.run-list a:hover, .run-list a:focus-visible { background: var(--color-surface-hover); }
	.run-list span { color: var(--color-muted); font-size: 0.88rem; }
	.status-ok { color: #7ee2a8 !important; font-weight: 650; }
	.status-warn { color: var(--color-warning); font-weight: 650; }
	.status-unknown { color: var(--color-muted); }
	.diag-list { margin: 0; }
	.diag-list > div { display: flex; gap: 1rem; padding: 0.5rem 0; border-bottom: 1px solid var(--color-border); }
	.diag-list dt { min-width: 8rem; font-weight: 650; }
	.diag-list dd { margin: 0; }
	.plugin-id-list { padding-left: 1.25rem; font-size: 0.9rem; }
	.navigation nav { display: flex; gap: 1rem; }
	@media (max-width: 600px) { .page-heading, .section-heading { flex-direction: column; } }

	.preference-row { display: flex; align-items: flex-start; gap: 0.6rem; }
	.preference-row small { display: block; color: var(--color-muted, #64748b); }
	.status.error { color: var(--color-danger, #dc2626); }
</style>
