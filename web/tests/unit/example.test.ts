import { describe, it, expect } from 'vitest';

/**
 * Example unit test to verify the test pipeline works.
 */
function sum(a: number, b: number): number {
	return a + b;
}

describe('example', () => {
	it('adds two numbers', () => {
		expect(sum(1, 2)).toBe(3);
		expect(sum(-1, 1)).toBe(0);
		expect(sum(0, 0)).toBe(0);
	});
});
