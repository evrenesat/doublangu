<script lang="ts">
	import type { ArticleOccurrence } from '$lib/api/client';

	type Props = {
		text: string;
		occurrence: ArticleOccurrence;
		popoverOccurrence: ArticleOccurrence;
		constructionIDs: string[];
		activeConstructionIDs: string[];
		onOpen: (occurrence: ArticleOccurrence, anchor: HTMLElement, pin: boolean) => void;
		onPreview: (occurrence: ArticleOccurrence, anchor: HTMLElement) => void;
		onHoverEnd: () => void;
		onHoverAudio: (occurrence: ArticleOccurrence, pointerType: string) => void;
		onLeaveAudio: (key: string) => void;
		onConstructionHover: (ids: string[]) => void;
	};

	let {
		text,
		occurrence,
		popoverOccurrence,
		constructionIDs,
		activeConstructionIDs,
		onOpen,
		onPreview,
		onHoverEnd,
		onHoverAudio,
		onLeaveAudio,
		onConstructionHover
	}: Props = $props();

	const subtitle = $derived(occurrence.show_shadow && occurrence.shadow_policy !== 'none' ? occurrence.shadow_text || occurrence.sense?.primary_translation || '' : '');
	const audioKey = $derived(occurrence.pronunciation?.render_id ?? occurrence.id);

	function activate(event: MouseEvent | KeyboardEvent): void {
		if (event instanceof KeyboardEvent && event.key !== 'Enter' && event.key !== ' ') return;
		if (event instanceof KeyboardEvent) event.preventDefault();
		onOpen(popoverOccurrence, event.currentTarget as HTMLElement, true);
	}

	function pointerEnter(event: PointerEvent): void {
		if (event.pointerType === 'touch') return;
		onPreview(popoverOccurrence, event.currentTarget as HTMLElement);
		onConstructionHover(constructionIDs);
		onHoverAudio(occurrence, event.pointerType || 'mouse');
	}

	function focusOccurrence(event: FocusEvent): void {
		onPreview(popoverOccurrence, event.currentTarget as HTMLElement);
		onConstructionHover(constructionIDs);
	}
</script>

<span
	class="text-occurrence"
	class:learned={!occurrence.show_shadow}
	class:construction-member={constructionIDs.length > 0}
	class:construction-active={constructionIDs.some((id) => activeConstructionIDs.includes(id))}
	class:group-unit={occurrence.role === 'contiguous_construction'}
	data-occurrence-id={occurrence.id}
	data-construction-ids={constructionIDs.join(' ')}
	role="button"
	tabindex="0"
	aria-label={`${text}${occurrence.sense?.primary_translation ? `: ${occurrence.sense.primary_translation}` : ''}`}
	onclick={activate}
	onkeydown={activate}
	onfocus={focusOccurrence}
	onblur={onHoverEnd}
	onpointerenter={pointerEnter}
	onpointerleave={() => { onLeaveAudio(audioKey); onHoverEnd(); onConstructionHover([]); }}
>
	<span class="source-text">{text}</span>
	{#if subtitle}<span class="translation-subtitle" aria-hidden="true">{subtitle}</span>{/if}
</span>

<style>
	.text-occurrence {
		/* In-flow interlinear unit: source and subtitle participate in layout,
		   so adjacent visible subtitle boxes can never overlap. The unit may
		   shrink and wrap at source spaces within the paragraph width: no
		   max-content minimum, so long group text cannot overflow narrow
		   readers. */
		display: inline-grid;
		grid-template-rows: auto auto;
		justify-items: center;
		min-width: 0;
		max-width: 100%;
		margin: 0 0.04em;
		border-radius: 0.2rem;
		cursor: pointer;
		vertical-align: baseline;
		-webkit-box-decoration-break: clone;
		box-decoration-break: clone;
	}

	.text-occurrence:hover,
	.text-occurrence:focus-visible {
		background: color-mix(in srgb, var(--reader-accent) 13%, transparent);
		outline: 2px solid var(--reader-accent);
		outline-offset: 2px;
	}

	.source-text {
		white-space: pre-wrap;
		word-break: normal;
		overflow-wrap: anywhere;
	}

	.translation-subtitle {
		display: -webkit-box;
		max-width: min(17rem, 58vw);
		color: var(--reader-subtitle);
		font-size: 0.57em;
		font-weight: 550;
		line-height: 1.15;
		text-align: center;
		overflow: hidden;
		-webkit-box-orient: vertical;
		-webkit-line-clamp: 1;
		line-clamp: 1;
		pointer-events: none;
		opacity: 0.82;
	}

	/* Long contiguous-group subtitles may wrap to at most two lines. */
	.text-occurrence.group-unit .translation-subtitle {
		-webkit-line-clamp: 2;
		line-clamp: 2;
		overflow-wrap: anywhere;
	}

	.text-occurrence.construction-member .source-text {
		text-decoration-line: underline;
		text-decoration-style: wavy;
		text-decoration-color: var(--reader-construction);
		text-underline-offset: 0.18em;
	}

	.text-occurrence.construction-active .source-text {
		background: color-mix(in srgb, var(--reader-construction) 22%, transparent);
		text-decoration-thickness: 0.15em;
	}

	@media (prefers-reduced-motion: reduce) {
		.text-occurrence { transition: none; }
	}
</style>
