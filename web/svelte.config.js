import adapter from '@sveltejs/adapter-static';

/** @type {import('@sveltejs/kit').Config} */
const config = {
	compilerOptions: {
		runes: true
	},
	kit: {
		alias: {
			'$contracts': '../contracts'
		},
		adapter: adapter({
			fallback: 'index.html'
		})
	}
};

export default config;
