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
  // One from CurseForge links there too, through the panel, which knows its page.
  await expect(page.getByText("curseforge:394468")).toHaveCount(0);
  await expect(mods.getByRole("link", { name: "Open on CurseForge" })).toHaveAttribute("href", "/api/v1/catalog/curseforge/projects/394468/page");
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

const fabulous = { source: "modrinth", project: "fabulously-optimized", version: "fo1", versionName: "6.4.0", title: "Fabulously Optimized", gameVersion: "1.21.4", loader: "fabric", iconUrl: "/fo-icon.png" };
const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAEElEQVR4nGM4oREFRAwQCgAl3gUpEzqvXwAAAAABJRU5ErkJggg==", "base64");

/** Answers for what survival's modpack lists, counting what is asked of its pack. */
async function packContents(page: Page, files: object[]) {
  const asked: string[] = [];
  await page.route("**/api/v1/orgs/friends/servers/survival/modpack/**", async (route) => {
    asked.push(new URL(route.request().url()).pathname.split("/modpack")[1]);
    return route.fulfill({ json: { files } });
  });
  await page.route("**/fo-icon.png", (route) => route.fulfill({ body: png, contentType: "image/png" }));
  return asked;
}

test("a server made from a modpack says what is added on top of it, in a line where there is none", async ({ page }) => {
  await survival(page, { loader: "fabric", modpack: fabulous }, []);
  await packContents(page, []);
  await page.goto("/o/friends/s/survival/content");
  await expect(page.getByText("Fabulously Optimized", { exact: true })).toBeVisible();
  // Each empty section is one line with its Add, under no heading.
  for (const [region, says, add] of [
    ["Mods", "No mods added on top of the pack.", "Add mods"],
    ["Datapacks", "No datapacks.", "Add datapacks"],
    ["Resource packs", "No resource packs.", "Add resource pack"],
  ]) {
    const section = page.getByRole("region", { name: region, exact: true });
    await expect(section).toHaveText(`${says}Add`);
    await expect(section.getByRole("heading")).toHaveCount(0);
    await expect(section.getByRole("button", { name: add })).toBeVisible();
  }
  // Its Add still adds.
  await page.getByRole("button", { name: "Add mods" }).click();
  await expect(page.getByRole("dialog")).toBeVisible();
});

test("mods on a modpack are said to be added on top of it", async ({ page }) => {
  await survival(page, { loader: "fabric", modpack: fabulous }, [lithium, fire]);
  await packContents(page, []);
  await page.goto("/o/friends/s/survival/content");
  const mods = page.getByRole("region", { name: "Mods", exact: true });
  await expect(mods.getByRole("heading")).toHaveText("Mods added on top of the pack1");
  await expect(mods.getByText("Lithium", { exact: true })).toBeVisible();
  await expect(mods.getByRole("button", { name: "Add mods" })).toBeVisible();
  await expect(page.getByRole("region", { name: "Datapacks", exact: true }).getByRole("heading")).toHaveText("Datapacks1");
});

test("a pack from Modrinth lists what is in it, by its version, with its picture from Modrinth", async ({ page }) => {
  await survival(page, { loader: "fabric", modpack: fabulous }, []);
  const asked = await packContents(page, [
    { path: "mods/lithium.jar", server: true, project: "gvQqBUqZ", title: "Lithium" },
    { path: "mods/sodium.jar", server: false, project: "AANobbMI", title: "Sodium" },
    { path: "resourcepacks/faithful.zip", server: true },
  ]);
  await page.goto("/o/friends/s/survival/content");
  await expect(page.locator('img[src="/fo-icon.png"]')).toBeVisible();
  // Not only mods.
  await page.getByRole("button", { name: "In this pack (3)" }).click();
  const files = page.getByRole("region", { name: "In the modpack" });
  await expect(files.getByRole("listitem")).toHaveText([/Lithium\s*Mod/, /faithful\s*Resource pack/, /Sodium\s*Client only, left out/]);
  // Each from Modrinth links to its page there; one from elsewhere does not.
  const link = (n: number) => files.getByRole("listitem").nth(n).getByRole("link", { name: "Open on Modrinth" });
  await expect(link(0)).toHaveAttribute("href", "https://modrinth.com/project/gvQqBUqZ");
  await expect(link(0)).toHaveAttribute("target", "_blank");
  await expect(link(1)).toHaveCount(0);
  await expect(link(2)).toHaveAttribute("href", "https://modrinth.com/project/AANobbMI");
  // Few enough not to need a search.
  await expect(files.getByRole("searchbox")).toHaveCount(0);
  expect(asked).toEqual(["/contents"]);
});

test("a server without a modpack has its Content page as before", async ({ page }) => {
  await survival(page, { loader: "fabric" }, []);
  const asked = await packContents(page, [{ path: "mods/lithium.jar", server: true }]);
  await page.goto("/o/friends/s/survival/content");
  for (const [region, heading, says] of [
    ["Mods", "Mods", "No mods."],
    ["Datapacks", "Datapacks", "No datapacks."],
    ["Resource packs", "Resource packs", "None. A pack added here is offered to each player as they join."],
  ]) {
    const section = page.getByRole("region", { name: region, exact: true });
    await expect(section.getByRole("heading")).toHaveText(heading);
    await expect(section.getByText(says)).toBeVisible();
  }
  await expect(page.getByText(/added on top of the pack/)).toHaveCount(0);
  await expect(page.getByRole("button", { name: /in this pack/i })).toHaveCount(0);
  expect(asked).toEqual([]);
});

test("a viewer sees the sections, and no way to add to them", async ({ page, as }) => {
  await as("viewer");
  await survival(page, { loader: "fabric" }, [lithium]);
  await page.goto("/o/friends/s/survival/content");
  await expect(page.getByRole("region", { name: "Mods", exact: true }).getByText("Lithium", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: /^Add / })).toHaveCount(0);
});
