import { describe, expect, it } from 'vitest';
import type { ArticleBlock, ArticleOccurrence } from '$lib/api/client';
import { SemanticRunError, buildSemanticRuns } from './semanticRuns';

const occurrence = (overrides: Partial<ArticleOccurrence>): ArticleOccurrence => ({
	id: '01J00000000000000000000010',
	article_block_id: '01J00000000000000000000001',
	article_sentence_id: null,
	semantic_sense_id: null,
	kind: 'word',
	role: 'token',
	shadow_policy: 'token',
	shadow_text: 'hint',
	confidence_milli: 900,
	sense: null,
	learning_state: null,
	show_shadow: true,
	subtitle_suppression_reason: 'none',
	member_occurrence_ids: [],
	pronunciation: null,
	canonical_pronunciation_text: '',
	context_pronunciation_key: '',
	spans: [{ id: '01J00000000000000000000011', article_occurrence_id: '01J00000000000000000000010', span_index: 0, start_utf16: 0, end_utf16: 1, source_text: 'x' }],
	...overrides
});

function block(source_text: string, occurrences: ArticleOccurrence[]): ArticleBlock {
	return {
		id: '01J00000000000000000000001',
		article_id: '01J00000000000000000000000',
		block_index: 0,
		kind: 'paragraph',
		source_text,
		annotations: [],
		sentences: [],
		occurrences
	};
}

describe('buildSemanticRuns', () => {
	it('keeps exact source text and groups a contiguous construction once', () => {
		const tokenA = occurrence({ id: 'token-a', spans: [{ id: 'span-a', article_occurrence_id: 'token-a', span_index: 0, start_utf16: 0, end_utf16: 3, source_text: 'tot' }] });
		const tokenB = occurrence({ id: 'token-b', spans: [{ id: 'span-b', article_occurrence_id: 'token-b', span_index: 0, start_utf16: 4, end_utf16: 8, source_text: 'rust' }] });
		const group = occurrence({ id: 'group', kind: 'expression', role: 'contiguous_construction', shadow_policy: 'group', spans: [{ id: 'group-span', article_occurrence_id: 'group', span_index: 0, start_utf16: 0, end_utf16: 8, source_text: 'tot rust' }] });
		const runs = buildSemanticRuns(block('tot rust.', [tokenA, tokenB, group]));
		expect(runs.map((run) => run.text).join('')).toBe('tot rust.');
		expect(runs.filter((run) => run.kind === 'occurrence')).toHaveLength(1);
		expect(runs[0]?.kind === 'occurrence' && runs[0].occurrence.id).toBe('group');
	});

	it('routes a discontinuous member to one shared construction popover', () => {
		const first = occurrence({ id: 'first', spans: [{ id: 'first-span', article_occurrence_id: 'first', span_index: 0, start_utf16: 0, end_utf16: 4, source_text: 'geef' }] });
		const second = occurrence({ id: 'second', spans: [{ id: 'second-span', article_occurrence_id: 'second', span_index: 0, start_utf16: 9, end_utf16: 11, source_text: 'op' }] });
		const construction = occurrence({ id: 'construction', kind: 'expression', role: 'discontinuous_construction', shadow_policy: 'marker', spans: [
			{ id: 'construction-a', article_occurrence_id: 'construction', span_index: 0, start_utf16: 0, end_utf16: 4, source_text: 'geef' },
			{ id: 'construction-b', article_occurrence_id: 'construction', span_index: 1, start_utf16: 9, end_utf16: 11, source_text: 'op' }
		] });
		const runs = buildSemanticRuns(block('geef het op', [first, second, construction]));
		const members = runs.filter((run) => run.kind === 'occurrence');
		expect(members).toHaveLength(2);
		for (const run of members) {
			if (run.kind === 'occurrence') {
				expect(run.constructionIDs).toEqual(['construction']);
				expect(run.popoverOccurrence.id).toBe('construction');
			}
		}
	});

	it('rejects a source mismatch and a surrogate split', () => {
		const bad = occurrence({ spans: [{ id: 'bad', article_occurrence_id: 'bad', span_index: 0, start_utf16: 0, end_utf16: 1, source_text: 'z' }] });
		expect(() => buildSemanticRuns(block('x', [bad]))).toThrow(SemanticRunError);
		const emoji = occurrence({ spans: [{ id: 'emoji', article_occurrence_id: 'emoji', span_index: 0, start_utf16: 1, end_utf16: 2, source_text: '\udc4b' }] });
		expect(() => buildSemanticRuns(block('😀', [emoji]))).toThrow(SemanticRunError);
	});
});
