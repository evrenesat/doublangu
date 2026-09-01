import type { ArticleAnnotation, ArticleBlock } from '$lib/api/client';

export class ArticleRunError extends Error {
	constructor(message: string) {
		super(message);
		this.name = 'ArticleRunError';
	}
}

export type ArticleRun =
	| { text: string; annotation?: undefined }
	| { text: string; annotation: ArticleAnnotation };

/**
 * Split a block into plain and annotated runs. JavaScript string offsets are
 * UTF-16 code-unit offsets, matching the backend contract and browser Range
 * APIs. Every source byte remains represented, even when an annotation is
 * malformed: callers can catch ArticleRunError and render block.source_text.
 */
export function buildRuns(block: ArticleBlock): ArticleRun[] {
	const text = block.source_text;
	const annotations = block.annotations;
	const runs: ArticleRun[] = [];
	let cursor = 0;

	for (const annotation of annotations) {
		if (!Number.isInteger(annotation.start_utf16) || !Number.isInteger(annotation.end_utf16)) {
			throw new ArticleRunError('annotation offsets must be integers');
		}
		if (annotation.start_utf16 < cursor || annotation.start_utf16 < 0 || annotation.end_utf16 <= annotation.start_utf16) {
			throw new ArticleRunError('annotation offsets overlap or are out of order');
		}
		if (
			annotation.end_utf16 > text.length ||
			!isUTF16Boundary(text, annotation.start_utf16) ||
			!isUTF16Boundary(text, annotation.end_utf16) ||
			text.slice(annotation.start_utf16, annotation.end_utf16) !== annotation.source_text
		) {
			throw new ArticleRunError('annotation source does not match its UTF-16 span');
		}
		if (annotation.start_utf16 > cursor) {
			runs.push({ text: text.slice(cursor, annotation.start_utf16) });
		}
		runs.push({ text: annotation.source_text, annotation });
		cursor = annotation.end_utf16;
	}

	if (cursor < text.length) runs.push({ text: text.slice(cursor) });
	if (runs.length === 0) runs.push({ text });
	return runs;
}

function isUTF16Boundary(text: string, offset: number): boolean {
	if (offset === 0 || offset === text.length) return true;
	const current = text.charCodeAt(offset);
	const previous = text.charCodeAt(offset - 1);
	return !(current >= 0xdc00 && current <= 0xdfff) && !(previous >= 0xd800 && previous <= 0xdbff);
}
