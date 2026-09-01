import { describe, expect, it } from 'vitest';
import { enrichmentMessage } from './enrichment';

describe('enrichmentMessage', () => {
	it('explains a timeout and confirms that the source is safe', () => {
		expect(enrichmentMessage('v1.enrichment_timeout')).toEqual({
			title: 'Codex took too long to process this article.',
			detail: 'Your article is saved. Retry once; if it times out again, split it into shorter parts.'
		});
	});

	it('uses a useful fallback without exposing an opaque code', () => {
		expect(enrichmentMessage('future.failure')).toEqual({
			title: 'English hints could not be generated.',
			detail: 'Your article is saved. Retry the enrichment.'
		});
	});
});
