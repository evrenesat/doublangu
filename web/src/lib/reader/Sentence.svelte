<script lang="ts">
	import type { ArticleBlock, ArticleOccurrence, ArticleSentence } from '$lib/api/client';
	import { buildSemanticRuns, type SemanticRun } from './semanticRuns';
	import TextOccurrence from './TextOccurrence.svelte';

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
		onFocus
	}: Props = $props();

	let dwellTimer: ReturnType<typeof setTimeout> | undefined;
	let anchor: HTMLElement | null = $state(null);

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
			return buildSemanticRuns(localBlock);
		} catch {
			return [{ kind: 'plain', text: sentence.source_text }];
		}
	});

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
	{#each runs as run, index (index)}
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
</span>

<style>
	.reader-sentence {
		position: relative;
		display: inline;
		border-radius: 0.3rem;
		transition: background-color 120ms ease, font-size 120ms ease, letter-spacing 120ms ease;
	}

	.reader-sentence:hover {
		background: color-mix(in srgb, var(--reader-accent) 4%, transparent);
	}

	.reader-sentence.focused {
		font-size: 1.07em;
		letter-spacing: 0.005em;
		background: color-mix(in srgb, var(--reader-accent) 5%, transparent);
	}

	@media (prefers-reduced-motion: reduce) {
		.reader-sentence { transition: none; }
	}
</style>
