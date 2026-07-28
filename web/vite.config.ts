import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vitest/config';

export default defineConfig({
	plugins: [sveltekit()],
	server: {
		proxy: {
			'/api': 'http://localhost:8080',
			'/health': 'http://localhost:8080'
		}
	},
	test: {
		include: ['tests/unit/**/*.{test,spec}.{js,ts}']
	}
});
