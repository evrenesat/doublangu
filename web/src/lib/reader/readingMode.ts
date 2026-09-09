// Browser-persisted article reading mode. Learning is the initial default;
// Condensed is a plain-paragraph presentation of the same semantic spans.
// This never touches the server-owned pronounce-on-hover preference.

export type ReadingMode = 'learning' | 'condensed';

export const READING_MODE_STORAGE_KEY = 'doublangu.reader.readingMode.v1';

export function parseReadingMode(value: unknown): ReadingMode {
	return value === 'condensed' ? 'condensed' : 'learning';
}

type StorageLike = Pick<Storage, 'getItem' | 'setItem'>;

function globalStorage(): StorageLike | undefined {
	try {
		const storage = globalThis.localStorage;
		return storage ?? undefined;
	} catch {
		return undefined;
	}
}

export function readReadingMode(storage: StorageLike | undefined = globalStorage()): ReadingMode {
	try {
		if (!storage) return 'learning';
		return parseReadingMode(storage.getItem(READING_MODE_STORAGE_KEY));
	} catch {
		// Blocked or unavailable storage falls back safely to learning.
		return 'learning';
	}
}

export function saveReadingMode(
	mode: ReadingMode,
	storage: StorageLike | undefined = globalStorage()
): void {
	try {
		storage?.setItem(READING_MODE_STORAGE_KEY, mode);
	} catch {
		// A blocked store must never break reading; the mode still applies
		// to the current view and learning remains the next-load default.
	}
}
