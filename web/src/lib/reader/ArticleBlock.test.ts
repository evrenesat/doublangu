import { cleanup, fireEvent, render, screen, waitFor } from '@testing-library/svelte';
import { afterEach, describe, expect, it, vi } from 'vitest';
import type { ArticleAnnotation, ArticleBlock } from '$lib/api/client';
import ArticleBlockView from './ArticleBlock.svelte';

const annotation: ArticleAnnotation = {
	id: '01J00000000000000000000002',
	article_block_id: '01J00000000000000000000001',
	start_utf16: 7,
	end_utf16: 21,
	source_text: 'tot rust komen',
	kind: 'expression',
	learning_key: 'tot rust komen',
	primary_translation: 'to calm down',
	alternatives: ['to settle down'],
	literal_translation: 'to come to rest',
	meaning_note: 'To become calm.',
	usage_note: 'Use after stress or activity.',
	parts_note: 'tot rust + komen',
	suggest_shadow: true,
	learning_state: null,
	show_shadow: true
};

const block: ArticleBlock = {
	id: '01J00000000000000000000001',
	article_id: '01J00000000000000000000000',
	block_index: 0,
	kind: 'paragraph',
	source_text: 'Ik wil tot rust komen.',
	annotations: [annotation],
	sentences: [],
	occurrences: []
};

const otherAnnotation: ArticleAnnotation = {
	...annotation,
	id: '01J00000000000000000000003',
	start_utf16: 0,
	end_utf16: 2,
	source_text: 'Ik',
	kind: 'word',
	learning_key: 'ik',
	primary_translation: 'I',
	alternatives: [],
	literal_translation: '',
	meaning_note: 'The speaker.',
	usage_note: '',
	parts_note: ''
};

const twoAnnotationBlock: ArticleBlock = {
	...block,
	annotations: [otherAnnotation, annotation]
};

afterEach(() => {
	cleanup();
	vi.useRealTimers();
});

describe('ArticleBlock', () => {
	it('shows a faint subtitle and opens immediate translation details on hover', async () => {
		const onLearningStatus = vi.fn(async () => {});
		render(ArticleBlockView, { block, onLearningStatus });
		const paragraph = document.querySelector('.article-block');
		if (!paragraph) throw new Error('article block was not rendered');
		const rendered = paragraph.cloneNode(true) as HTMLElement;
		rendered.querySelectorAll('.translation-subtitle').forEach((node) => node.remove());
		expect(rendered.textContent).toBe(block.source_text);
		const trigger = screen.getByRole('button', { name: 'tot rust komen: to calm down' });
		expect(screen.getByText('to calm down')).toBeTruthy();
		await fireEvent.pointerEnter(trigger);
		expect(screen.getByRole('dialog')).toBeTruthy();
		expect(screen.getByText('Also: to settle down')).toBeTruthy();
	});

	it('pins on click, exposes one explore detail at a time, and closes with Escape', async () => {
		render(ArticleBlockView, { block, onLearningStatus: vi.fn(async () => {}) });
		const trigger = screen.getByRole('button', { name: 'tot rust komen: to calm down' });
		await fireEvent.click(trigger);
		expect(screen.getByRole('dialog')).toBeTruthy();
		await fireEvent.click(screen.getByRole('button', { name: 'Explore' }));
		expect(screen.getByRole('button', { name: 'Meaning' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Usage' })).toBeTruthy();
		expect(screen.getByRole('button', { name: 'Parts' })).toBeTruthy();
		expect(screen.getByText('To become calm.')).toBeTruthy();
		await fireEvent.click(screen.getByRole('button', { name: 'Usage' }));
		expect(screen.getByText('Use after stress or activity.')).toBeTruthy();
		expect(screen.queryByText('To become calm.')).toBeNull();
		await fireEvent.keyDown(document, { key: 'Escape' });
		await waitFor(() => expect(screen.queryByRole('dialog')).toBeNull());
	});

	it('keeps a pinned annotation when another annotation receives hover or focus', async () => {
		render(ArticleBlockView, { block: twoAnnotationBlock, onLearningStatus: vi.fn(async () => {}) });
		const expressionTrigger = screen.getByRole('button', { name: 'tot rust komen: to calm down' });
		const wordTrigger = screen.getByRole('button', { name: 'Ik: I' });
		await fireEvent.click(expressionTrigger);
		expect(screen.getByRole('dialog').getAttribute('aria-label')).toBe('Translation for tot rust komen');
		await fireEvent.pointerEnter(wordTrigger);
		await fireEvent.focus(wordTrigger);
		expect(screen.getByRole('dialog').getAttribute('aria-label')).toBe('Translation for tot rust komen');
		expect(screen.getByRole('dialog').textContent).toContain('to calm down');
	});

	it('keeps a hover popover open while the pointer crosses the grace period', async () => {
		vi.useFakeTimers();
		render(ArticleBlockView, { block, onLearningStatus: vi.fn(async () => {}) });
		const trigger = screen.getByRole('button', { name: 'tot rust komen: to calm down' });
		await fireEvent.pointerEnter(trigger);
		expect(screen.getByRole('dialog')).toBeTruthy();
		await fireEvent.pointerLeave(trigger);
		vi.advanceTimersByTime(119);
		expect(screen.getByRole('dialog')).toBeTruthy();
		await fireEvent.pointerEnter(screen.getByRole('dialog'));
		vi.advanceTimersByTime(20);
		expect(screen.getByRole('dialog')).toBeTruthy();
	});

	it('calls the optimistic learning action and keeps the annotated word hoverable without a subtitle', async () => {
		const onLearningStatus = vi.fn(async () => {});
		const learnedBlock = { ...block, annotations: [{ ...annotation, show_shadow: false }] };
		render(ArticleBlockView, { block: learnedBlock, onLearningStatus });
		const trigger = screen.getByRole('button', { name: 'tot rust komen: to calm down' });
		expect(screen.queryByText('to calm down')).toBeNull();
		await fireEvent.click(trigger);
		await fireEvent.click(screen.getByRole('button', { name: 'Mark learned' }));
		await waitFor(() => expect(onLearningStatus).toHaveBeenCalledWith(learnedBlock.annotations[0], 'learned'));
		expect(screen.getByRole('status').textContent).toContain('Marked learned');
	});
});
