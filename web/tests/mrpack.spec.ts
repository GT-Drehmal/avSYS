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
      { path: "mods/lithium-fabric-0.15.3.jar", env: { client: "required", server: "required" }, downloads: ["https://cdn.modrinth.com/data/gvQqBUqZ/versions/Vy9QGdBz/l.jar"] },
      { path: "mods/sodium-fabric-0.6.13.jar", env: { client: "required", server: "unsupported" }, downloads: ["https://cdn.modrinth.com/data/b/sodium.jar"] },
      { path: "resourcepacks/faithful.zip", downloads: ["https://x.org/f.zip"] },
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
  // One from Modrinth links to its page there, as read from where it is
  // downloaded; one from elsewhere does not.
  const link = (n: number) => files.getByRole("listitem").nth(n).getByRole("link", { name: "Open on Modrinth" });
  await expect(link(0)).toHaveAttribute("href", "https://modrinth.com/project/gvQqBUqZ");
  await expect(link(1)).toHaveCount(0);
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

test("an .mrpack built for an older game than the world is asked about first, as another version of a pack is", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await packServer(page, api("owner"), slug);
  const older = zip({ "modrinth.index.json": index((i) => ((i.versionId = "1.0.0"), (i.dependencies.minecraft = "1.21"))) });
  const newer = zip({ "modrinth.index.json": index((i) => ((i.versionId = "2.1.0"), (i.dependencies.minecraft = "1.21.5"))) });
  const sent: string[] = [];
  await page.route(`**/api/v1/orgs/${slug}/servers/pack/modpack?*`, async (route) => {
    sent.push(new URL(route.request().url()).searchParams.get("newerWorld") ?? "");
    return route.fulfill({ status: 503, json: { code: "test", message: "Not sent by the test." } });
  });
  await page.goto(`/o/${slug}/s/pack/content`);
  await page.getByLabel("Modpack file").setInputFiles(packFile(older, "probe-1.0.mrpack"));
  const ask = page.getByRole("dialog", { name: "Open a 1.21.4 world in 1.21?" });
  await expect(ask.getByText("The world was last opened in 1.21.4. 1.21 cannot open the world, so the server will not start.")).toBeVisible();
  await ask.getByRole("button", { name: "Cancel" }).click();
  await expect(ask).toHaveCount(0);
  expect(sent).toEqual([]);
  await page.getByLabel("Modpack file").setInputFiles(packFile(older, "probe-1.0.mrpack"));
  await ask.getByRole("button", { name: "Change to 1.0.0 anyway" }).click();
  await expect.poll(() => sent).toEqual(["true"]);
  await expect(ask).toHaveCount(0);
  // A newer one is not asked about.
  await page.getByLabel("Modpack file").setInputFiles(packFile(newer, "probe-2.1.mrpack"));
  await expect.poll(() => sent).toEqual(["true", ""]);
  await expect(ask).toHaveCount(0);
});

/** What the panel says an uploaded pack lists: nine mods, one for players only. */
const packed = [
  ...["lithium", "ferritecore", "krypton", "c2me", "modernfix", "spark", "chunky", "noisium"].map((n, i) => ({
    path: `mods/${n}-fabric-1.0.${i}.jar`,
    server: true,
    ...(n === "lithium" ? { project: "gvQqBUqZ", title: "Lithium", iconUrl: `data:image/png;base64,${png.toString("base64")}` } : {}),
  })),
  { path: "mods/sodium-fabric-0.6.13.jar", server: false, project: "AANobbMI", title: "Sodium" },
];

/** Answers for the pack's contents and picture, counting what is asked. */
async function packAnswers(page: Page, slug: string, files: object[] | null, icon: Buffer | null = png) {
  const asked = { contents: 0, icon: [] as string[] };
  await page.route(`**/api/v1/orgs/${slug}/servers/pack/modpack/contents`, async (route) => {
    asked.contents++;
    return files ? route.fulfill({ json: { files } }) : route.fulfill({ status: 502, json: { code: "upstream", message: "No." } });
  });
  await page.route(`**/api/v1/orgs/${slug}/servers/pack/modpack/icon?*`, async (route) => {
    asked.icon.push(new URL(route.request().url()).search);
    return icon ? route.fulfill({ body: icon, contentType: "image/png" }) : route.fulfill({ status: 404, json: { code: "not_found", message: "Not found." } });
  });
  return asked;
}

test("the Content page shows an uploaded pack's own picture, and the mods in it once asked", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await packServer(page, api("owner"), slug);
  const asked = await packAnswers(page, slug, packed);
  await page.goto(`/o/${slug}/s/pack/content`);

  // The pack's picture, drawn as the small picture it is.
  const icon = page.locator(`img[src*="/servers/pack/modpack/icon"]`);
  await expect(icon).toBeVisible();
  await expect.poll(() => icon.evaluate((img: HTMLImageElement) => img.naturalWidth)).toBeGreaterThan(0);
  expect(await icon.evaluate((img) => getComputedStyle(img).imageRendering)).toBe("pixelated");
  expect(asked.icon).toEqual([`?v=${sha1(probe)}`]);

  // Closed at first.
  const toggle = page.getByRole("button", { name: "Mods in this pack (9)" });
  await expect(toggle).toHaveAttribute("aria-expanded", "false");
  const files = page.getByRole("region", { name: "In the modpack" });
  await expect(files).toHaveCount(0);
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-expanded", "true");
  await expect(files.getByText("8 on the server · 1 left out, client only")).toBeVisible();
  await expect(files.getByRole("listitem")).toHaveCount(9);
  // A mod from Modrinth by its name and picture, others by their file and
  // initials; the one for players only last, said to be left out.
  await expect(files.getByRole("listitem").first()).toHaveText(/Lithium\s*Mod/);
  await expect(files.getByRole("listitem").first().locator("img")).toBeVisible();
  await expect(files.getByRole("listitem").nth(1)).toHaveText(/^F\s*ferritecore-fabric-1\.0\.1\s*Mod/);
  // The one from Modrinth links to its page there, the others do not.
  await expect(files.getByRole("listitem").first().getByRole("link", { name: "Open on Modrinth" })).toHaveAttribute("href", "https://modrinth.com/project/gvQqBUqZ");
  await expect(files.getByRole("listitem").nth(1).getByRole("link")).toHaveCount(0);
  await expect(files.getByRole("listitem").last()).toHaveText(/Sodium\s*Client only, left out/);

  // Searched, by name or by file.
  const search = files.getByRole("searchbox", { name: "Search the modpack" });
  await search.fill("sodium");
  await expect(files.getByRole("listitem")).toHaveText([/Sodium\s*Client only, left out/]);
  await search.fill("krypton");
  await expect(files.getByRole("listitem")).toHaveText([/krypton-fabric-1\.0\.2\s*Mod/]);
  await search.fill("nothing like it");
  await expect(files.getByText("Nothing matches.")).toBeVisible();
  await toggle.click();
  await expect(files).toHaveCount(0);
});

test("a pack with no picture shows the plain mark, and one whose list cannot be read shows no list", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await packServer(page, api("owner"), slug);
  const asked = await packAnswers(page, slug, null, null);
  await page.goto(`/o/${slug}/s/pack/content`);
  await expect(page.getByText("2.0.1 · Fabric 1.21.4 · probe.mrpack")).toBeVisible();
  await expect.poll(() => asked.contents).toBeGreaterThan(0);
  await expect.poll(() => asked.icon.length).toBe(1);
  await expect(page.locator('[data-mark="modpack"]')).toBeVisible();
  await expect(page.getByRole("button", { name: /in this pack/i })).toHaveCount(0);
  await expect(page.getByText("No.", { exact: true })).toHaveCount(0);
});

test("a pack not uploaded yet is asked for neither its picture nor its list", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await packServer(page, api("owner"), slug, "");
  const asked = await packAnswers(page, slug, packed);
  await page.goto(`/o/${slug}/s/pack/content`);
  await expect(page.getByText("The .mrpack file was not uploaded.")).toBeVisible();
  await expect(page.locator('[data-mark="modpack"]')).toBeVisible();
  await expect(page.getByRole("button", { name: /in this pack/i })).toHaveCount(0);
  expect(asked).toEqual({ contents: 0, icon: [] });
});

/** Counts what is sent to a new server after it is made, answering for the pack file and picture. */
async function afterMade(page: Page, slug: string, name: string) {
  const icons: Buffer[] = [];
  await page.route(`**/api/v1/orgs/${slug}/servers/${name}/**`, async (route) => {
    const req = route.request();
    if (req.method() === "GET") return route.fallback();
    const url = new URL(req.url());
    if (url.pathname.endsWith("/icon")) {
      icons.push(req.postDataBuffer() ?? Buffer.alloc(0));
      return route.fulfill({ status: 204 });
    }
    if (url.pathname.endsWith("/modpack")) {
      const server = await (await page.request.get(`/api/v1/orgs/${slug}/servers/${name}`)).json();
      return route.fulfill({ json: server });
    }
    return route.fallback();
  });
  return icons;
}

test("an uploaded pack's picture stays the server's when another world is chosen without one", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  const icons = await afterMade(page, slug, "probe-pack");
  await newModpack(page, slug);
  await page.getByLabel("Modpack file").setInputFiles(packFile(probe));
  await expect(page.getByText("Probe Pack", { exact: true })).toBeVisible();
  // Going through the world choices, none of which has a picture, and back.
  const worldChoice = page.getByRole("group", { name: "World", exact: true });
  await worldChoice.getByRole("button", { name: "Upload world" }).click();
  await worldChoice.getByRole("button", { name: "New world" }).click();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/probe-pack$`));
  await expect.poll(() => icons.length).toBe(1);
  // WebKit does not show a test the bytes of a file sent: checked where they can be seen.
  if (icons[0].length > 0) expect(icons[0].equals(png)).toBe(true);
});

test("a pack picked from Modrinth gives the server its picture, as a PNG", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  const icons = await afterMade(page, slug, "adventure-pack");
  // Its picture where Modrinth keeps it.
  await page.route("**/api/v1/catalog/modrinth/search?*", async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    for (const hit of body.hits ?? []) if (hit.slug === "adventure-pack") hit.icon_url = "https://cdn.modrinth.com/data/P0000200/icon.webp";
    return route.fulfill({ response: res, json: body });
  });
  // Slow to come: the server is not made without it.
  await page.route("https://cdn.modrinth.com/data/P0000200/icon.webp", async (route) => {
    await new Promise((resolve) => setTimeout(resolve, 1500));
    await route.fulfill({ body: png, contentType: "image/png", headers: { "Access-Control-Allow-Origin": "*" } });
  });
  await newModpack(page, slug);
  await page.getByRole("button", { name: /^Adventure Pack/ }).click();
  await expect(page.getByText("modrinth:adventure-pack")).toBeVisible();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/adventure-pack$`));
  await expect.poll(() => icons.length).toBe(1);
  // Sent as a PNG, which the panel takes whatever Modrinth's was.
  if (icons[0].length > 0) expect(icons[0].subarray(1, 4).toString()).toBe("PNG");
});

test("a pack picked from Modrinth without a picture leaves the server its letter", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  const icons = await afterMade(page, slug, "adventure-pack");
  await newModpack(page, slug);
  await page.getByRole("button", { name: /^Adventure Pack/ }).click();
  await expect(page.getByText("modrinth:adventure-pack")).toBeVisible();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/adventure-pack$`));
  await page.waitForTimeout(500);
  expect(icons).toHaveLength(0);
});
