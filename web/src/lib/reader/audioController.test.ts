import { describe, expect, it, vi } from 'vitest';
import type { AudioRef } from '$lib/api/client';
import { HoverAudioController, type AudioLike } from './audioController';

const ref = (id: string): AudioRef => ({ render_id: id, url: `/api/v1/audio/${id}`, ready: true, duration_ms: 100, size_bytes: 10, error_code: '' });

function fakeAudio(): AudioLike & { plays: number; pauses: number } {
	return {
		preload: '', src: '', muted: false, currentTime: 0, paused: true, plays: 0, pauses: 0,
		play: vi.fn(async function (this: AudioLike & { plays: number; pauses: number }) { this.paused = false; this.plays += 1; }),
		pause: vi.fn(function (this: AudioLike & { plays: number; pauses: number }) { this.paused = true; this.pauses += 1; }),
		load: vi.fn()
	};
}

describe('HoverAudioController', () => {
	it('debounces, cancels rather than queues, and deduplicates identical hovers', async () => {
		vi.useFakeTimers();
		const audio = fakeAudio();
		const controller = new HoverAudioController(() => audio);
		controller.setEnabled(true);
		controller.enter(ref('one'), 'one');
		controller.enter(ref('two'), 'two');
		await vi.advanceTimersByTimeAsync(149);
		expect(audio.plays).toBe(0);
		await vi.advanceTimersByTimeAsync(1);
		expect(audio.plays).toBe(1);
		controller.enter(ref('two'), 'two');
		await vi.advanceTimersByTimeAsync(200);
		expect(audio.plays).toBe(1);
		controller.enter(ref('three'), 'three');
		expect(audio.pauses).toBe(1);
		controller.destroy();
		vi.useRealTimers();
	});
});
