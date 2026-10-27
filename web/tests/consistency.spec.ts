import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// The same thing is done the same way wherever it is done. Content is added
// in four places: making a server, duplicating one, making one from a
// snapshot, and a server's Content tab. Each offers every kind the server's
// software takes and the resource pack, and each kind from every source:
// one that was missing somewhere was found by using the panel, and is now
// found here instead.

const id = "abcd1234";

/** A snapshot of survival, Paper 1.21.4, as the panel lists it. */
async function snapshot(page: Page) {
  await page.route(`**/api/v1/orgs/friends/snapshots/${id}`, (route) =>
    route.fulfill({
      json: {
        id,
        time: new Date(Date.now() - 3600_000).toISOString(),
        server: "survival",
        trigger: "manual",
        size: 1 << 20,
        added: 1024,
        files: 10,
        spec: { displayName: "Survival", version: "1.21.4", loader: "paper", eula: true, resources: { memory: "4Gi" }, storage: { size: "10Gi" }, mods: [] },
        retention: { scheduled: true, kept: ["daily"] },
      },
    }),
  );
}

const places: { name: string; open: (page: Page, slug: string) => Promise<void> }[] = [
  {
    name: "a new server",
    open: async (page, slug) => {
      await page.goto(`/o/${slug}/new`);
      await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
      await page.getByLabel("Game version").selectOption("1.21.4");
    },
  },
  {
    name: "a duplicate",
    open: async (page) => {
      await page.goto("/o/friends/new?from=server:survival");
    },
  },
  {
    name: "a server from a snapshot",
    open: async (page) => {
      await snapshot(page);
      await page.goto(`/o/friends/new?from=snapshot:${id}`);
    },
  },
];

for (const place of places) {
  test(`${place.name} takes plugins, datapacks and a resource pack, each from Modrinth, a URL or this computer`, async ({ page, as, org }) => {
    await as("owner");
    const slug = await org([]);
    await place.open(page, slug);
    // A section for each, as on a server's Content page.
    for (const kind of ["plugins", "datapacks", "resource pack"]) {
      await page.getByRole("button", { name: `Add ${kind}` }).click();
      const dialog = page.getByRole("dialog", { name: `Add ${kind}` });
      for (const source of ["Modrinth", "From URL", "Upload"]) {
        await expect(dialog.getByRole("button", { name: source, exact: true }), `${kind} from ${source}`).toBeVisible();
      }
      await page.keyboard.press("Escape");
      await expect(dialog).toHaveCount(0);
    }
    // Nothing a Paper server cannot run.
    await expect(page.getByRole("region", { name: "Mods" })).toHaveCount(0);
    await expect(page.getByRole("button", { name: "Add mods" })).toHaveCount(0);
  });
}

test("a server's Content tab has a section for plugins, datapacks and the resource pack, each added from Modrinth, a URL or this computer", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/s/survival/content");
  for (const [region, kind] of [
    ["Plugins", "Plugins"],
    ["Datapacks", "Datapacks"],
    ["Resource packs", "Resource pack"],
  ]) {
    const section = page.getByRole("region", { name: region, exact: true });
    await section.getByRole("button", { name: `Add ${kind.toLowerCase()}` }).click();
    // The picker is for that kind alone.
    const dialog = page.getByRole("dialog");
    await expect(dialog.getByRole("heading", { name: `Add ${kind.toLowerCase()}` })).toBeVisible();
    await expect(dialog.getByRole("button", { name: kind, exact: true })).toHaveCount(0);
    for (const source of ["Modrinth", "From URL", "Upload"]) {
      await expect(dialog.getByRole("button", { name: source, exact: true }), `${kind} from ${source}`).toBeVisible();
    }
    await page.keyboard.press("Escape");
    await expect(dialog).toHaveCount(0);
  }
  // Nothing a Paper server cannot run.
  await expect(page.getByRole("region", { name: "Mods", exact: true })).toHaveCount(0);
});
