import { test, expect } from '@playwright/test';

test('page loads and shows health status', async ({ page }) => {
	await page.goto('/');

	// Verify the page title
	await expect(page.locator('h1')).toHaveText('Doublangu');

	// The health check should eventually show either ok or error
	// (depends on whether the Go server is running)
	await page.waitForTimeout(3000);

	const healthText = await page.locator('.health').innerText();
	expect(healthText.length).toBeGreaterThan(0);
});
