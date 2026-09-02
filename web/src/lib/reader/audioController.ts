import type { AudioRef } from '$lib/api/client';

export interface AudioLike {
	preload: string;
	src: string;
	muted: boolean;
	currentTime: number;
	paused: boolean;
	play: () => Promise<void>;
	pause: () => void;
	load: () => void;
}

export type AudioFactory = () => AudioLike;

export type HoverAudioOptions = {
	/** Called once after a hover play is rejected by the browser's
	 * autoplay policy. The preference itself is never disabled. */
	onBlocked?: () => void;
};

/** One-cue pronunciation playback: new hover cancels the pending/active cue. */
export class HoverAudioController {
	private readonly createAudio: AudioFactory;
	private readonly onBlocked?: () => void;
	private audio: AudioLike | undefined;
	private timer: ReturnType<typeof setTimeout> | undefined;
	private pendingKey = '';
	private activeKey = '';
	private enabled = false;
	private unlocked = false;
	private blockedNotified = false;

	constructor(createAudioOrOptions: AudioFactory | HoverAudioOptions = () => new Audio(), maybeOptions?: HoverAudioOptions) {
		if (typeof createAudioOrOptions === 'function') {
			this.createAudio = createAudioOrOptions;
			this.onBlocked = maybeOptions?.onBlocked;
		} else {
			this.createAudio = () => new Audio();
			this.onBlocked = createAudioOrOptions.onBlocked;
		}
	}

	setEnabled(enabled: boolean): void {
		this.enabled = enabled;
		if (!enabled) this.cancel();
	}

	get isEnabled(): boolean {
		return this.enabled;
	}

	enter(ref: AudioRef | null | undefined, key: string): void {
		if (!this.enabled) return;
		if (key === this.pendingKey || key === this.activeKey) return;
		this.clearTimer();
		this.stopCurrent();
		this.pendingKey = key;
		if (!ref?.ready || !ref.url) {
			this.pendingKey = '';
			return;
		}
		this.timer = setTimeout(() => {
			this.timer = undefined;
			const pendingKey = this.pendingKey;
			this.pendingKey = '';
			if (!this.enabled || pendingKey !== key) return;
			void this.play(ref, key);
		}, 150);
	}

	leave(key?: string): void {
		if (key && this.pendingKey && key !== this.pendingKey) return;
		this.clearTimer();
		this.pendingKey = '';
	}

	async unlock(): Promise<void> {
		if (this.unlocked) return;
		const audio = this.audio ?? this.createAudio();
		this.audio = audio;
		audio.muted = true;
		try {
			await audio.play();
			audio.pause();
			audio.currentTime = 0;
			this.unlocked = true;
			this.blockedNotified = false;
		} catch {
			// A rejected unlock is harmless; a later activation retries.
		}
	}

	async playNow(ref: AudioRef | null | undefined, key = 'explicit'): Promise<void> {
		if (!ref?.ready || !ref.url) return;
		this.clearTimer();
		this.pendingKey = '';
		this.stopCurrent();
		await this.play(ref, key);
	}

	prefetch(refs: Array<AudioRef | null | undefined>, max = 2): void {
		let count = 0;
		for (const ref of refs) {
			if (count >= max) break;
			if (!ref?.ready || !ref.url) continue;
			const audio = this.createAudio();
			audio.preload = 'metadata';
			audio.src = ref.url;
			audio.load();
			count += 1;
		}
	}

	cancel(): void {
		this.clearTimer();
		this.pendingKey = '';
		this.stopCurrent();
	}

	destroy(): void {
		this.cancel();
		this.audio = undefined;
	}

	private async play(ref: AudioRef, key: string): Promise<void> {
		const audio = this.audio ?? this.createAudio();
		this.audio = audio;
		this.activeKey = key;
		audio.muted = false;
		audio.src = ref.url;
		audio.currentTime = 0;
		try {
			await audio.play();
			this.blockedNotified = false;
		} catch (cause) {
			if (this.activeKey === key) this.activeKey = '';
			const name = cause instanceof DOMException ? cause.name : cause instanceof Error ? cause.name : '';
			if (name === 'NotAllowedError' && !this.blockedNotified) {
				// The saved preference stays enabled; only the hint is new.
				this.blockedNotified = true;
				this.onBlocked?.();
			}
		}
	}

	private stopCurrent(): void {
		if (this.audio && !this.audio.paused) this.audio.pause();
		this.activeKey = '';
	}

	private clearTimer(): void {
		if (this.timer) clearTimeout(this.timer);
		this.timer = undefined;
	}
}
