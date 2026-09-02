import type { components } from './generated';
import { appAudioPath, appPath } from '$lib/paths';

export type Library = components['schemas']['Library'];
export type LibraryCreate = components['schemas']['LibraryCreate'];
export type LibraryUpdate = components['schemas']['LibraryUpdate'];
export type Work = components['schemas']['Work'];
export type WorkCreate = components['schemas']['WorkCreate'];
export type WorkUpdate = components['schemas']['WorkUpdate'];
export type Edition = components['schemas']['Edition'];
export type EditionCreate = components['schemas']['EditionCreate'];
export type EditionUpdate = components['schemas']['EditionUpdate'];
export type Chapter = components['schemas']['Chapter'];
export type ChapterCreate = components['schemas']['ChapterCreate'];
export type ChapterUpdate = components['schemas']['ChapterUpdate'];
export type APIError = components['schemas']['APIError'];
export type Article = components['schemas']['Article'];
export type ArticleSummary = components['schemas']['ArticleSummary'];
export type ArticleCreate = components['schemas']['ArticleCreate'];
export type ArticleBlock = components['schemas']['ArticleBlock'];
export type ArticleAnnotation = components['schemas']['ArticleAnnotation'];
export type LearningState = components['schemas']['LearningState'];
export type LearningStateInput = components['schemas']['LearningStateInput'];
export type LearningStatus = components['schemas']['LearningStatus'];
export type AnalysisStatus = components['schemas']['AnalysisStatus'];
export type ReasoningEffort = components['schemas']['ReasoningEffort'];
export type AnalysisModel = components['schemas']['AnalysisModel'];
export type AnalysisModelsResponse = components['schemas']['AnalysisModelsResponse'];
export type AnalysisSettings = components['schemas']['AnalysisSettings'];
export type AnalysisSettingsInput = components['schemas']['AnalysisSettingsInput'];
export type AnalysisTurn = components['schemas']['AnalysisTurn'];
export type AnalysisRunSummary = components['schemas']['AnalysisRunSummary'];
export type AnalysisRun = components['schemas']['AnalysisRun'];
export type AnalysisRunsPage = components['schemas']['AnalysisRunsPage'];
export type ReaderSettings = components['schemas']['ReaderSettings'];
export type ReaderSettingsInput = components['schemas']['ReaderSettingsInput'];
export type NarrationStatus = components['schemas']['NarrationStatus'];
export type SemanticLearningState = components['schemas']['SemanticLearningState'];
export type SemanticLearningStateInput = components['schemas']['SemanticLearningStateInput'];
export type ArticleOccurrence = components['schemas']['ArticleOccurrence'];
export type ArticleOccurrenceSpan = components['schemas']['ArticleOccurrenceSpan'];
export type ArticleSentence = components['schemas']['ArticleSentence'];
export type SemanticSense = components['schemas']['SemanticSense'];
export type AudioRef = components['schemas']['AudioRef'];
export type Narration = components['schemas']['Narration'];
export type NarrationClearResult = components['schemas']['NarrationClearResult'];

export interface SessionStatus {
	authenticated: boolean;
}

export class DoublanguAPIError extends Error {
	code: string;
	status: number;

	constructor(status: number, body: APIError) {
		super(body.error);
		this.name = 'DoublanguAPIError';
		this.code = body.code;
		this.status = status;
	}
}

export class DoublanguNetworkError extends Error {
	constructor(message: string) {
		super(message);
		this.name = 'DoublanguNetworkError';
	}
}

function id(value: string): string {
	return encodeURIComponent(value);
}

function csrfToken(): string {
	return document.cookie.match(/(?:^|;\s*)csrf_token=([^;]*)/)?.[1] ?? '';
}

function isAPIError(value: unknown): value is APIError {
	return (
		typeof value === 'object' &&
		value !== null &&
		typeof (value as Record<string, unknown>).error === 'string' &&
		typeof (value as Record<string, unknown>).code === 'string'
	);
}

function appAudioRef(ref: AudioRef | null): AudioRef | null {
	if (!ref) return null;
	return { ...ref, url: appAudioPath(ref.url) };
}

function appArticleAudio(article: Article): Article {
	const result = { ...article };
	if (article.blocks) {
		result.blocks = article.blocks.map((block) => {
			const next = { ...block };
			if (Array.isArray(block.sentences)) {
				next.sentences = block.sentences.map((sentence) => ({ ...sentence, audio: appAudioRef(sentence.audio) }));
			}
			if (Array.isArray(block.occurrences)) {
				next.occurrences = block.occurrences.map((occurrence) => ({ ...occurrence, pronunciation: appAudioRef(occurrence.pronunciation) }));
			}
			return next;
		});
	}
	if (Array.isArray(article.sentences)) {
		result.sentences = article.sentences.map((sentence) => ({ ...sentence, audio: appAudioRef(sentence.audio) }));
	}
	if (Array.isArray(article.occurrences)) {
		result.occurrences = article.occurrences.map((occurrence) => ({ ...occurrence, pronunciation: appAudioRef(occurrence.pronunciation) }));
	}
	return result;
}

function appNarrationAudio(narration: Narration): Narration {
	return {
		...narration,
		clips: narration.clips.map((clip) => ({ ...clip, audio: appAudioRef(clip.audio) }))
	};
}

async function throwResponseError(response: Response, context = 'Request'): Promise<never> {
	let body: unknown;
	try {
		body = await response.json();
	} catch {
		throw new DoublanguNetworkError(`${context} failed with status ${response.status}`);
	}
	if (!isAPIError(body)) {
		throw new DoublanguNetworkError(`${context} returned a malformed error response`);
	}
	throw new DoublanguAPIError(response.status, body);
}

async function mutationHeaders(): Promise<Headers> {
	let token = csrfToken();
	if (!token) {
		const response = await fetch(appPath('/api/v1/auth/csrf'), { credentials: 'same-origin' });
		if (!response.ok) await throwResponseError(response, 'CSRF bootstrap');
		token = csrfToken();
		if (!token) throw new DoublanguNetworkError('CSRF cookie not set after bootstrap');
	}
	return new Headers({ 'Content-Type': 'application/json', 'X-CSRF-Token': token });
}

async function apiFetch<T>(url: string, init?: RequestInit & { csrf?: boolean }): Promise<T> {
	const headers = new Headers(init?.headers);
	if (init?.csrf) {
		for (const [name, value] of (await mutationHeaders()).entries()) headers.set(name, value);
	}
	const response = await fetch(appPath(url as `/${string}`), { ...init, credentials: 'same-origin', headers });
	if (!response.ok) return throwResponseError(response);
	if (response.status === 204 || response.headers.get('content-length') === '0') return undefined as T;
	return (await response.json()) as T;
}

export async function getSession(): Promise<SessionStatus> {
	return apiFetch('/api/v1/auth/session');
}

export async function logoutSession(): Promise<void> {
	return apiFetch('/api/v1/auth/logout', { method: 'POST', csrf: true });
}

export async function listLibraries(): Promise<Library[]> {
	return apiFetch('/api/v1/libraries');
}

export async function getLibrary(libraryId: string): Promise<Library> {
	return apiFetch(`/api/v1/libraries/${id(libraryId)}`);
}

export async function createLibrary(data: LibraryCreate): Promise<Library> {
	return apiFetch('/api/v1/libraries', { method: 'POST', body: JSON.stringify(data), csrf: true });
}

export async function updateLibrary(libraryId: string, data: LibraryUpdate): Promise<Library> {
	return apiFetch(`/api/v1/libraries/${id(libraryId)}`, { method: 'PUT', body: JSON.stringify(data), csrf: true });
}

export async function deleteLibrary(libraryId: string): Promise<void> {
	return apiFetch(`/api/v1/libraries/${id(libraryId)}`, { method: 'DELETE', csrf: true });
}

export async function listWorks(libraryId: string): Promise<Work[]> {
	return apiFetch(`/api/v1/libraries/${id(libraryId)}/works`);
}

export async function getWork(workId: string): Promise<Work> {
	return apiFetch(`/api/v1/works/${id(workId)}`);
}

export async function createWork(libraryId: string, data: WorkCreate): Promise<Work> {
	return apiFetch(`/api/v1/libraries/${id(libraryId)}/works`, { method: 'POST', body: JSON.stringify(data), csrf: true });
}

export async function updateWork(workId: string, data: WorkUpdate): Promise<Work> {
	return apiFetch(`/api/v1/works/${id(workId)}`, { method: 'PUT', body: JSON.stringify(data), csrf: true });
}

export async function deleteWork(workId: string): Promise<void> {
	return apiFetch(`/api/v1/works/${id(workId)}`, { method: 'DELETE', csrf: true });
}

export async function listEditions(workId: string): Promise<Edition[]> {
	return apiFetch(`/api/v1/works/${id(workId)}/editions`);
}

export async function getEdition(editionId: string): Promise<Edition> {
	return apiFetch(`/api/v1/editions/${id(editionId)}`);
}

export async function createEdition(workId: string, data: EditionCreate): Promise<Edition> {
	return apiFetch(`/api/v1/works/${id(workId)}/editions`, { method: 'POST', body: JSON.stringify(data), csrf: true });
}

export async function updateEdition(editionId: string, data: EditionUpdate): Promise<Edition> {
	return apiFetch(`/api/v1/editions/${id(editionId)}`, { method: 'PUT', body: JSON.stringify(data), csrf: true });
}

export async function deleteEdition(editionId: string): Promise<void> {
	return apiFetch(`/api/v1/editions/${id(editionId)}`, { method: 'DELETE', csrf: true });
}

export async function listChapters(editionId: string): Promise<Chapter[]> {
	return apiFetch(`/api/v1/editions/${id(editionId)}/chapters`);
}

export async function getChapter(chapterId: string): Promise<Chapter> {
	return apiFetch(`/api/v1/chapters/${id(chapterId)}`);
}

export async function createChapter(editionId: string, data: ChapterCreate): Promise<Chapter> {
	return apiFetch(`/api/v1/editions/${id(editionId)}/chapters`, { method: 'POST', body: JSON.stringify(data), csrf: true });
}

export async function updateChapter(chapterId: string, data: ChapterUpdate): Promise<Chapter> {
	return apiFetch(`/api/v1/chapters/${id(chapterId)}`, { method: 'PUT', body: JSON.stringify(data), csrf: true });
}

export async function deleteChapter(chapterId: string): Promise<void> {
	return apiFetch(`/api/v1/chapters/${id(chapterId)}`, { method: 'DELETE', csrf: true });
}

export async function listArticles(): Promise<ArticleSummary[]> {
	return apiFetch('/api/v1/articles');
}

export async function createArticle(data: ArticleCreate): Promise<Article> {
	return appArticleAudio(await apiFetch<Article>('/api/v1/articles', { method: 'POST', body: JSON.stringify(data), csrf: true }));
}

export async function getArticle(articleId: string): Promise<Article> {
	return appArticleAudio(await apiFetch<Article>(`/api/v1/articles/${id(articleId)}`));
}

export async function enrichArticle(articleId: string): Promise<Article> {
	return appArticleAudio(await apiFetch<Article>(`/api/v1/articles/${id(articleId)}/enrich`, { method: 'POST', csrf: true }));
}

export async function reanalyzeArticle(articleId: string, fresh = false): Promise<Article> {
	return appArticleAudio(await apiFetch<Article>(`/api/v1/articles/${id(articleId)}/reanalyze`, {
		method: 'POST', body: fresh ? JSON.stringify({ fresh: true }) : undefined, csrf: true
	}));
}

export async function getAnalysisModels(refresh = false): Promise<AnalysisModelsResponse> {
	return apiFetch(`/api/v1/analysis/models${refresh ? '?refresh=true' : ''}`);
}

export async function getAnalysisSettings(): Promise<AnalysisSettings> {
	return apiFetch('/api/v1/analysis/settings');
}

export async function getReaderSettings(): Promise<ReaderSettings> {
	return apiFetch('/api/v1/reader/settings');
}

export async function saveReaderSettings(data: ReaderSettingsInput): Promise<ReaderSettings> {
	return apiFetch('/api/v1/reader/settings', { method: 'PUT', body: JSON.stringify(data), csrf: true });
}

export async function saveAnalysisSettings(data: AnalysisSettingsInput): Promise<AnalysisSettings> {
	return apiFetch('/api/v1/analysis/settings', { method: 'PUT', body: JSON.stringify(data), csrf: true });
}

export async function listAnalysisRuns(options: { articleId?: string; limit?: number; cursor?: string } = {}): Promise<AnalysisRunsPage> {
	const query = new URLSearchParams();
	if (options.articleId) query.set('article_id', options.articleId);
	if (options.limit !== undefined) query.set('limit', String(options.limit));
	if (options.cursor) query.set('cursor', options.cursor);
	const suffix = query.toString() ? `?${query.toString()}` : '';
	return apiFetch(`/api/v1/analysis/runs${suffix}`);
}

export async function getAnalysisRun(runId: string): Promise<AnalysisRun> {
	return apiFetch(`/api/v1/analysis/runs/${id(runId)}`);
}

export async function getNarration(articleId: string): Promise<Narration> {
	return appNarrationAudio(await apiFetch<Narration>(`/api/v1/articles/${id(articleId)}/narration`));
}

export async function generateNarration(articleId: string): Promise<Article> {
	return appArticleAudio(await apiFetch<Article>(`/api/v1/articles/${id(articleId)}/narration`, { method: 'POST', csrf: true }));
}

export async function clearNarration(articleId: string): Promise<NarrationClearResult> {
	return apiFetch(`/api/v1/articles/${id(articleId)}/narration`, { method: 'DELETE', csrf: true });
}

export async function updateLearningState(data: LearningStateInput): Promise<LearningState> {
	return apiFetch('/api/v1/learning-state', { method: 'PUT', body: JSON.stringify(data), csrf: true });
}

export async function updateSemanticLearningState(data: SemanticLearningStateInput): Promise<SemanticLearningState> {
	return apiFetch('/api/v1/learning-state', { method: 'PUT', body: JSON.stringify(data), csrf: true });
}
