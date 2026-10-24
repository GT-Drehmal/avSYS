import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// A server's Content tab has a section for each kind of content it takes.

test.beforeEach(async ({ as }) => {
  await as("owner");
});

const lithium = { source: "modrinth", project: "gvQqBUqZ", title: "Lithium", kind: "mod", version: "v1", versionName: "0.20.1" };
const fire = { source: "upload", project: "hell-is-fire", title: "Hell_Is_Fire_v0.1.0.zip", kind: "datapack" };
const sodium = { source: "curseforge", project: "394468", title: "Sodium", kind: "mod" };
const essentials = { source: "upload", project: "essentials", title: "EssentialsX.jar", kind: "plugin" };

/** Shows survival as running this software with this content, and keeps what is saved. */
async function survival(page: Page, software: object, mods: object[]) {
  const state = { mods, sent: [] as any[] };
  const as = (s: { name: string; spec: object }) => (s.name === "survival" ? { ...s, spec: { ...s.spec, ...software, mods: state.mods } } : s);
  await page.route("**/api/v1/orgs/friends/servers", async (route) => route.fulfill({ json: (await (await route.fetch()).json()).map(as) }));
  await page.route("**/api/v1/orgs/friends/servers/survival", async (route) => {
    const server = as(await (await page.request.get("/api/v1/orgs/friends/servers/survival")).json());
    if (route.request().method() === "PATCH") {
      const { spec } = route.request().postDataJSON();
      state.sent.push(spec);
      if (spec.mods) state.mods = spec.mods;
      return route.fulfill({ json: as(server) });
    }
    return route.fulfill({ json: server });
  });
  return state;
}

const sections = (page: Page) => page.getByRole("region").evaluateAll((all) => all.map((s) => s.getAttribute("aria-label")));

test("which sections a server has follows what it runs", async ({ page }) => {
  for (const [software, want] of [
    [{ loader: "vanilla" }, ["Datapacks", "Resource packs"]],
    [{ loader: "fabric" }, ["Mods", "Datapacks", "Resource packs"]],
    [{ loader: "paper" }, ["Plugins", "Datapacks", "Resource packs"]],
  ] as const) {
    await page.unrouteAll({ behavior: "wait" });
    await survival(page, software, []);
    await page.goto("/o/friends/s/survival/content");
    await expect.poll(() => sections(page), software.loader).toEqual(want);
  }
});

test("each kind is in its own section, without a chip saying what it is", async ({ page }) => {
  await survival(page, { loader: "fabric" }, [lithium, fire, sodium]);
  await page.goto("/o/friends/s/survival/content");
  const mods = page.getByRole("region", { name: "Mods", exact: true });
  const datapacks = page.getByRole("region", { name: "Datapacks", exact: true });
  await expect(mods.getByText("Lithium", { exact: true })).toBeVisible();
  await expect(datapacks.getByText("Lithium", { exact: true })).toHaveCount(0);
  // An upload shows its name without its extension, and one from Modrinth no ID.
  await expect(datapacks.getByText("Hell_Is_Fire_v0.1.0", { exact: true })).toBeVisible();
  await expect(page.getByText("modrinth:gvQqBUqZ")).toHaveCount(0);
  // One from elsewhere has no link, so it says where it is from.
  await expect(page.getByText("curseforge:394468")).toBeVisible();
  await expect(page.getByText("mod", { exact: true })).toHaveCount(0);
  await expect(page.getByText("datapack", { exact: true })).toHaveCount(0);
  await expect(page.getByRole("region", { name: "Resource packs" }).getByText("None.", { exact: false })).toBeVisible();
});

test("removing or changing one in a section changes that one in the server's list", async ({ page }) => {
  const state = await survival(page, { loader: "fabric" }, [fire, lithium]);
  await page.goto("/o/friends/s/survival/content");
  await page.getByRole("region", { name: "Mods", exact: true }).getByRole("button", { name: "0.20.1" }).click();
  await page.getByRole("dialog").getByRole("button", { name: /^Update to the best fit/ }).click();
  await expect.poll(() => state.sent.length).toBe(1);
  expect(state.sent[0].mods).toEqual([fire, { ...lithium, version: "", versionName: "" }]);
  await page.getByRole("button", { name: "Remove Hell_Is_Fire_v0.1.0" }).click();
  await expect.poll(() => state.sent.length).toBe(2);
  expect(state.sent[1].mods).toEqual([{ ...lithium, version: "", versionName: "" }]);
  await expect(page.getByRole("region", { name: "Datapacks", exact: true }).getByText("No datapacks.")).toBeVisible();
});

test("what a server has from before its software was changed still shows, but nothing more of it is added", async ({ page }) => {
  await survival(page, { loader: "fabric" }, [essentials]);
  await page.goto("/o/friends/s/survival/content");
  const plugins = page.getByRole("region", { name: "Plugins", exact: true });
  await expect(plugins.getByText("EssentialsX", { exact: true })).toBeVisible();
  await expect(plugins.getByRole("button", { name: "Remove EssentialsX" })).toBeVisible();
  await expect(plugins.getByRole("button", { name: "Add plugins" })).toHaveCount(0);
});

test("a server made from a modpack says what is added on top of it", async ({ page }) => {
  await survival(page, { loader: "fabric", modpack: { source: "modrinth", project: "fabulously-optimized", title: "Fabulously Optimized" } }, []);
  await page.goto("/o/friends/s/survival/content");
  await expect(page.getByText("Fabulously Optimized", { exact: true })).toBeVisible();
  await expect(page.getByRole("region", { name: "Mods", exact: true }).getByText("None on top of the modpack.")).toBeVisible();
});

test("a viewer sees the sections, and no way to add to them", async ({ page, as }) => {
  await as("viewer");
  await survival(page, { loader: "fabric" }, [lithium]);
  await page.goto("/o/friends/s/survival/content");
  await expect(page.getByRole("region", { name: "Mods", exact: true }).getByText("Lithium", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Add / })).toHaveCount(0);
});
