export interface EnrichmentMessage {
	title: string;
	detail: string;
}

const failures: Record<string, EnrichmentMessage> = {
	'v1.annotator_unavailable': {
		title: 'English hints are temporarily offline.',
		detail: 'The Codex annotator is not enabled on the server. Your article is saved.'
	},
	'v1.enrichment_not_authenticated': {
		title: 'The server lost its Codex connection.',
		detail: 'Reconnect the server’s Codex account, then retry. Your article is saved.'
	},
	'v1.enrichment_timeout': {
		title: 'Codex took too long to process this article.',
		detail: 'Your article is saved. Retry once; if it times out again, split it into shorter parts.'
	},
	'v1.enrichment_protocol_error': {
		title: 'The Codex connection returned an unexpected response.',
		detail: 'Your article is saved. Retry the enrichment.'
	},
	'v1.enrichment_invalid_output': {
		title: 'Codex produced hints the reader could not safely place.',
		detail: 'Your article is saved. Retry to generate a new set of hints.'
	},
	'v1.enrichment_provider_failure': {
		title: 'Codex could not generate English hints.',
		detail: 'Your article is saved. Check the connection and retry.'
	},
	'v1.enrichment_invalid_input': {
		title: 'This article cannot be enriched as entered.',
		detail: 'Check that it contains Dutch text and is not too long.'
	},
	'v1.enrichment_interrupted': {
		title: 'Enrichment was interrupted by a server restart.',
		detail: 'Your article is saved. Retry the enrichment.'
	}
};

export function enrichmentMessage(code: string): EnrichmentMessage {
	return failures[code] ?? {
		title: 'English hints could not be generated.',
		detail: 'Your article is saved. Retry the enrichment.'
	};
}
