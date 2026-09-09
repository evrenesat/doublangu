import {
	getSentenceTranslation,
	startSentenceTranslation,
	type SentenceTranslationEnvelope
} from '$lib/api/client';

export type SentenceTranslationPhase =
	| 'checking'
	| 'missing'
	| 'queued'
	| 'running'
	| 'ready'
	| 'failed';

export type SentenceTranslationState = {
	phase: SentenceTranslationPhase;
	sentenceId: string;
	jobId: string;
	runId: string;
	/** Last successful translation; retained while a replacement is pending or failed. */
	translation: string | null;
	errorCode: string;
	errorSummary: string;
};

export type SentenceTranslationControllerOptions = {
	lookup: typeof getSentenceTranslation;
	start: typeof startSentenceTranslation;
	poll: typeof getSentenceTranslation;
	pollIntervalMs?: number;
	onChange: (state: SentenceTranslationState) => void;
};

export const initialSentenceTranslationState: SentenceTranslationState = {
	phase: 'checking',
	sentenceId: '',
	jobId: '',
	runId: '',
	translation: null,
	errorCode: '',
	errorSummary: ''
};

// Cross-instance guard: re-renders or repeated dwell timers must not POST a
// second generation for the same sentence while one is already in flight.
// The server transaction stays authoritative across tabs.
const inFlightPosts = new Map<string, Promise<SentenceTranslationEnvelope>>();

function dedupedPost(
	key: string,
	post: () => Promise<SentenceTranslationEnvelope>
): Promise<SentenceTranslationEnvelope> {
	const existing = inFlightPosts.get(key);
	if (existing) return existing;
	const pending = post().finally(() => {
		if (inFlightPosts.get(key) === pending) inFlightPosts.delete(key);
	});
	inFlightPosts.set(key, pending);
	return pending;
}

/**
 * One sentence subject's durable state machine. Opening performs the
 * read-only lookup first and never generates: only an explicit ensure (the
 * Translate control's dwell/focus/tap) may POST, and only while the subject
 * is still missing. A failed initial generation never retries on later
 * hovers; replacement is always an explicit regenerate that keeps the old
 * text visible until a validated replacement commits. Polling runs only
 * while pending and stops on close, subject change, or a terminal state.
 * Every response carries the request identity it was opened with, so a
 * stale response after a subject switch or close is discarded.
 */
export function createSentenceTranslationController(
	options: SentenceTranslationControllerOptions
) {
	const pollIntervalMs = options.pollIntervalMs ?? 1500;
	let generation = 0;
	let timer: ReturnType<typeof setTimeout> | undefined;
	let state: SentenceTranslationState = { ...initialSentenceTranslationState };

	function emit(next: Partial<SentenceTranslationState>): void {
		state = { ...state, ...next };
		options.onChange(state);
	}

	function clearTimer(): void {
		if (timer) clearTimeout(timer);
		timer = undefined;
	}

	function adopt(
		envelope: SentenceTranslationEnvelope,
		articleId: string,
		sentenceId: string
	): void {
		const status = envelope.status ?? 'missing';
		const resolvedId = envelope.sentence_id ?? sentenceId;
		if (status === 'ready') {
			emit({
				phase: 'ready',
				sentenceId: resolvedId,
				jobId: envelope.job_id ?? '',
				runId: envelope.run_id ?? '',
				translation: envelope.translation ?? null,
				errorCode: '',
				errorSummary: ''
			});
			return;
		}
		if (status === 'failed') {
			// A failed replacement keeps the last successful text; a failed
			// initial generation has none to keep.
			emit({
				phase: 'failed',
				sentenceId: resolvedId,
				jobId: envelope.job_id ?? '',
				runId: envelope.run_id ?? '',
				errorCode: envelope.error_code ?? '',
				errorSummary: envelope.error_summary ?? ''
			});
			return;
		}
		if (status === 'missing') {
			emit({ phase: 'missing', sentenceId: resolvedId });
			return;
		}
		// queued or running: poll the read-only endpoint while expanded,
		// keeping any previously saved translation on screen.
		emit({
			phase: status === 'running' ? 'running' : 'queued',
			sentenceId: resolvedId,
			jobId: envelope.job_id ?? '',
			runId: envelope.run_id ?? '',
			errorCode: '',
			errorSummary: ''
		});
		schedulePoll(articleId, sentenceId);
	}

	function schedulePoll(articleId: string, sentenceId: string): void {
		clearTimer();
		timer = setTimeout(() => {
			timer = undefined;
			const identity = generation;
			options
				.poll(articleId, sentenceId)
				.then((envelope) => {
					if (identity !== generation) return; // superseded by close/subject switch
					adopt(envelope, articleId, sentenceId);
				})
				.catch(() => {
					if (identity !== generation) return;
					emit({ phase: 'failed', errorCode: '', errorSummary: '' });
				});
		}, pollIntervalMs);
	}

	async function openOnce(articleId: string, sentenceId: string): Promise<void> {
		const identity = generation;
		try {
			const envelope = await options.lookup(articleId, sentenceId);
			if (identity !== generation) return;
			adopt(envelope, articleId, sentenceId);
		} catch {
			if (identity !== generation) return;
			emit({ phase: 'failed', sentenceId, errorCode: '', errorSummary: '' });
		}
	}

	async function postOnce(
		articleId: string,
		sentenceId: string,
		mode: 'ensure' | 'regenerate'
	): Promise<void> {
		const identity = generation;
		// emit merges: a retained translation stays on screen, so the UI
		// reads this as a replacement-in-progress rather than a blank load.
		emit({ phase: 'queued' });
		try {
			const envelope = await dedupedPost(`${articleId}:${sentenceId}:${mode}`, () =>
				options.start(articleId, sentenceId, { mode })
			);
			if (identity !== generation) return;
			adopt(envelope, articleId, sentenceId);
		} catch {
			if (identity !== generation) return;
			emit({ phase: 'failed', errorCode: '', errorSummary: '' });
		}
	}

	return {
		get state(): SentenceTranslationState {
			return state;
		},
		// open resolves the saved state for one sentence without ever
		// starting generation; the returned promise settles after the flow
		// reaches a pending or terminal state so callers can position.
		open(articleId: string, sentenceId: string): Promise<void> {
			generation += 1;
			clearTimer();
			state = { ...initialSentenceTranslationState };
			options.onChange(state);
			return openOnce(articleId, sentenceId);
		},
		// ensure starts one generation only while the subject is still
		// missing. Ready, pending, and failed subjects are left alone, so
		// later hovers never call the LLM again and never loop a failure.
		ensure(articleId: string, sentenceId: string): Promise<void> {
			if (state.phase !== 'missing') return Promise.resolve();
			return postOnce(articleId, sentenceId, 'ensure');
		},
		// regenerate always starts a fresh request and keeps the old text
		// visible; a duplicate call while pending rejoins the same work.
		regenerate(articleId: string, sentenceId: string): Promise<void> {
			if (state.phase === 'queued' || state.phase === 'running') return Promise.resolve();
			return postOnce(articleId, sentenceId, 'regenerate');
		},
		close(): void {
			generation += 1;
			clearTimer();
			state = { ...initialSentenceTranslationState };
			options.onChange(state);
		}
	};
}
