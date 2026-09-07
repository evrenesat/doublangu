<script lang="ts">
	import { tick } from 'svelte';
	import type { ArticleOccurrence, LearningStatus } from '$lib/api/client';
	import ExplorePanel from './ExplorePanel.svelte';

	type Props = {
		occurrence: ArticleOccurrence;
		/** The construction occurrence when the selected token is a member. */
		expressionOccurrence?: ArticleOccurrence | null;
		articleId: string;
		anchor: HTMLElement;
		feedback: string;
		feedbackIsError: boolean;
		onEnter: () => void;
		onLeave: () => void;
		onClose: () => void;
		onLearningStatus: (status: LearningStatus) => Promise<void>;
		onHear: () => void;
		/** Pins the popover so the expanded panel survives pointer drift. */
		onPin?: () => void;
		/** Notifies the reader so Hear and learning target the new subject. */
		onSubjectChange?: (subject: 'word' | 'expression') => void;
		hearReady?: boolean;
		hearPending?: boolean;
		learningEnabled?: boolean;
	};

	let {
		occurrence,
		expressionOccurrence = null,
		articleId,
		anchor,
		feedback,
		feedbackIsError,
		onEnter,
		onLeave,
		onClose,
		onLearningStatus,
		onHear,
		onPin,
		onSubjectChange,
		hearReady,
		hearPending,
		learningEnabled = true
	}: Props = $props();
	let popover: HTMLDivElement | null = $state(null);
	let bottomSheet = $state(false);
	let subject = $state<'word' | 'expression'>('word');
	let explored = $state(false);
	let saving = $state(false);
	let frame = 0;
	let resizeObserver: ResizeObserver | undefined;

	// The explicit subject: the selected occurrence itself (word) or its
	// owning construction (expression). Switching changes the popover's
	// source, translation, Hear/learning target, and Explore target together
	// and never generates anything.
	const currentOccurrence = $derived(
		subject === 'expression' && expressionOccurrence ? expressionOccurrence : occurrence
	);
	const currentText = $derived(currentOccurrence.spans.map((span) => span.source_text).join(' … '));
	const sense = $derived(currentOccurrence.sense);
	const canHear = $derived(hearReady ?? Boolean(currentOccurrence.pronunciation?.ready));
	const hasPendingHear = $derived(hearPending ?? Boolean(currentOccurrence.pronunciation && !currentOccurrence.pronunciation.ready));

	$effect(() => {
		// A different selected occurrence resets to the word subject and
		// closes the panel; Explore is always an explicit click.
		void occurrence.id;
		subject = 'word';
		explored = false;
	});

	$effect(() => {
		if (anchor && popover) position(anchor, popover);
	});

	$effect(() => {
		if (typeof ResizeObserver === 'undefined' || !popover) return;
		resizeObserver = new ResizeObserver(schedulePosition);
		resizeObserver.observe(popover);
		return () => {
			resizeObserver?.disconnect();
			resizeObserver = undefined;
		};
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

	async function toggleLearning(): Promise<void> {
		if (!sense) return;
		saving = true;
		try {
			await onLearningStatus(currentOccurrence.learning_state?.status === 'learned' ? 'unlearned' : 'learned');
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
	aria-label={`Translation for ${currentText}`}
	onpointerenter={onEnter}
	onpointerleave={onLeave}
	onfocusin={onEnter}
	onfocusout={onLeave}
>
	<div class="popover-heading">
		<span class="kind">{currentOccurrence.kind}</span>
		<strong>{currentText}</strong>
	</div>
	{#if expressionOccurrence}
		<div class="subject-selector" role="group" aria-label="Explore subject">
			<button
				type="button"
				class:selected={subject === 'word'}
				aria-pressed={subject === 'word'}
				onclick={() => { subject = 'word'; onSubjectChange?.('word'); }}
			>
				Word: {occurrence.spans.map((span) => span.source_text).join(' … ')}
			</button>
			<button
				type="button"
				class:selected={subject === 'expression'}
				aria-pressed={subject === 'expression'}
				onclick={() => { subject = 'expression'; onSubjectChange?.('expression'); }}
			>
				Expression
			</button>
		</div>
	{/if}
	{#if sense}
		<p class="primary-translation">{sense.primary_translation}</p>
		{#if sense.alternatives.length}<p class="alternatives">Also: {sense.alternatives.join(' · ')}</p>{/if}
	{:else}
		<p class="primary-translation">{currentOccurrence.shadow_text || 'No translation available yet'}</p>
	{/if}

	<div class="popover-actions">
		{#if canHear}
			<button type="button" onclick={hear}>Hear</button>
		{:else if hasPendingHear}
			<span class="audio-state">Audio preparing…</span>
		{/if}
		{#if sense}
			<button type="button" class="state-action" disabled={saving || !learningEnabled} title={learningEnabled ? undefined : 'Learning is available on saved articles'} onclick={() => void toggleLearning()}>
				{currentOccurrence.learning_state?.status === 'learned' ? 'Mark unlearned' : 'Mark learned'}
			</button>
		{/if}
		<button
			type="button"
			aria-expanded={explored}
			onclick={() => {
				onPin?.();
				explored = true;
				void tick().then(schedulePosition);
			}}
		>
			Explore
		</button>
		<button type="button" class="close-action" aria-label="Close translation" onclick={onClose}>×</button>
	</div>

	{#if explored && (onPin || occurrence.id)}
		{#key `${occurrence.id}:${subject}`}
			<ExplorePanel
				{articleId}
				occurrenceId={currentOccurrence.id}
				onReposition={schedulePosition}
			/>
		{/key}
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
	.alternatives, .feedback { margin: 0.45rem 0 0; font-size: 0.85rem; line-height: 1.4; }
	.popover-actions { display: flex; flex-wrap: wrap; align-items: center; gap: 0.45rem; margin-top: 0.8rem; }
	.popover-actions button { padding: 0.35rem 0.55rem; border: 1px solid var(--reader-border); border-radius: 999px; background: transparent; color: inherit; cursor: pointer; }
	.popover-actions button:hover, .popover-actions button:focus-visible { background: color-mix(in srgb, var(--reader-accent) 16%, transparent); }
	.close-action { margin-left: auto; font-size: 1.15rem; line-height: 1; }
	.feedback[role='alert'] { color: var(--reader-danger); }

	.subject-selector {
		display: flex;
		flex-wrap: wrap;
		gap: 0.35rem;
		margin-top: 0.55rem;
	}

	.subject-selector button {
		padding: 0.2rem 0.5rem;
		border: 1px solid var(--reader-border);
		border-radius: 999px;
		background: transparent;
		color: var(--reader-muted);
		font-size: 0.75rem;
		cursor: pointer;
	}

	.subject-selector button.selected {
		background: color-mix(in srgb, var(--reader-accent) 16%, transparent);
		color: inherit;
		border-color: var(--reader-accent);
	}
</style>
