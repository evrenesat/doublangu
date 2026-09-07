import { describe, expect, it, vi } from 'vitest';
import type { DictionaryStatusEnvelope } from '$lib/api/client';
import { createExploreController, initialExploreState, type ExploreRef, type ExploreState } from './exploreController';

type Envelope = DictionaryStatusEnvelope;

function readyEnvelope(entryId = 'entry-1'): Envelope {
	return {
		status: 'ready',
		entry_id: entryId,
		document: {
			version: 'reader.dictionary.v1', lookup_form: 'bank', lookup_kind: 'word',
			source_language: 'nl', target_language: 'en',
			senses: [{ part_of_speech: 'noun', translation_en: 'bench', meaning_en: 'A long seat.', usage_en: '', pattern_nl: '', parts: [], examples: [{ text_nl: 'a', translation_en: 'b' }] }]
		}
	};
}

function harness() {
	const states: ExploreState[] = [];
	const lookup = vi.fn((_articleId: string, _ref: ExploreRef): Promise<Envelope> => Promise.resolve({ status: 'missing' }));
	const start = vi.fn((_articleId: string, _input: { occurrence_id?: string; annotation_id?: string; retry: boolean }): Promise<Envelope> => Promise.resolve({ status: 'missing' }));
	const poll = vi.fn((_entryId: string): Promise<Envelope> => Promise.resolve({ status: 'missing' }));
	const controller = createExploreController({
		lookup: (articleId, ref) => lookup(articleId, ref),
		start: (articleId, input) => start(articleId, input),
		poll: (entryId) => poll(entryId),
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

describe('exploreController', () => {
	it('starts checking then adopts a ready entry without generating', async () => {
		const { states, lookup, start, controller } = harness();
		lookup.mockResolvedValue(readyEnvelope());
		await controller.open({ occurrenceId: 'w1' }, 'article-1');
		expect(start).not.toHaveBeenCalled();
		expect(controller.state.phase).toBe('ready');
		expect(controller.state.entryId).toBe('entry-1');
		expect(states[0]).toEqual(initialExploreState);
	});

	it('performs one explicit start for a missing entry and polls until ready', async () => {
		const { lookup, start, poll, controller } = harness();
		const server = pollServer();
		poll.mockImplementation(server.impl);
		lookup.mockResolvedValue({ status: 'missing' });
		start.mockResolvedValue({ status: 'queued', entry_id: 'entry-9', job_id: 'job-1' });
		await controller.open({ occurrenceId: 'w1' }, 'article-1');
		expect(start).toHaveBeenCalledWith('article-1', { occurrence_id: 'w1', annotation_id: undefined, retry: false });
		await flush();
		expect(controller.state.phase).toBe('queued');
		expect(poll).toHaveBeenCalledTimes(1);
		server.releaseNext({ status: 'generating', entry_id: 'entry-9' } as Envelope);
		await flush();
		expect(controller.state.phase).toBe('generating');
		server.releaseNext(readyEnvelope('entry-9') as Envelope);
		await flush();
		await flush();
		expect(controller.state.phase).toBe('ready');
		expect(poll).toHaveBeenCalledWith('entry-9');
	});

	it('stops polling after close', async () => {
		const { lookup, start, poll, controller } = harness();
		const server = pollServer();
		poll.mockImplementation(server.impl);
		lookup.mockResolvedValue({ status: 'missing' });
		start.mockResolvedValue({ status: 'queued', entry_id: 'entry-2' });
		await controller.open({ occurrenceId: 'w1' }, 'article-1');
		await flush();
		expect(poll).toHaveBeenCalledTimes(1);
		controller.close();
		// A pending poll resolving after close must not resurrect the state
		// or schedule another poll.
		server.releaseNext({ status: 'queued', entry_id: 'entry-2' } as Envelope);
		await flush();
		expect(controller.state.phase).toBe('checking');
		expect(poll).toHaveBeenCalledTimes(1);
	});

	it('discards stale responses after a subject switch', async () => {
		const { lookup, poll, controller } = harness();
		lookup.mockImplementation(async (_articleId, ref) => {
			// The first subject resolves slowly; the second immediately.
			if (ref.occurrenceId === 'slow') {
				await new Promise((resolve) => setTimeout(resolve, 10));
				return { status: 'queued', entry_id: 'slow-entry' };
			}
			return readyEnvelope('fast-entry');
		});
		const first = controller.open({ occurrenceId: 'slow' }, 'article-1');
		await controller.open({ occurrenceId: 'fast' }, 'article-1');
		expect(controller.state.phase).toBe('ready');
		expect(controller.state.entryId).toBe('fast-entry');
		await first;
		// The slow response arrives after the switch and must not win.
		expect(controller.state.entryId).toBe('fast-entry');
		expect(poll).not.toHaveBeenCalled();
	});

	it('reports failure and retries explicitly', async () => {
		const { lookup, start, controller } = harness();
		lookup.mockResolvedValue({ status: 'failed', entry_id: 'entry-3', error_code: 'v1.dictionary_invalid_output' });
		await controller.open({ occurrenceId: 'w1' }, 'article-1');
		expect(controller.state.phase).toBe('failed');
		expect(controller.state.errorCode).toBe('v1.dictionary_invalid_output');
		expect(start).not.toHaveBeenCalled();
		// Explicit retry requests generation with retry:true.
		start.mockResolvedValue({ status: 'queued', entry_id: 'entry-4' });
		await controller.retry({ occurrenceId: 'w1' }, 'article-1');
		expect(start).toHaveBeenCalledWith('article-1', { occurrence_id: 'w1', annotation_id: undefined, retry: true });
		expect(controller.state.phase).toBe('queued');
	});

	it('failed network requests surface as failed phase without generation loops', async () => {
		const { lookup, start, controller } = harness();
		lookup.mockRejectedValue(new Error('offline'));
		await controller.open({ occurrenceId: 'w1' }, 'article-1');
		expect(controller.state.phase).toBe('failed');
		expect(start).not.toHaveBeenCalled();
	});
});
