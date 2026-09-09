import { describe, expect, it, vi } from 'vitest';
import type { SentenceTranslationEnvelope } from '$lib/api/client';
import {
	createSentenceTranslationController,
	initialSentenceTranslationState,
	type SentenceTranslationState
} from './sentenceTranslationController';

type Envelope = SentenceTranslationEnvelope;

function readyEnvelope(sentenceId = 'sentence-1', translation = 'She is sitting on a bench.'): Envelope {
	return {
		status: 'ready',
		sentence_id: sentenceId,
		translation,
		job_id: 'job-1',
		run_id: 'run-1'
	};
}

function harness() {
	const states: SentenceTranslationState[] = [];
	const lookup = vi.fn(
		(_articleId: string, _sentenceId: string): Promise<Envelope> =>
			Promise.resolve({ status: 'missing' })
	);
	const start = vi.fn(
		(_articleId: string, _sentenceId: string, _input: { mode: 'ensure' | 'regenerate' }): Promise<Envelope> =>
			Promise.resolve({ status: 'missing' })
	);
	const poll = vi.fn(
		(_articleId: string, _sentenceId: string): Promise<Envelope> =>
			Promise.resolve({ status: 'missing' })
	);
	const controller = createSentenceTranslationController({
		lookup: (articleId, sentenceId) => lookup(articleId, sentenceId),
		start: (articleId, sentenceId, input) => start(articleId, sentenceId, input),
		poll: (articleId, sentenceId) => poll(articleId, sentenceId),
		pollIntervalMs: 1,
		onChange: (state) => states.push(state)
	});
	return { states, lookup, start, poll, controller };
}

const flush = () => new Promise<void>((resolve) => setTimeout(resolve, 5));

function deferred() {
	let release!: (value: Envelope) => void;
	const promise = new Promise<Envelope>((resolve) => {
		release = resolve;
	});
	return { promise, release };
}

// pollServer records every poll as an unreleased deferred; the test resolves
// them in order, so the interval can never race the assertions.
function pollServer() {
	const pending: ReturnType<typeof deferred>[] = [];
	const impl = vi.fn(async () => {
		const gate = deferred();
		pending.push(gate);
		return gate.promise;
	});
	return {
		impl,
		releaseNext(result: Envelope): void {
			const gate = pending.shift();
			if (gate) gate.release(result);
		},
		count: () => pending.length
	};
}

describe('sentenceTranslationController', () => {
	it('opens a saved translation without generating', async () => {
		const { states, lookup, start, controller } = harness();
		lookup.mockResolvedValue(readyEnvelope());
		await controller.open('article-1', 'sentence-1');
		expect(start).not.toHaveBeenCalled();
		expect(controller.state.phase).toBe('ready');
		expect(controller.state.translation).toBe('She is sitting on a bench.');
		expect(controller.state.runId).toBe('run-1');
		expect(states[0]).toEqual(initialSentenceTranslationState);
	});

	it('ensure posts once for a missing translation and polls until ready', async () => {
		const { lookup, start, poll, controller } = harness();
		const server = pollServer();
		poll.mockImplementation(server.impl);
		lookup.mockResolvedValue({ status: 'missing' });
		start.mockResolvedValue({ status: 'queued', sentence_id: 'sentence-1', job_id: 'job-9', run_id: 'run-9' });
		await controller.open('article-1', 'sentence-1');
		expect(controller.state.phase).toBe('missing');
		expect(start).not.toHaveBeenCalled();
		await controller.ensure('article-1', 'sentence-1');
		expect(start).toHaveBeenCalledWith('article-1', 'sentence-1', { mode: 'ensure' });
		await flush();
		expect(controller.state.phase).toBe('queued');
		expect(poll).toHaveBeenCalledTimes(1);
		server.releaseNext({ status: 'running', sentence_id: 'sentence-1' } as Envelope);
		await flush();
		expect(controller.state.phase).toBe('running');
		server.releaseNext(readyEnvelope('sentence-1') as Envelope);
		await flush();
		await flush();
		expect(controller.state.phase).toBe('ready');
		expect(controller.state.translation).toBe('She is sitting on a bench.');
		expect(poll).toHaveBeenCalledWith('article-1', 'sentence-1');
	});

	it('ensure never fires for ready, pending, or failed subjects', async () => {
		const { lookup, start, controller } = harness();
		lookup.mockResolvedValue(readyEnvelope());
		await controller.open('article-1', 'sentence-1');
		await controller.ensure('article-1', 'sentence-1');
		expect(start).not.toHaveBeenCalled();

		lookup.mockResolvedValue({ status: 'failed', sentence_id: 'sentence-2', error_code: 'v1.st_invalid' });
		await controller.open('article-1', 'sentence-2');
		expect(controller.state.phase).toBe('failed');
		await controller.ensure('article-1', 'sentence-2');
		expect(start).not.toHaveBeenCalled();
	});

	it('concurrent ensures share one generation request', async () => {
		const { lookup, start, controller } = harness();
		lookup.mockResolvedValue({ status: 'missing' });
		const gate = deferred();
		start.mockReturnValue(gate.promise);
		await controller.open('article-1', 'sentence-1');
		const first = controller.ensure('article-1', 'sentence-1');
		const second = controller.ensure('article-1', 'sentence-1');
		gate.release({ status: 'queued', sentence_id: 'sentence-1' });
		await first;
		await second;
		expect(start).toHaveBeenCalledTimes(1);
	});

	it('stops polling after close', async () => {
		const { lookup, start, poll, controller } = harness();
		const server = pollServer();
		poll.mockImplementation(server.impl);
		lookup.mockResolvedValue({ status: 'missing' });
		start.mockResolvedValue({ status: 'queued', sentence_id: 'sentence-1' });
		await controller.open('article-1', 'sentence-1');
		await controller.ensure('article-1', 'sentence-1');
		await flush();
		expect(poll).toHaveBeenCalledTimes(1);
		controller.close();
		// A pending poll resolving after close must not resurrect the state
		// or schedule another poll.
		server.releaseNext({ status: 'queued', sentence_id: 'sentence-1' } as Envelope);
		await flush();
		expect(controller.state.phase).toBe('checking');
		expect(poll).toHaveBeenCalledTimes(1);
	});

	it('discards stale responses after a subject switch', async () => {
		const { lookup, poll, controller } = harness();
		lookup.mockImplementation(async (_articleId, sentenceId) => {
			// The first sentence resolves slowly; the second immediately.
			if (sentenceId === 'slow') {
				await new Promise((resolve) => setTimeout(resolve, 10));
				return { status: 'queued', sentence_id: 'slow' };
			}
			return readyEnvelope('fast', 'Fast translation.');
		});
		const first = controller.open('article-1', 'slow');
		await controller.open('article-1', 'fast');
		expect(controller.state.phase).toBe('ready');
		expect(controller.state.translation).toBe('Fast translation.');
		await first;
		// The slow response arrives after the switch and must not win or
		// mislabel the new sentence.
		expect(controller.state.translation).toBe('Fast translation.');
		expect(controller.state.sentenceId).toBe('fast');
		expect(poll).not.toHaveBeenCalled();
	});

	it('failed generation offers an explicit regenerate that keeps the old text', async () => {
		const { lookup, start, controller } = harness();
		lookup.mockResolvedValue(
			readyEnvelope('sentence-1', 'Old translation.') as Envelope
		);
		await controller.open('article-1', 'sentence-1');
		start.mockResolvedValue({
			status: 'failed',
			sentence_id: 'sentence-1',
			error_code: 'v1.st_invalid',
			error_summary: 'Provider returned invalid output.'
		});
		await controller.regenerate('article-1', 'sentence-1');
		expect(start).toHaveBeenCalledWith('article-1', 'sentence-1', { mode: 'regenerate' });
		expect(controller.state.phase).toBe('failed');
		expect(controller.state.translation).toBe('Old translation.');
		expect(controller.state.errorSummary).toBe('Provider returned invalid output.');
	});

	it('duplicate regenerate calls while pending rejoin the same work', async () => {
		const { lookup, start, controller } = harness();
		lookup.mockResolvedValue(readyEnvelope('sentence-1', 'Old translation.'));
		await controller.open('article-1', 'sentence-1');
		const gate = deferred();
		start.mockReturnValue(gate.promise);
		const first = controller.regenerate('article-1', 'sentence-1');
		await controller.regenerate('article-1', 'sentence-1');
		gate.release({ status: 'queued', sentence_id: 'sentence-1' });
		await first;
		expect(start).toHaveBeenCalledTimes(1);
		expect(controller.state.phase).toBe('queued');
		expect(controller.state.translation).toBe('Old translation.');
	});

	it('failed network requests surface as failed phase without generation loops', async () => {
		const { lookup, start, controller } = harness();
		lookup.mockRejectedValue(new Error('offline'));
		await controller.open('article-1', 'sentence-1');
		expect(controller.state.phase).toBe('failed');
		expect(start).not.toHaveBeenCalled();
		// A later hover must not retry the failure on its own.
		await controller.ensure('article-1', 'sentence-1');
		expect(start).not.toHaveBeenCalled();
	});
});
