<script lang="ts">
	import { onDestroy } from 'svelte';
	import { getDictionaryEntry, getDictionaryEntryById, startDictionaryExplore, type DictionarySense } from '$lib/api/client';
	import { createExploreController, initialExploreState, type ExploreRef, type ExploreState } from './exploreController';

	type Props = {
		articleId: string;
		/** Exactly one of occurrenceId / annotationId identifies the subject. */
		occurrenceId?: string;
		annotationId?: string;
		onReposition?: () => void;
		/** Test seam: pin the controller used for polling and fetches. */
		_controller?: ReturnType<typeof createExploreController>;
	};

	let { articleId, occurrenceId = undefined, annotationId = undefined, onReposition = undefined, _controller = undefined }: Props = $props();

	let state = $state<ExploreState>({ ...initialExploreState });

	const ref = $derived.by((): ExploreRef => {
		if (occurrenceId) return { occurrenceId };
		if (annotationId) return { annotationId };
		return {};
	});
	const subjectKey = $derived(`${occurrenceId ?? ''}|${annotationId ?? ''}`);

	// svelte-ignore state_referenced_locally -- the test seam never changes after mount.
	const controller = _controller ?? createExploreController({
		lookup: (articleIdValue, exploreRef) => {
			void articleIdValue;
			return getDictionaryEntry(articleId, exploreRef);
		},
		start: (articleIdValue, input) => {
			void articleIdValue;
			return startDictionaryExplore(articleId, input);
		},
		poll: (entryId) => getDictionaryEntryById(entryId),
		onChange: (next) => {
			state = next;
			onReposition?.();
		}
	});

	// Each subject opens exactly one explicit flow. Reopening the same subject
	// must not retry a failed entry silently; a fresh subject always starts
	// from the read-only lookup.
	$effect(() => {
		const key = subjectKey;
		if (!key) return;
		void controller.open(ref, articleId, false);
		return () => controller.close();
	});

	onDestroy(() => controller.close());

	const statusLabel = $derived.by(() => {
		switch (state.phase) {
			case 'checking': return 'Checking dictionary…';
			case 'queued': return 'Queued…';
			case 'generating': return 'Generating explanation…';
			case 'failed': return 'Could not generate an explanation.';
			default: return '';
		}
	});

	function retry(): void {
		void controller.retry(ref, articleId);
	}

	function partOfSpeechLabel(value: string): string {
		return value.replaceAll('_', ' ');
	}
</script>

<div class="explore-panel" data-explore-phase={state.phase} data-explore-entry={state.entryId}>
	{#if state.phase !== 'ready'}
		<div class="explore-status" role="status">
			<span>{statusLabel}</span>
			{#if state.phase === 'failed'}
				<button type="button" class="retry-action" onclick={retry}>Retry</button>
			{/if}
		</div>
	{:else if state.document}
		<p class="explore-caption">Saved dictionary entry</p>
		<ol class="senses">
			{#each state.document.senses as sense, index (index)}
				{@render Sense({ sense, index })}
			{/each}
		</ol>
	{/if}
</div>

{#snippet Sense({ sense, index }: { sense: DictionarySense; index: number })}
	<li class="sense">
		<div class="sense-heading">
			<span class="sense-number">{index + 1}</span>
			<span class="sense-translation" lang="en">{sense.translation_en}</span>
			<span class="sense-pos">{partOfSpeechLabel(sense.part_of_speech)}</span>
		</div>
		<p class="sense-meaning" lang="en">{sense.meaning_en}</p>
		{#if sense.usage_en}
			<p class="sense-section"><span class="sense-label" lang="en">Usage</span><span lang="en">{sense.usage_en}</span></p>
		{/if}
		{#if sense.parts.length > 0}
			<div class="sense-section">
				<span class="sense-label" lang="en">Parts</span>
				<ul class="sense-parts">
					{#each sense.parts as part (part.source_nl)}
						<li><span lang="nl">{part.source_nl}</span> — <span lang="en">{part.explanation_en}</span></li>
					{/each}
				</ul>
			</div>
		{/if}
		<ul class="sense-examples">
			{#each sense.examples as example (example.text_nl)}
				<li class="example">
					<span class="example-nl" lang="nl">{example.text_nl}</span>
					<span class="example-en" lang="en">{example.translation_en}</span>
				</li>
			{/each}
		</ul>
	</li>
{/snippet}

<style>
	.explore-panel {
		margin-top: 0.75rem;
		border-top: 1px solid var(--reader-border, rgb(255 255 255 / 12%));
		padding-top: 0.65rem;
	}

	.explore-status {
		display: flex;
		align-items: center;
		gap: 0.6rem;
		color: var(--reader-muted);
		font-size: 0.82rem;
	}

	.explore-status:has(.retry-action) {
		color: var(--reader-danger, #ffabbc);
	}

	.retry-action {
		padding: 0.2rem 0.55rem;
		border: 1px solid currentColor;
		border-radius: 999px;
		background: transparent;
		color: inherit;
		font-size: 0.78rem;
		cursor: pointer;
	}

	.explore-caption {
		margin: 0 0 0.4rem;
		color: var(--reader-muted);
		font-size: 0.7rem;
		letter-spacing: 0.08em;
		text-transform: uppercase;
	}

	.senses {
		margin: 0;
		padding: 0;
		list-style: none;
		display: grid;
		gap: 0.7rem;
	}

	.sense-heading {
		display: flex;
		align-items: baseline;
		gap: 0.45rem;
	}

	.sense-number {
		color: var(--reader-accent);
		font-weight: 700;
		font-size: 0.8rem;
	}

	.sense-translation {
		font-weight: 650;
		font-size: 0.95rem;
	}

	.sense-pos {
		color: var(--reader-muted);
		font-size: 0.7rem;
		font-style: italic;
	}

	.sense-meaning,
	.sense-section {
		margin: 0.3rem 0 0;
		font-size: 0.85rem;
		line-height: 1.45;
	}

	.sense-label {
		display: inline-block;
		margin-right: 0.4rem;
		color: var(--reader-muted);
		font-size: 0.68rem;
		font-weight: 650;
		letter-spacing: 0.06em;
		text-transform: uppercase;
	}

	.sense-parts {
		margin: 0.2rem 0 0;
		padding: 0;
		list-style: none;
		display: grid;
		gap: 0.15rem;
		font-size: 0.82rem;
	}

	.sense-parts span[lang='nl'] {
		font-weight: 600;
	}

	.sense-examples {
		margin: 0.4rem 0 0;
		padding: 0;
		list-style: none;
		display: grid;
		gap: 0.3rem;
	}

	.example {
		display: grid;
		font-size: 0.82rem;
		line-height: 1.4;
	}

	.example-nl {
		font-weight: 550;
	}

	.example-en {
		color: var(--reader-muted);
	}
</style>
