<script lang="ts">
	import { onMount } from 'svelte';
	import {
		DoublanguAPIError,
		DoublanguNetworkError,
		getAnalysisModels,
		getAnalysisSettings,
		listAnalysisRuns,
		saveAnalysisSettings,
		type AnalysisModel,
		type AnalysisRunSummary
	} from '$lib/api/client';
	import { appPath } from '$lib/paths';

	let coreReady = $state<boolean | null>(null);
	let loaderReady = $state<boolean | null>(null);
	let schemaAvailable = $state<boolean | null>(null);
	let registryState = $state<string | null>(null);
	let pluginCount = $state<number | null>(null);
	let pluginIds = $state<string[]>([]);

	let models = $state<AnalysisModel[]>([]);
	let selectedModel = $state('');
	let selectedEffort = $state('');
	let savedModel = $state('');
	let savedEffort = $state('');
	let catalogRetrievedAt = $state('');
	let catalogStale = $state(false);
	let catalogError = $state('');
	let analysisError = $state('');
	let runs = $state<AnalysisRunSummary[]>([]);
	let saving = $state(false);
	let refreshing = $state(false);
	let analysisLoading = $state(true);
	let diagnosticsLoading = $state(true);
	let diagnosticsError = $state('');

	const selectedModelInfo = $derived(models.find((model) => model.id === selectedModel));
	const efforts = $derived(selectedModelInfo?.supported_reasoning_efforts ?? []);
	const selectionChanged = $derived(selectedModel !== savedModel || selectedEffort !== savedEffort);
	const selectionSupported = $derived(
		Boolean(selectedModelInfo && efforts.some((effort) => effort.value === selectedEffort))
	);

	onMount(() => {
		void loadDiagnostics();
		void loadAnalysisSettings();
	});

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

	async function loadAnalysisSettings() {
		analysisLoading = true;
		analysisError = '';
		const [catalogResult, settingsResult, runsResult] = await Promise.allSettled([
			getAnalysisModels(),
			getAnalysisSettings(),
			listAnalysisRuns({ limit: 10 })
		]);
		if (catalogResult.status === 'fulfilled') {
			models = catalogResult.value.models;
			catalogRetrievedAt = catalogResult.value.retrieved_at;
			catalogStale = catalogResult.value.stale;
			catalogError = catalogResult.value.last_error ?? '';
		} else {
			catalogError = errorMessage(catalogResult.reason, 'Analysis model catalog is unavailable.');
		}
		if (settingsResult.status === 'fulfilled') {
			savedModel = settingsResult.value.model;
			savedEffort = settingsResult.value.effort;
			selectedModel = settingsResult.value.model;
			selectedEffort = settingsResult.value.effort;
		} else {
			analysisError = errorMessage(settingsResult.reason, 'Analysis settings are unavailable.');
		}
		if (runsResult.status === 'fulfilled') {
			runs = runsResult.value.runs;
		} else if (!analysisError) {
			analysisError = errorMessage(runsResult.reason, 'Analysis history is unavailable.');
		}
		analysisLoading = false;
	}

	async function refreshModels() {
		refreshing = true;
		catalogError = '';
		try {
			const result = await getAnalysisModels(true);
			models = result.models;
			catalogRetrievedAt = result.retrieved_at;
			catalogStale = result.stale;
			catalogError = result.last_error ?? '';
		} catch (cause) {
			catalogError = errorMessage(cause, 'Analysis model catalog is unavailable.');
		} finally {
			refreshing = false;
		}
	}

	function chooseModel(value: string) {
		selectedModel = value;
		const nextEfforts = models.find((model) => model.id === value)?.supported_reasoning_efforts ?? [];
		if (!nextEfforts.some((effort) => effort.value === selectedEffort)) {
			selectedEffort = '';
		}
	}

	async function saveSelection() {
		if (!selectionSupported || saving) return;
		saving = true;
		analysisError = '';
		try {
			const result = await saveAnalysisSettings({ model: selectedModel, effort: selectedEffort });
			savedModel = result.model;
			savedEffort = result.effort;
			selectedModel = result.model;
			selectedEffort = result.effort;
		} catch (cause) {
			analysisError = errorMessage(cause, 'Could not save analysis settings.');
		} finally {
			saving = false;
		}
	}

	function statusLabel(status: string): string {
		if (status === 'succeeded') return 'Succeeded';
		if (status === 'failed') return 'Failed';
		return 'Running';
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
			<p>Choose the model and reasoning effort used for new article analysis.</p>
		</div>
		<a href={appPath('/reader')}>Back to articles</a>
	</div>

	<section class="panel analysis-settings" aria-labelledby="analysis-heading">
		<div class="section-heading">
			<div>
				<h2 id="analysis-heading">Article analysis</h2>
				<p class="muted">Each run is isolated per paragraph and retained here for troubleshooting.</p>
			</div>
			<button type="button" class="secondary" disabled={refreshing} onclick={() => void refreshModels()}>
				{refreshing ? 'Refreshing…' : 'Refresh models'}
			</button>
		</div>

		{#if analysisLoading}
			<p class="status" role="status">Loading analysis settings…</p>
		{:else}
			{#if analysisError}<p class="error-text" role="alert">{analysisError}</p>{/if}
			{#if catalogError}
				<p class="muted" role="status">{catalogStale ? `Using the last known model catalog. ${catalogError}` : catalogError}</p>
			{/if}
			{#if !savedModel}<p class="muted" role="status">No analysis model is selected. Choose one before saving or retrying an article.</p>{/if}
			<div class="settings-grid">
				<label>
					<span>Model</span>
					<select value={selectedModel} onchange={(event) => chooseModel(event.currentTarget.value)}>
						<option value="">Select a model</option>
						{#each models as model (model.id)}
							<option value={model.id}>{model.display_name}{model.hidden ? ' · hidden' : ''}{model.is_default ? ' · default' : ''}</option>
						{/each}
					</select>
				</label>
				<label>
					<span>Reasoning effort</span>
					<select bind:value={selectedEffort} disabled={efforts.length === 0}>
						<option value="">Select an effort</option>
						{#each efforts as effort (effort.value)}
							<option value={effort.value}>{effort.value}{effort.description ? ` — ${effort.description}` : ''}</option>
						{/each}
					</select>
				</label>
			</div>
			<div class="setting-actions">
				<button class="primary" type="button" disabled={!selectionSupported || !selectionChanged || saving} onclick={() => void saveSelection()}>
					{saving ? 'Saving…' : 'Save selection'}
				</button>
				{#if selectionChanged}
					<span class="muted" role="status">Unsaved selection</span>
				{:else if savedModel}
					<span class="muted" role="status">Saved: {savedModel} · {savedEffort}</span>
				{/if}
				{#if catalogRetrievedAt}<span class="muted">Catalog retrieved {catalogRetrievedAt}</span>{/if}
			</div>

			<h3>Recent runs</h3>
			{#if runs.length === 0}
				<p class="muted">No analysis runs have been recorded yet.</p>
			{:else}
				<ul class="run-list" role="list">
					{#each runs as run (run.id)}
						<li>
							<a href={appPath(`/settings/analysis-runs/${encodeURIComponent(run.id)}`)}>
								<strong>{run.article_title}</strong>
								<span>{run.requested_model || 'No model'} · {run.requested_effort}</span>
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
	.settings-grid { display: grid; grid-template-columns: 1fr 1fr; gap: 1rem; margin-top: 1.1rem; }
	.settings-grid label { display: grid; gap: 0.35rem; }
	.settings-grid label > span { font-weight: 650; }
	.settings-grid select { width: 100%; padding: 0.55rem 0.65rem; }
	.primary, .secondary { border: 1px solid var(--color-border); border-radius: 0.5rem; padding: 0.5rem 0.75rem; cursor: pointer; }
	.primary { background: var(--color-accent); color: #171325; border-color: transparent; font-weight: 700; }
	.secondary { background: var(--color-surface-raised); color: var(--color-text); }
	button:disabled { opacity: 0.5; cursor: not-allowed; }
	.setting-actions { display: flex; align-items: center; gap: 0.85rem; margin-top: 1rem; }
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
	@media (max-width: 600px) { .page-heading, .section-heading { flex-direction: column; } .settings-grid { grid-template-columns: 1fr; } .setting-actions { align-items: start; flex-direction: column; } }
</style>
