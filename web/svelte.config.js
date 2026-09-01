import adapter from '@sveltejs/adapter-static';

const base = process.env.DOUBLANGU_WEB_BASE_PATH ?? '';

/** @type {import('@sveltejs/kit').Config} */
const config = {
	compilerOptions: {
		runes: true
	},
	kit: {
		paths: {
			base
		},
		alias: {
			'$contracts': '../contracts'
		},
		adapter: adapter({
			fallback: 'index.html'
		})
	}
};

export default config;
