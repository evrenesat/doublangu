<script lang="ts">
	import {
		DoublanguAPIError,
		DoublanguNetworkError,
		type ArticleAnnotation,
		type ArticleBlock as ArticleBlockData,
		type LearningStatus
	} from '$lib/api/client';
	import { ArticleRunError, buildRuns, type ArticleRun } from './buildRuns';
	import TranslationPopover from './TranslationPopover.svelte';

	type Props = {
		block: ArticleBlockData;
		onLearningStatus: (annotation: ArticleAnnotation, status: LearningStatus) => Promise<void>;
	};

	let { block, onLearningStatus }: Props = $props();
	let selectedID = $state<string | null>(null);
	let anchor = $state<HTMLElement | null>(null);
	let pinned = $state(false);
	let feedback = $state('');
	let feedbackIsError = $state(false);
	let closeTimer: ReturnType<typeof setTimeout> | undefined;

	let runs = $derived.by((): ArticleRun[] => {
		try {
			return buildRuns(block);
		} catch (error) {
			if (!(error instanceof ArticleRunError)) throw error;
			return [{ text: block.source_text }];
		}
	});
	let selectedAnnotation = $derived(
		selectedID ? block.annotations.find((annotation) => annotation.id === selectedID) ?? null : null
	);

	function clearCloseTimer() {
		if (closeTimer) clearTimeout(closeTimer);
		closeTimer = undefined;
	}

	function open(annotation: ArticleAnnotation, target: HTMLElement, shouldPin = false) {
		if (pinned && !shouldPin) return;
		clearCloseTimer();
		selectedID = annotation.id;
		anchor = target;
		if (shouldPin) pinned = true;
		feedback = '';
		feedbackIsError = false;
	}

	function scheduleClose() {
		if (pinned) return;
		clearCloseTimer();
		closeTimer = setTimeout(close, 120);
	}

	function keepOpen() {
		clearCloseTimer();
	}

	function close() {
		clearCloseTimer();
		if (pinned) return;
		selectedID = null;
		anchor = null;
	}

	function closePinned() {
		pinned = false;
		close();
	}

	async function saveLearningStatus(annotation: ArticleAnnotation, status: LearningStatus) {
		feedback = '';
		feedbackIsError = false;
		try {
			await onLearningStatus(annotation, status);
			feedback = status === 'learned' ? 'Marked learned. Subtitle hidden.' : 'Marked unlearned. Subtitle restored.';
		} catch (cause) {
			feedbackIsError = true;
			if (cause instanceof DoublanguAPIError) feedback = cause.message;
			else if (cause instanceof DoublanguNetworkError) feedback = 'Could not save learning state. Check your connection.';
			else if (cause instanceof Error) feedback = cause.message;
			else feedback = 'Could not save learning state.';
		}
	}
</script>

<p class="article-block" data-block-index={block.block_index}>
	{#each runs as run}{#if run.annotation}<button
		type="button"
		class="annotation-trigger"
		class:learned={!run.annotation.show_shadow}
		data-annotation-id={run.annotation.id}
		aria-label={`${run.annotation.source_text}: ${run.annotation.primary_translation}`}
		onpointerenter={(event) => open(run.annotation!, event.currentTarget as HTMLElement)}
		onfocus={(event) => open(run.annotation!, event.currentTarget as HTMLElement)}
		onpointerleave={scheduleClose}
		onblur={scheduleClose}
		onclick={(event) => open(run.annotation!, event.currentTarget as HTMLElement, true)}
	><span class="source-text">{run.text}</span>{#if run.annotation.show_shadow}<span class="translation-subtitle" aria-hidden="true">{run.annotation.primary_translation}</span>{/if}</button>{:else}{run.text}{/if}{/each}
</p>

{#if selectedAnnotation && anchor}
	<TranslationPopover
		annotation={selectedAnnotation}
		anchor={anchor}
		feedback={feedback}
		feedbackIsError={feedbackIsError}
		onEnter={keepOpen}
		onLeave={scheduleClose}
		onClose={closePinned}
		onLearningStatus={(status) => saveLearningStatus(selectedAnnotation!, status)}
	/>
{/if}

<style>
	.article-block {
		margin: 0 0 1.2rem;
		font-size: clamp(1.05rem, 1rem + 0.25vw, 1.2rem);
		line-height: 2;
		white-space: pre-wrap;
		word-break: normal;
	}

	.annotation-trigger {
		position: relative;
		display: inline-block;
		margin: 0;
		padding: 0 0.08em;
		border: 0;
		border-radius: 0.25rem;
		background: transparent;
		color: inherit;
		font: inherit;
		line-height: inherit;
		text-align: inherit;
		cursor: pointer;
		vertical-align: baseline;
	}

	.annotation-trigger:hover,
	.annotation-trigger:focus-visible {
		background: color-mix(in srgb, var(--color-accent, #2563eb) 11%, transparent);
		outline: 2px solid var(--color-accent, #2563eb);
		outline-offset: 1px;
	}

	.translation-subtitle {
		display: block;
		max-width: 100%;
		overflow: hidden;
		color: var(--color-muted, #64748b);
		font-size: 0.62em;
		font-weight: 500;
		line-height: 1.1;
		text-align: center;
		text-overflow: ellipsis;
		white-space: nowrap;
		opacity: 0.68;
	}

	@media (max-width: 420px) {
		.article-block {
			font-size: 1rem;
			line-height: 1.9;
		}
	}
</style>
