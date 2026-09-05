import { sveltekit } from '@sveltejs/kit/vite';
import { defineConfig } from 'vite';
import { readerDemoPlugin } from './dev/readerDemoPlugin';

export default defineConfig({
	plugins: [sveltekit(), ...(process.env.DOUBLANGU_READER_DEMO === '1' ? [readerDemoPlugin()] : [])],
	server: {
		proxy: {
			'/health': process.env.DOUBLANGU_DEV_API_TARGET || 'http://localhost:8080',
			'/api': process.env.DOUBLANGU_DEV_API_TARGET || 'http://localhost:8080'
		}
	}
});
