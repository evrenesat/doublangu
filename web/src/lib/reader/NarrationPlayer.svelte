<script lang="ts">
	import type { Narration } from '$lib/api/client';

	type Props = {
		narration: Narration | null;
		activeIndex: number;
		playing: boolean;
		speed: number;
		followFocus: boolean;
		loading: boolean;
		onPlay: () => void;
		onPause: () => void;
		onPrevious: () => void;
		onNext: () => void;
		onEnded: () => void;
		onSpeed: (speed: number) => void;
		onFollowFocus: () => void;
		onRegenerate: () => void;
		onClear: () => Promise<void>;
	};

	let {
		narration,
		activeIndex,
		playing,
		speed,
		followFocus,
		loading,
		onPlay,
		onPause,
		onPrevious,
		onNext,
		onEnded,
		onSpeed,
		onFollowFocus,
		onRegenerate,
		onClear
	}: Props = $props();

	let audio: HTMLAudioElement | null = $state(null);
	let clearing = $state(false);
	let playbackError = $state('');
	let loadedURL = '';

	const activeClip = $derived(narration?.clips[activeIndex] ?? null);
	const activeURL = $derived(activeClip?.audio?.ready ? activeClip.audio.url : '');
	const progress = $derived(narration && narration.sentence_count > 0 ? ((activeIndex + 1) / narration.sentence_count) * 100 : 0);
	const hasReadyAudio = $derived(Boolean(narration && narration.ready_count > 0));

	$effect(() => {
		const player = audio;
		if (!player) return;
		player.playbackRate = speed;
		if (activeURL && activeURL !== loadedURL) {
			loadedURL = activeURL;
			player.src = activeURL;
			player.load();
		} else if (!activeURL && loadedURL) {
			loadedURL = '';
			player.pause();
			player.removeAttribute('src');
			player.load();
		}
		if (playing && activeURL) {
			playbackError = '';
			void player.play().catch(() => {
				playbackError = 'Playback needs a browser interaction. Press Play again.';
			});
		} else {
			player.pause();
		}
	});

	function togglePlayback(): void {
		if (playing) onPause();
		else onPlay();
	}

	async function clear(): Promise<void> {
		if (!narration || clearing) return;
		const reclaimable = formatBytes(narration.reclaimable_bytes);
		const retained = narration.size_bytes > narration.reclaimable_bytes ? ` ${formatBytes(narration.size_bytes - narration.reclaimable_bytes)} remains shared.` : '';
		if (!window.confirm(`Clear sentence narration? ${reclaimable} can be reclaimed.${retained} Word pronunciation and analysis will remain.`)) return;
		clearing = true;
		try {
			await onClear();
		} finally {
			clearing = false;
		}
	}

	function formatBytes(value: number): string {
		if (value < 1024) return `${value} B`;
		if (value < 1024 * 1024) return `${Math.round(value / 1024)} KB`;
		return `${(value / (1024 * 1024)).toFixed(1)} MB`;
	}
</script>

<section class="narration-player" aria-label="Article narration">
	<div class="player-heading">
		<div>
			<strong>Article narration</strong>
			<span class="player-status">
				{#if loading}Loading clips…{:else if narration?.status === 'ready'}Ready{:else if narration?.status === 'partial'}Partly ready{:else if narration?.status === 'purged'}Cleared{:else if narration?.status === 'failed'}Generation failed{:else if hasReadyAudio}Preparing remaining clips…{:else}Waiting for the worker…{/if}
			</span>
		</div>
		{#if narration?.reclaimable_bytes || narration?.size_bytes}<span class="storage">{formatBytes(narration.reclaimable_bytes || narration.size_bytes)} stored</span>{/if}
	</div>

	<div class="progress-track" role="progressbar" aria-label="Narration progress" aria-valuemin="0" aria-valuemax="100" aria-valuenow={progress}>
		<span style={`width: ${progress}%`}></span>
	</div>
	<p class="clip-status" aria-live="polite">
		{#if narration && narration.sentence_count > 0}Sentence {Math.min(activeIndex + 1, narration.sentence_count)} of {narration.sentence_count}{:else}No sentence clips yet{/if}
	</p>

	<div class="player-controls">
		<button type="button" aria-label="Previous sentence" disabled={activeIndex <= 0} onclick={onPrevious}>Previous</button>
		<button type="button" class="play" disabled={loading || !activeURL && !hasReadyAudio} onclick={togglePlayback}>{playing ? 'Pause' : 'Play'}</button>
		<button type="button" aria-label="Next sentence" disabled={!narration || activeIndex >= narration.sentence_count - 1} onclick={onNext}>Next</button>
	</div>

	<div class="secondary-controls">
		<span>Speed</span>
		{#each [0.75, 1, 1.25] as option}
			<button type="button" aria-pressed={speed === option} class:active={speed === option} onclick={() => onSpeed(option)}>{option}×</button>
		{/each}
		<label><input type="checkbox" checked={followFocus} onchange={onFollowFocus} /> Follow focus</label>
	</div>

	<div class="storage-controls">
		<button type="button" onclick={onRegenerate} disabled={loading}>Regenerate narration</button>
		<button type="button" onclick={() => void clear()} disabled={clearing || !narration || narration.sentence_count === 0}>Clear narration</button>
	</div>
	{#if playbackError}<p class="player-error" role="alert">{playbackError}</p>{/if}
	<audio bind:this={audio} onended={onEnded} onerror={() => (playbackError = 'This clip could not be played.')} preload="metadata" aria-hidden="true"></audio>
</section>

<style>
	.narration-player { display: grid; gap: 0.65rem; padding: 0.9rem; border: 1px solid var(--reader-border); border-radius: 0.7rem; background: var(--reader-surface); }
	.player-heading, .player-controls, .secondary-controls, .storage-controls { display: flex; align-items: center; flex-wrap: wrap; gap: 0.5rem; }
	.player-heading { justify-content: space-between; }
	.player-status, .storage, .clip-status { color: var(--reader-muted); font-size: 0.82rem; }
	.player-status { margin-left: 0.5rem; }
	.storage { white-space: nowrap; }
	.progress-track { height: 0.35rem; overflow: hidden; border-radius: 999px; background: var(--reader-border); }
	.progress-track span { display: block; height: 100%; border-radius: inherit; background: var(--reader-accent); transition: width 150ms ease; }
	.clip-status { margin: 0; }
	.player-controls button, .secondary-controls button, .storage-controls button { padding: 0.4rem 0.65rem; border: 1px solid var(--reader-border); border-radius: 0.45rem; background: transparent; color: inherit; cursor: pointer; }
	.player-controls .play, .secondary-controls button.active { background: var(--reader-accent); color: var(--reader-bg); }
	.player-controls button:disabled, .storage-controls button:disabled { cursor: not-allowed; opacity: 0.45; }
	.secondary-controls { color: var(--reader-muted); font-size: 0.83rem; }
	.secondary-controls label { display: inline-flex; align-items: center; gap: 0.35rem; margin-left: auto; }
	.storage-controls { border-top: 1px solid var(--reader-border); padding-top: 0.6rem; }
	.player-error { margin: 0; color: var(--reader-danger); font-size: 0.82rem; }
	@media (prefers-reduced-motion: reduce) { .progress-track span { transition: none; } }
</style>
