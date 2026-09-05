<script lang="ts">
	import type { ArticleBlock, ArticleOccurrence, ArticleSentence } from '$lib/api/client';
	import { buildSemanticRuns, type SemanticRun } from './semanticRuns';
	import TextOccurrence from './TextOccurrence.svelte';
	import ConstructionOverlay from './ConstructionOverlay.svelte';
	import { onDestroy } from 'svelte';

	type Props = {
		block: ArticleBlock;
		sentence: ArticleSentence;
		occurrences: ArticleOccurrence[];
		active: boolean;
		activeConstructionIDs: string[];
		onOpen: (occurrence: ArticleOccurrence, anchor: HTMLElement, pin: boolean) => void;
		onPreview: (occurrence: ArticleOccurrence, anchor: HTMLElement) => void;
		onHoverEnd: () => void;
		onHoverAudio: (occurrence: ArticleOccurrence, pointerType: string) => void;
		onLeaveAudio: (key: string) => void;
		onConstructionHover: (ids: string[]) => void;
		onFocus: (sentenceID: string, anchor: HTMLElement) => void;
		onPlay?: (sentence: ArticleSentence) => void;
	};

	let {
		block,
		sentence,
		occurrences,
		active,
		activeConstructionIDs,
		onOpen,
		onPreview,
		onHoverEnd,
		onHoverAudio,
		onLeaveAudio,
		onConstructionHover,
		onFocus,
		onPlay
	}: Props = $props();

	let dwellTimer: ReturnType<typeof setTimeout> | undefined;
	let anchor: HTMLElement | null = $state(null);
	let words: HTMLElement | null = $state(null);
	const constructions = $derived(occurrences.filter((item) => item.role !== 'token'));
	onDestroy(clearDwell);

	const localBlock = $derived.by((): ArticleBlock => ({
		...block,
		source_text: sentence.source_text,
		occurrences: occurrences
			.filter((occurrence) => occurrence.spans.length > 0 && occurrence.spans.every((span) => span.start_utf16 >= sentence.start_utf16 && span.end_utf16 <= sentence.end_utf16))
			.map((occurrence) => ({
				...occurrence,
				spans: occurrence.spans.map((span) => ({
					...span,
					start_utf16: span.start_utf16 - sentence.start_utf16,
					end_utf16: span.end_utf16 - sentence.start_utf16
				}))
			}))
	}));

	const runs = $derived.by((): SemanticRun[] => {
		try {
			return buildSemanticRuns(localBlock, true);
		} catch {
			return [{ kind: 'plain', text: sentence.source_text }];
		}
	});
	function punctuationAfter(index: number): string {
		const next = runs[index + 1];
		return next?.kind === 'plain' ? next.text.match(/^[.,!?;:…’”)\]]+/u)?.[0] ?? '' : '';
	}

	function clearDwell(): void {
		if (dwellTimer) clearTimeout(dwellTimer);
		dwellTimer = undefined;
	}

	function focusSentence(event: FocusEvent | MouseEvent | KeyboardEvent): void {
		const element = event.currentTarget as HTMLElement;
		anchor = element;
		onFocus(sentence.id, element);
	}

	function scheduleDwell(event: PointerEvent): void {
		if (event.pointerType === 'touch') return;
		clearDwell();
		const element = event.currentTarget as HTMLElement;
		dwellTimer = setTimeout(() => {
			dwellTimer = undefined;
			anchor = element;
			onFocus(sentence.id, element);
		}, 350);
	}

	function handleClick(event: MouseEvent): void {
		if ((event.target as HTMLElement | null)?.closest('.text-occurrence')) return;
		focusSentence(event);
	}

	function handleKeydown(event: KeyboardEvent): void {
		if (event.target !== event.currentTarget) return;
		if (event.key !== 'Enter' && event.key !== ' ') return;
		event.preventDefault();
		focusSentence(event);
	}
</script>

<!-- svelte-ignore a11y_no_noninteractive_tabindex -->
<!-- svelte-ignore a11y_no_noninteractive_element_interactions -->
<span
	class="reader-sentence"
	class:focused={active}
	data-sentence-id={sentence.id}
	role="group"
	tabindex="0"
	aria-label={`Sentence ${sentence.sentence_index + 1}: ${sentence.source_text}`}
	onpointerenter={scheduleDwell}
	onpointerleave={clearDwell}
	onfocusin={focusSentence}
	onclick={handleClick}
	onkeydown={handleKeydown}
>
	<span class="sentence-words" bind:this={words} style:--connector-space={`${constructions.length ? 22 + constructions.length * 6 : 7}px`}>
	{#each runs as run, index (index)}
		{#if run.kind === 'plain'}<span class="plain-text">{runs[index - 1]?.kind === 'occurrence' ? run.text.slice(punctuationAfter(index - 1).length) : run.text}</span>{:else}
			<TextOccurrence
				text={run.text}
				suffix={punctuationAfter(index)}
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
	<ConstructionOverlay {constructions} activeIDs={activeConstructionIDs} root={words} />
	</span>
	<span class="sentence-footer" class:without-expressions={constructions.length === 0} class:without-audio={!sentence.audio?.ready}>
		<span class="focus-label">{active ? 'Focused sentence' : `Sentence ${sentence.sentence_index + 1}`}</span>
		<button type="button" class="play-sentence" disabled={!sentence.audio?.ready} onclick={(event) => { event.stopPropagation(); onPlay?.(sentence); }}>
			<span aria-hidden="true">▶</span> {sentence.audio?.ready ? 'Play sentence' : 'Audio not ready'}
		</button>
		<span class="expression-keys">
			{#each constructions as construction, index (construction.id)}
				<button type="button" class="expression-key" class:expression-active={activeConstructionIDs.includes(construction.id)}
					onclick={(event) => { event.stopPropagation(); onOpen(construction, event.currentTarget, true); }}
					onpointerenter={() => onConstructionHover([construction.id])} onpointerleave={() => onConstructionHover([])}
					onfocus={() => onConstructionHover([construction.id])} onblur={() => onConstructionHover([])}>
					<span class="line-key" class:split={construction.role === 'discontinuous_construction'} aria-hidden="true"></span>
					<span>{constructions.length > 1 ? `${index + 1}. ` : ''}{construction.sense?.canonical_form || construction.spans.map((span) => span.source_text).join(' … ')} <span class="expression-meaning">· {construction.shadow_text || construction.sense?.primary_translation}</span></span>
				</button>
			{/each}
		</span>
	</span>
</span>

<style>
	.reader-sentence {
		position: relative;
		display: block;
		padding: 1.35rem 1.5rem 0.85rem;
		margin: 0 0 1.4rem;
		border: 1px solid transparent;
		border-radius: 1rem;
		transition: background-color 160ms ease, border-color 160ms ease;
	}

	.reader-sentence:hover {
		background: color-mix(in srgb, var(--reader-accent) 4%, transparent);
	}

	.reader-sentence.focused {
		border-color: var(--reader-border);
		background: var(--reader-surface);
	}
	.sentence-words {
		position: relative;
		display: block;
		font: 400 clamp(1.45rem, 1.1rem + 1.35vw, 2rem)/1.3 Georgia, 'Times New Roman', serif;
		transform: scale(var(--reader-rest-scale, 0.94));
		transform-origin: top left;
		transition: transform 160ms ease;
		/* Layout always uses the enlarged metrics. Transform never affects wrapping. */
	}
	.focused .sentence-words { transform: scale(1); }
	.sentence-words :global(.text-occurrence) { margin-bottom: var(--connector-space); }
	.plain-text { white-space: pre-wrap; }
	.sentence-footer { display: flex; flex-wrap: wrap; align-items: baseline; gap: 0.6rem 1.2rem; padding-top: 0.7rem; border-top: 1px solid transparent; font: 400 0.79rem/1.5 ui-sans-serif, system-ui, sans-serif; }
	.focused .sentence-footer { border-top-color: var(--reader-border); }
	.focus-label { width: 8.2rem; flex-shrink: 0; color: var(--reader-muted); }
	.focused .focus-label { color: var(--reader-accent); font-weight: 650; }
	.sentence-footer button { border: 0; padding: 0; background: transparent; cursor: pointer; color: var(--reader-accent); font: inherit; text-align: left; }
	.sentence-footer .play-sentence:disabled { cursor: default; color: var(--reader-muted); }
	.expression-keys { display: flex; flex-wrap: wrap; gap: 0.6rem 1rem; margin-left: auto; }
	.sentence-footer .expression-key { display: flex; align-items: center; gap: 0.45rem; color: var(--reader-construction); }
	.expression-meaning { color: var(--reader-muted); }
	.line-key { width: 1.6rem; flex-shrink: 0; border-top: 2px solid currentColor; }
	.line-key.split { border-top-style: dashed; }
	.expression-active .expression-meaning { color: var(--reader-text); }
	@media (max-width: 600px) {
		.reader-sentence { padding: 0.5rem 0.35rem 0.35rem; margin-bottom: 0.2rem; border-radius: 0.65rem; }
		.sentence-words { font-size: 1.375rem; line-height: 1.2; }
		.focus-label, .without-audio .play-sentence { display: none; }
		.without-expressions.without-audio { display: none; }
		.expression-keys { width: 100%; margin-left: 0; }
		.sentence-footer { font-size: 0.75rem; padding-top: 0.1rem; gap: 0.25rem; }
		.expression-keys { gap: 0.25rem; }
		.expression-key { min-height: 1.65rem; }
		.line-key { width: 1rem; }
	}

	@media (prefers-reduced-motion: reduce) {
		.reader-sentence, .sentence-words { transition: none; }
	}
</style>
