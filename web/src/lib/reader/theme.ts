export const readerThemes = ['midnight', 'paper', 'high-contrast'] as const;
export type ReaderTheme = (typeof readerThemes)[number];

const storageKey = 'doublangu.reader.theme';

export function isReaderTheme(value: string | null | undefined): value is ReaderTheme {
	return value !== undefined && value !== null && (readerThemes as readonly string[]).includes(value);
}

export function readReaderTheme(storage: Storage | undefined = typeof localStorage === 'undefined' ? undefined : localStorage): ReaderTheme {
	const value = storage?.getItem(storageKey);
	return isReaderTheme(value) ? value : 'midnight';
}

export function applyReaderTheme(theme: ReaderTheme, root: HTMLElement | undefined = typeof document === 'undefined' ? undefined : document.documentElement): void {
	root?.setAttribute('data-reader-theme', theme);
}

export function saveReaderTheme(theme: ReaderTheme, storage: Storage | undefined = typeof localStorage === 'undefined' ? undefined : localStorage): void {
	storage?.setItem(storageKey, theme);
}

export function setReaderTheme(theme: ReaderTheme, root?: HTMLElement, storage?: Storage): void {
	applyReaderTheme(theme, root);
	saveReaderTheme(theme, storage);
}
