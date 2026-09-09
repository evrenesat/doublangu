import { expect, test, type Page } from "@playwright/test";

const PROMPT_TYPES = [
  "linguistic_analysis",
  "article_translation",
  "explore",
  "sentence_translation",
  "correction",
] as const;

type PromptType = (typeof PROMPT_TYPES)[number];

const provider = {
  id: "codex-app-server",
  label: "Codex",
  type: "codex_app_server",
  enabled: true,
  stale: false,
  health: "healthy",
  models: [
    {
      id: "model-a",
      display_name: "Model A",
      supported_reasoning_efforts: [{ value: "low" }, { value: "medium" }],
    },
    {
      id: "model-b",
      display_name: "Model B",
      supported_reasoning_efforts: [{ value: "high" }],
    },
  ],
};

function versionRecord(promptType: PromptType, version: number) {
  return {
    id: `v${version}-${promptType}`,
    prompt_type: promptType,
    version,
    label: "",
    instruction_text: `Builtin ${promptType} v${version}.`,
    content_hash: `hash-${version}`,
    created_at: `2026-01-0${version}T00:00:00Z`,
  };
}

function storedProfile(id: string, name: string) {
  return {
    id,
    name,
    is_active: id === "alpha",
    bindings: [
      {
        stage_id: "linguistic_analysis",
        provider_id: "codex-app-server",
        model_id: "model-a",
        options: { reasoning_effort: "low" },
      },
      {
        stage_id: "translation",
        provider_id: "codex-app-server",
        model_id: "model-a",
        options: { reasoning_effort: "low" },
      },
    ],
    explore_binding: {
      stage_id: "translation",
      provider_id: "codex-app-server",
      model_id: "model-a",
      options: { reasoning_effort: "low" },
    },
    prompt_versions: Object.fromEntries(
      PROMPT_TYPES.map((promptType) => [
        promptType,
        { id: `v1-${promptType}`, version: 1, label: "" },
      ]),
    ),
  };
}

const putBodies: Array<Record<string, unknown>> = [];
const stored = [storedProfile("alpha", "Alpha"), storedProfile("beta", "Beta")];

function json(body: unknown, status = 200) {
  return {
    status,
    contentType: "application/json",
    body: JSON.stringify(body),
  };
}

async function mockSettingsAPI(page: Page): Promise<void> {
  putBodies.length = 0;
  await page.route("**/api/v1/auth/session", (route) =>
    route.fulfill(json({ authenticated: true })),
  );
  await page
    .context()
    .addCookies([
      {
        name: "csrf_token",
        value: "test-csrf-token",
        domain: "localhost",
        path: "/",
      },
    ]);
  await page.route("**/api/v1/analysis/providers*", (route) =>
    route.fulfill(json({ providers: [provider] })),
  );
  await page.route("**/api/v1/analysis/profiles", (route) =>
    route.fulfill(json({ profiles: stored })),
  );
  await page.route("**/api/v1/analysis/settings", (route) =>
    route.fulfill(json({ active_profile_id: "alpha" })),
  );
  await page.route("**/api/v1/analysis/prompts/*/versions", async (route) => {
    const url = new URL(route.request().url());
    const segments = url.pathname.split("/");
    const promptType = (segments[segments.length - 2] ?? "") as PromptType;
    if (route.request().method() === "POST") {
      const body = JSON.parse(route.request().postData() ?? "{}") as {
        instruction_text: string;
        label?: string;
      };
      const saved = {
        id: `v3-${promptType}`,
        prompt_type: promptType,
        version: 3,
        label: body.label ?? "",
        instruction_text: body.instruction_text,
        content_hash: "hash-3",
        created_at: "2026-01-03T00:00:00Z",
      };
      await route.fulfill(
        json({ prompt_type: promptType, version: saved }, 201),
      );
      return;
    }
    const versions =
      promptType === "explore"
        ? [versionRecord(promptType, 2), versionRecord(promptType, 1)]
        : [versionRecord(promptType, 1)];
    await route.fulfill(json({ prompt_type: promptType, versions }));
  });
  await page.route("**/api/v1/analysis/profiles/*", async (route) => {
    if (route.request().method() !== "PUT") {
      await route.fulfill(json({ error: "unexpected" }, 500));
      return;
    }
    const body = JSON.parse(route.request().postData() ?? "{}") as Record<
      string,
      unknown
    >;
    putBodies.push(body);
    const profileId = route.request().url().split("/").pop() ?? "";
    const current =
      stored.find((candidate) => candidate.id === profileId) ?? stored[0]!;
    const pinned = body["prompt_versions"] as Record<string, string>;
    const updated = {
      ...current,
      name: body["name"],
      explore_binding: {
        stage_id: "translation",
        ...(body["explore_binding"] as Record<string, unknown>),
      },
      prompt_versions: Object.fromEntries(
        PROMPT_TYPES.map((promptType) => [
          promptType,
          {
            id: pinned[promptType],
            version: pinned[promptType] === `v2-${promptType}` ? 2 : 1,
            label: "",
          },
        ]),
      ),
    };
    await route.fulfill(json(updated));
  });
}

function profileRow(page: Page, name: string) {
  return page.locator("ul.profile-list > li", { hasText: name });
}

test("saving a new prompt version never activates it on any profile", async ({
  page,
}) => {
  await mockSettingsAPI(page);
  await page.goto("/settings/analysis");
  await expect(page.getByRole("heading", { name: "Prompts" })).toBeVisible();

  const library = page.locator("section.prompt-library");
  await library.getByLabel("Prompt type").selectOption("explore");
  await library.getByRole("button", { name: "Edit as new version" }).click();
  await library
    .getByLabel(/Instruction text/)
    .fill("Experiment wording for Explore v3.");
  await library.getByRole("button", { name: "Save new version" }).click();
  await expect(library.getByRole("status")).toContainText(
    "Saved v3 for Explore. Pin it on a profile to use it; nothing was activated.",
  );
  expect(putBodies).toHaveLength(0);

  const alpha = profileRow(page, "Alpha");
  await alpha.getByRole("button", { name: "Edit" }).click();
  const editor = alpha.getByRole("group", { name: "Edit profile" });
  await expect(editor.getByLabel("Explore").first()).toHaveValue("v1-explore");
});

test("two profiles pin different prompt versions independently", async ({
  page,
}) => {
  await mockSettingsAPI(page);
  await page.goto("/settings/analysis");
  await expect(page.getByRole("heading", { name: "Profiles" })).toBeVisible();

  const alpha = profileRow(page, "Alpha");
  await alpha.getByRole("button", { name: "Edit" }).click();
  const editor = alpha.getByRole("group", { name: "Edit profile" });
  await editor.getByLabel("Explore").first().selectOption("v2-explore");
  await editor.getByRole("button", { name: "Save changes" }).click();
  await expect(alpha.getByRole("group", { name: "Edit profile" })).toHaveCount(
    0,
  );

  expect(putBodies).toHaveLength(1);
  const saved = putBodies[0]!["prompt_versions"] as Record<string, string>;
  expect(saved["explore"]).toBe("v2-explore");
  expect(saved["linguistic_analysis"]).toBe("v1-linguistic_analysis");

  const beta = profileRow(page, "Beta");
  await beta.getByRole("button", { name: "Edit" }).click();
  await expect(
    beta
      .getByRole("group", { name: "Edit profile" })
      .getByLabel("Explore")
      .first(),
  ).toHaveValue("v1-explore");
  expect(putBodies).toHaveLength(1);
});

test("Explore binding stays independent of the Translation binding", async ({
  page,
}) => {
  await mockSettingsAPI(page);
  await page.goto("/settings/analysis");
  await expect(page.getByRole("heading", { name: "Profiles" })).toBeVisible();

  const alpha = profileRow(page, "Alpha");
  await alpha.getByRole("button", { name: "Edit" }).click();
  const editor = alpha.getByRole("group", { name: "Edit profile" });
  const explore = editor.getByRole("group", { name: "Explore (on-demand)" });
  const translation = editor.getByRole("group", { name: "Translation" });
  await expect(translation.getByLabel("Model")).toHaveValue("model-a");
  await explore.getByLabel("Model").selectOption("model-b");
  await editor.getByRole("button", { name: "Save changes" }).click();
  await expect(alpha.getByRole("group", { name: "Edit profile" })).toHaveCount(
    0,
  );

  expect(putBodies).toHaveLength(1);
  const saved = putBodies[0] as {
    bindings: Array<{ stage_id: string; model_id: string }>;
    explore_binding: { model_id: string };
  };
  expect(saved.explore_binding.model_id).toBe("model-b");
  expect(
    saved.bindings.find((binding) => binding.stage_id === "translation")
      ?.model_id,
  ).toBe("model-a");
});
