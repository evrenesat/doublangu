<script lang="ts">
	import type { ArticleOccurrence } from '$lib/api/client';

	type Props = {
		text: string;
		suffix?: string;
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
		suffix = '',
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

	const subtitle = $derived(occurrence.shadow_text || occurrence.sense?.primary_translation || '');
	const audioKey = $derived(occurrence.pronunciation?.render_id ?? occurrence.id);

	function activate(event: MouseEvent | KeyboardEvent): void {
		if (event instanceof KeyboardEvent && event.key !== 'Enter' && event.key !== ' ') return;
		if (event instanceof KeyboardEvent) event.preventDefault();
		onOpen(occurrence, event.currentTarget as HTMLElement, true);
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
	class:learned={occurrence.learning_state?.status === 'learned'}
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
	<span class="source-text">{text}{suffix}</span>
	<span class="translation-subtitle" aria-hidden="true">{subtitle || '·'}</span>
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
		margin: 0 0.10em 0.9em 0;
		padding: 0.08em 0.06em 0.1em;
		border-radius: 0.2rem;
		cursor: pointer;
		vertical-align: top;
		line-height: 1.25;
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
		display: block;
		max-width: min(12rem, 50vw);
		color: var(--reader-subtitle);
		font-family: ui-sans-serif, system-ui, sans-serif;
		font-size: 0.46em;
		font-weight: 450;
		line-height: 1.3;
		text-align: center;
		white-space: normal;
		overflow-wrap: anywhere;
		pointer-events: none;
		min-height: 1.3em;
	}

	@media (max-width: 600px) {
		.text-occurrence { margin-right: 0.04em; padding-inline: 0.035em; }
		.translation-subtitle { font-size: 0.585em; line-height: 1.2; }
	}

	.text-occurrence.construction-active {
		background: color-mix(in srgb, var(--reader-construction) 13%, transparent);
	}

	@media (prefers-reduced-motion: reduce) {
		.text-occurrence { transition: none; }
	}
</style>
