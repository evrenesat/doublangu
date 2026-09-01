import { describe, expect, it } from 'vitest';
import { applyReaderTheme, readReaderTheme, saveReaderTheme } from './theme';

describe('reader theme', () => {
	it('persists only supported themes and applies a semantic root attribute', () => {
		const storage = new Map<string, string>();
		const fakeStorage = {
			getItem: (key: string) => storage.get(key) ?? null,
			setItem: (key: string, value: string) => storage.set(key, value)
		} as unknown as Storage;
		const root = document.createElement('html');
		expect(readReaderTheme(fakeStorage)).toBe('midnight');
		saveReaderTheme('paper', fakeStorage);
		applyReaderTheme(readReaderTheme(fakeStorage), root);
		expect(root.dataset.readerTheme).toBe('paper');
		storage.set('doublangu.reader.theme', 'unknown');
		expect(readReaderTheme(fakeStorage)).toBe('midnight');
	});
});
