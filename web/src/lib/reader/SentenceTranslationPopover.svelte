<script lang="ts">
	import { onMount } from 'svelte';
	import { getSentenceTranslation, startSentenceTranslation } from '$lib/api/client';
	import { appPath } from '$lib/paths';
	import {
		createSentenceTranslationController,
		initialSentenceTranslationState,
		type SentenceTranslationState
	} from './sentenceTranslationController';

	type Props = {
		articleId: string;
		sentenceId: string;
		/** Source text for the dialog label; the shown translation always follows sentenceId. */
		sentenceLabel: string;
		anchor: HTMLElement | null;
		/** When true, a still-missing translation is generated once. Set by the Translate control after its dwell. */
		autoEnsure: boolean;
		onEnter: () => void;
		onLeave: () => void;
		onClose: () => void;
		onReposition?: () => void;
		/** Test seam: pin the lookup/generation backend used by the controller. */
		_backend?: {
			lookup: typeof getSentenceTranslation;
			start: typeof startSentenceTranslation;
			poll: typeof getSentenceTranslation;
			pollIntervalMs?: number;
		};
	};

	let {
		articleId,
		sentenceId,
		sentenceLabel,
		anchor,
		autoEnsure,
		onEnter,
		onLeave,
		onClose,
		onReposition = undefined,
		_backend = undefined
	}: Props = $props();

	let translationState: SentenceTranslationState = $state({ ...initialSentenceTranslationState });
	let popover: HTMLDivElement | null = $state(null);
	let bottomSheet = $state(false);
	let frame = 0;
	let resizeObserver: ResizeObserver | undefined;

	// svelte-ignore state_referenced_locally -- the test seam never changes after mount.
	const backend = _backend ?? {
		lookup: (articleIdValue: string, sentenceIdValue: string) => {
			void articleIdValue;
			return getSentenceTranslation(articleId, sentenceIdValue);
		},
		start: (
			articleIdValue: string,
			sentenceIdValue: string,
			input: { mode: 'ensure' | 'regenerate' }
		) => {
			void articleIdValue;
			return startSentenceTranslation(articleId, sentenceIdValue, input);
		},
		poll: (articleIdValue: string, sentenceIdValue: string) => {
			void articleIdValue;
			return getSentenceTranslation(articleId, sentenceIdValue);
		}
	};
	const controller = createSentenceTranslationController({
		...backend,
		onChange: (next) => {
			translationState = next;
			onReposition?.();
		}
	});

	// Each sentence opens exactly one read-only lookup. Generation only
	// follows the explicit autoEnsure signal, so an abandoned hover never
	// starts provider work. A fresh sentence always starts from the lookup
	// and never inherits the previous sentence's response.
	$effect(() => {
		if (!articleId || !sentenceId) return;
		void controller.open(articleId, sentenceId);
		return () => controller.close();
	});

	$effect(() => {
		if (autoEnsure && articleId && sentenceId && translationState.phase === 'missing') {
			void controller.ensure(articleId, sentenceId);
		}
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
		document.addEventListener('pointerdown', handleOutside, true);
		document.addEventListener('keydown', handleKeydown);
		window.addEventListener('resize', schedulePosition);
		window.addEventListener('scroll', schedulePosition, true);
		return () => {
			document.removeEventListener('pointerdown', handleOutside, true);
			document.removeEventListener('keydown', handleKeydown);
			window.removeEventListener('resize', schedulePosition);
			window.removeEventListener('scroll', schedulePosition, true);
			if (frame) cancelAnimationFrame(frame);
			frame = 0;
		};
	});

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
		const left = Math.min(
			Math.max(margin, anchorRect.left + (anchorRect.width - popoverRect.width) / 2),
			window.innerWidth - popoverRect.width - margin
		);
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

	const showingCurrent = $derived(translationState.sentenceId === sentenceId);
	const savedTranslation = $derived(showingCurrent ? translationState.translation : null);
	const pending = $derived(translationState.phase === 'queued' || translationState.phase === 'running');
	const runHref = $derived(translationState.runId ? appPath(`/analysis-runs/${translationState.runId}`) : '');

	const statusLabel = $derived.by(() => {
		switch (translationState.phase) {
			case 'checking':
				return 'Checking translation…';
			case 'missing':
				return 'Preparing translation…';
			case 'queued':
			case 'running':
				return savedTranslation ? 'Replacing translation…' : 'Generating translation…';
			case 'failed':
				return translationState.errorSummary || 'Could not generate a translation.';
			default:
				return '';
		}
	});

	function retry(): void {
		// An initial failure never retries on hover; only this explicit
		// button starts a replacement generation.
		void controller.regenerate(articleId, sentenceId);
	}

	function regenerate(): void {
		void controller.regenerate(articleId, sentenceId);
	}
</script>

<div
	class="sentence-translation-popover"
	class:bottom-sheet={bottomSheet}
	bind:this={popover}
	role="dialog"
	tabindex="-1"
	aria-label={sentenceId ? `Sentence translation: ${sentenceLabel}` : 'Sentence translation'}
	data-sentence-translation-phase={translationState.phase}
	onpointerenter={onEnter}
	onpointerleave={onLeave}
	onfocusin={onEnter}
	onfocusout={onLeave}
>
	{#if !sentenceId}
		<p class="translation-status" role="status">No active sentence</p>
	{:else if translationState.phase === 'ready' && savedTranslation}
		<p class="translation-caption">Saved sentence translation</p>
		<p class="translation-text" lang="en">{savedTranslation}</p>
		<div class="translation-actions">
			<button type="button" onclick={regenerate}>Regenerate</button>
			{#if runHref}<a class="run-link" href={runHref}>View run</a>{/if}
		</div>
	{:else}
		{#if savedTranslation}
			<p class="translation-caption">Saved sentence translation</p>
			<p class="translation-text retained" lang="en">{savedTranslation}</p>
		{/if}
		{#if translationState.phase === 'failed'}
			<p class="translation-status failure" role="alert">{statusLabel}</p>
			<div class="translation-actions">
				{#if savedTranslation}
					<button type="button" disabled={pending} onclick={regenerate}>
						{pending ? 'Regenerating…' : 'Retry regeneration'}
					</button>
				{:else}
					<button type="button" onclick={retry}>Retry</button>
				{/if}
				{#if runHref}<a class="run-link" href={runHref}>View run</a>{/if}
			</div>
		{:else}
			<p class="translation-status" role="status">
				<span>{statusLabel}</span>
				{#if pending && savedTranslation}
					<button type="button" disabled>Regenerating…</button>
				{/if}
			</p>
		{/if}
	{/if}
</div>

<style>
	.sentence-translation-popover {
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

	.sentence-translation-popover.bottom-sheet {
		border-radius: 0.9rem;
	}

	.translation-caption {
		margin: 0 0 0.4rem;
		color: var(--reader-muted);
		font-size: 0.7rem;
		letter-spacing: 0.08em;
		text-transform: uppercase;
	}

	.translation-text {
		margin: 0;
		font-size: 1rem;
		line-height: 1.5;
	}

	.translation-status {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		margin: 0;
		color: var(--reader-muted);
		font-size: 0.85rem;
	}

	.translation-status.failure {
		color: var(--reader-danger);
	}

	.translation-actions {
		display: flex;
		flex-wrap: wrap;
		align-items: center;
		gap: 0.45rem;
		margin-top: 0.8rem;
	}

	.translation-actions button {
		padding: 0.35rem 0.55rem;
		border: 1px solid var(--reader-border);
		border-radius: 999px;
		background: transparent;
		color: inherit;
		cursor: pointer;
	}

	.translation-actions button:hover:not(:disabled),
	.translation-actions button:focus-visible:not(:disabled) {
		background: color-mix(in srgb, var(--reader-accent) 16%, transparent);
	}

	.translation-actions button:disabled {
		cursor: wait;
		opacity: 0.6;
	}

	.translation-status button {
		padding: 0.35rem 0.55rem;
		border: 1px solid currentColor;
		border-radius: 999px;
		background: transparent;
		color: inherit;
		cursor: pointer;
	}

	.translation-status button:disabled {
		cursor: wait;
		opacity: 0.6;
	}

	.run-link {
		color: var(--reader-accent);
		font-size: 0.82rem;
	}
</style>
