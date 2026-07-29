import { defineConfig } from '@playwright/test';
import { mkdtempSync } from 'node:fs';
import { tmpdir } from 'node:os';
import { join } from 'node:path';

const testDataRoot = mkdtempSync(join(tmpdir(), 'doublangu-playwright-'));
const serverEnv = {
	DOUBLANGU_SECRET: process.env.DOUBLANGU_SECRET ?? Buffer.alloc(32, 7).toString('base64'),
	DOUBLANGU_DB_PATH: process.env.DOUBLANGU_DB_PATH ?? join(testDataRoot, 'doublangu.db'),
	DOUBLANGU_MEDIA_PATH: process.env.DOUBLANGU_MEDIA_PATH ?? join(testDataRoot, 'media'),
	DOUBLANGU_DATA_PATH: process.env.DOUBLANGU_DATA_PATH ?? join(testDataRoot, 'data')
};

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
			env: { ...process.env, ...serverEnv },
			port: 8080,
			reuseExistingServer: false,
			timeout: 15000
		},
		{
			command: 'rm -rf .svelte-kit/generated/client-optimized && npm run dev -- --port 5173',
			port: 5173,
			reuseExistingServer: false,
			timeout: 15000
		}
	]
});
