import type { components } from './generated';

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
		const response = await fetch('/api/v1/auth/csrf', { credentials: 'same-origin' });
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
	const response = await fetch(url, { ...init, credentials: 'same-origin', headers });
	if (!response.ok) return throwResponseError(response);
	if (response.status === 204 || response.headers.get('content-length') === '0') return undefined as T;
	return (await response.json()) as T;
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
