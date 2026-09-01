/**
 * Preserves the sentence's viewport position when its focused style changes
 * font metrics or surrounding line wrapping.
 */
export function compensateFocusReflow(
	element: HTMLElement,
	mutate: () => void,
	frame: (callback: FrameRequestCallback) => number = (callback) => requestAnimationFrame(callback),
	scroll: (x: number, y: number) => void = (x, y) => window.scrollBy(x, y)
): () => void {
	const before = element.getBoundingClientRect().top;
	mutate();
	const handle = frame(() => {
		const delta = element.getBoundingClientRect().top - before;
		if (delta !== 0) scroll(0, delta);
	});
	return () => cancelAnimationFrame(handle);
}

export function sentenceIsFocusable(element: HTMLElement | null): boolean {
	return Boolean(element && element.matches('[data-sentence-id]'));
}
