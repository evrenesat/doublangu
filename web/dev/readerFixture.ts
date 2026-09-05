import type { Article, ArticleBlock, ArticleOccurrence } from '../src/lib/api/client';

export const readerFixtureID = '01J00000000000000000000DEMO';
type Expression = { words: number[]; label: string; meaning: string; split?: boolean };

export function paragraph(index: number, source: string, glosses: string[], expressions: Expression[]): ArticleBlock {
	const blockID = `demo-block-${index}`;
	const sentenceID = `demo-sentence-${index}`;
	const matches = Array.from(source.matchAll(/[\p{L}\p{N}]+(?:['’\-][\p{L}\p{N}]+)*/gu));
	if (matches.length !== glosses.length) throw new Error(`Fixture ${index}: each word needs a gloss`);
	const tokens: ArticleOccurrence[] = matches.map((match, tokenIndex) => {
		const id = `demo-${index}-word-${tokenIndex}`;
		const text = match[0];
		const gloss = glosses[tokenIndex]!;
		return {
			id, article_block_id: blockID, article_sentence_id: sentenceID,
			semantic_sense_id: `${id}-sense`, kind: 'word', role: 'token', shadow_policy: 'token',
			shadow_text: gloss, subtitle_suppression_reason: 'none', canonical_pronunciation_text: text,
			context_pronunciation_key: '', confidence_milli: 1000, learning_state: null, show_shadow: true,
			member_occurrence_ids: [], pronunciation: null,
			sense: { id: `${id}-sense`, semantic_item_id: `${id}-item`, kind: 'word', canonical_form: text,
				sense_discriminator: gloss, primary_translation: gloss, alternatives: [], literal_translation: gloss,
				meaning_note: text.toLowerCase() === 'bank' ? (gloss === 'sofa' ? 'A seat to sit on. The financial meaning is a separate learning item.' : 'A financial institution. The sofa meaning is a separate learning item.') : '',
				usage_note: '', parts_note: '', canonical_pronunciation_text: text },
			spans: [{ id: `${id}-span`, article_occurrence_id: id, span_index: 0, start_utf16: match.index!, end_utf16: match.index! + text.length, source_text: text }]
		};
	});
	const groups: ArticleOccurrence[] = expressions.map((expression, groupIndex) => {
		const id = `demo-${index}-expression-${groupIndex}`;
		const members = expression.words.map((wordIndex) => tokens[wordIndex]!);
		const spans = expression.split ? members.map((member, spanIndex) => ({ ...member.spans[0]!, id: `${id}-span-${spanIndex}`, article_occurrence_id: id, span_index: spanIndex }))
			: [{ id: `${id}-span`, article_occurrence_id: id, span_index: 0,
				start_utf16: members[0]!.spans[0]!.start_utf16, end_utf16: members.at(-1)!.spans[0]!.end_utf16,
				source_text: source.slice(members[0]!.spans[0]!.start_utf16, members.at(-1)!.spans[0]!.end_utf16) }];
		return { ...tokens[0]!, id, kind: 'expression', role: expression.split ? 'discontinuous_construction' : 'contiguous_construction',
			shadow_policy: expression.split ? 'marker' : 'group', shadow_text: expression.meaning,
			semantic_sense_id: `${id}-sense`, member_occurrence_ids: members.map((member) => member.id), spans,
			sense: { ...tokens[0]!.sense!, id: `${id}-sense`, kind: 'expression', canonical_form: expression.label,
				primary_translation: expression.meaning, literal_translation: members.map((member) => member.shadow_text).join(' '),
				meaning_note: 'The connected words form one expression. Each word keeps its own literal subtitle; the expression has a separate meaning.',
				parts_note: members.map((member) => `${member.spans[0]!.source_text}: ${member.shadow_text}`).join(' · ') } };
	});
	return { id: blockID, article_id: readerFixtureID, block_index: index, kind: 'paragraph', source_text: source,
		annotations: [], analysis_status: 'ready', has_analysis: true, analysis_is_current: true,
		sentences: [{ id: sentenceID, article_block_id: blockID, sentence_index: index, start_utf16: 0, end_utf16: source.length, source_text: source, source_hash: '0'.repeat(64), audio: null }],
		occurrences: [...tokens, ...groups] };
}

const blocks = [
	paragraph(0, 'Na het werk ging Noor op de bank zitten om tot rust te komen.',
		['After','the','work','went','Noor','on','the','sofa','sit','to','to','rest','to','come'],
		[{ words: [10,11,12,13], label: 'tot rust te komen', meaning: 'to calm down' }]),
	paragraph(1, 'Ze gaf haar plan niet op en belde een vriend.',
		['She','gave','her','plan','not','up','and','called','a','friend'],
		[{ words: [1,5], label: 'gaf … op', meaning: 'gave up', split: true }]),
	paragraph(2, 'De bank keurde haar aanvraag goed.',
		['The','bank','assessed','her','application','good'],
		[{ words: [2,5], label: 'keurde … goed', meaning: 'approved', split: true }]),
	paragraph(3, 'Hij gooide gisteren bijna het bijltje erbij neer.',
		['He','threw','yesterday','almost','the','little axe','therewith','down'],
		[{ words: [1,4,5,6,7], label: 'gooide … het bijltje erbij neer', meaning: 'gave up', split: true }]),
	paragraph(4, 'Ze viel met de deur in huis, maar hij luisterde rustig.',
		['She','fell','with','the','door','in','house','but','he','listened','calmly'],
		[{ words: [1,2,3,4,5,6], label: 'met de deur in huis vallen', meaning: 'get straight to the point' }]),
	paragraph(5, 'Hij gaf het plan dat hij samen met zijn vrienden zorgvuldig had uitgewerkt uiteindelijk toch niet op.',
		['He','gave','the','plan','that','he','together','with','his','friends','carefully','had','developed','eventually','after all','not','up'],
		[{ words: [1,16], label: 'gaf … op', meaning: 'gave up', split: true }])
];

export const readerFixture: Article = {
	id: readerFixtureID, title: 'Een rustige avond na een lange dag', source_language: 'nl', target_language: 'en',
	enrichment_status: 'ready', enrichment_error_code: '', created_at: '2026-09-05T00:00:00Z', updated_at: '2026-09-05T00:00:00Z',
	blocks, content_hash: '0'.repeat(64), analysis_status: 'ready', analysis_revision: 'reader.design.fixture', analysis_error_code: '',
	analysis_model: '', analysis_effort: '', narration_status: 'not_requested', narration_error_code: '',
	analysis_progress: { total_paragraphs: blocks.length, completed_paragraphs: blocks.length, current_block_index: -1, failed_block_index: -1 },
	sentences: blocks.flatMap((block) => block.sentences), occurrences: blocks.flatMap((block) => block.occurrences),
	narration: { status: 'not_requested', sentence_count: blocks.length, ready_count: 0, duration_ms: 0, size_bytes: 0, reclaimable_bytes: 0 }
};
