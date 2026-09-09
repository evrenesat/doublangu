import { beforeEach, describe, expect, it } from 'vitest';
import {
	parseReadingMode,
	readReadingMode,
	READING_MODE_STORAGE_KEY,
	saveReadingMode
} from './readingMode';

function memoryStorage(initial: Record<string, string> = {}): Storage {
	const data = new Map(Object.entries(initial));
	return {
		get length() {
			return data.size;
		},
		clear: () => data.clear(),
		getItem: (key: string) => data.get(key) ?? null,
		key: (index: number) => [...data.keys()][index] ?? null,
		removeItem: (key: string) => {
			data.delete(key);
		},
		setItem: (key: string, value: string) => {
			data.set(key, value);
		}
	};
}

describe('parseReadingMode', () => {
	it('defaults to learning for missing or invalid values', () => {
		expect(parseReadingMode(null)).toBe('learning');
		expect(parseReadingMode(undefined)).toBe('learning');
		expect(parseReadingMode('')).toBe('learning');
		expect(parseReadingMode('LEARNING')).toBe('learning');
		expect(parseReadingMode('condensed ')).toBe('learning');
	});

	it('accepts the exact condensed value', () => {
		expect(parseReadingMode('condensed')).toBe('condensed');
		expect(parseReadingMode('learning')).toBe('learning');
	});
});

describe('readReadingMode', () => {
	let storage: Storage;

	beforeEach(() => {
		storage = memoryStorage();
	});

	it('defaults to learning when nothing is stored', () => {
		expect(readReadingMode(storage)).toBe('learning');
	});

	it('restores a stored condensed choice', () => {
		storage.setItem(READING_MODE_STORAGE_KEY, 'condensed');
		expect(readReadingMode(storage)).toBe('condensed');
	});

	it('falls back to learning for corrupt values and missing storage', () => {
		storage.setItem(READING_MODE_STORAGE_KEY, 'compact');
		expect(readReadingMode(storage)).toBe('learning');
		expect(readReadingMode(undefined)).toBe('learning');
	});

	it('falls back to learning when storage throws', () => {
		const broken = {
			getItem: () => {
				throw new Error('blocked');
			},
			setItem: () => {}
		} as unknown as Storage;
		expect(readReadingMode(broken)).toBe('learning');
	});
});

describe('saveReadingMode', () => {
	it('round-trips the mode through storage', () => {
		const storage = memoryStorage();
		saveReadingMode('condensed', storage);
		expect(storage.getItem(READING_MODE_STORAGE_KEY)).toBe('condensed');
		expect(readReadingMode(storage)).toBe('condensed');
		saveReadingMode('learning', storage);
		expect(readReadingMode(storage)).toBe('learning');
	});

	it('never throws when storage is blocked', () => {
		const broken = {
			getItem: () => null,
			setItem: () => {
				throw new Error('blocked');
			}
		} as unknown as Storage;
		expect(() => saveReadingMode('condensed', broken)).not.toThrow();
		expect(() => saveReadingMode('condensed', undefined)).not.toThrow();
	});
});
