/**
 * Derives the compact analysis/narration wait-state surface above the reader
 * body. Pure and testable: the Svelte component only maps these values to
 * ARIA roles and styles.
 */

export type ProgressShape = {
	visible: boolean;
	kind: 'analysis' | 'narration';
	text: string;
	detail?: string;
	/** 0-100 when the phase is determinate, undefined for indeterminate. */
	percent?: number;
	/** Numeric aria value when determinate. */
	ariaValueNow?: number;
};

export type ProgressInput = {
	analysis_status?: string;
	narration_status?: string;
	total?: number;
	completed?: number;
	current_block_index?: number;
	failed_block_index?: number;
	ready_sentences?: number;
	total_sentences?: number;
};

const clampParagraph = (value: number, total: number): number =>
	Math.max(1, Math.min(total, value));

export function waitStateProgress(input: ProgressInput): ProgressShape {
	const total = Math.max(0, input.total ?? 0);
	const completed = Math.max(0, input.completed ?? 0);
	const totalSentences = Math.max(0, input.total_sentences ?? 0);
	const readySentences = Math.max(0, input.ready_sentences ?? 0);
	const narrationPending =
		input.narration_status === 'queued' ||
		input.narration_status === 'generating' ||
		input.narration_status === 'partial';

	const narrationBusy =
		narrationPending &&
		(input.analysis_status !== 'queued' && input.analysis_status !== 'processing' && input.analysis_status !== 'failed');

	if (narrationBusy) {
		return {
			visible: true,
			kind: 'narration',
			text: 'Generating narration',
			detail: `${readySentences} of ${totalSentences} sentences ready`,
			percent: totalSentences > 0 ? Math.round((readySentences * 100) / totalSentences) : undefined,
			ariaValueNow: totalSentences > 0 ? readySentences : undefined
		};
	}

	switch (input.analysis_status) {
		case 'queued':
			return {
				visible: true,
				kind: 'analysis',
				text: 'Waiting to analyze',
				detail: `${Math.min(completed, total)} of ${total} paragraphs`
			};
		case 'processing': {
			const current = input.current_block_index !== undefined && input.current_block_index >= 0 ? input.current_block_index + 1 : clampParagraph(completed + 1, Math.max(1, total));
			return {
				visible: true,
				kind: 'analysis',
				text: total > 0 ? `Analyzing paragraph ${clampParagraph(current, total)} of ${total}` : 'Analyzing paragraphs',
				detail: `${completed} complete`,
				percent: total > 0 ? Math.round((completed * 100) / total) : undefined,
				ariaValueNow: total > 0 ? completed : undefined
			};
		}
		case 'failed': {
			const failed = input.failed_block_index !== undefined && input.failed_block_index >= 0 ? input.failed_block_index + 1 : clampParagraph(completed + 1, Math.max(1, total));
			return {
				visible: true,
				kind: 'analysis',
				text: total > 0 ? `Stopped at paragraph ${clampParagraph(failed, total)}` : 'Analysis stopped',
				detail: `${completed} of ${total} paragraphs updated`
			};
		}
		default:
			return { visible: false, kind: 'analysis', text: '' };
	}
}
