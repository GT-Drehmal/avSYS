import { expect, test } from "./panel";

// "Latest release" is the newest version at the moment a server is made or
// its version is set. The server is saved with that version, and keeps it
// until someone changes it. A template keeps "Latest" itself.

test("a new server on the latest release is saved with that version", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  const type = page.getByRole("group", { name: "Server type" });
  const version = page.getByLabel("Game version");
  const bar = page.locator("[data-bottom-bar]");
  await page.getByLabel("Display name").fill("Fresh");
  // Each kind's own newest, said where it is picked and in what is made.
  await type.getByRole("button", { name: /^Folia/ }).click();
  await expect(version).toHaveValue("LATEST");
  await expect(version.locator("option:checked")).toHaveText("Latest release (26.2)");
  await expect(bar).toContainText("Fresh · Folia 26.2 · 4 GB");
  await type.getByRole("button", { name: /^Paper/ }).click();
  await expect(version.locator("option:checked")).toHaveText("Latest release (26.3)");
  await expect(bar).toContainText("Fresh · Paper 26.3 · 4 GB");

  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  const sent = page.waitForRequest((r) => r.method() === "POST" && r.url().endsWith(`/orgs/${slug}/servers`));
  await page.getByRole("button", { name: "Create server" }).click();
  expect((await sent).postDataJSON().spec.version).toBe("26.3");
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/fresh$`));
  expect((await api("owner").get(`/orgs/${slug}/servers/fresh`)).spec.version).toBe("26.3");
  await expect(page.getByText(/^Paper 26\.3/)).toBeVisible();
});

test("a template keeps Latest, and a server made from it gets the newest version then", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  // Made in the form, it keeps the choice as it is.
  await page.goto(`/o/${slug}/templates/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await expect(page.getByLabel("Game version")).toHaveValue("LATEST");
  await page.getByLabel("Name", { exact: true }).fill("Base");
  await expect(page.locator("[data-bottom-bar]")).toContainText("Base · Paper, latest");
  const made = page.waitForRequest((r) => r.method() === "POST" && r.url().endsWith(`/orgs/${slug}/templates`));
  await page.getByRole("button", { name: "Create template" }).click();
  expect((await made).postDataJSON().spec.version).toBe("LATEST");
  const row = page.getByRole("listitem", { name: "Base" });
  await expect(row).toContainText("Paper, latest · settings only");

  await row.getByRole("button", { name: "New server" }).click();
  await expect(page.getByRole("heading", { name: "New server from Base" })).toBeVisible();
  await expect(page.getByLabel("Game version")).toHaveValue("LATEST");
  await expect(page.locator("[data-bottom-bar]")).toContainText("Paper 26.3");
  await page.getByLabel("Short name").fill("from-base");
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/from-base$`));
  expect((await api("owner").get(`/orgs/${slug}/servers/from-base`)).spec.version).toBe("26.3");
  const templates = await api("owner").get(`/orgs/${slug}/templates`);
  expect(templates[0].spec.version).toBe("LATEST");
});

test("in a server's settings, the latest release saves the newest version, and only a change of version is sent", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await api("owner").post(`/orgs/${slug}/servers`, { name: "old", spec: { eula: true, loader: "paper", version: "1.21.4", state: "Stopped", resources: { memory: "2Gi" }, storage: { size: "2Gi" } } });
  await as("owner");
  await page.goto(`/o/${slug}/s/old/settings`);
  const version = page.getByLabel("Game version");
  await expect(version).toHaveValue("1.21.4");

  // Another setting alone does not name the version.
  let patched = page.waitForRequest((r) => r.method() === "PATCH" && r.url().endsWith(`/orgs/${slug}/servers/old`));
  await page.getByLabel("Message of the day").fill("Hello");
  await page.getByRole("button", { name: "Save changes" }).click();
  expect((await patched).postDataJSON().spec).not.toHaveProperty("version");
  await expect(page.getByText("Settings saved")).toBeVisible();

  patched = page.waitForRequest((r) => r.method() === "PATCH" && r.url().endsWith(`/orgs/${slug}/servers/old`));
  await version.selectOption("LATEST");
  await page.getByRole("button", { name: "Save changes" }).click();
  expect((await patched).postDataJSON().spec.version).toBe("26.3");
  await expect(version).toHaveValue("26.3");
  expect((await api("owner").get(`/orgs/${slug}/servers/old`)).spec.version).toBe("26.3");
});

test("a server still on Latest from before is shown with the version it runs", async ({ page, as }) => {
  await page.route("**/api/v1/orgs/friends/servers", async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const list = await (await route.fetch()).json();
    return route.fulfill({ json: list.map((s: { name: string; spec: object }) => (s.name === "survival" ? { ...s, spec: { ...s.spec, version: "LATEST" } } : s)) });
  });
  await as("owner");
  await page.goto("/o/friends");
  const row = page.getByRole("listitem").filter({ hasText: "Survival" });
  await expect(row).toContainText("Paper 1.21.4");
  await expect(row).not.toContainText("latest");
});

test("a snapshot from before servers were kept on a version says latest plainly", async ({ page, as }) => {
  await page.route("**/api/v1/orgs/friends/snapshots**", async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const answer = await (await route.fetch()).json();
    const latest = (s: { version?: string }) => ({ ...s, version: "LATEST" });
    if (Array.isArray(answer.snapshots)) return route.fulfill({ json: { ...answer, snapshots: answer.snapshots.map(latest) } });
    if (answer.spec) return route.fulfill({ json: { ...answer, spec: { ...answer.spec, version: "LATEST" } } });
    return route.fulfill({ json: answer });
  });
  await as("owner");
  await page.goto("/o/friends/backups");
  await page.getByRole("button", { expanded: false }).filter({ hasText: "Survival" }).click();
  await expect(page.getByText(/^Paper, latest · /)).toHaveCount(4);
  await page.getByRole("button", { name: /^About the snapshot of survival/ }).first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Paper, latest");
  await expect(dialog).not.toContainText("LATEST");
});
