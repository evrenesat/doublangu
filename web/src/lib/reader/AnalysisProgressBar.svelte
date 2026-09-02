<script lang="ts">
	import type { ProgressShape } from './progressText';

	type Props = {
		shape: ProgressShape;
	};

	let { shape }: Props = $props();

	const determinate = $derived(shape.percent !== undefined);
</script>

{#if shape.visible}
	<div
		class="analysis-progress"
		role="status"
		aria-live="polite"
		data-progress-kind={shape.kind}
	>
		<div class="progress-copy">
			<strong>{shape.text}</strong>
			{#if shape.detail}<span class="progress-detail">{shape.detail}</span>{/if}
		</div>
		{#if determinate}
			<div
				class="progress-track"
				role="progressbar"
				aria-label={shape.kind === 'narration' ? 'Narration generation progress' : 'Paragraph analysis progress'}
				aria-valuemin={0}
				aria-valuemax={100}
				aria-valuenow={shape.percent}
				aria-valuetext={shape.detail}
			>
				<div class="progress-fill" style:width={`${shape.percent}%`}></div>
			</div>
		{:else}
			<div class="progress-track indeterminate" aria-hidden="true">
				<div class="progress-fill"></div>
			</div>
		{/if}
	</div>
{/if}

<style>
	.analysis-progress {
		display: flex;
		align-items: center;
		gap: 0.75rem;
		margin: 0 0 0.6rem;
		padding: 0.45rem 0.7rem;
		border: 1px solid var(--reader-border);
		border-radius: 0.6rem;
		background: var(--reader-surface);
		font-size: 0.8rem;
	}

	.progress-copy {
		display: flex;
		align-items: baseline;
		flex-wrap: wrap;
		gap: 0.35rem 0.55rem;
		min-width: 0;
	}

	.progress-copy strong { font-weight: 620; }
	.progress-detail { color: var(--reader-muted); }

	.progress-track {
		position: relative;
		flex: 1 1 6rem;
		height: 0.32rem;
		overflow: hidden;
		border-radius: 999px;
		background: color-mix(in srgb, var(--reader-muted) 22%, transparent);
		min-width: 3rem;
	}

	.progress-fill {
		height: 100%;
		border-radius: inherit;
		background: var(--reader-accent);
		transition: width 300ms ease;
	}

	.progress-track.indeterminate .progress-fill {
		width: 34%;
		animation: accent-slide 1.4s ease-in-out infinite;
	}

	@keyframes accent-slide {
		0% { transform: translateX(-110%); }
		60% { transform: translateX(210%); }
		100% { transform: translateX(310%); }
	}

	@media (prefers-reduced-motion: reduce) {
		.progress-fill { transition: none; }
		.progress-track.indeterminate .progress-fill { animation: none; width: 100%; opacity: 0.35; }
	}
</style>
