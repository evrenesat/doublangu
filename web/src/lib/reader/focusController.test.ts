import { describe, expect, it, vi } from 'vitest';
import { compensateFocusReflow, sentenceIsFocusable } from './focusController';

describe('focusController', () => {
	it('compensates the viewport by the exact layout delta', () => {
		const element = document.createElement('span');
		let top = 100;
		vi.spyOn(element, 'getBoundingClientRect').mockImplementation(() => ({ top } as DOMRect));
		let callback: FrameRequestCallback | undefined;
		let scroll: [number, number] | undefined;
		compensateFocusReflow(element, () => { top = 136; }, (next) => { callback = next; return 1; }, (x, y) => { scroll = [x, y]; });
		callback?.(0);
		expect(scroll).toEqual([0, 36]);
	});

	it('recognizes reader sentence anchors', () => {
		const element = document.createElement('span');
		element.dataset.sentenceId = 'sentence-1';
		expect(sentenceIsFocusable(element)).toBe(true);
		expect(sentenceIsFocusable(null)).toBe(false);
	});
});
