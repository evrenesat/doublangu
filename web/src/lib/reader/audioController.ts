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

/** One-cue pronunciation playback: new hover cancels the pending/active cue. */
export class HoverAudioController {
	private readonly createAudio: AudioFactory;
	private audio: AudioLike | undefined;
	private timer: ReturnType<typeof setTimeout> | undefined;
	private pendingKey = '';
	private activeKey = '';
	private enabled = false;
	private unlocked = false;

	constructor(createAudio: AudioFactory = () => new Audio()) {
		this.createAudio = createAudio;
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
		} catch {
			// A rejected unlock is harmless; a later explicit Hear action retries.
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
		} catch {
			if (this.activeKey === key) this.activeKey = '';
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
