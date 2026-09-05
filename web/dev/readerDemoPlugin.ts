import type { Plugin } from 'vite';
import { readerFixture, readerFixtureID } from './readerFixture';
import { longReaderFixture, longReaderFixtureID } from './longReaderFixture';

/** Only the synthetic article is mocked. Login/settings use the real local Go API. */
export function readerDemoPlugin(): Plugin {
	return {
		name: 'doublangu-reader-design-fixture', apply: 'serve',
		configureServer(server) {
			server.middlewares.use((request, response, next) => {
				const path = request.url?.split('?')[0];
				const id = path?.split('/')[4];
				const fixture = id === readerFixtureID ? readerFixture : id === longReaderFixtureID ? longReaderFixture : null;
				if (!fixture || !path?.startsWith('/api/v1/articles/')) return next();
				response.setHeader('Content-Type', 'application/json');
				if (request.method !== 'GET') {
					response.statusCode = 409;
					response.end(JSON.stringify({ error: 'This sample article is read-only. Use a saved article for analysis and audio.' }));
				} else if (path.endsWith('/narration')) {
					response.end(JSON.stringify({ article_id: id, ...fixture.narration, clips: [] }));
				} else if (path === `/api/v1/articles/${id}`) {
					response.end(JSON.stringify(fixture));
				} else { response.statusCode = 404; response.end(JSON.stringify({ error: 'Sample endpoint not found' })); }
			});
		}
	};
}
