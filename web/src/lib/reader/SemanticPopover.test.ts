import { cleanup, fireEvent, render, screen } from '@testing-library/svelte';
import { afterEach, expect, it, vi } from 'vitest';
import type { ArticleOccurrence } from '$lib/api/client';
import SemanticPopover from './SemanticPopover.svelte';

vi.mock('$lib/api/client', () => ({
	getDictionaryEntry: vi.fn(async () => ({ status: 'missing' }))
}));

afterEach(cleanup);

it('keeps Explore open across refreshed occurrence objects and closes for a new selection', async () => {
	const occurrence = { id: 'word-1', kind: 'word', spans: [{ source_text: 'bank' }] } as ArticleOccurrence;
	const props = {
		occurrence, articleId: 'article-1', anchor: document.createElement('button'),
		feedback: '', feedbackIsError: false, onEnter: vi.fn(), onLeave: vi.fn(),
		onClose: vi.fn(), onLearningStatus: vi.fn(), onHear: vi.fn()
	};
	const view = render(SemanticPopover, { props });
	const explore = screen.getByRole('button', { name: 'Explore' });
	await fireEvent.click(explore);
	expect(explore.getAttribute('aria-expanded')).toBe('true');
	await view.rerender({ ...props, occurrence: { ...occurrence, shadow_text: 'bench' } });
	expect(explore.getAttribute('aria-expanded')).toBe('true');
	await view.rerender({ ...props, occurrence: { ...occurrence, id: 'word-2' } });
	expect(explore.getAttribute('aria-expanded')).toBe('false');
});
