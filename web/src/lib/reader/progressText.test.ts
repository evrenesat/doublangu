import { describe, expect, it } from 'vitest';
import { waitStateProgress } from './progressText';

describe('waitStateProgress', () => {
	it('reports the queued copy with a determinate count', () => {
		const shape = waitStateProgress({ analysis_status: 'queued', total: 4, completed: 1 });
		expect(shape.visible).toBe(true);
		expect(shape.kind).toBe('analysis');
		expect(shape.text).toBe('Waiting to analyze');
		expect(shape.detail).toBe('1 of 4 paragraphs');
		expect(shape.percent).toBeUndefined();
	});

	it('reports the processing paragraph and completed count once per paragraph', () => {
		const first = waitStateProgress({ analysis_status: 'processing', total: 3, completed: 1, current_block_index: 1 });
		expect(first.text).toBe('Analyzing paragraph 2 of 3');
		expect(first.detail).toBe('1 complete');
		expect(first.percent).toBe(33);
		expect(first.ariaValueNow).toBe(1);
		// No current block yet: the next pending paragraph is announced.
		const fallback = waitStateProgress({ analysis_status: 'processing', total: 3, completed: 1, current_block_index: -1 });
		expect(fallback.text).toBe('Analyzing paragraph 2 of 3');
	});

	it('reports a failed run and its stopped paragraph', () => {
		const shape = waitStateProgress({ analysis_status: 'failed', total: 3, completed: 1, failed_block_index: 1 });
		expect(shape.text).toBe('Stopped at paragraph 2');
		expect(shape.detail).toBe('1 of 3 paragraphs updated');
	});

	it('reports narration generation with ready sentence counts', () => {
		const shape = waitStateProgress({
			analysis_status: 'ready',
			narration_status: 'generating',
			ready_sentences: 2,
			total_sentences: 5
		});
		expect(shape.kind).toBe('narration');
		expect(shape.text).toBe('Generating narration');
		expect(shape.detail).toBe('2 of 5 sentences ready');
		expect(shape.percent).toBe(40);
	});

	it('stays hidden for a ready and idle article', () => {
		const shape = waitStateProgress({ analysis_status: 'ready', narration_status: 'ready' });
		expect(shape.visible).toBe(false);
	});
});
