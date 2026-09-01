import { describe, expect, it } from 'vitest';
import type { ArticleAnnotation, ArticleBlock } from '$lib/api/client';
import { ArticleRunError, buildRuns } from './buildRuns';

const annotation = (overrides: Partial<ArticleAnnotation>): ArticleAnnotation => ({
	id: '01J00000000000000000000000',
	article_block_id: '01J00000000000000000000001',
	start_utf16: 0,
	end_utf16: 1,
	source_text: 'x',
	kind: 'word',
	learning_key: 'x',
	primary_translation: 'x',
	 alternatives: [],
	literal_translation: '',
	meaning_note: '',
	usage_note: '',
	parts_note: '',
	suggest_shadow: true,
	learning_state: null,
	show_shadow: true,
	...overrides
});
function block(source_text: string, annotations: ArticleAnnotation[]): ArticleBlock {
	return {
		id: '01J00000000000000000000001',
		article_id: '01J00000000000000000000000',
		block_index: 0,
		kind: 'paragraph',
		source_text,
		annotations,
		sentences: [],
		occurrences: []
	};
}

describe('buildRuns', () => {
	it('uses UTF-16 offsets around non-BMP source text and keeps every character', () => {
		const runs = buildRuns(block('Hoi 👋 wereld', [annotation({ start_utf16: 7, end_utf16: 13, source_text: 'wereld' })]));
		expect(runs.map((run) => run.text)).toEqual(['Hoi 👋 ', 'wereld']);
		expect(runs[1]?.annotation?.source_text).toBe('wereld');
	});

	it('keeps a contiguous phrase as one annotated run', () => {
		const runs = buildRuns(block('tot rust komen', [annotation({
			start_utf16: 0,
			end_utf16: 14,
			source_text: 'tot rust komen',
			kind: 'expression'
		})]));
		expect(runs).toHaveLength(1);
		expect(runs[0]?.annotation?.kind).toBe('expression');
	});

	it('rejects unsorted, overlapping, and source-mismatched annotations', () => {
		const first = annotation({ start_utf16: 0, end_utf16: 2, source_text: 'ab' });
		const second = annotation({ start_utf16: 1, end_utf16: 3, source_text: 'bc' });
		expect(() => buildRuns(block('abc', [first, second]))).toThrow(ArticleRunError);
		expect(() => buildRuns(block('abc', [annotation({ start_utf16: 1, end_utf16: 2, source_text: 'b' }), first]))).toThrow(ArticleRunError);
		expect(() => buildRuns(block('abc', [annotation({ start_utf16: 0, end_utf16: 2, source_text: 'zz' })]))).toThrow(ArticleRunError);
	});

	it('rejects a span that splits an emoji surrogate pair', () => {
		expect(() => buildRuns(block('👋', [annotation({ start_utf16: 1, end_utf16: 2, source_text: '\udc4b' })]))).toThrow(ArticleRunError);
	});
});
