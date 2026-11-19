import type { Page } from "@playwright/test";
import { type Api, expect, type Role, test } from "./panel";

// A server moved in from elsewhere has its mods, plugins and datapacks as
// files in its folders, which the panel did not add. They are taken into
// its Content list as if they had been added there: what Modrinth knows by
// its hash as that project at that version, the rest as uploads, named as
// the files say. The test panel gives servers named bingo such files (see
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
const row = (page: Page, title: string) => mods(page).getByRole("listitem").filter({ has: page.getByText(title, { exact: true }) });

test("content in a server's folders is taken into its list, as Modrinth or the files say", async ({ page, as, org, api }) => {
  const slug = await bingo(org, api);
  await as("owner");
  await page.goto(`/o/${slug}/s/bingo/content`);

  // Modrinth has these by their hash: its projects, at those very versions,
  // with the version button any from Modrinth has.
  for (const [title, version] of [
    ["Yet Another Bingo", "2.9.6+mc1.20.1"],
    ["Fabric API", "0.92.2+1.20.1"],
    ["Lithium", "mc1.20.1-0.11.2"],
  ]) {
    await expect(row(page, title)).toHaveCount(1);
    await expect(row(page, title).getByRole("button", { name: version })).toBeVisible();
  }
  await expect(row(page, "Yet Another Bingo").getByRole("link", { name: "Open on Modrinth" })).toHaveAttribute("href", "https://modrinth.com/project/P0000101");
  // These are uploads, named as they say; WI Zoom with its own picture.
  await expect(row(page, "WI Zoom").getByText("1.5-MC1.20.1", { exact: true })).toBeVisible();
  await expect(row(page, "WI Zoom").locator("img")).toHaveAttribute("src", /\/content\/icon\?kind=mod&name=WI-Zoom-1\.5-MC1\.20\.1\.jar/);
  await expect.poll(() => row(page, "WI Zoom").locator("img").evaluate((i) => (i as HTMLImageElement).naturalWidth)).toBe(64);
  await expect(row(page, "Animatica").getByText("0.6+1.20", { exact: true })).toBeVisible();
  // And this one says nothing: its file's name.
  await expect(row(page, "c2me-fabric-mc1.20.1-0.2.0")).toHaveCount(1);
  await expect(mods(page).getByRole("heading")).toHaveText("Mods6");
  // Datapacks say what they are, and show their pack.png where they have one.
  const bern = datapacks(page).getByRole("listitem").filter({ has: page.getByText("bern", { exact: true }) });
  await expect(bern.getByText("Bern's trades", { exact: true })).toBeVisible();
  await expect.poll(() => bern.locator("img").evaluate((i) => (i as HTMLImageElement).naturalWidth)).toBe(64);
  const hi = datapacks(page).getByRole("listitem").filter({ has: page.getByText("hi_drehmal", { exact: true }) });
  await expect(hi.getByText("Hi, Drehmal", { exact: true })).toBeVisible();
  await expect(hi.locator("img")).toHaveCount(0);
  // Nothing sets them apart from what was added here.
  await expect(page.getByText("Added outside the panel")).toHaveCount(0);
  const server = await api("owner").get(`/orgs/${slug}/servers/bingo`);
  expect(server.found).toEqual([]);
  expect(server.spec.mods.map((m: { source: string; project: string; version?: string }) => `${m.source} ${m.project}${m.version ? `@${m.version}` : ""}`).sort()).toEqual([
    "modrinth P0000101@V0000101",
    "modrinth P0000102@V0000102",
    "modrinth P0000103@V0000103",
    "upload WI-Zoom-1.5-MC1.20.1.jar",
    "upload animatica-0.6+1.20.jar",
    "upload bern.zip",
    "upload c2me-fabric-mc1.20.1-0.2.0.jar",
    "upload hi_drehmal.zip",
  ]);

  // A list this long is searched, every section at once.
  const search = page.getByRole("searchbox", { name: "Search content" });
  await search.fill("zoom");
  await expect(mods(page).getByRole("listitem")).toHaveCount(1);
  await expect(datapacks(page).getByText("No datapacks match.")).toBeVisible();
  await search.fill("");
  await expect(mods(page).getByRole("listitem")).toHaveCount(6);
});

test("content taken from a server's folders is updated and removed as any other", async ({ page, as, org, api }) => {
  const slug = await bingo(org, api);
  await as("owner");
  await page.goto(`/o/${slug}/s/bingo/content`);
  await expect(mods(page).getByRole("listitem")).toHaveCount(6);

  // Bingo to another version, from Modrinth.
  await row(page, "Yet Another Bingo").getByRole("button", { name: "2.9.6+mc1.20.1" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: /^2\.0\.0/ }).click();
  await expect(dialog).toHaveCount(0);
  await expect.poll(async () => (await api("owner").get(`/orgs/${slug}/servers/bingo`)).spec.mods.find((m: { project: string }) => m.project === "P0000101")?.version).not.toBe("V0000101");

  // An upload taken off, and a datapack.
  await page.getByRole("button", { name: "Remove c2me-fabric-mc1.20.1-0.2.0" }).click();
  await expect(row(page, "c2me-fabric-mc1.20.1-0.2.0")).toHaveCount(0);
  await page.getByRole("button", { name: "Remove bern" }).click();
  await expect(datapacks(page).getByRole("listitem")).toHaveCount(1);

  // Their files are gone with them: looked at again, nothing comes back,
  // nor the other version of bingo.
  await page.reload();
  await expect(row(page, "Lithium")).toHaveCount(1);
  await expect(mods(page).getByRole("listitem")).toHaveCount(5);
  await expect(row(page, "c2me-fabric-mc1.20.1-0.2.0")).toHaveCount(0);
  await expect(datapacks(page).getByText("bern", { exact: true })).toHaveCount(0);
  const server = await api("owner").get(`/orgs/${slug}/servers/bingo`);
  expect(server.found).toEqual([]);
  expect(server.spec.mods).toHaveLength(6);
});

test("a datapack taken off leaves the others where they are kept", async ({ page, as, org, api }) => {
  const slug = await bingo(org, api);
  await as("owner");
  await page.goto(`/o/${slug}/s/bingo/content`);
  await expect(datapacks(page).getByRole("listitem")).toHaveCount(2);
  await page.getByRole("button", { name: "Remove bern" }).click();
  await expect(datapacks(page).getByRole("listitem")).toHaveCount(1);
  // The rest are still kept in the server's own files, as found, and say
  // what they are: none became an upload the image would look for
  // elsewhere.
  await expect
    .poll(async () => {
      const { spec } = await api("owner").get(`/orgs/${slug}/servers/bingo`);
      return spec.mods
        .filter((m: { source: string }) => m.source === "upload")
        .map((m: { project: string; file?: string; description?: string }) => `${m.project} ${m.file ?? "-"}${m.description ? ` ${m.description}` : ""}`)
        .sort();
    })
    .toEqual([
      "WI-Zoom-1.5-MC1.20.1.jar mods/WI-Zoom-1.5-MC1.20.1.jar",
      "animatica-0.6+1.20.jar mods/animatica-0.6+1.20.jar",
      "c2me-fabric-mc1.20.1-0.2.0.jar mods/c2me-fabric-mc1.20.1-0.2.0.jar",
      "hi_drehmal.zip world/datapacks/hi_drehmal.zip Hi, Drehmal",
    ]);
  await page.reload();
  await expect(datapacks(page).getByText("Hi, Drehmal", { exact: true })).toBeVisible();
});

test("anyone who sees a server sees what was taken from its folders; only admins change it", async ({ org, api, pageAs }) => {
  const slug = await bingo(org, api);
  for (const who of ["viewer", "operator"] as const) {
    const invite = await api("owner").post(`/orgs/${slug}/invites`, { role: who });
    await api(who).post(`/invites/${invite.token}/accept`);
    const page = await pageAs(who);
    await page.goto(`/o/${slug}/s/bingo/content`);
    await expect(row(page, "Yet Another Bingo")).toHaveCount(1);
    await expect(mods(page).getByRole("listitem")).toHaveCount(6);
    await expect(page.getByRole("button", { name: /^Remove / })).toHaveCount(0);
  }
});

test("a search stays to be cleared while it is on, however few are left", async ({ page, as, org, api }) => {
  const slug = await bingo(org, api);
  await as("owner");
  await page.goto(`/o/${slug}/s/bingo/content`);
  await expect(mods(page).getByRole("listitem")).toHaveCount(6);
  const search = page.getByRole("searchbox", { name: "Search content" });
  // Eight: a search box. Two taken off while searched for leaves six.
  await search.fill("c2me");
  await page.getByRole("button", { name: "Remove c2me-fabric-mc1.20.1-0.2.0" }).click();
  await expect(mods(page).getByText("No mods match.")).toBeVisible();
  await search.fill("bern");
  await page.getByRole("button", { name: "Remove bern" }).click();
  await expect(datapacks(page).getByText("No datapacks match.")).toBeVisible();
  await expect(search).toBeVisible();
  await search.fill("");
  await expect(mods(page).getByRole("listitem")).toHaveCount(5);
  await expect(datapacks(page).getByRole("listitem")).toHaveCount(1);
  // Six, and nothing searched for: no box.
  await expect(search).toHaveCount(0);
});

test("a file found that is on the list already shows once, as it is on the list", async ({ page, as, org, api }) => {
  const slug = await bingo(org, api);
  await as("owner");
  await page.goto(`/o/${slug}/s/bingo/content`);
  await expect(mods(page).getByRole("listitem")).toHaveCount(6);
  // As in the moment a file is taken into the list: listed, and still noted
  // as found.
  type Listed = { source: string; kind: string; project: string; version?: string; file?: string; title?: string };
  const twice = (server: { name: string; spec: { mods: Listed[] }; found: unknown[] }) => {
    if (server.name !== "bingo") return server;
    server.found = server.spec.mods.map((m) => ({
      path: m.file ?? `mods/${m.project}.jar`,
      kind: m.kind,
      size: 1,
      title: m.title,
      ...(m.source === "modrinth" ? { project: m.project, versionId: m.version } : {}),
    }));
    return server;
  };
  await page.route(new RegExp(`/api/v1/orgs/${slug}/servers(/bingo)?$`), async (route) => {
    const got = await (await route.fetch()).json();
    await route.fulfill({ json: Array.isArray(got) ? got.map(twice) : twice(got) });
  });
  await page.reload();
  await expect(mods(page).getByRole("heading")).toHaveText("Mods6");
  await expect(mods(page).getByRole("listitem")).toHaveCount(6);
  await expect(datapacks(page).getByRole("listitem")).toHaveCount(2);
});
