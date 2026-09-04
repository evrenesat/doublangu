import { cleanup, render, screen } from '@testing-library/svelte';
import { createRawSnippet } from 'svelte';
import { afterEach, expect, it } from 'vitest';
import SettingsLayout from '../../routes/settings/+layout.svelte';

type PageStub = { route: { id: string }; params: Record<string, string> };

function setRoute(id: string): void {
	(globalThis as typeof globalThis & { __doublanguPage?: PageStub }).__doublanguPage = { route: { id }, params: {} };
}

const children = createRawSnippet(() => ({
	render: () => '<p data-testid="child-content">CHILD CONTENT</p>'
}));

afterEach(() => {
	cleanup();
	delete (globalThis as typeof globalThis & { __doublanguPage?: PageStub }).__doublanguPage;
});

it('renders the heading, introduction, and one link per Settings section', () => {
	setRoute('/settings');
	render(SettingsLayout, { children });

	expect(screen.getByRole('heading', { name: 'Settings' })).toBeTruthy();
	expect(
		screen.getByText('Configure reading behavior, analysis models, workers, and server diagnostics.')
	).toBeTruthy();

	expect((screen.getByRole('link', { name: 'Reader' }) as HTMLAnchorElement).getAttribute('href')).toBe('/settings');
	expect((screen.getByRole('link', { name: 'Analysis' }) as HTMLAnchorElement).getAttribute('href')).toBe('/settings/analysis');
	expect((screen.getByRole('link', { name: 'Workers' }) as HTMLAnchorElement).getAttribute('href')).toBe('/settings/workers');
	expect((screen.getByRole('link', { name: 'System' }) as HTMLAnchorElement).getAttribute('href')).toBe('/settings/system');
	expect(screen.getByTestId('child-content')).toBeTruthy();
});

it('marks the active local section for every settings route', () => {
	for (const [route, label] of [
		['/settings', 'Reader'],
		['/settings/analysis', 'Analysis'],
		['/settings/workers', 'Workers'],
		['/settings/system', 'System']
	] as const) {
		cleanup();
		setRoute(route);
		render(SettingsLayout, { children });
		const active = screen.getByRole('link', { name: label });
		expect(active.getAttribute('aria-current')).toBe('page');
		for (const other of ['Reader', 'Analysis', 'Workers', 'System']) {
			if (other === label) continue;
			expect(screen.getByRole('link', { name: other }).getAttribute('aria-current')).toBeNull();
		}
	}
});
