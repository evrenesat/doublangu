import { afterEach, beforeEach, expect, it, vi } from 'vitest';
import {
	createChapter, createEdition, createLibrary, createWork, deleteChapter, deleteEdition, deleteLibrary,
	deleteWork, DoublanguAPIError, DoublanguNetworkError, getChapter, getEdition, getLibrary, getWork,
	listChapters, listEditions, listLibraries, listWorks, updateChapter, updateEdition, updateLibrary, updateWork,
	createArticle, enrichArticle, getArticle, listArticles, updateLearningState,
	clearNarration, generateNarration, getNarration, reanalyzeArticle, updateSemanticLearningState,
	getSession, logoutSession, getAnalysisModels, getAnalysisSettings, saveAnalysisSettings,
	listAnalysisRuns, getAnalysisRun, createAnalysisProfile, deleteAnalysisProfile,
	getPipelineAnalysisSettings, listAnalysisProfiles, listAnalysisProviders,
	savePipelineAnalysisSettings, testAnalysisProvider
} from './client';

const library = { id: 'library-id', name: 'Dutch Library', source_language: 'nl', target_language: 'en', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-02T00:00:00Z' };
const work = { id: 'work-id', library_id: library.id, title: 'De Avonden', author: 'Gerard Reve', kind: 'audiobook', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-02T00:00:00Z' };
const edition = { id: 'edition-id', work_id: work.id, name: 'Original', language: 'nl', format: 'audiobook', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-02T00:00:00Z' };
const chapter = { id: 'chapter-id', edition_id: edition.id, title: 'Chapter 1', chapter_number: 1, start_ms: 0, end_ms: 120000, duration_ms: 120000, created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-02T00:00:00Z' };
const article = { id: 'article-id', title: 'Een dag', source_language: 'nl', target_language: 'en', enrichment_status: 'ready', enrichment_error_code: '', created_at: '2026-01-01T00:00:00Z', updated_at: '2026-01-02T00:00:00Z', blocks: [] };

function json(status: number, body: unknown): Response {
	return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
}

function noContent(): Response {
	return new Response(null, { status: 204 });
}

function mock(response: Response): void {
	vi.stubGlobal('fetch', vi.fn().mockResolvedValue(response));
}

function call(): [string, RequestInit] {
	return (fetch as ReturnType<typeof vi.fn>).mock.calls[0] as [string, RequestInit];
}

function expectRequest(url: string, method = 'GET', body?: unknown): void {
	const [actual, init] = call();
	expect(actual).toBe(url);
	expect(init.credentials).toBe('same-origin');
	expect(init.method ?? 'GET').toBe(method);
	if (method !== 'GET') {
		if (body === undefined) expect(init.body).toBeUndefined();
		else expect(init.body).toBe(JSON.stringify(body));
		const headers = new Headers(init.headers);
		expect(headers.get('content-type')).toBe('application/json');
		expect(headers.get('x-csrf-token')).toBe('test-csrf-token');
	}
}

function clearCookies(): void {
	for (const item of document.cookie.split(';')) {
		const name = item.split('=')[0]?.trim();
		if (name) document.cookie = `${name}=; Max-Age=0; Path=/`;
	}
}

beforeEach(() => {
	clearCookies();
	document.cookie = 'csrf_token=test-csrf-token; Path=/';
});

afterEach(() => {
	clearCookies();
	vi.unstubAllGlobals();
});

it('lists libraries at the collection path', async () => {
	mock(json(200, [library]));
	expect(await listLibraries()).toEqual([library]);
	expectRequest('/api/v1/libraries');
});

it('checks and ends the owner session', async () => {
	mock(json(200, { authenticated: true }));
	expect(await getSession()).toEqual({ authenticated: true });
	expectRequest('/api/v1/auth/session');

	mock(json(200, { ok: true }));
	await logoutSession();
	expectRequest('/api/v1/auth/logout', 'POST');
});

it('gets an encoded library item path', async () => {
	mock(json(200, library));
	expect(await getLibrary('library /?')).toEqual(library);
	expectRequest('/api/v1/libraries/library%20%2F%3F');
});

it('creates a library with a CSRF JSON mutation', async () => {
	const payload = { name: 'New', source_language: 'nl', target_language: 'en' };
	mock(json(201, library));
	expect(await createLibrary(payload)).toEqual(library);
	expectRequest('/api/v1/libraries', 'POST', payload);
});

it('updates a library with a CSRF JSON mutation', async () => {
	const payload = { name: 'Updated' };
	mock(json(200, library));
	expect(await updateLibrary(library.id, payload)).toEqual(library);
	expectRequest('/api/v1/libraries/library-id', 'PUT', payload);
});

it('deletes a library from a 204 response', async () => {
	mock(noContent());
	expect(await deleteLibrary(library.id)).toBeUndefined();
	expectRequest('/api/v1/libraries/library-id', 'DELETE');
});

it('lists works at the encoded library collection path', async () => {
	mock(json(200, [work]));
	expect(await listWorks('library /?')).toEqual([work]);
	expectRequest('/api/v1/libraries/library%20%2F%3F/works');
});

it('gets a work from its encoded top-level item path', async () => {
	mock(json(200, work));
	expect(await getWork('work /?')).toEqual(work);
	expectRequest('/api/v1/works/work%20%2F%3F');
});

it('creates a work at its library collection path', async () => {
	const payload = { title: 'New', kind: 'ebook' };
	mock(json(201, work));
	expect(await createWork(library.id, payload)).toEqual(work);
	expectRequest('/api/v1/libraries/library-id/works', 'POST', payload);
});

it('updates a work at its top-level item path', async () => {
	const payload = { title: 'Updated' };
	mock(json(200, work));
	expect(await updateWork(work.id, payload)).toEqual(work);
	expectRequest('/api/v1/works/work-id', 'PUT', payload);
});

it('deletes a work at its top-level item path', async () => {
	mock(noContent());
	expect(await deleteWork(work.id)).toBeUndefined();
	expectRequest('/api/v1/works/work-id', 'DELETE');
});

it('lists editions at the encoded work collection path', async () => {
	mock(json(200, [edition]));
	expect(await listEditions('work /?')).toEqual([edition]);
	expectRequest('/api/v1/works/work%20%2F%3F/editions');
});

it('gets an edition from its encoded top-level item path', async () => {
	mock(json(200, edition));
	expect(await getEdition('edition /?')).toEqual(edition);
	expectRequest('/api/v1/editions/edition%20%2F%3F');
});

it('creates an edition at its work collection path', async () => {
	const payload = { name: 'New', language: 'nl', format: 'text' };
	mock(json(201, edition));
	expect(await createEdition(work.id, payload)).toEqual(edition);
	expectRequest('/api/v1/works/work-id/editions', 'POST', payload);
});

it('updates an edition at its top-level item path', async () => {
	const payload = { name: 'Updated' };
	mock(json(200, edition));
	expect(await updateEdition(edition.id, payload)).toEqual(edition);
	expectRequest('/api/v1/editions/edition-id', 'PUT', payload);
});

it('deletes an edition at its top-level item path', async () => {
	mock(noContent());
	expect(await deleteEdition(edition.id)).toBeUndefined();
	expectRequest('/api/v1/editions/edition-id', 'DELETE');
});

it('lists chapters at the encoded edition collection path', async () => {
	mock(json(200, [chapter]));
	expect(await listChapters('edition /?')).toEqual([chapter]);
	expectRequest('/api/v1/editions/edition%20%2F%3F/chapters');
});

it('gets a chapter from its encoded top-level item path', async () => {
	mock(json(200, chapter));
	expect(await getChapter('chapter /?')).toEqual(chapter);
	expectRequest('/api/v1/chapters/chapter%20%2F%3F');
});

it('creates a chapter at its edition collection path', async () => {
	const payload = { title: 'New', chapter_number: 2, start_ms: 0, end_ms: 1, duration_ms: 1 };
	mock(json(201, chapter));
	expect(await createChapter(edition.id, payload)).toEqual(chapter);
	expectRequest('/api/v1/editions/edition-id/chapters', 'POST', payload);
});

it('updates a chapter at its top-level item path', async () => {
	const payload = { title: 'Updated' };
	mock(json(200, chapter));
	expect(await updateChapter(chapter.id, payload)).toEqual(chapter);
	expectRequest('/api/v1/chapters/chapter-id', 'PUT', payload);
});

it('deletes a chapter at its top-level item path', async () => {
	mock(noContent());
	expect(await deleteChapter(chapter.id)).toBeUndefined();
	expectRequest('/api/v1/chapters/chapter-id', 'DELETE');
});

it('lists, creates, gets, and enriches reader articles', async () => {
	mock(json(200, [article]));
	expect(await listArticles()).toEqual([article]);
	expectRequest('/api/v1/articles');

	const payload = { title: 'Een dag', body: 'Een zin.', source_language: 'nl', target_language: 'en' };
	mock(json(201, article));
	expect(await createArticle(payload)).toEqual(article);
	expectRequest('/api/v1/articles', 'POST', payload);

	mock(json(200, article));
	expect(await getArticle('article /?')).toEqual(article);
	expectRequest('/api/v1/articles/article%20%2F%3F');

	mock(json(200, article));
	expect(await enrichArticle(article.id)).toEqual(article);
	expectRequest('/api/v1/articles/article-id/enrich', 'POST');
});

it('uses the background analysis, narration, and sense-keyed learning routes', async () => {
	mock(json(202, article));
	expect(await reanalyzeArticle('article /?')).toEqual(article);
	expectRequest('/api/v1/articles/article%20%2F%3F/reanalyze', 'POST');

	mock(json(200, { article_id: article.id, status: 'ready', error_code: '', sentence_count: 1, ready_count: 1, duration_ms: 100, size_bytes: 10, reclaimable_bytes: 10, clips: [] }));
	const narration = await getNarration(article.id);
	expect(narration.status).toBe('ready');
	expectRequest('/api/v1/articles/article-id/narration');

	mock(json(202, article));
	expect(await generateNarration(article.id)).toEqual(article);
	expectRequest('/api/v1/articles/article-id/narration', 'POST');

	mock(json(200, { article_id: article.id, sentence_count: 1, reclaimed_bytes: 10, retained_bytes: 0, purged_render_count: 1, status: 'purged' }));
	await clearNarration(article.id);
	expectRequest('/api/v1/articles/article-id/narration', 'DELETE');

	const semanticInput = { semantic_sense_id: 'sense-id', article_occurrence_id: 'occurrence-id', status: 'learned' as const };
	const semanticState = { semantic_sense_id: 'sense-id', status: 'learned' as const, updated_at: '2026-01-02T00:00:00Z' };
	mock(json(200, semanticState));
	expect(await updateSemanticLearningState(semanticInput)).toEqual(semanticState);
	expectRequest('/api/v1/learning-state', 'PUT', semanticInput);
});

it('discovers analysis settings and history through owner API routes', async () => {
	const models = { models: [{ id: 'model-a', display_name: 'Model A', is_default: true, hidden: false, supported_reasoning_efforts: [{ value: 'low' }] }], retrieved_at: '2026-01-02T00:00:00Z', stale: false };
	const settings = { model: 'model-a', effort: 'low', updated_at: '2026-01-02T00:00:00Z' };
	const runs = { runs: [], next_cursor: '' };
	const run = { id: 'run /?', article_id: 'article-id', article_title: 'Een dag', job_id: 'job-id', attempt_count: 1, content_hash: 'hash', contract_version: 'reader.analysis.v2', prompt_version: 'reader-analysis-prompt.v2', requested_model: 'model-a', requested_effort: 'low', provider_id: 'codex.appserver', codex_cli_version: '', reported_model: '', started_at: '', completed_at: '', duration_ms: 0, status: 'succeeded', total_paragraphs: 1, completed_paragraphs: 1, failed_block_index: -1, error_code: '', turns: [] };
	const fetchMock = vi.fn()
		.mockResolvedValueOnce(json(200, models))
		.mockResolvedValueOnce(json(200, { ...models, stale: true, last_error: 'refresh failed' }))
		.mockResolvedValueOnce(json(200, settings))
		.mockResolvedValueOnce(json(200, settings))
		.mockResolvedValueOnce(json(200, runs))
		.mockResolvedValueOnce(json(200, run));
	vi.stubGlobal('fetch', fetchMock);

	expect(await getAnalysisModels()).toEqual(models);
	expect(await getAnalysisModels(true)).toEqual({ ...models, stale: true, last_error: 'refresh failed' });
	expect(await getAnalysisSettings()).toEqual(settings);
	expect(await saveAnalysisSettings({ model: 'model-a', effort: 'low' })).toEqual(settings);
	expect(await listAnalysisRuns({ articleId: 'article /?', limit: 10, cursor: 'next /?' })).toEqual(runs);
	expect(await getAnalysisRun('run /?')).toEqual(run);

	const calls = fetchMock.mock.calls as Array<[string, RequestInit]>;
	expect(calls.map(([url]) => url)).toEqual([
		'/api/v1/analysis/models',
		'/api/v1/analysis/models?refresh=true',
		'/api/v1/analysis/settings',
		'/api/v1/analysis/settings',
		'/api/v1/analysis/runs?article_id=article+%2F%3F&limit=10&cursor=next+%2F%3F',
		'/api/v1/analysis/runs/run%20%2F%3F'
	]);
	const saveInit = calls[3]![1];
	expect(saveInit.method).toBe('PUT');
	expect(saveInit.body).toBe(JSON.stringify({ model: 'model-a', effort: 'low' }));
});

it('sends a fresh flag only for an explicit fresh analysis retry', async () => {
	mock(json(202, article));
	expect(await reanalyzeArticle(article.id, true)).toEqual(article);
	expectRequest('/api/v1/articles/article-id/reanalyze', 'POST', { fresh: true });
});

it('updates reader learning state with a CSRF JSON mutation', async () => {
	const payload = { source_language: 'nl', kind: 'word' as const, learning_key: 'woord', status: 'learned' as const };
	const state = { ...payload, updated_at: '2026-01-02T00:00:00Z' };
	mock(json(200, state));
	expect(await updateLearningState(payload)).toEqual(state);
	expectRequest('/api/v1/learning-state', 'PUT', payload);
});

it('decodes a structured ordinary API error', async () => {
	mock(json(404, { error: 'Missing', code: 'v1.not_found' }));
	await expect(getLibrary('missing')).rejects.toMatchObject({ name: 'DoublanguAPIError', status: 404, code: 'v1.not_found' });
});

it('maps a malformed ordinary error envelope to a network error', async () => {
	mock(json(500, { message: 'not an API error' }));
	await expect(listLibraries()).rejects.toBeInstanceOf(DoublanguNetworkError);
});

it('decodes a structured mutation API error', async () => {
	mock(json(403, { error: 'CSRF denied', code: 'v1.csrf_error' }));
	await expect(deleteLibrary(library.id)).rejects.toBeInstanceOf(DoublanguAPIError);
});

it('maps a malformed mutation error envelope to a network error', async () => {
	mock(new Response('unavailable', { status: 503 }));
	await expect(createLibrary({ name: 'New', source_language: 'nl', target_language: 'en' })).rejects.toBeInstanceOf(DoublanguNetworkError);
});

it('bootstraps CSRF once before a mutation when the cookie is absent', async () => {
	clearCookies();
	vi.stubGlobal('fetch', vi.fn(async (url: string) => {
		if (url === '/api/v1/auth/csrf') {
			document.cookie = 'csrf_token=bootstrapped; Path=/';
			return json(200, { ok: true });
		}
		return json(201, library);
	}));
	expect(await createLibrary({ name: 'New', source_language: 'nl', target_language: 'en' })).toEqual(library);
	expect((fetch as ReturnType<typeof vi.fn>).mock.calls.map(([url]) => url)).toEqual(['/api/v1/auth/csrf', '/api/v1/libraries']);
	expect(new Headers((fetch as ReturnType<typeof vi.fn>).mock.calls[1]![1].headers).get('x-csrf-token')).toBe('bootstrapped');
});

it('decodes structured and malformed CSRF bootstrap failures', async () => {
	clearCookies();
	mock(json(403, { error: 'CSRF denied', code: 'v1.csrf_error' }));
	await expect(createLibrary({ name: 'New', source_language: 'nl', target_language: 'en' })).rejects.toBeInstanceOf(DoublanguAPIError);
	mock(new Response('bad gateway', { status: 502 }));
	await expect(createLibrary({ name: 'New', source_language: 'nl', target_language: 'en' })).rejects.toBeInstanceOf(DoublanguNetworkError);
});

it('rejects a successful CSRF bootstrap that does not set a cookie', async () => {
	clearCookies();
	mock(json(200, { ok: true }));
	await expect(createLibrary({ name: 'New', source_language: 'nl', target_language: 'en' })).rejects.toThrow('CSRF cookie not set');
});
