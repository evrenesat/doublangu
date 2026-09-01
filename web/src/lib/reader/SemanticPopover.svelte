<script lang="ts">
	import type { ArticleOccurrence, LearningStatus } from '$lib/api/client';

	type Props = {
		occurrence: ArticleOccurrence;
		anchor: HTMLElement;
		feedback: string;
		feedbackIsError: boolean;
		onEnter: () => void;
		onLeave: () => void;
		onClose: () => void;
		onLearningStatus: (status: LearningStatus) => Promise<void>;
		onHear: () => void;
		hearReady?: boolean;
		hearPending?: boolean;
	};

	let { occurrence, anchor, feedback, feedbackIsError, onEnter, onLeave, onClose, onLearningStatus, onHear, hearReady, hearPending }: Props = $props();
	let popover: HTMLDivElement | null = $state(null);
	let bottomSheet = $state(false);
	let explored = $state(false);
	let selectedDetail = $state<'meaning' | 'usage' | 'parts' | null>(null);
	let saving = $state(false);
	let frame = 0;

	const sense = $derived(occurrence.sense);
	const canHear = $derived(hearReady ?? Boolean(occurrence.pronunciation?.ready));
	const hasPendingHear = $derived(hearPending ?? Boolean(occurrence.pronunciation && !occurrence.pronunciation.ready));

	$effect(() => {
		if (anchor && popover) position(anchor, popover);
	});

	function schedulePosition(): void {
		if (frame) cancelAnimationFrame(frame);
		frame = requestAnimationFrame(() => {
			frame = 0;
			if (anchor && popover) position(anchor, popover);
		});
	}

	function position(currentAnchor: HTMLElement, currentPopover: HTMLDivElement): void {
		const margin = 12;
		const width = Math.min(360, window.innerWidth - margin * 2);
		currentPopover.style.width = `${Math.max(0, width)}px`;
		currentPopover.style.maxHeight = `${Math.max(150, window.innerHeight - margin * 2)}px`;
		currentPopover.style.visibility = 'hidden';
		currentPopover.style.left = `${margin}px`;
		currentPopover.style.top = `${margin}px`;
		const anchorRect = currentAnchor.getBoundingClientRect();
		const popoverRect = currentPopover.getBoundingClientRect();
		const left = Math.min(Math.max(margin, anchorRect.left + (anchorRect.width - popoverRect.width) / 2), window.innerWidth - popoverRect.width - margin);
		const below = anchorRect.bottom + 8;
		const above = anchorRect.top - popoverRect.height - 8;
		const canBelow = below + popoverRect.height <= window.innerHeight - margin;
		const canAbove = above >= margin;
		bottomSheet = !canBelow && !canAbove && window.innerWidth <= 600;
		if (bottomSheet) {
			currentPopover.style.left = `${margin}px`;
			currentPopover.style.right = `${margin}px`;
			currentPopover.style.top = 'auto';
			currentPopover.style.bottom = `${margin}px`;
		} else {
			currentPopover.style.left = `${Math.max(margin, left)}px`;
			currentPopover.style.top = `${canBelow ? below : canAbove ? above : Math.max(margin, window.innerHeight - popoverRect.height - margin)}px`;
		}
		currentPopover.style.visibility = 'visible';
	}

	function detailText(detail: 'meaning' | 'usage' | 'parts'): string {
		if (!sense) return '';
		return detail === 'meaning' ? sense.meaning_note : detail === 'usage' ? sense.usage_note : sense.parts_note;
	}

	async function toggleLearning(): Promise<void> {
		if (!sense) return;
		saving = true;
		try {
			await onLearningStatus(occurrence.learning_state?.status === 'learned' ? 'unlearned' : 'learned');
		} finally {
			saving = false;
		}
	}

	function hear(): void {
		if (canHear) onHear();
	}
</script>

<svelte:window onresize={schedulePosition} onscroll={schedulePosition} />

<div
	class="semantic-popover"
	class:bottom-sheet={bottomSheet}
	bind:this={popover}
	role="dialog"
	tabindex="-1"
	aria-label={`Translation for ${occurrence.spans.map((span) => span.source_text).join(' … ')}`}
	onpointerenter={onEnter}
	onpointerleave={onLeave}
	onfocusin={onEnter}
	onfocusout={onLeave}
>
	<div class="popover-heading">
		<span class="kind">{occurrence.kind}</span>
		<strong>{occurrence.spans.map((span) => span.source_text).join(' … ')}</strong>
	</div>
	{#if sense}
		<p class="primary-translation">{sense.primary_translation}</p>
		{#if sense.alternatives.length}<p class="alternatives">Also: {sense.alternatives.join(' · ')}</p>{/if}
	{:else}
		<p class="primary-translation">{occurrence.shadow_text || 'No translation available yet'}</p>
	{/if}

	<div class="popover-actions">
		{#if canHear}
			<button type="button" onclick={hear}>Hear</button>
		{:else if hasPendingHear}
			<span class="audio-state">Audio preparing…</span>
		{/if}
		{#if sense}
			<button type="button" class="state-action" disabled={saving} onclick={() => void toggleLearning()}>
				{occurrence.learning_state?.status === 'learned' ? 'Mark unlearned' : 'Mark learned'}
			</button>
		{/if}
		{#if sense && (sense.meaning_note || sense.usage_note || sense.parts_note)}
			<button type="button" onclick={() => (explored = !explored)} aria-expanded={explored}>Explore</button>
		{/if}
		<button type="button" class="close-action" aria-label="Close translation" onclick={onClose}>×</button>
	</div>

	{#if explored && sense}
		<div class="detail-actions" aria-label="Explore annotation">
			{#each (['meaning', 'usage', 'parts'] as const) as detail}
				{#if detailText(detail)}
					<button type="button" class:selected={selectedDetail === detail} aria-pressed={selectedDetail === detail} onclick={() => (selectedDetail = detail)}>{detail[0]?.toUpperCase()}{detail.slice(1)}</button>
				{/if}
			{/each}
		</div>
		{#if selectedDetail}<p class="detail-line">{detailText(selectedDetail)}</p>{/if}
	{/if}
	{#if feedback}<p class="feedback" role={feedbackIsError ? 'alert' : 'status'}>{feedback}</p>{/if}
</div>

<style>
	.semantic-popover {
		position: fixed;
		z-index: 30;
		box-sizing: border-box;
		padding: 0.9rem;
		border: 1px solid var(--reader-border);
		border-radius: 0.75rem;
		background: var(--reader-surface-raised);
		color: var(--reader-text);
		box-shadow: 0 16px 36px rgb(0 0 0 / 28%);
		overflow: auto;
		visibility: hidden;
	}

	.semantic-popover.bottom-sheet { border-radius: 0.9rem; }
	.popover-heading { display: flex; align-items: baseline; justify-content: space-between; gap: 0.6rem; }
	.kind, .alternatives, .audio-state { color: var(--reader-muted); }
	.kind { font-size: 0.7rem; letter-spacing: 0.08em; text-transform: uppercase; }
	.primary-translation { margin: 0.55rem 0 0; font-size: 1.1rem; font-weight: 700; }
	.alternatives, .detail-line, .feedback { margin: 0.45rem 0 0; font-size: 0.85rem; line-height: 1.4; }
	.popover-actions, .detail-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 0.45rem; margin-top: 0.8rem; }
	.popover-actions button, .detail-actions button { padding: 0.35rem 0.55rem; border: 1px solid var(--reader-border); border-radius: 999px; background: transparent; color: inherit; cursor: pointer; }
	.popover-actions button:hover, .popover-actions button:focus-visible, .detail-actions button:hover, .detail-actions button:focus-visible, .detail-actions button.selected { background: color-mix(in srgb, var(--reader-accent) 16%, transparent); }
	.close-action { margin-left: auto; font-size: 1.15rem; line-height: 1; }
	.feedback[role='alert'] { color: var(--reader-danger); }
</style>
