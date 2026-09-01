import type { ArticleBlock, ArticleOccurrence } from '$lib/api/client';

export class SemanticRunError extends Error {
	constructor(message: string) {
		super(message);
		this.name = 'SemanticRunError';
	}
}

export type SemanticRun =
	| { kind: 'plain'; text: string }
	| {
			kind: 'occurrence';
			text: string;
			occurrence: ArticleOccurrence;
			popoverOccurrence: ArticleOccurrence;
			constructionIDs: string[];
		};

type Span = { start: number; end: number };

/**
 * Builds readable, source-first runs from the layered v2 representation. The
 * returned text is always sliced from the canonical block; provider labels
 * never replace or mutate source text.
 */
export function buildSemanticRuns(block: ArticleBlock): SemanticRun[] {
	const source = block.source_text;
	const occurrences = block.occurrences ?? [];
	const tokens = occurrences.filter((item) => item.role === 'token' && item.spans.length === 1);
	const groups = occurrences.filter((item) => item.role === 'contiguous_construction' && item.spans.length === 1);
	const discontinuous = occurrences.filter((item) => item.role === 'discontinuous_construction');

	for (const occurrence of occurrences) {
		for (const span of occurrence.spans) {
			assertSpan(source, span.start_utf16, span.end_utf16, span.source_text);
		}
	}

	const grouped = groups
		.map((occurrence) => ({ occurrence, span: spanOf(occurrence) }))
		.sort((left, right) => left.span.start - right.span.start || right.span.end - left.span.end);
	for (let index = 1; index < grouped.length; index += 1) {
		const previous = grouped[index - 1];
		const current = grouped[index];
		if (previous && current && current.span.start < previous.span.end) {
			throw new SemanticRunError('contiguous constructions overlap');
		}
	}

	const groupedTokenIDs = new Set<string>();
	for (const group of grouped) {
		for (const token of tokens) {
			const tokenSpan = spanOf(token);
			if (inside(tokenSpan, group.span)) groupedTokenIDs.add(token.id);
		}
	}

	type Unit = { start: number; end: number; occurrence: ArticleOccurrence };
	const units: Unit[] = [];
	for (const group of grouped) units.push({ ...group.span, occurrence: group.occurrence });
	for (const token of tokens) {
		if (!groupedTokenIDs.has(token.id)) {
			const span = spanOf(token);
			units.push({ ...span, occurrence: token });
		}
	}
	units.sort((left, right) => left.start - right.start || left.end - right.end);
	for (let index = 1; index < units.length; index += 1) {
		const previous = units[index - 1];
		const current = units[index];
		if (previous && current && current.start < previous.end) {
			throw new SemanticRunError('source occurrences overlap outside a construction group');
		}
	}

	const runs: SemanticRun[] = [];
	let cursor = 0;
	for (const unit of units) {
		if (unit.start > cursor) runs.push({ kind: 'plain', text: sliceUTF16(source, cursor, unit.start) });
		const constructionIDs = new Set<string>();
		for (const construction of discontinuous) {
			if (construction.spans.some((span) => inside({ start: unit.start, end: unit.end }, { start: span.start_utf16, end: span.end_utf16 }))) {
				constructionIDs.add(construction.id);
			}
		}
		const popoverOccurrence =
			[...constructionIDs]
				.map((id) => discontinuous.find((item) => item.id === id))
				.find((item): item is ArticleOccurrence => Boolean(item)) ?? unit.occurrence;
		runs.push({
			kind: 'occurrence',
			text: sliceUTF16(source, unit.start, unit.end),
			occurrence: unit.occurrence,
			popoverOccurrence,
			constructionIDs: [...constructionIDs]
		});
		cursor = unit.end;
	}
	if (cursor < source.length) runs.push({ kind: 'plain', text: source.slice(cursor) });
	if (runs.length === 0) runs.push({ kind: 'plain', text: source });
	return runs;
}

function spanOf(occurrence: ArticleOccurrence): Span {
	const span = occurrence.spans[0];
	if (!span) throw new SemanticRunError(`occurrence ${occurrence.id} has no span`);
	return { start: span.start_utf16, end: span.end_utf16 };
}

function inside(value: Span, container: Span): boolean {
	return value.start >= container.start && value.end <= container.end;
}

function assertSpan(source: string, start: number, end: number, expected: string): void {
	if (!Number.isInteger(start) || !Number.isInteger(end) || start < 0 || end <= start || end > source.length) {
		throw new SemanticRunError('occurrence span is outside the source');
	}
	if (!isUTF16Boundary(source, start) || !isUTF16Boundary(source, end)) {
		throw new SemanticRunError('occurrence span splits a surrogate pair');
	}
	if (source.slice(start, end) !== expected) {
		throw new SemanticRunError('occurrence source does not match its UTF-16 span');
	}
}

export function sliceUTF16(source: string, start: number, end: number): string {
	if (!isUTF16Boundary(source, start) || !isUTF16Boundary(source, end)) {
		throw new SemanticRunError('source slice splits a surrogate pair');
	}
	return source.slice(start, end);
}

function isUTF16Boundary(source: string, offset: number): boolean {
	if (offset === 0 || offset === source.length) return true;
	const current = source.charCodeAt(offset);
	const previous = source.charCodeAt(offset - 1);
	return !(current >= 0xdc00 && current <= 0xdfff) && !(previous >= 0xd800 && previous <= 0xdbff);
}
