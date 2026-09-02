<script lang="ts">
	import type { ArticleBlock, ArticleOccurrence, ArticleSentence } from '$lib/api/client';
	import { buildSemanticRuns, type SemanticRun } from './semanticRuns';
	import ConstructionOverlay from './ConstructionOverlay.svelte';
	import Sentence from './Sentence.svelte';
	import TextOccurrence from './TextOccurrence.svelte';

	type Props = {
		block: ArticleBlock;
		activeSentenceID: string | null;
		activeConstructionIDs: string[];
		onOpen: (occurrence: ArticleOccurrence, anchor: HTMLElement, pin: boolean) => void;
		onPreview: (occurrence: ArticleOccurrence, anchor: HTMLElement) => void;
		onHoverEnd: () => void;
		onHoverAudio: (occurrence: ArticleOccurrence, pointerType: string) => void;
		onLeaveAudio: (key: string) => void;
		onConstructionHover: (ids: string[]) => void;
		onFocusSentence: (sentenceID: string, anchor: HTMLElement) => void;
	};

	let {
		block,
		activeSentenceID,
		activeConstructionIDs,
		onOpen,
		onPreview,
		onHoverEnd,
		onHoverAudio,
		onLeaveAudio,
		onConstructionHover,
		onFocusSentence
	}: Props = $props();

	const sentences = $derived((block.sentences ?? []).slice().sort((left, right) => left.sentence_index - right.sentence_index));
	const constructions = $derived((block.occurrences ?? []).filter((occurrence) => occurrence.role !== 'token'));
	const fallbackRuns = $derived.by((): SemanticRun[] => {
		try {
			return buildSemanticRuns(block);
		} catch {
			return [{ kind: 'plain', text: block.source_text }];
		}
	});

	function sentenceOccurrences(sentence: ArticleSentence): ArticleOccurrence[] {
		return (block.occurrences ?? []).filter((occurrence) => occurrence.spans.some((span) => span.start_utf16 >= sentence.start_utf16 && span.end_utf16 <= sentence.end_utf16));
	}

	// Per-paragraph analysis state: raw Dutch is always readable; a block
	// without semantics shows a waiting/analyzing note, a stale accepted
	// materialization shows "Updating this paragraph", and a failed block
	// keeps its readable content with a retry note.
	const analysisNote = $derived.by(() => {
		if (!block.has_analysis && block.analysis_status === 'pending') return 'Waiting';
		if (block.analysis_status === 'processing' && !block.has_analysis) return 'Analyzing';
		if (block.has_analysis && block.analysis_status !== 'ready' && !block.analysis_is_current) return 'Updating this paragraph';
		if (block.analysis_status === 'failed' && !block.has_analysis) return 'Analysis stopped for this paragraph';
		return null;
	});
	const showSkeleton = $derived(analysisNote !== null && !block.has_analysis && block.analysis_status !== 'failed');
</script>

<div class="paragraph-zone" data-block-index={block.block_index}>
	{#if analysisNote}
		<div class="paragraph-note" role="status" data-analysis-note>
			<span class:note-updating={analysisNote === 'Updating this paragraph'}>{analysisNote}</span>
			{#if showSkeleton}<span class="subtitle-skeleton" aria-hidden="true"></span>{/if}
		</div>
	{/if}
	<p class="reader-paragraph">
		{#if sentences.length === 0}
			{#each fallbackRuns as run, index (index)}
				{#if run.kind === 'plain'}{run.text}{:else}
					<TextOccurrence
						text={run.text}
						occurrence={run.occurrence}
						popoverOccurrence={run.popoverOccurrence}
						constructionIDs={run.constructionIDs}
						activeConstructionIDs={activeConstructionIDs}
						onOpen={onOpen}
						onPreview={onPreview}
						onHoverEnd={onHoverEnd}
						onHoverAudio={onHoverAudio}
						onLeaveAudio={onLeaveAudio}
						onConstructionHover={onConstructionHover}
					/>
				{/if}
			{/each}
		{:else}
			{@const ordered = sentences}
			{@const first = ordered[0]}
			{#if first && first.start_utf16 > 0}{block.source_text.slice(0, first.start_utf16)}{/if}
			{#each ordered as sentence, index (sentence.id)}
				{#if index > 0}
					{@const previous = ordered[index - 1]}
					{#if previous}{block.source_text.slice(previous.end_utf16, sentence.start_utf16)}{/if}
				{/if}
				<Sentence
					{block}
					{sentence}
					occurrences={sentenceOccurrences(sentence)}
					active={activeSentenceID === sentence.id}
					activeConstructionIDs={activeConstructionIDs}
					onOpen={onOpen}
					onPreview={onPreview}
					onHoverEnd={onHoverEnd}
					onHoverAudio={onHoverAudio}
					onLeaveAudio={onLeaveAudio}
					onConstructionHover={onConstructionHover}
					onFocus={onFocusSentence}
				/>
			{/each}
			{@const last = ordered[ordered.length - 1]}
			{#if last && last.end_utf16 < block.source_text.length}{block.source_text.slice(last.end_utf16)}{/if}
		{/if}
			<ConstructionOverlay constructions={constructions} activeIDs={activeConstructionIDs} />
	</p>
</div>

<style>
	.paragraph-zone { margin: 0 0 2rem; }
	.reader-paragraph {
		max-width: 42rem;
		margin: 0;
		font-size: clamp(1.08rem, 1rem + 0.22vw, 1.23rem);
		line-height: 2.18;
		white-space: pre-wrap;
		word-break: normal;
		transition: opacity 120ms ease;
	}

	.paragraph-note {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		margin: 0 0 0.35rem;
		color: var(--reader-muted);
		font-size: 0.74rem;
		font-weight: 550;
		letter-spacing: 0.02em;
		text-transform: uppercase;
	}

	.paragraph-note .note-updating { color: var(--reader-accent); }

	.subtitle-skeleton {
		position: relative;
		display: block;
		width: min(14rem, 55%);
		height: 0.5rem;
		border-radius: 999px;
		overflow: hidden;
		background: color-mix(in srgb, var(--reader-muted) 16%, transparent);
	}

	.subtitle-skeleton::after {
		content: '';
		position: absolute;
		inset: 0;
		transform: translateX(-100%);
		background: linear-gradient(90deg, transparent, color-mix(in srgb, var(--reader-muted) 30%, transparent), transparent);
		animation: skeleton-sweep 1.6s ease-in-out infinite;
	}

	@keyframes skeleton-sweep {
		100% { transform: translateX(100%); }
	}

	@media (max-width: 520px) {
		.reader-paragraph {
			font-size: 1.03rem;
			line-height: 2.08;
		}
	}

	@media (prefers-reduced-motion: reduce) {
		.reader-paragraph { transition: none; }
		.subtitle-skeleton::after { animation: none; display: none; }
	}
</style>
