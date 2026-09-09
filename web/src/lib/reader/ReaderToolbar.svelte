<script lang="ts">
	import { readerThemes, type ReaderTheme } from './theme';
	import type { ReadingMode } from './readingMode';

	let {
		hoverEnabled,
		hoverSaving,
		theme,
		onToggleHover,
		onTheme,
		enlargeFocus = true,
		onFocusMode = (_value: boolean) => {},
		readingMode = 'learning',
		onReadingMode = (_value: ReadingMode) => {}
	}: {
		hoverEnabled: boolean;
		hoverSaving: boolean;
		theme: ReaderTheme;
		onToggleHover: () => void;
		onTheme: (theme: ReaderTheme) => void;
		enlargeFocus?: boolean;
		onFocusMode?: (value: boolean) => void;
		readingMode?: ReadingMode;
		onReadingMode?: (value: ReadingMode) => void;
	} = $props();
	let appearanceOpen = $state(false);
</script>

<div class="reader-toolbar" class:appearance-open={appearanceOpen} aria-label="Reader controls">
	<div class="mode-picker" role="group" aria-label="Reading mode">
		<button type="button" aria-pressed={readingMode === 'learning'} onclick={() => onReadingMode('learning')}>Learning</button><button type="button" aria-pressed={readingMode === 'condensed'} onclick={() => onReadingMode('condensed')}>Condensed</button>
	</div>
	<label class="focus-picker"><span>Focus</span>
		<select aria-label="Sentence focus style" value={enlargeFocus ? 'enlarge' : 'highlight'} onchange={(event) => onFocusMode(event.currentTarget.value === 'enlarge')}>
			<option value="enlarge">Enlarge without reflow</option><option value="highlight">Highlight only</option>
		</select>
	</label>
	<button type="button" class="mobile-appearance" aria-label="Reading appearance" aria-expanded={appearanceOpen} onclick={() => appearanceOpen = !appearanceOpen}>Aa</button>
	<label class="hover-toggle">
		<input type="checkbox" checked={hoverEnabled} disabled={hoverSaving} onchange={onToggleHover} />
		<span>Pronounce on hover</span>
	</label>
	<div class="theme-picker" role="group" aria-label="Reading theme">
		{#each readerThemes as option}<button type="button" aria-pressed={theme === option} onclick={() => onTheme(option)}>{option === 'midnight' ? 'Ink' : option === 'high-contrast' ? 'Contrast' : option[0]?.toUpperCase() + option.slice(1)}</button>{/each}
	</div>
</div>

<style>
	.reader-toolbar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.8rem;
		padding: 0.85rem 1.5rem;
		border-block: 1px solid var(--reader-border);
		color: var(--reader-muted);
		font-size: 0.85rem;
	}

	.hover-toggle, .theme-picker, .focus-picker, .mode-picker { display: inline-flex; align-items: center; gap: 0.45rem; }
	select, .theme-picker button, .mode-picker button { padding: 0.5rem 0.65rem; border: 1px solid var(--reader-border); border-radius: 0.6rem; background: transparent; color: var(--reader-muted); }
	.theme-picker button, .mode-picker button { cursor: pointer; }
	.theme-picker button[aria-pressed='true'], .mode-picker button[aria-pressed='true'] { border-color: var(--reader-accent); color: var(--reader-accent); background: color-mix(in srgb, var(--reader-accent) 9%, transparent); }
	.mobile-appearance { display: none; }
	@media (max-width: 850px) { .reader-toolbar { flex-wrap: wrap; justify-content: flex-start; padding-inline: 0.7rem; } }
	@media (max-width: 600px) {
		.reader-toolbar { gap: 0.5rem; padding: 0.35rem 0.35rem; }
		.focus-picker { flex: 1; min-width: 0; }
		.focus-picker > span { display: none; }
		.focus-picker select { width: 100%; min-width: 0; font-size: 0.8rem; }
		.mobile-appearance { display: block; padding: 0.5rem 0.7rem; border: 1px solid var(--reader-border); border-radius: 0.6rem; background: transparent; color: var(--reader-accent); font: inherit; }
		.reader-toolbar:not(.appearance-open) .hover-toggle, .reader-toolbar:not(.appearance-open) .theme-picker { display: none; }
		.hover-toggle, .theme-picker { flex-basis: 100%; }
	}
</style>
