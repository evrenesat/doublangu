<script lang="ts">
	import { readerThemes, type ReaderTheme } from './theme';

	let {
		hoverEnabled,
		theme,
		onToggleHover,
		onTheme
	}: {
		hoverEnabled: boolean;
		theme: ReaderTheme;
		onToggleHover: () => void;
		onTheme: (theme: ReaderTheme) => void;
	} = $props();
</script>

<div class="reader-toolbar" aria-label="Reader controls">
	<label class="hover-toggle">
		<input type="checkbox" checked={hoverEnabled} onchange={onToggleHover} />
		<span>Pronounce on hover</span>
	</label>
	<label class="theme-picker">
		<span>Theme</span>
		<select value={theme} onchange={(event) => onTheme((event.currentTarget as HTMLSelectElement).value as ReaderTheme)}>
			{#each readerThemes as option}<option value={option}>{option === 'high-contrast' ? 'High contrast' : option[0]?.toUpperCase() + option.slice(1)}</option>{/each}
		</select>
	</label>
</div>

<style>
	.reader-toolbar {
		display: flex;
		align-items: center;
		justify-content: space-between;
		gap: 0.8rem;
		padding: 0.65rem 0.75rem;
		border: 1px solid var(--reader-border);
		border-radius: 0.65rem;
		background: var(--reader-surface);
		color: var(--reader-muted);
		font-size: 0.85rem;
	}

	.hover-toggle, .theme-picker { display: inline-flex; align-items: center; gap: 0.45rem; }
	.theme-picker select { padding: 0.25rem 0.4rem; border: 1px solid var(--reader-border); border-radius: 0.35rem; background: var(--reader-surface-raised); color: var(--reader-text); }
	@media (max-width: 520px) { .reader-toolbar { align-items: flex-start; flex-direction: column; } }
</style>
