<script lang="ts">
	import { onMount } from 'svelte';
	import type { ArticleAnnotation, LearningStatus } from '$lib/api/client';

	type Detail = 'meaning' | 'usage' | 'parts';

	type Props = {
		annotation: ArticleAnnotation;
		anchor: HTMLElement | null;
		feedback: string;
		feedbackIsError: boolean;
		onEnter: () => void;
		onLeave: () => void;
		onClose: () => void;
		onLearningStatus: (status: LearningStatus) => Promise<void>;
	};

	let {
		annotation,
		anchor,
		feedback,
		feedbackIsError,
		onEnter,
		onLeave,
		onClose,
		onLearningStatus
	}: Props = $props();

	let popover: HTMLDivElement | null = $state(null);
	let explored = $state(false);
	let selectedDetail = $state<Detail | null>(null);
	let saving = $state(false);
	let bottomSheet = $state(false);
	let frame = 0;
	let resizeObserver: ResizeObserver | undefined;

	const detailText: Record<Detail, () => string> = {
		meaning: () => annotation.meaning_note,
		usage: () => annotation.usage_note,
		parts: () => annotation.parts_note
	};

	$effect(() => {
		const currentAnchor = anchor;
		const currentPopover = popover;
		if (!currentAnchor || !currentPopover) return;
		position(currentAnchor, currentPopover);
	});

	function schedulePosition() {
		if (frame) cancelAnimationFrame(frame);
		frame = requestAnimationFrame(() => {
			frame = 0;
			if (anchor && popover) position(anchor, popover);
		});
	}

	onMount(() => {
		const handleOutside = (event: PointerEvent) => {
			const target = event.target as Node | null;
			if (target && (popover?.contains(target) || anchor?.contains(target))) return;
			onClose();
		};
		const handleKeydown = (event: KeyboardEvent) => {
			if (event.key === 'Escape') {
				event.preventDefault();
				onClose();
			}
		};
		if (typeof ResizeObserver !== 'undefined' && popover) {
			resizeObserver = new ResizeObserver(schedulePosition);
			resizeObserver.observe(popover);
		}
		document.addEventListener('pointerdown', handleOutside, true);
		document.addEventListener('keydown', handleKeydown);
		window.addEventListener('resize', schedulePosition);
		window.addEventListener('scroll', schedulePosition, true);
		window.visualViewport?.addEventListener('resize', schedulePosition);
		window.visualViewport?.addEventListener('scroll', schedulePosition);
		return () => {
			document.removeEventListener('pointerdown', handleOutside, true);
			document.removeEventListener('keydown', handleKeydown);
			window.removeEventListener('resize', schedulePosition);
			window.removeEventListener('scroll', schedulePosition, true);
			window.visualViewport?.removeEventListener('resize', schedulePosition);
			window.visualViewport?.removeEventListener('scroll', schedulePosition);
			resizeObserver?.disconnect();
			resizeObserver = undefined;
			if (frame) cancelAnimationFrame(frame);
			frame = 0;
		};
	});

	function position(currentAnchor: HTMLElement, currentPopover: HTMLDivElement) {
		if (typeof window === 'undefined') return;
		const margin = 12;
		const viewportWidth = window.visualViewport?.width ?? window.innerWidth;
		const viewportHeight = window.visualViewport?.height ?? window.innerHeight;
		const width = Math.min(360, Math.max(0, viewportWidth - margin * 2));
		currentPopover.style.width = `${width}px`;
		currentPopover.style.maxHeight = `${Math.max(120, viewportHeight - margin * 2)}px`;
		currentPopover.style.visibility = 'hidden';
		currentPopover.style.left = `${margin}px`;
		currentPopover.style.top = `${margin}px`;
		currentPopover.style.right = 'auto';
		currentPopover.style.bottom = 'auto';

		const anchorRect = currentAnchor.getBoundingClientRect();
		const popoverRect = currentPopover.getBoundingClientRect();
		const left = Math.min(Math.max(margin, anchorRect.left + (anchorRect.width - popoverRect.width) / 2), viewportWidth - popoverRect.width - margin);
		const below = anchorRect.bottom + 8;
		const above = anchorRect.top - popoverRect.height - 8;
		const canPlaceBelow = below + popoverRect.height <= viewportHeight - margin;
		const canPlaceAbove = above >= margin;
		const shouldUseSheet = !canPlaceBelow && !canPlaceAbove && viewportWidth <= 600;
		bottomSheet = shouldUseSheet;
		if (shouldUseSheet) {
			currentPopover.style.left = `${margin}px`;
			currentPopover.style.right = `${margin}px`;
			currentPopover.style.bottom = `${margin}px`;
			currentPopover.style.top = 'auto';
			currentPopover.style.width = 'auto';
		} else {
			const top = canPlaceBelow ? below : canPlaceAbove ? above : Math.min(Math.max(margin, below), viewportHeight - popoverRect.height - margin);
			currentPopover.style.left = `${Math.max(margin, left)}px`;
			currentPopover.style.top = `${top}px`;
		}
		currentPopover.style.visibility = 'visible';
	}

	function availableDetail(detail: Detail): boolean {
		return detailText[detail]().trim().length > 0;
	}

	function toggleExplore() {
		explored = !explored;
		if (explored && !selectedDetail) {
			selectedDetail = (['meaning', 'usage', 'parts'] as Detail[]).find(availableDetail) ?? null;
		}
	}

	async function mark(status: LearningStatus) {
		saving = true;
		try {
			await onLearningStatus(status);
		} finally {
			saving = false;
		}
	}

	function detailLabel(detail: Detail): string {
		return detail.charAt(0).toUpperCase() + detail.slice(1);
	}
</script>

<div
	class="translation-popover"
	class:bottom-sheet={bottomSheet}
	bind:this={popover}
	role="dialog"
	tabindex="-1"
	aria-label={`Translation for ${annotation.source_text}`}
	onpointerenter={onEnter}
	onpointerleave={onLeave}
	onfocusin={onEnter}
	onfocusout={onLeave}
>
	<div class="popover-heading">
		<span class="kind">{annotation.kind}</span>
		<strong>{annotation.source_text}</strong>
	</div>
	<p class="primary-translation">{annotation.primary_translation}</p>
	{#if annotation.alternatives.length > 0}
		<p class="alternatives">Also: {annotation.alternatives.join(' · ')}</p>
	{/if}

	<div class="popover-actions">
		<button type="button" class="state-action" disabled={saving} onclick={() => void mark(annotation.learning_state?.status === 'learned' ? 'unlearned' : 'learned')}>
			{annotation.learning_state?.status === 'learned' ? 'Mark unlearned' : 'Mark learned'}
		</button>
		<button type="button" class="explore-action" aria-expanded={explored} onclick={toggleExplore}>Explore</button>
	</div>

	{#if explored}
		<div class="detail-actions" aria-label="Explore annotation">
			{#each (['meaning', 'usage', 'parts'] as Detail[]) as detail}
				{#if availableDetail(detail)}
					<button
						type="button"
						class="detail-button"
						class:selected={selectedDetail === detail}
						aria-label={detailLabel(detail)}
						aria-pressed={selectedDetail === detail}
						onclick={() => (selectedDetail = detail)}
					>
						{detailLabel(detail)}
					</button>
				{/if}
			{/each}
		</div>
		{#if selectedDetail && availableDetail(selectedDetail)}
			<p class="detail-line" aria-live="polite">{detailText[selectedDetail]()}</p>
		{/if}
	{/if}

	{#if feedback}
		<p class="feedback" role={feedbackIsError ? 'alert' : 'status'} aria-live="polite">{feedback}</p>
	{/if}
</div>

<style>
	.translation-popover {
		position: fixed;
		z-index: 20;
		box-sizing: border-box;
		padding: 0.9rem;
		border: 1px solid var(--color-border, #cbd5e1);
		border-radius: 0.75rem;
		background: var(--color-bg, #fff);
		color: var(--color-text, #172033);
		box-shadow: 0 12px 32px rgb(15 23 42 / 18%);
		overflow: auto;
		visibility: hidden;
	}

	.translation-popover.bottom-sheet {
		border-radius: 0.9rem;
	}

	.popover-heading {
		display: flex;
		align-items: baseline;
		gap: 0.5rem;
		justify-content: space-between;
	}

	.kind {
		font-size: 0.7rem;
		letter-spacing: 0.08em;
		text-transform: uppercase;
		color: var(--color-muted, #64748b);
	}

	.primary-translation {
		margin: 0.55rem 0 0;
		font-size: 1.1rem;
		font-weight: 700;
	}

	.alternatives,
	.detail-line,
	.feedback {
		margin: 0.45rem 0 0;
		font-size: 0.85rem;
		line-height: 1.4;
	}

	.alternatives,
	.kind {
		color: var(--color-muted, #64748b);
	}

	.popover-actions,
	.detail-actions {
		display: flex;
		flex-wrap: wrap;
		gap: 0.45rem;
		margin-top: 0.8rem;
	}

	.popover-actions button,
	.detail-button {
		font: inherit;
		cursor: pointer;
	}

	.popover-actions button {
		padding: 0.42rem 0.65rem;
		border: 1px solid var(--color-border, #cbd5e1);
		border-radius: 999px;
		background: var(--color-surface, #f8fafc);
		color: inherit;
	}

	.popover-actions button:hover,
	.popover-actions button:focus-visible,
	.detail-button:hover,
	.detail-button:focus-visible {
		outline: 2px solid var(--color-accent, #2563eb);
		outline-offset: 1px;
	}

	.detail-button {
		width: 2.7rem;
		height: 2.7rem;
		padding: 0.15rem;
		border: 1px solid var(--color-border, #cbd5e1);
		border-radius: 50%;
		background: transparent;
		font-size: 0.68rem;
		color: inherit;
	}

	.detail-button.selected {
		background: var(--color-accent, #2563eb);
		border-color: var(--color-accent, #2563eb);
		color: #fff;
	}

	.feedback[role='alert'] {
		color: #991b1b;
	}

	@media (prefers-reduced-motion: no-preference) {
		.translation-popover {
			animation: popover-in 100ms ease-out;
		}
	}

	@keyframes popover-in {
		from { opacity: 0; transform: translateY(-2px); }
		to { opacity: 1; transform: translateY(0); }
	}
</style>
