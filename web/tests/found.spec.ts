import type { Page } from "@playwright/test";
import { type Api, expect, type Role, test } from "./panel";

// A server moved in from elsewhere has its mods, plugins and datapacks as
// files in its folders, which the panel did not add. Its Content page lists
// them with the rest: as Modrinth knows them by their hash, or as the files
// say, or by name. The test panel gives servers named bingo such files (see
// cmd/avsys-uitest/loose.go).

/** A Fabric server named bingo in an organization of its own. */
async function bingo(org: (servers: string[]) => Promise<string>, api: (who: Role) => Api) {
  const slug = await org([]);
  await api("owner").post(`/orgs/${slug}/servers`, {
    name: "bingo",
    spec: { eula: true, displayName: "Drehmal Bingo", loader: "fabric", version: "1.21.4", state: "Stopped", resources: { memory: "2Gi" }, storage: { size: "2Gi" } },
  });
  return slug;
}

const mods = (page: Page) => page.getByRole("region", { name: "Mods", exact: true });
const datapacks = (page: Page) => page.getByRole("region", { name: "Datapacks", exact: true });

test("content in a server's folders is listed with the rest, named as Modrinth or the files say", async ({ page, as, org, api }) => {
  const slug = await bingo(org, api);
  await as("owner");
  await page.goto(`/o/${slug}/s/bingo/content`);

  // Modrinth has these by their hash: its names, at that very version.
  for (const [title, version] of [
    ["Yet Another Bingo", "2.9.6+mc1.20.1"],
    ["Fabric API", "0.92.2+1.20.1"],
    ["Lithium", "mc1.20.1-0.11.2"],
  ]) {
    const row = mods(page).getByRole("listitem").filter({ hasText: title });
    await expect(row).toHaveCount(1);
    await expect(row.getByText(version, { exact: true })).toBeVisible();
    await expect(row.getByText("Added outside the panel")).toBeVisible();
  }
  await expect(mods(page).getByRole("listitem").filter({ hasText: "Yet Another Bingo" }).getByRole("link", { name: "Open on Modrinth" })).toHaveAttribute(
    "href",
    "https://modrinth.com/mod/yet-another-bingo",
  );
  // These say what they are themselves; WI Zoom has a picture of its own.
  const zoom = mods(page).getByRole("listitem").filter({ hasText: "WI Zoom" });
  await expect(zoom.getByText("1.5-MC1.20.1", { exact: true })).toBeVisible();
  await expect(zoom.locator("img")).toHaveAttribute("src", /\/content\/found\/icon\?path=mods%2FWI-Zoom-1\.5-MC1\.20\.1\.jar/);
  await expect.poll(() => zoom.locator("img").evaluate((i) => (i as HTMLImageElement).naturalWidth)).toBe(64);
  await expect(mods(page).getByText("Animatica", { exact: true })).toBeVisible();
  // And this one says nothing: its file's name.
  await expect(mods(page).getByText("c2me-fabric-mc1.20.1-0.2.0", { exact: true })).toBeVisible();
  await expect(mods(page).getByRole("heading")).toHaveText("Mods6");
  // Datapacks of its world, with what they say of themselves.
  await expect(datapacks(page).getByText("Added outside the panel · Bern's trades")).toBeVisible();
  await expect(datapacks(page).getByText("hi_drehmal", { exact: true })).toBeVisible();

  // A list this long is searched, every section at once.
  const search = page.getByRole("searchbox", { name: "Search content" });
  await search.fill("zoom");
  await expect(mods(page).getByRole("listitem")).toHaveCount(1);
  await expect(mods(page).getByText("WI Zoom", { exact: true })).toBeVisible();
  await expect(datapacks(page).getByText("No datapacks match.")).toBeVisible();
  await search.fill("trades");
  await expect(mods(page).getByText("No mods match.")).toBeVisible();
  await expect(datapacks(page).getByRole("listitem")).toHaveCount(1);
  await search.fill("");
  await expect(mods(page).getByRole("listitem")).toHaveCount(6);
});

test("content found in a server's folders is removed as any other, its file with it", async ({ page, as, org, api }) => {
  const slug = await bingo(org, api);
  await as("owner");
  await page.goto(`/o/${slug}/s/bingo/content`);
  await expect(mods(page).getByRole("listitem")).toHaveCount(6);
  await page.getByRole("button", { name: "Remove c2me-fabric-mc1.20.1-0.2.0" }).click();
  await expect(mods(page).getByText("c2me-fabric-mc1.20.1-0.2.0")).toHaveCount(0);
  await expect(mods(page).getByRole("listitem")).toHaveCount(5);
  await page.getByRole("button", { name: "Remove bern" }).click();
  await expect(datapacks(page).getByRole("listitem")).toHaveCount(1);
  // Gone from its folders: looked at again, it is not there.
  await page.reload();
  await expect(mods(page).getByText("Lithium", { exact: true })).toBeVisible();
  await expect(mods(page).getByText("c2me-fabric-mc1.20.1-0.2.0")).toHaveCount(0);
  await expect(datapacks(page).getByText("bern", { exact: true })).toHaveCount(0);
  const server = await api("owner").get(`/orgs/${slug}/servers/bingo`);
  expect(server.found.map((f: { path: string }) => f.path)).not.toContain("mods/c2me-fabric-mc1.20.1-0.2.0.jar");
  expect(server.found).toHaveLength(6);
});

test("anyone who sees a server sees what is in its folders; only admins remove it", async ({ org, api, pageAs }) => {
  const slug = await bingo(org, api);
  for (const who of ["viewer", "operator"] as const) {
    const invite = await api("owner").post(`/orgs/${slug}/invites`, { role: who });
    await api(who).post(`/invites/${invite.token}/accept`);
    const page = await pageAs(who);
    await page.goto(`/o/${slug}/s/bingo/content`);
    await expect(mods(page).getByText("Yet Another Bingo", { exact: true })).toBeVisible();
    await expect(mods(page).getByRole("listitem")).toHaveCount(6);
    await expect(page.getByRole("button", { name: /^Remove / })).toHaveCount(0);
  }
});
