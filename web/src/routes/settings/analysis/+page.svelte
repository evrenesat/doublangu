<script lang="ts">
	import AnalysisPipelinePanel from '$lib/settings/AnalysisPipelinePanel.svelte';
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

<AnalysisPipelinePanel />

<style>
	h2 {
		margin-bottom: 0.35rem;
	}

	.intro {
		margin: 0 0 1.25rem;
		color: var(--color-muted);
	}
</style>
