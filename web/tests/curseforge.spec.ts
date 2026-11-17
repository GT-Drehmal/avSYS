import type { Page } from "@playwright/test";
import { type Api, expect, test, withCurseForge } from "./panel";

// CurseForge beside Modrinth, wherever content is picked. Its API needs a
// key: without one its tab says how it is turned on, and has nothing to
// search. The panel installed with one answers for CurseForge from
// tests/upstream, in the shape its API has.

test.describe("without a key", () => {
  const forAdmin = "Add a CurseForge API key in Instance settings to search CurseForge.";
  const forOthers = "The panel's administrator can turn on CurseForge.";

  test("the CurseForge tab tells the administrator where to add a key, and has nothing to search", async ({ page, as }) => {
    await as("owner");
    await page.goto("/o/friends/s/creative/content");
    await page.getByRole("button", { name: /^Add/ }).first().click();
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByPlaceholder(/^Search .* on Modrinth$/)).toBeVisible();
    await dialog.getByRole("button", { name: "CurseForge", exact: true }).click();
    const off = dialog.getByTestId("curseforge-off");
    await expect(off).toHaveText(forAdmin);
    await expect(off.getByRole("link", { name: "Instance settings" })).toHaveAttribute("href", "/settings");
    await expect(dialog.getByRole("textbox")).toHaveCount(0);
    await expect(dialog.getByRole("combobox")).toHaveCount(0);
    // The other tabs are as they were.
    await dialog.getByRole("button", { name: "Modrinth", exact: true }).click();
    await expect(dialog.getByPlaceholder(/^Search .* on Modrinth$/)).toBeVisible();
    await page.keyboard.press("Escape");

    // The resource pack's picker too.
    await page.getByRole("button", { name: "Add resource pack" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "CurseForge", exact: true }).click();
    await expect(page.getByRole("dialog").getByTestId("curseforge-off")).toHaveText(forAdmin);
    await page.keyboard.press("Escape");

    // And the modpack picker.
    await page.goto("/o/friends/new");
    await page.getByRole("button", { name: "Modpack", exact: true }).click();
    await expect(page.getByPlaceholder("Search modpacks on Modrinth")).toBeVisible();
    const on = page.getByRole("group", { name: "Search on" });
    await on.getByRole("button", { name: "CurseForge" }).click();
    await expect(on.getByRole("button", { name: "CurseForge" })).toHaveAttribute("aria-pressed", "true");
    await expect(page.getByTestId("curseforge-off")).toHaveText(forAdmin);
    await expect(page.getByPlaceholder("Search modpacks on CurseForge")).toHaveCount(0);
    await on.getByRole("button", { name: "Modrinth" }).click();
    await expect(page.getByPlaceholder("Search modpacks on Modrinth")).toBeVisible();
  });

  test("anyone else is told the administrator can turn it on, with no link", async ({ page, as }) => {
    await as("admin");
    await page.goto("/o/friends/s/creative/content");
    await page.getByRole("button", { name: /^Add/ }).first().click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "CurseForge", exact: true }).click();
    await expect(dialog.getByTestId("curseforge-off")).toHaveText(forOthers);
    await expect(dialog.getByTestId("curseforge-off").getByRole("link")).toHaveCount(0);
    await expect(dialog.getByRole("textbox")).toHaveCount(0);
    await page.keyboard.press("Escape");

    await page.goto("/o/friends/new");
    await page.getByRole("button", { name: "Modpack", exact: true }).click();
    await page.getByRole("group", { name: "Search on" }).getByRole("button", { name: "CurseForge" }).click();
    await expect(page.getByTestId("curseforge-off")).toHaveText(forOthers);
  });

  test("a key added in another tab is found on coming back, and the tab searches", async ({ page, as }) => {
    await as("owner");
    let key = false;
    await page.route("**/api/v1/meta", async (route) => {
      const meta = await (await route.fetch()).json();
      await route.fulfill({ json: { ...meta, curseforge: key } });
    });
    // What it finds is answered for: this panel has no key to search with.
    await page.route(/\/api\/v1\/catalog\/curseforge\//, (route) =>
      route.fulfill({ json: route.request().url().includes("/search") ? { hits: [], offset: 0, limit: 20, total_hits: 0 } : [] }),
    );
    await page.goto("/o/friends/new");
    await page.getByRole("button", { name: "Modpack", exact: true }).click();
    await page.getByRole("group", { name: "Search on" }).getByRole("button", { name: "CurseForge" }).click();
    await expect(page.getByTestId("curseforge-off")).toBeVisible();
    key = true;
    await page.evaluate(() => window.dispatchEvent(new Event("focus")));
    await expect(page.getByPlaceholder("Search modpacks on CurseForge")).toBeVisible();
    await expect(page.getByTestId("curseforge-off")).toHaveCount(0);
  });

  test("the administrator enters a key in Instance settings, which is never shown again, and can replace or remove it", async ({ page, as }) => {
    await as("owner");
    // The panel's own answers, so that this panel stays without a key for the other tests.
    let from = "";
    const sent: unknown[] = [];
    await page.route("**/api/v1/panel", async (route) => {
      if (route.request().method() === "GET") {
        const json = await (await route.fetch()).json();
        return route.fulfill({ json: { ...json, panel: { ...json.panel, curseforge: from } } });
      }
      const body = route.request().postDataJSON();
      sent.push(body);
      from = body.curseforgeKey ? "panel" : "";
      await route.fulfill({ json: { version: "v0.1.0", canUpdate: true, autoUpdate: false, curseforge: from } });
    });
    await page.goto("/settings");
    const section = page.getByRole("region", { name: "CurseForge" });
    await expect(section).toContainText("Add an API key to find and install mods and modpacks from CurseForge.");
    await expect(section.getByRole("link", { name: "console.curseforge.com" })).toHaveAttribute("href", "https://console.curseforge.com");
    await expect(section.getByRole("button", { name: "Remove" })).toHaveCount(0);
    await section.getByLabel("API key").fill(" $2a$10$secret ");
    await section.getByRole("button", { name: "Save" }).click();
    await expect(section.getByText("key set")).toBeVisible();
    await expect(section.getByLabel("API key")).toHaveCount(0);
    await expect(page.getByText("$2a$10$secret")).toHaveCount(0);
    expect(sent).toEqual([{ curseforgeKey: "$2a$10$secret" }]);

    await section.getByRole("button", { name: "Replace" }).click();
    await expect(section.getByLabel("API key")).toHaveValue("");
    await section.getByRole("button", { name: "Cancel" }).click();
    await section.getByRole("button", { name: "Remove" }).click();
    await page.getByRole("dialog", { name: "Remove the CurseForge key?" }).getByRole("button", { name: "Remove" }).click();
    await expect(section.getByLabel("API key")).toBeVisible();
    expect(sent).toEqual([{ curseforgeKey: "$2a$10$secret" }, { curseforgeKey: "" }]);
  });

  test("a key the panel was installed with can be replaced, not removed", async ({ page, as }) => {
    await as("owner");
    await page.route("**/api/v1/panel", async (route) => {
      const json = await (await route.fetch()).json();
      await route.fulfill({ json: { ...json, panel: { ...json.panel, curseforge: "install" } } });
    });
    await page.goto("/settings");
    const section = page.getByRole("region", { name: "CurseForge" });
    await expect(section).toContainText("with the key avSYS was installed with.");
    await expect(section.getByText("key set")).toBeVisible();
    await expect(section.getByRole("button", { name: "Replace" })).toBeVisible();
    await expect(section.getByRole("button", { name: "Remove" })).toHaveCount(0);
    await expect(section.getByLabel("API key")).toHaveCount(0);
  });
});

test.describe("with a key", () => {
  test.use({ baseURL: withCurseForge });

  test.beforeEach(async ({ as }) => {
    await as("owner");
  });

  /** A new organization with a Fabric 1.21.4 server in it, called modded. */
  async function modded(org: (servers: string[]) => Promise<string>, api: (who: "owner") => Api) {
    const slug = await org([]);
    await api("owner").post(`/orgs/${slug}/servers`, {
      name: "modded",
      spec: { eula: true, loader: "fabric", version: "1.21.4", state: "Stopped", resources: { memory: "2Gi" }, storage: { size: "2Gi" } },
    });
    return slug;
  }

  async function openPicker(page: Page, slug: string) {
    await page.goto(`/o/${slug}/s/modded/content`);
    await page.getByRole("button", { name: /^Add/ }).first().click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "CurseForge", exact: true }).click();
    return dialog;
  }

  const modsOf = async (api: Api, slug: string) => (await api.get(`/orgs/${slug}/servers/modded`)).spec.mods ?? [];

  test("CurseForge is searched as Modrinth is: what fits the server, its categories, and a closer look", async ({ page, org, api }) => {
    const slug = await modded(org, api);
    const searched: URLSearchParams[] = [];
    page.on("request", (r) => {
      if (r.url().includes("/catalog/curseforge/search")) searched.push(new URL(r.url()).searchParams);
    });
    const dialog = await openPicker(page, slug);
    await expect(dialog.getByPlaceholder("Search mods on CurseForge")).toBeVisible();
    await expect(dialog.getByText("3 results for Fabric 1.21.4")).toBeVisible();
    expect(searched.at(-1)?.get("loader")).toBe("fabric");
    expect(searched.at(-1)?.get("gameVersion")).toBe("1.21.4");
    // CurseForge does not say where a mod runs, so there is nothing to filter by.
    await dialog.getByRole("button", { name: /Filters/ }).click();
    await expect(dialog.getByRole("checkbox", { name: "Server", exact: true })).toHaveCount(0);
    await dialog.getByRole("button", { name: "World gen", exact: true }).click();
    await expect(dialog.getByText("1 result for Fabric 1.21.4")).toBeVisible();
    await dialog.getByRole("button", { name: "Clear filters" }).click();
    await expect(dialog.getByText("3 results for Fabric 1.21.4")).toBeVisible();

    await dialog.getByRole("button", { name: "About Just Enough Items (JEI)" }).click();
    await expect(dialog.getByRole("heading", { name: "Versions for Fabric 1.21.4" })).toBeVisible();
    await expect(dialog.getByText("jei-1.21.4-fabric-19.21.0.247.jar")).toBeVisible();
    await expect(dialog.getByText("Architectury API")).toBeVisible();
    await expect(dialog.getByRole("link", { name: /Open on CurseForge/ })).toHaveAttribute("href", "https://www.curseforge.com/minecraft/mc-mods/jei");
    await expect(dialog.getByRole("button", { name: "Add", exact: true })).toBeEnabled();
  });

  test("one picked is pinned to its newest file for the server, links to its page, and can be put on another version", async ({ page, org, api }) => {
    const slug = await modded(org, api);
    const dialog = await openPicker(page, slug);
    await dialog.getByRole("button", { name: /^Just Enough Items/ }).click();
    await expect
      .poll(() => modsOf(api("owner"), slug))
      .toEqual([expect.objectContaining({ source: "curseforge", kind: "mod", project: "238222", version: "6000001", versionName: "jei-1.21.4-fabric-19.21.0.247.jar" })]);
    // Shown as picked.
    await expect(dialog.getByRole("button", { name: /^Just Enough Items/ })).toBeDisabled();
    await page.keyboard.press("Escape");

    const mods = page.getByRole("region", { name: "Mods", exact: true });
    await expect(mods.getByText("Just Enough Items (JEI)", { exact: true })).toBeVisible();
    await expect(mods.getByRole("link", { name: "Open on CurseForge" })).toHaveAttribute("href", "/api/v1/catalog/curseforge/projects/238222/page");
    await mods.getByRole("button", { name: "jei-1.21.4-fabric-19.21.0.247.jar" }).click();
    await page.getByRole("dialog").getByRole("button", { name: /jei-1.21.4-fabric-19.20.0.240.jar/ }).click();
    await expect.poll(async () => (await modsOf(api("owner"), slug))[0]?.version).toBe("5900000");
    await expect(mods.getByRole("button", { name: "jei-1.21.4-fabric-19.20.0.240.jar" })).toBeVisible();
  });

  test("one whose author allows downloads only on CurseForge says to download it there and upload it", async ({ page, org, api }) => {
    const slug = await modded(org, api);
    const dialog = await openPicker(page, slug);
    const row = dialog.getByRole("button", { name: /^Hand Only/ });
    await expect(row.getByText("upload by hand")).toBeVisible();
    // Picked, it is looked at, not added.
    await row.click();
    await expect(dialog.getByText("Its author only allows downloads on CurseForge. Download it there, then add it with Upload.")).toBeVisible();
    await expect(dialog.getByRole("link", { name: "Download on CurseForge" })).toHaveAttribute("href", "https://www.curseforge.com/minecraft/mc-mods/hand-only");
    await expect(dialog.getByRole("button", { name: "Add", exact: true })).toHaveCount(0);
    expect(await modsOf(api("owner"), slug)).toEqual([]);
    // Upload is where it goes.
    await dialog.getByRole("button", { name: "Upload", exact: true }).click();
    await expect(dialog.getByText("Choose .jar files, or drop them here")).toBeVisible();
  });

  test("a resource pack from CurseForge is the newest file for the game version, linked to its page", async ({ page, org, api }) => {
    const slug = await modded(org, api);
    await page.goto(`/o/${slug}/s/modded/content`);
    await page.getByRole("button", { name: "Add resource pack" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: "CurseForge", exact: true }).click();
    await dialog.getByRole("button", { name: /^Stay True/ }).click();
    await expect(dialog).toHaveCount(0);
    await expect
      .poll(async () => (await api("owner").get(`/orgs/${slug}/servers/modded`)).spec.resourcePack)
      .toMatchObject({ url: "https://files.invalid/stay-true-1.21.4.zip", sha1: "ab".repeat(20), project: { source: "curseforge", id: "300001", version: "4800001", title: "Stay True" } });
    const packs = page.getByRole("region", { name: "Resource packs" });
    await expect(packs.getByText("Stay True", { exact: true })).toBeVisible();
    await expect(packs.getByRole("link", { name: "Open on CurseForge" })).toHaveAttribute("href", "/api/v1/catalog/curseforge/projects/300001/page");
  });

  test("a modpack from CurseForge makes a server from the same form, and its Content page shows the pack and what it lists", async ({ page, org, api }) => {
    const slug = await org([]);
    await page.goto(`/o/${slug}/new`);
    await page.getByRole("button", { name: "Modpack", exact: true }).click();
    await page.getByRole("group", { name: "Search on" }).getByRole("button", { name: "CurseForge" }).click();
    await expect(page.getByPlaceholder("Search modpacks on CurseForge")).toBeVisible();
    await page.getByRole("button", { name: /^All the Mods 9/ }).click();
    await expect(page.getByText("curseforge:all-the-mods-9")).toBeVisible();
    // The rest of the form is as for any modpack, the world among it.
    await expect(page.getByRole("group", { name: "World", exact: true }).getByRole("button", { name: "Library world" })).toBeVisible();
    await expect(page.getByLabel("Display name")).toHaveValue("All the Mods 9");
    await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
    await page.getByRole("button", { name: "Create server" }).click();
    await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/all-the-mods-9$`));
    const { spec } = await api("owner").get(`/orgs/${slug}/servers/all-the-mods-9`);
    expect(spec.modpack).toMatchObject({ source: "curseforge", project: "all-the-mods-9", version: "5000003", versionName: "All the Mods 9-1.0.0.zip", gameVersion: "1.21.5", loader: "neoforge" });

    // Through the tab, not by address: the form may still be moving on to
    // the new server, and a second navigation would cut this one short.
    await page.getByRole("link", { name: "Content", exact: true }).click();
    await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/all-the-mods-9/content$`));
    await expect(page.getByRole("button", { name: "All the Mods 9-1.0.0.zip" })).toBeVisible();
    // One of its files has to be put in by hand, which shows the list at once.
    await expect(page.getByText("One file in this pack can only be downloaded on CurseForge.")).toBeVisible();
    const files = page.getByRole("region", { name: "In the modpack" });
    await expect(files.getByText("Just Enough Items (JEI)")).toBeVisible();
    await expect(files.getByRole("link", { name: "Open on CurseForge" }).first()).toHaveAttribute("href", "/api/v1/catalog/curseforge/projects/238222/page");
    await expect(files.getByRole("link", { name: "download by hand" })).toHaveAttribute("href", "https://www.curseforge.com/minecraft/mc-mods/hand-only");

    // Another version, built for an older game than the world was opened
    // in, asks first, as for a pack from Modrinth.
    await page.getByRole("button", { name: "All the Mods 9-1.0.0.zip" }).click();
    await page.getByRole("dialog").getByRole("button", { name: /All the Mods 9-0.2.44.zip/ }).click();
    const ask = page.getByRole("dialog", { name: "Open a 1.21.5 world in 1.21.4?" });
    await expect(ask).toBeVisible();
    await ask.getByRole("button", { name: "Change to All the Mods 9-0.2.44.zip anyway" }).click();
    await expect
      .poll(async () => (await api("owner").get(`/orgs/${slug}/servers/all-the-mods-9`)).spec.modpack)
      .toMatchObject({ version: "5000001", gameVersion: "1.21.4", loader: "forge" });
  });
});
