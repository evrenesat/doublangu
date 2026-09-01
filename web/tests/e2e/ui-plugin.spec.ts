import { expect, test, type Page } from "@playwright/test";

const prefix = "/api/v1/plugins/assets/v1/";
const healthy = `${prefix}1ba97c704d58ca3de92ad1aa26fc8937051883d6e7c9fdbe1381930f05eb73aa/healthy.js`;
const throwing = `${prefix}915ce48a6eda07fe33329b3238a9bbaf15859bd481463fcc2fdb0c98410531f7/throwing.js`;
const malformed = `${prefix}bcf176938fea006380fd8c08b75c144da25ab099196a30e10d52d01b8c42fa66/malformed.js`;
const contributions = [
  { id: "route", label: "Route", type: "view", container: "", priority: 0, icon: "", source_url: healthy, plugin_id: "sample-plugin" },
  { id: "panel", label: "Panel", type: "panel", container: "", priority: 1, icon: "", source_url: healthy, plugin_id: "sample-plugin" },
  { id: "settings", label: "Settings", type: "panel", container: "", priority: 2, icon: "", source_url: healthy, plugin_id: "sample-plugin" },
  { id: "command", label: "Command", type: "widget", container: "", priority: 3, icon: "", source_url: healthy, plugin_id: "sample-plugin" },
  { id: "reader", label: "Reader", type: "view", container: "", priority: 4, icon: "", source_url: healthy, plugin_id: "sample-plugin" },
  { id: "radial", label: "Radial", type: "widget", container: "", priority: 5, icon: "", source_url: healthy, plugin_id: "sample-plugin" },
];

async function serveContributions(page: Page, items: unknown = contributions) {
  await page.route("**/api/v1/ui/contributions", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ version: "v1", contributions: items }) }));
}

async function servePluginAssets(page: Page) {
  await page.route("**/api/v1/plugins/assets/v1/**", async (route) => {
    const fixtureURL = new URL(route.request().url());
    fixtureURL.pathname = fixtureURL.pathname.replace("/api/v1/plugins/assets/", "/plugin-assets/");
    const response = await route.fetch({ url: fixtureURL.toString() });
    await route.fulfill({ response });
  });
}

test.describe("UI plugin host", () => {
  test.beforeEach(async ({ page }) => {
    await page.route("**/api/v1/auth/session", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify({ authenticated: true }) }));
    await servePluginAssets(page);
  });

  test("redirects an unauthenticated contributions request to sign-in", async ({ page }) => {
    await page.route("**/api/v1/ui/contributions", (route) => route.fulfill({
      status: 401,
      contentType: "application/json",
      body: JSON.stringify({ error: "authentication required", code: "v1.authentication_error" }),
    }));
    await page.goto("/plugins");
    await expect(page).toHaveURL("/login");
    await expect(page.getByLabel("Password")).toBeVisible();
    await expect(page.getByText("UI contributions request failed")).toHaveCount(0);
  });

  test("mounts six healthy surfaces beside a throwing sibling and preserves shell interactivity", async ({ page }) => {
    await serveContributions(page, [...contributions, { id: "throwing", label: "Throwing", type: "panel", container: "", priority: 99, icon: "", source_url: throwing, plugin_id: "throwing-plugin" }]);
    await page.goto("/plugins");
    await expect(page.getByText("Healthy panel")).toBeVisible();
    await expect(page.locator("[data-contribution-id]")).toHaveCount(7);
    await expect(page.getByTestId("mount-error-throwing")).toContainText("mount failed");
    await expect(page.locator(".brand")).toBeVisible();
    await expect(page.getByTestId("linear-sample-command")).toBeVisible();
    await expect(page.getByTestId("radial-sample-command")).toHaveAttribute("data-command-id", "sample-command");
    await page.getByTestId("linear-sample-command").click();
    await page.getByTestId("radial-sample-command").click();
    await expect(page.locator("#command-executions")).toHaveText("2");
    await expect(page.locator("[data-plugin-navigation='sample-navigation']")).toBeVisible();
  });

  test("unload and reload remove and recreate plugin-owned state", async ({ page }) => {
    await serveContributions(page);
    await page.goto("/plugins");
    await expect(page.getByTestId("linear-sample-command")).toBeVisible();
    await page.getByTestId("unload-plugins").click();
    await expect(page.getByTestId("linear-sample-command")).toHaveCount(0);
    await page.getByTestId("reload-plugins").click();
    await expect(page.getByTestId("linear-sample-command")).toBeVisible();
  });

  test("rejects a malformed default export while retaining a healthy sibling", async ({ page }) => {
    await serveContributions(page, [...contributions, { id: "malformed", label: "Malformed", type: "panel", container: "", priority: 99, icon: "", source_url: malformed, plugin_id: "expected-plugin" }]);
    await page.goto("/plugins");
    await expect(page.getByText("Healthy panel")).toBeVisible();
    await expect(page.getByTestId("mount-error-malformed")).toContainText("invalid default export");
  });

  test("rejects data, cross-origin, and duplicate contribution payloads", async ({ page }) => {
    for (const source_url of ["data:text/javascript,export default {}", "https://example.invalid/plugin.js"]) {
      await serveContributions(page, [{ id: "bad", label: "Bad", type: "panel", container: "", priority: 1, icon: "", source_url, plugin_id: "bad-plugin" }]);
      await page.goto("/plugins");
      await expect(page.locator(".shell-error")).toContainText("authorized asset URL");
      await page.unroute("**/api/v1/ui/contributions");
    }
    await serveContributions(page, [contributions[0]!, { ...contributions[0]!, label: "Duplicate" }]);
    await page.goto("/plugins");
    await expect(page.locator(".shell-error")).toContainText("duplicated");
  });
});
