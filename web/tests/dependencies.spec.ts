import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// What Modrinth content needs is listed with it on the Content page: added
// for it, unless the server has it already, as an upload of a fork, say.
// The test panel's Modrinth has Kin's Wathe, which needs Wathe (see
// tests/upstream).

test.beforeEach(async ({ as }) => {
  await as("owner");
});

const mods = (page: Page) => page.getByRole("region", { name: "Mods", exact: true });
const row = (page: Page, title: string) => mods(page).getByRole("listitem").filter({ has: page.getByText(title, { exact: true }) });

const fabric = { eula: true, loader: "fabric", version: "1.21.4", state: "Stopped", resources: { memory: "2Gi" }, storage: { size: "2Gi" } };

test("a mod added from Modrinth brings what it needs, which goes with it", async ({ page, org, api }) => {
  const slug = await org([]);
  await api("owner").post(`/orgs/${slug}/servers`, { name: "harpy", spec: fabric });
  // Modrinth's search finds Kin's Wathe first, whatever is searched for.
  await page.route("**/api/v1/catalog/modrinth/search?*", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.hits[0] = { ...body.hits[0], project_id: "PKINS001", slug: "kinswathe", title: "Kin's Wathe", author: "kin", project_type: "mod", categories: ["fabric"] };
    await route.fulfill({ response, json: body });
  });
  await page.goto(`/o/${slug}/s/harpy/content`);
  await page.getByRole("button", { name: "Add mods" }).click();
  await page.getByRole("dialog").getByRole("button", { name: /^Kin's Wathe by/ }).click();
  await page.keyboard.press("Escape");

  await expect(row(page, "Kin's Wathe")).toHaveCount(1);
  await expect(row(page, "Wathe")).toHaveCount(1);
  await expect(row(page, "Wathe").getByText("Needed by Kin's Wathe")).toBeVisible();
  await expect(row(page, "Wathe").getByRole("button", { name: "1.4.1-1.21.4" })).toBeVisible();
  // It goes with what needs it, not on its own.
  await expect(row(page, "Wathe").getByRole("button", { name: "Remove Wathe" })).toHaveCount(0);
  const server = await api("owner").get(`/orgs/${slug}/servers/harpy`);
  expect(server.spec.dependenciesListed).toBe(true);
  expect(server.spec.mods.map((m: { project: string; version?: string; dependency?: boolean; neededBy?: string[] }) => [m.project, m.version, !!m.dependency, m.neededBy ?? []])).toEqual([
    ["kinswathe", "vKINS001", false, []],
    ["PWATHE01", "vWATHE01", true, ["kinswathe"]],
  ]);

  await row(page, "Kin's Wathe").getByRole("button", { name: "Remove Kin's Wathe" }).click();
  await expect(mods(page).getByRole("listitem")).toHaveCount(0);
  expect((await api("owner").get(`/orgs/${slug}/servers/harpy`)).spec.mods ?? []).toEqual([]);
});

test("what the server has already is what is needed, and a version that will not do is said", async ({ page, org, api }) => {
  const slug = await org([]);
  // Harpy Express: a fork of Wathe, uploaded, and a mod that needs Wathe 1.5 or newer.
  await api("owner").post(`/orgs/${slug}/servers`, {
    name: "harpy",
    spec: {
      ...fabric,
      mods: [
        { source: "upload", kind: "mod", project: "wathe-fork.jar", title: "wathe-fork.jar", versionName: "1.4.1+wathez.1", provides: ["wathe"] },
        { source: "modrinth", kind: "mod", project: "kinswathe", version: "vKINS002", title: "Kin's Wathe" },
      ],
    },
  });
  await page.goto(`/o/${slug}/s/harpy/content`);
  await expect(row(page, "wathe-fork").getByText("Needed by Kin's Wathe")).toBeVisible();
  await expect(row(page, "Kin's Wathe").getByRole("note")).toHaveText("Needs wathe >=1.5.0. The server has 1.4.1+wathez.1 in wathe-fork.");
  // No second Wathe beside it.
  await expect(mods(page).getByRole("listitem")).toHaveCount(2);
  await expect(mods(page).getByRole("heading")).toHaveText("Mods2");
});
