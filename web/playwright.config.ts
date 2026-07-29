import { defineConfig } from '@playwright/test';

export default defineConfig({
	testDir: 'tests/e2e',
	fullyParallel: false,
	retries: 0,
	workers: 1,
	reporter: 'list',
	use: {
		baseURL: 'http://localhost:5173',
		headless: true,
		viewport: { width: 1280, height: 720 }
	},
	webServer: [
		{
			command: 'cd .. && go run ./cmd/doublangu-server',
			port: 8080,
			reuseExistingServer: !process.env.CI,
			timeout: 15000
		},
		{
			command: 'npm run dev -- --port 5173',
			port: 5173,
			reuseExistingServer: !process.env.CI,
			timeout: 15000
		}
	]
});
