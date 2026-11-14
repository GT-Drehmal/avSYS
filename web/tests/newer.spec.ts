import type { Page } from "@playwright/test";
import { type Api, expect, test } from "./panel";

// A server keeps its game version until someone changes it. Its settings
// say when its type has a newer release, and picking that is up to them.
// The fixtures' game is at 26.3 and Folia's newest is 26.2; Fabric has the
// 26.4 snapshot and Paper the 26.3 release candidate.

test.beforeEach(async ({ as }) => {
  await as("owner");
});

/** A stopped server in this org, of this type and version. */
const make = (api: Api, slug: string, name: string, loader: string, version: string) =>
  api.post(`/orgs/${slug}/servers`, { name, spec: { eula: true, loader, version, state: "Stopped", resources: { memory: "2Gi" }, storage: { size: "2Gi" } } });

/** Opens a server's settings once the versions its type has are listed, with the newest release given. */
async function settings(page: Page, slug: string, name: string, newest: string) {
  await page.goto(`/o/${slug}/s/${name}/settings`);
  await expect(page.getByLabel("Game version").locator('option[value="LATEST"]')).toHaveText(`Latest release (${newest})`);
}

const note = (page: Page) => page.getByText(/ is out\./);

test("a server on an older release than its type has is told, and picking the newer one fills the version", async ({ page, org, api }) => {
  const slug = await org([]);
  await make(api("owner"), slug, "old", "paper", "1.21.4");
  let patches = 0;
  page.on("request", (r) => {
    if (r.method() === "PATCH") patches++;
  });
  await settings(page, slug, "old", "26.3");
  await expect(note(page)).toHaveText("Paper 26.3 is out. Use 26.3");

  await page.getByRole("button", { name: "Use 26.3" }).click();
  const version = page.getByLabel("Game version");
  await expect(version).toHaveValue("26.3");
  await expect(note(page)).toHaveCount(0);
  // Only picked: nothing is saved until it is.
  await expect(page.locator("[data-bottom-bar]")).toContainText("Unsaved changes");
  expect(patches).toBe(0);
  expect((await api("owner").get(`/orgs/${slug}/servers/old`)).spec.version).toBe("1.21.4");
  // Picked back, it is told again.
  await version.selectOption("1.21.4");
  await expect(note(page)).toHaveText("Paper 26.3 is out. Use 26.3");

  await page.getByRole("button", { name: "Use 26.3" }).click();
  const patched = page.waitForRequest((r) => r.method() === "PATCH" && r.url().endsWith(`/orgs/${slug}/servers/old`));
  await page.getByRole("button", { name: "Save changes" }).click();
  expect((await patched).postDataJSON().spec.version).toBe("26.3");
  await expect(page.getByText("Settings saved")).toBeVisible();
  expect((await api("owner").get(`/orgs/${slug}/servers/old`)).spec.version).toBe("26.3");
  // On the newest now.
  await expect(version).toHaveValue("26.3");
  await expect(note(page)).toHaveCount(0);
});

test("the newer version is the newest its type has a build for, not the game's newest", async ({ page, org, api }) => {
  const slug = await org([]);
  await make(api("owner"), slug, "behind", "folia", "1.21.4");
  await make(api("owner"), slug, "current", "folia", "26.2");
  // Folia has no 26.3 yet.
  await settings(page, slug, "behind", "26.2");
  await expect(note(page)).toHaveText("Folia 26.2 is out. Use 26.2");
  await settings(page, slug, "current", "26.2");
  await expect(page.getByLabel("Game version")).toHaveValue("26.2");
  await expect(note(page)).toHaveCount(0);
});

test("a server on a release is not told of snapshots, and one on a pre-release is told of the release", async ({ page, org, api }) => {
  const slug = await org([]);
  await make(api("owner"), slug, "fabric", "fabric", "26.3");
  await make(api("owner"), slug, "candidate", "paper", "26.3-rc-1");
  // Fabric has the 26.4 snapshot, newer than 26.3.
  await settings(page, slug, "fabric", "26.3");
  await expect(page.getByLabel("Game version")).toHaveValue("26.3");
  await expect(note(page)).toHaveCount(0);

  await settings(page, slug, "candidate", "26.3");
  await expect(note(page)).toHaveText("Paper 26.3 is out. Use 26.3");
  await page.getByRole("button", { name: "Use 26.3" }).click();
  await expect(page.getByLabel("Game version")).toHaveValue("26.3");
});

test("a modpack server is not told: the pack has the version", async ({ page, org, api }) => {
  const slug = await org([]);
  await make(api("owner"), slug, "pack", "fabric", "1.21.4");
  const withPack = (s: { name: string; spec: object }) =>
    s.name === "pack" ? { ...s, spec: { ...s.spec, modpack: { source: "modrinth", project: "fabulously-optimized", title: "Fabulously Optimized", gameVersion: "1.21.4" } } } : s;
  await page.route(`**/api/v1/orgs/${slug}/servers`, async (route) => route.fulfill({ json: (await (await route.fetch()).json()).map(withPack) }));
  await page.route(`**/api/v1/orgs/${slug}/servers/pack`, async (route) => route.fulfill({ json: withPack(await (await route.fetch()).json()) }));
  const listed = page.waitForResponse((r) => r.url().includes("/catalog/minecraft/versions?loader=fabric"));
  await page.goto(`/o/${slug}/s/pack/settings`);
  await expect(page.getByText("Running the Fabulously Optimized modpack.")).toBeVisible();
  await listed;
  await expect(note(page)).toHaveCount(0);
});

test("when the versions a type has cannot be found out, nothing is said", async ({ page, org, api }) => {
  const slug = await org([]);
  await make(api("owner"), slug, "folia", "folia", "26.2");
  await make(api("owner"), slug, "paper", "paper", "1.21.4");
  // Folia's own list down: the panel lists every version there is, 26.3 too,
  // which Folia has no build for.
  const every = await api("owner").get("/catalog/minecraft/versions");
  await page.route("**/api/v1/catalog/minecraft/versions?loader=folia", (route) => route.fulfill({ json: every }));
  await settings(page, slug, "folia", "26.3");
  await expect(note(page)).toHaveCount(0);

  // No list at all.
  let asked = 0;
  await page.route("**/api/v1/catalog/minecraft/versions?loader=paper", (route) => {
    asked++;
    return route.fulfill({ status: 502, json: { code: "upstream", message: "Mojang's version list could not be read." } });
  });
  await page.goto(`/o/${slug}/s/paper/settings`);
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.4");
  // Asked again before it is given up on.
  await expect.poll(() => asked, { timeout: 10_000 }).toBeGreaterThanOrEqual(3);
  await expect(note(page)).toHaveCount(0);
  await expect(page.getByText(/could not be read/)).toHaveCount(0);
});

test("saving the newer version says what a change of version says", async ({ page, org, api }) => {
  const slug = await org([]);
  await make(api("owner"), slug, "packed", "paper", "1.21.4");
  // A resource pack made for 1.21.4's format.
  const pack = { url: "https://files.invalid/faithful-1.21.4-a.zip", sha1: "2".repeat(40), formats: { min: 46, max: 46 } };
  const withPack = (s: { name: string; spec: object }) => (s.name === "packed" ? { ...s, spec: { ...s.spec, resourcePack: pack } } : s);
  await page.route(`**/api/v1/orgs/${slug}/servers`, async (route) => route.fulfill({ json: (await (await route.fetch()).json()).map(withPack) }));
  await page.route(`**/api/v1/orgs/${slug}/servers/packed`, async (route) => route.fulfill({ json: withPack(await (await route.fetch()).json()) }));
  const formats: Record<string, number> = { "1.21.4": 46, "26.3": 97 };
  await page.route("**/api/v1/catalog/minecraft/pack-formats?*", (route) => {
    const v = new URL(route.request().url()).searchParams.get("versions") ?? "";
    return v in formats ? route.fulfill({ json: { min: formats[v], max: formats[v] } }) : route.fulfill({ status: 404, json: { code: "not_found", message: "Not found." } });
  });

  await settings(page, slug, "packed", "26.3");
  const fit = page.getByText("This pack is for an older game version (pack format 46; 26.3 uses 97). Some parts may not work.");
  await expect(fit).toHaveCount(0);
  await page.getByRole("button", { name: "Use 26.3" }).click();
  await expect(fit).toBeVisible();
  const patched = page.waitForRequest((r) => r.method() === "PATCH" && r.url().endsWith(`/orgs/${slug}/servers/packed`));
  await page.getByRole("button", { name: "Save changes" }).click();
  expect((await patched).postDataJSON().spec.version).toBe("26.3");
  await expect(page.getByText("Settings saved")).toBeVisible();
});
