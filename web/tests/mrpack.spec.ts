import type { Page } from "@playwright/test";
import { createHash } from "node:crypto";
import { zip } from "./archives";
import { type Api, expect, test } from "./panel";

// A modpack can be a Modrinth pack file, a .mrpack, from this computer: read
// in the page, it fills the form in as a pack picked from Modrinth would, and
// the file is uploaded once the server is made, before it first starts.

const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAEElEQVR4nGM4oREFRAwQCgAl3gUpEzqvXwAAAAABJRU5ErkJggg==", "base64");

const index = (change: (i: any) => void = () => {}) => {
  const i = {
    formatVersion: 1,
    game: "minecraft",
    versionId: "2.0.1",
    name: "Probe Pack",
    files: [
      { path: "mods/lithium-fabric-0.15.3.jar", env: { client: "required", server: "required" }, downloads: ["https://cdn.modrinth.com/data/a/lithium.jar"] },
      { path: "mods/sodium-fabric-0.6.13.jar", env: { client: "required", server: "unsupported" }, downloads: ["https://cdn.modrinth.com/data/b/sodium.jar"] },
      { path: "resourcepacks/faithful.zip", downloads: ["https://cdn.modrinth.com/data/c/faithful.zip"] },
    ],
    dependencies: { minecraft: "1.21.4", "fabric-loader": "0.16.10" },
  };
  change(i);
  return JSON.stringify(i);
};

const probe = zip({ "modrinth.index.json": index(), "overrides/icon.png": png, "overrides/config/lithium.properties": "x=1" });
const sha1 = (b: Buffer) => createHash("sha1").update(b).digest("hex");

const packFile = (buffer: Buffer, name = "probe.mrpack") => ({ name, mimeType: "application/octet-stream", buffer });

/** The new-server form, on Modpack. */
async function newModpack(page: Page, slug: string) {
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Modpack", exact: true }).click();
}

test("a .mrpack fills the form in, and the server is made with it, then given the file before it starts", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  const asked: string[] = [];
  let made: any = null;
  let sent = 0;
  await page.route(`**/api/v1/orgs/${slug}/servers`, async (route) => {
    if (route.request().method() === "POST") made = route.request().postDataJSON();
    return route.fallback();
  });
  await page.route(`**/api/v1/orgs/${slug}/servers/probe-pack/**`, async (route) => {
    const req = route.request();
    if (req.method() === "GET") return route.fallback();
    const url = new URL(req.url());
    const q = url.searchParams;
    asked.push(`${req.method()} ${url.pathname.split("/servers/probe-pack")[1]} ${q.get("name") ?? ""} ${q.get("wait") ?? ""} ${q.get("start") ?? ""}`.trim());
    if (url.pathname.endsWith("/modpack")) {
      // Sent in parts to the organization first, or else as the body.
      sent = q.get("upload") ? Number(q.get("size")) : (req.postDataBuffer()?.length ?? 0);
      const server = await (await page.request.get(`/api/v1/orgs/${slug}/servers/probe-pack`)).json();
      return route.fulfill({ json: { ...server, spec: { ...server.spec, modpack: { ...server.spec.modpack, version: sha1(probe) } } } });
    }
    if (url.pathname.endsWith("/icon")) return route.fulfill({ status: 204 });
    return route.fallback();
  });

  await newModpack(page, slug);
  await page.getByLabel("Modpack file").setInputFiles(packFile(probe));
  // As a pack picked from Modrinth: its name, version and what it is built for.
  await expect(page.getByText("Probe Pack", { exact: true })).toBeVisible();
  await expect(page.getByText(`2.0.1 · Fabric 1.21.4 · probe.mrpack, ${probe.length} B`)).toBeVisible();
  await expect(page.getByLabel("Display name")).toHaveValue("Probe Pack");
  await expect(page.getByLabel("Short name")).toHaveValue("probe-pack");
  // What it brings, the one for players only left out.
  const files = page.getByRole("region", { name: "In the modpack" });
  await expect(files.getByText("2 on the server · 1 left out, client only")).toBeVisible();
  await expect(files.getByRole("listitem")).toHaveText([/lithium-fabric-0\.15\.3\s*Mod/, /faithful\s*Resource pack/, /sodium-fabric-0\.6\.13\s*Client only, left out/]);
  // Along the bottom, what is made.
  await expect(page.getByText("Probe Pack · Probe Pack · 4 GB")).toBeVisible();

  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/probe-pack$`));
  await expect(page.getByText("Probe Pack is set up")).toBeVisible();
  // Made stopped with the pack as it was read, its file not there yet.
  expect(made.spec).toMatchObject({ state: "Stopped", modpack: { source: "upload", project: "probe.mrpack", title: "Probe Pack", versionName: "2.0.1", gameVersion: "1.21.4", loader: "fabric" } });
  expect(made.spec.modpack).not.toHaveProperty("version");
  expect(made.spec).not.toHaveProperty("loader");
  // Then the file, all of it, then the picture, which starts it.
  expect(asked).toEqual(["PUT /modpack probe.mrpack true", "PUT /icon  true true"]);
  expect(sent).toBe(probe.length);
});

test("a .mrpack can be dropped on the form", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await newModpack(page, slug);
  const drop = await page.evaluateHandle((bytes) => {
    const d = new DataTransfer();
    d.items.add(new File([new Uint8Array(bytes)], "dropped.mrpack"));
    return d;
  }, [...probe]);
  await page.getByText("Upload a .mrpack, or drop one here").dispatchEvent("drop", { dataTransfer: drop });
  await expect(page.getByText("2.0.1 · Fabric 1.21.4 · dropped.mrpack", { exact: false })).toBeVisible();
  // Taken off, the search is back, and so is no picture.
  await page.getByRole("button", { name: "Choose a different modpack" }).click();
  await expect(page.getByPlaceholder("Search modpacks on Modrinth")).toBeVisible();
  await expect(page.getByRole("region", { name: "In the modpack" })).toHaveCount(0);
});

test("a file that is not a modpack the server can install is turned down, saying why", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await newModpack(page, slug);
  for (const [file, says] of [
    [Buffer.from("not a zip at all"), "This is not a Modrinth modpack (.mrpack)."],
    [zip({ "overrides/config/a.txt": "x" }), "This is not a Modrinth modpack (.mrpack)."],
    [zip({ "modrinth.index.json": "{" }), "The modpack's modrinth.index.json cannot be read."],
    [zip({ "modrinth.index.json": index((i) => delete i.dependencies["fabric-loader"]) }), "The modpack needs Fabric, Quilt, Forge or NeoForge."],
    [zip({ "modrinth.index.json": index((i) => delete i.dependencies.minecraft) }), "The modpack does not say which Minecraft version it is for."],
  ] as const) {
    await page.getByLabel("Modpack file").setInputFiles(packFile(file));
    await expect(page.getByRole("alert")).toHaveText(says);
    await expect(page.getByPlaceholder("Search modpacks on Modrinth")).toBeVisible();
  }
  // None of them is the modpack.
  await page.getByLabel("Display name").fill("Packed");
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page.getByRole("alert")).toHaveText("Pick a modpack.");
});

test("a template of settings alone has no .mrpack to offer", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/templates/new`);
  await page.getByRole("button", { name: "Modpack", exact: true }).click();
  await expect(page.getByPlaceholder("Search modpacks on Modrinth")).toBeVisible();
  await expect(page.getByText("Upload a .mrpack, or drop one here")).toHaveCount(0);
});

const uploaded = { source: "upload", project: "probe.mrpack", title: "Probe Pack", versionName: "2.0.1", gameVersion: "1.21.4", loader: "fabric" };

/** A server made from a .mrpack, its file uploaded unless not. */
async function packServer(page: Page, api: Api, slug: string, version = sha1(probe)) {
  await api.post(`/orgs/${slug}/servers`, { name: "pack", spec: { eula: true, state: "Stopped", modpack: uploaded, resources: { memory: "2Gi" }, storage: { size: "2Gi" } } });
  const state = { modpack: { ...uploaded, ...(version ? { version } : {}) } as Record<string, string> };
  const as = (s: { name: string; spec: object }) => (s.name === "pack" ? { ...s, spec: { ...s.spec, modpack: state.modpack } } : s);
  await page.route(`**/api/v1/orgs/${slug}/servers`, async (route) =>
    route.request().method() === "GET" ? route.fulfill({ json: (await (await route.fetch()).json()).map(as) }) : route.fallback(),
  );
  await page.route(`**/api/v1/orgs/${slug}/servers/pack`, async (route) => route.fulfill({ json: as(await (await route.fetch()).json()) }));
  return { state, as };
}

test("everywhere the server's software shows, an uploaded modpack reads as one", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await packServer(page, api("owner"), slug);
  await page.goto(`/o/${slug}`);
  await expect(page.getByRole("main").getByText("Probe Pack", { exact: true })).toBeVisible();
  await page.goto(`/o/${slug}/s/pack/settings`);
  await expect(page.getByText("Running the Probe Pack modpack.")).toBeVisible();
  await page.goto(`/o/${slug}/s/pack/content`);
  await expect(page.getByText("2.0.1 · Fabric 1.21.4 · probe.mrpack")).toBeVisible();
  await expect(page.getByText("Each start puts the pack's own files back, configs too.")).toBeVisible();

  // A duplicate keeps it, as it keeps a pack from Modrinth: it is not asked for again.
  let copy: any = null;
  await page.route(`**/api/v1/orgs/${slug}/servers`, async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    copy = route.request().postDataJSON();
    return route.fulfill({ status: 503, json: { code: "unavailable", message: "The cluster did not answer." } });
  });
  await page.goto(`/o/${slug}/new?from=server:pack`);
  await expect(page.getByText("Probe Pack modpack")).toBeVisible();
  await page.getByRole("button", { name: "Duplicate" }).click();
  await expect(page.getByRole("alert")).toHaveText("The cluster did not answer.");
  expect(copy.fromServer).toBe("pack");
  expect(copy.spec).not.toHaveProperty("modpack");
});

test("a newer .mrpack is uploaded on the Content page, and one not uploaded yet is asked for", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  const { state } = await packServer(page, api("owner"), slug, "");
  const newer = zip({ "modrinth.index.json": index((i) => ((i.versionId = "2.1.0"), (i.dependencies.minecraft = "1.21.5"))) });
  let sent: { name: string | null; size: number } | null = null;
  await page.route(`**/api/v1/orgs/${slug}/servers/pack/modpack?*`, async (route) => {
    const q = new URL(route.request().url()).searchParams;
    sent = { name: q.get("name"), size: q.get("upload") ? Number(q.get("size")) : (route.request().postDataBuffer()?.length ?? 0) };
    state.modpack = { ...uploaded, project: "probe-2.1.mrpack", versionName: "2.1.0", gameVersion: "1.21.5", version: sha1(newer) };
    const server = await (await page.request.get(`/api/v1/orgs/${slug}/servers/pack`)).json();
    return route.fulfill({ json: { ...server, spec: { ...server.spec, modpack: state.modpack } } });
  });
  await page.goto(`/o/${slug}/s/pack/content`);
  await expect(page.getByText("The .mrpack file was not uploaded.")).toBeVisible();
  // One that is not a pack is said at once, and not sent.
  await page.getByLabel("Modpack file").setInputFiles(packFile(Buffer.from("nope"), "nope.mrpack"));
  await expect(page.getByText("This is not a Modrinth modpack (.mrpack).")).toBeVisible();
  expect(sent).toBeNull();
  await page.getByLabel("Modpack file").setInputFiles(packFile(newer, "probe-2.1.mrpack"));
  await expect(page.getByText("2.1.0 · Fabric 1.21.5 · probe-2.1.mrpack")).toBeVisible();
  await expect(page.getByRole("button", { name: "Upload newer" })).toBeVisible();
  expect(sent).toEqual({ name: "probe-2.1.mrpack", size: newer.length });
});
