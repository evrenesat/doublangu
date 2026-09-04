import adapter from '@sveltejs/adapter-static';

const base = process.env.DOUBLANGU_WEB_BASE_PATH ?? '';
const versionName = process.env.DOUBLANGU_BUILD_VERSION ?? 'development';

/** @type {import('@sveltejs/kit').Config} */
const config = {
	compilerOptions: {
		runes: true
	},
	kit: {
		version: {
			name: versionName
		},
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
