<script lang="ts">
	import AnalysisPipelinePanel from '$lib/settings/AnalysisPipelinePanel.svelte';
	import PromptLibraryPanel from '$lib/settings/PromptLibraryPanel.svelte';
	import type { AnalysisPromptType, AnalysisPromptVersion } from '$lib/api/client';

	// A successful library save flows to the profile panel so the just-saved
	// version can be pinned without a page reload. Merging only extends the
	// selector catalog: open drafts and pinned selections are never replaced.
	let mergePromptVersion: ((promptType: AnalysisPromptType, version: AnalysisPromptVersion) => void) | null = null;
</script>

<svelte:head>
	<title>Analysis settings — Doublangu</title>
</svelte:head>

<section class="analysis-settings" aria-labelledby="analysis-heading">
	<h2 id="analysis-heading">Analysis</h2>
	<p class="intro">
		Profiles define the provider, model, and options used for linguistic analysis and translation. New analysis runs use the active
		profile.
	</p>
</section>

<AnalysisPipelinePanel registerPromptVersionMerge={(merge) => (mergePromptVersion = merge)} />

<PromptLibraryPanel onsaved={(promptType, version) => mergePromptVersion?.(promptType, version)} />

<style>
	h2 {
		margin-bottom: 0.35rem;
	}

	.intro {
		margin: 0 0 1.25rem;
		color: var(--color-muted);
	}
</style>
