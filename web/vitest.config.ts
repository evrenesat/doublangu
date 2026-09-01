import { defineConfig } from 'vitest/config';
import { svelte } from '@sveltejs/vite-plugin-svelte';

// Test-appropriate Svelte configuration using client-side resolution.
// Vitest uses SSR mode by default which resolves svelte to its server-side entry.
// Override the export conditions to force browser/client resolution.
export default defineConfig({
	resolve: {
		alias: {
			'$lib': new URL('./src/lib', import.meta.url).pathname,
			'$contracts': new URL('../contracts', import.meta.url).pathname
		}
	},
	plugins: [
		{
			name: 'test-sveltekit-runtime',
			resolveId(id) {
				if (id === '$app/navigation') return '\u0000test-sveltekit-navigation';
				if (id === '$app/paths') return '\u0000test-sveltekit-paths';
				return undefined;
			},
			load(id) {
				if (id === '\u0000test-sveltekit-navigation') {
					return 'export async function goto(path) { globalThis.__doublanguLastNavigation = path; }';
				}
				if (id === '\u0000test-sveltekit-paths') return 'export const base = "";';
				return undefined;
			}
		},
		svelte({
			compilerOptions: {
				runes: true,
				dev: true
			}
		}),
		{
			name: 'svelte-client-resolve',
			enforce: 'post',
			config(cfg) {
				if (!cfg.resolve) cfg.resolve = {};
				if (!cfg.resolve.conditions) cfg.resolve.conditions = [];
				cfg.resolve.conditions = ['browser', ...cfg.resolve.conditions];
			}
		}
	],
	test: {
		environment: 'jsdom',
		globals: true,
		include: ['src/**/*.{test,spec}.{js,ts,svelte}'],
		deps: {
			inline: ['svelte']
		}
	}
});
