import type { Page, Request } from "@playwright/test";
import { levelDat, zip } from "./archives";
import { type Api, expect, test } from "./panel";

// Every way a server is made, whatever it runs, starts it with a world that
// can be chosen: a new one, one from the library, or one of one's own, and
// the one its files or archive have where there is one. The same checks go
// with each: room for it, its picture, and a world newer than the game the
// server runs, a modpack's being the game the pack is built for. The fixtures'
// Modrinth packs are built for 1.21.4.

const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");

const libraryWorld = (id: string, name: string, version: string) => ({
  id,
  name,
  description: "",
  createdBy: "owner",
  createdAt: new Date(Date.now() - 86_400_000).toISOString(),
  updatedAt: new Date().toISOString(),
  root: "world",
  version,
  size: 12 << 20,
  source: "upload",
  ready: true,
});

/** The library the panel would have: one world, with a picture. */
async function library(page: Page, org: string, version = "1.21.4") {
  await page.route(new RegExp(`/api/v1/orgs/${org}/worlds(/.*)?(\\?.*)?$`), (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/icon")) return route.fulfill({ status: 200, contentType: "image/png", body: png });
    return route.fulfill({ json: [libraryWorld("w1", "Island", version)] });
  });
}

/** What was asked of a server, in order, but for reading it. */
function changes(page: Page, org: string) {
  const seen: string[] = [];
  page.on("request", (r: Request) => {
    const url = new URL(r.url());
    if (r.method() === "GET" || !url.pathname.startsWith(`/api/v1/orgs/${org}/servers`) || url.pathname.endsWith("/import/upload")) return;
    const q = url.searchParams;
    for (const k of ["upload", "size", "wait", "root"]) q.delete(k);
    seen.push(`${r.method()} ${url.pathname.replace(`/api/v1/orgs/${org}`, "")}${q.size ? `?${q}` : ""}`);
  });
  return seen;
}

/** A stopped server in this org that runs a modpack built for game, as the panel would say. */
async function packServer(page: Page, api: Api, org: string, name: string, game: string, extra: Record<string, unknown> = {}) {
  await api.post(`/orgs/${org}/servers`, { name, spec: { eula: true, loader: "fabric", version: "26.3", state: "Stopped", resources: { memory: "2Gi" }, storage: { size: "2Gi" } } });
  const withPack = (s: { name: string; spec: object; status: object }) =>
    s.name === name
      ? { ...s, spec: { ...s.spec, modpack: { source: "modrinth", project: "adventure-pack", title: "Adventure Pack", version: "pv2", versionName: "2.0.0", gameVersion: game, loader: "fabric" } }, status: { ...s.status, phase: "Stopped" }, ...extra }
      : s;
  await page.route(`**/api/v1/orgs/${org}/servers`, async (route) =>
    route.request().method() === "GET" ? route.fulfill({ json: (await (await route.fetch()).json()).map(withPack) }) : route.fallback(),
  );
  await page.route(`**/api/v1/orgs/${org}/servers/${name}`, async (route) =>
    route.request().method() === "GET" ? route.fulfill({ json: withPack(await (await route.fetch()).json()) }) : route.fallback(),
  );
  // The panel's own word of it, which knows nothing of the pack, is not heard.
  await page.route("**/api/v1/events?*", (route) => route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body: "event: waiting\ndata: null\n\n" }));
}

/** Picks the fixtures' Adventure Pack as the modpack of a new server or template. */
async function pickPack(page: Page) {
  await page.getByRole("button", { name: "Modpack", exact: true }).click();
  await page.getByRole("button", { name: /^Adventure Pack/ }).click();
  await expect(page.getByText("modrinth:adventure-pack")).toBeVisible();
}

test.beforeEach(async ({ as, page }) => {
  await as("owner");
  // Archives are sent in parts, which the panel would keep: all of each is taken.
  await page.route(/\/api\/v1\/orgs\/[^/]+\/servers\/[^/]+\/import\/upload/, (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() === "GET") return route.fulfill({ json: { received: 0 } });
    return route.fulfill({ json: { received: Number(url.searchParams.get("offset")) + Number(url.searchParams.get("length")) } });
  });
});

test("a new modpack server starts with a world from the library, checked against the pack's game, with the world's picture", async ({ page, org, api }) => {
  const slug = await org([]);
  await library(page, slug, "1.21.5");
  const seen = changes(page, slug);
  await page.route(`**/api/v1/orgs/${slug}/servers/island/import?*`, async (route) => route.fulfill({ json: await api("owner").get(`/orgs/${slug}/servers/island`) }));
  await page.route(`**/api/v1/orgs/${slug}/servers/island/icon?*`, (route) => route.fulfill({ status: 204 }));
  await page.goto(`/o/${slug}/new`);
  await page.getByLabel("Display name").fill("Island");
  await pickPack(page);
  const worldChoice = page.getByRole("group", { name: "World", exact: true });
  await expect(worldChoice.getByRole("button", { name: "New world" })).toHaveAttribute("aria-pressed", "true");
  await worldChoice.getByRole("button", { name: "Library world" }).click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  // The pack is built for 1.21.4, which cannot open the world.
  await expect(page.getByText("The world was last opened in 1.21.5. 1.21.4 cannot open the world, so the server will not start.")).toBeVisible();
  // It looks as the world does.
  await expect(page.getByRole("button", { name: "Replace" })).toBeVisible();
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  const ask = page.getByRole("dialog", { name: "Open a 1.21.5 world in 1.21.4?" });
  await ask.getByRole("button", { name: "Create server anyway" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/island$`));
  await expect(page.getByText("Island is set up")).toBeVisible();
  // Made stopped, with the pack; the world put in; then the picture, which starts it.
  expect(seen).toEqual(["POST /servers", "PUT /servers/island/import?world=true&library=w1&newerWorld=true", "PUT /servers/island/icon?start=true"]);
});

test("a new modpack server starts with a world of one's own", async ({ page, org, api }) => {
  const slug = await org([]);
  const seen = changes(page, slug);
  await page.route(`**/api/v1/orgs/${slug}/servers/island/import?*`, async (route) => route.fulfill({ json: await api("owner").get(`/orgs/${slug}/servers/island`) }));
  await page.route(`**/api/v1/orgs/${slug}/servers/island/icon?*`, (route) => route.fulfill({ status: 204 }));
  await page.goto(`/o/${slug}/new`);
  await page.getByLabel("Display name").fill("Island");
  await pickPack(page);
  await page.getByRole("group", { name: "World", exact: true }).getByRole("button", { name: "Upload world" }).click();
  await page.getByLabel("World archive").setInputFiles({ name: "save.zip", mimeType: "application/zip", buffer: zip({ "Save/level.dat": levelDat("1.21.4", "Save"), "Save/icon.png": png }) });
  await expect(page.getByTestId("world-summary")).toContainText("Last opened in 1.21.4");
  // The pack's own game opens it.
  await expect(page.getByText(/cannot open the world/)).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Replace" })).toBeVisible();
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page.getByText("Island is set up")).toBeVisible();
  expect(seen).toEqual(["POST /servers", "PUT /servers/island/import?world=true&version=1.21.4&from=upload&file=save.zip", "PUT /servers/island/icon?start=true"]);
  const { spec } = await api("owner").get(`/orgs/${slug}/servers/island`);
  expect(spec.modpack).toMatchObject({ source: "modrinth", project: "adventure-pack", gameVersion: "1.21.4" });
});

test("a server's archive keeps the world in it, unless given a new one or another", async ({ page, org, api }) => {
  const slug = await org([]);
  await library(page, slug);
  const archive = zip({ "MyServer/server.properties": "motd=Old\nlevel-seed=5\n", "MyServer/world/level.dat": levelDat("1.21.4"), "MyServer/paper-1.21.4-151.jar": "jar" });
  const imports: URLSearchParams[] = [];
  // A world's picture goes on once its files are in, and starts it.
  await page.route(`**/api/v1/orgs/${slug}/servers/*/icon?*`, (route) => route.fulfill({ status: 204 }));
  await page.route(`**/api/v1/orgs/${slug}/servers/*/import?*`, async (route) => {
    const url = new URL(route.request().url());
    imports.push(url.searchParams);
    await route.fulfill({ json: await api("owner").get(url.pathname.replace("/api/v1", "").replace(/\/import$/, "")) });
  });
  const make = async (name: string, choose: (choice: ReturnType<Page["getByRole"]>) => Promise<void>) => {
    await page.goto(`/o/${slug}/new`);
    await page.getByRole("button", { name: "Import", exact: true }).click();
    await page.getByLabel("Server archive").setInputFiles({ name: "myserver.zip", mimeType: "application/zip", buffer: archive });
    await expect(page.getByTestId("archive-summary")).toContainText('world "world"');
    await page.getByLabel("Display name").fill(name);
    const worldChoice = page.getByRole("group", { name: "World", exact: true });
    await expect(worldChoice.getByRole("button", { name: "Keep world" })).toHaveAttribute("aria-pressed", "true");
    await choose(worldChoice);
    await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
    await page.getByRole("button", { name: "Create server" }).click();
    await page.getByRole("dialog").getByRole("button", { name: "Create without a whitelist" }).click();
    await expect(page.getByText(`${name} is set up`)).toBeVisible();
  };

  // Kept: the archive as it is.
  await make("Kept", async () => {
    await page.getByRole("button", { name: /World options and more/ }).click();
    await expect(page.getByLabel("Seed")).toBeDisabled();
  });
  expect(imports.map((q) => [q.get("world"), q.get("version")])).toEqual([[null, "1.21.4"]]);

  // A new one, made as asked: the archive without its world.
  imports.length = 0;
  await make("Fresh", async (choice) => {
    await choice.getByRole("button", { name: "New world" }).click();
    await page.getByRole("button", { name: /World options and more/ }).click();
    await expect(page.getByLabel("Seed")).toBeEnabled();
    await page.getByLabel("Seed").fill("42");
  });
  expect(imports.map((q) => [q.get("world"), q.get("version")])).toEqual([["none", null]]);
  expect((await api("owner").get(`/orgs/${slug}/servers/fresh`)).spec.properties).toMatchObject({ "level-seed": "42" });

  // One from the library, put in after the rest of the archive.
  imports.length = 0;
  await make("Other", async (choice) => {
    await choice.getByRole("button", { name: "Library world" }).click();
    await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
    await expect(page.getByText("Plugin data from the archive, such as claims and warps, may point at places this world does not have.")).toBeVisible();
  });
  expect(imports.map((q) => [q.get("world"), q.get("library")])).toEqual([
    ["none", null],
    ["true", "w1"],
  ]);
});

test("a server's archive with no world in it starts with a new one, or one given", async ({ page, org }) => {
  const slug = await org([]);
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("button", { name: "Import", exact: true }).click();
  await page.getByLabel("Server archive").setInputFiles({ name: "myserver.zip", mimeType: "application/zip", buffer: zip({ "MyServer/server.properties": "motd=Old\n", "MyServer/paper-1.21.4-151.jar": "jar" }) });
  await expect(page.getByTestId("archive-summary")).toContainText("no world");
  const worldChoice = page.getByRole("group", { name: "World", exact: true });
  await expect(worldChoice.getByRole("button", { name: "Keep world" })).toHaveCount(0);
  await expect(worldChoice.getByRole("button", { name: "New world" })).toHaveAttribute("aria-pressed", "true");
  for (const other of ["Library world", "Upload world"]) await expect(worldChoice.getByRole("button", { name: other })).toBeVisible();
});

test("a copy of a modpack server is warned of a world newer than the pack's game, and takes the world's picture", async ({ page, org, api }) => {
  const slug = await org([]);
  await packServer(page, api("owner"), slug, "pack", "1.21.4");
  await library(page, slug, "1.21.5");
  await page.goto(`/o/${slug}/new?from=server:pack`);
  await expect(page.getByRole("heading", { name: "Duplicate pack" })).toBeVisible();
  const worldChoice = page.getByRole("group", { name: "World", exact: true });
  await expect(worldChoice.getByRole("button", { name: "Keep world" })).toHaveAttribute("aria-pressed", "true");
  await worldChoice.getByRole("button", { name: "Library world" }).click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  // Its settings say 26.3, its pack 1.21.4: the pack's is the game it runs.
  await expect(page.getByText("The world was last opened in 1.21.5. 1.21.4 cannot open the world, so the server will not start.")).toBeVisible();
  const sent = page.waitForRequest((r) => r.method() === "POST" && r.url().endsWith(`/orgs/${slug}/servers`));
  await page.route(`**/api/v1/orgs/${slug}/servers`, (route) =>
    route.request().method() === "POST" ? route.fulfill({ status: 503, json: { code: "test", message: "Not sent by the test." } }) : route.fallback(),
  );
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  await page.getByRole("dialog", { name: "Open a 1.21.5 world in 1.21.4?" }).getByRole("button", { name: "Duplicate anyway" }).click();
  const body = (await sent).postDataJSON();
  // Without the original's world, and looking as the one given does.
  expect(body).toMatchObject({ fromServer: "pack", world: "new" });
  expect(typeof body.picture).toBe("string");
  // Kept, it keeps the original's picture.
  await worldChoice.getByRole("button", { name: "Keep world" }).click();
  const again = page.waitForRequest((r) => r.method() === "POST" && r.url().endsWith(`/orgs/${slug}/servers`));
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  expect((await again).postDataJSON()).not.toHaveProperty("picture");
});

test("a template of settings alone that runs a modpack is given a world from the library", async ({ page, org }) => {
  const slug = await org([]);
  await library(page, slug);
  let put: string | null = null;
  await page.route(new RegExp(`/api/v1/orgs/${slug}/templates/[^/]+/world\\?`), (route) => {
    put = new URL(route.request().url()).search;
    return route.fulfill({ json: {} });
  });
  await page.goto(`/o/${slug}/templates/new`);
  await page.getByLabel("Name", { exact: true }).fill("Pack base");
  await pickPack(page);
  await page.getByRole("group", { name: "World", exact: true }).getByRole("button", { name: "Library world" }).click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  await page.getByRole("button", { name: "Create template" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/templates$`));
  expect(put).toBe("?library=w1");
});

test("a stopped server's world is replaced with a new one, made as asked", async ({ page, org, api }) => {
  const slug = await org([]);
  await packServer(page, api("owner"), slug, "pack", "1.21.4", {});
  let sent: { query: Record<string, string>; body: unknown } | null = null;
  await page.route(`**/api/v1/orgs/${slug}/servers/pack/import?*`, async (route) => {
    sent = { query: Object.fromEntries(new URL(route.request().url()).searchParams), body: route.request().postDataJSON() };
    await route.fulfill({ status: 202, json: await api("owner").get(`/orgs/${slug}/servers/pack`) });
  });
  const patched: string[] = [];
  page.on("request", (r) => r.method() === "PATCH" && patched.push(r.url()));
  await page.goto(`/o/${slug}/s/pack/settings`);
  await page.getByRole("button", { name: "Replace the world" }).click();
  const dialog = page.getByRole("dialog", { name: "Replace the world?" });
  await dialog.getByRole("group", { name: "World", exact: true }).getByRole("button", { name: "New world" }).click();
  await expect(dialog.getByLabel("Seed")).toHaveValue("");
  await dialog.getByLabel("Seed").fill("42");
  await dialog.getByLabel("World type").selectOption("amplified");
  await dialog.getByRole("switch", { name: "Generate structures" }).click();
  await dialog.getByRole("button", { name: "Replace the world" }).click();
  await expect(dialog).toHaveCount(0);
  expect(sent).toEqual({
    query: { world: "new", replace: "true" },
    body: { properties: { "level-seed": "42", "level-type": "amplified", "generate-structures": "false", hardcore: null } },
  });
  // The server's settings around it are not saved with it.
  expect(patched).toEqual([]);
});

test("a modpack server's world replaced with one newer than the pack's game is asked about first", async ({ page, org, api }) => {
  const slug = await org([]);
  await packServer(page, api("owner"), slug, "pack", "1.21.4");
  await library(page, slug, "1.21.5");
  let sent: URLSearchParams | null = null;
  await page.route(`**/api/v1/orgs/${slug}/servers/pack/import?*`, async (route) => {
    sent = new URL(route.request().url()).searchParams;
    await route.fulfill({ status: 202, json: await api("owner").get(`/orgs/${slug}/servers/pack`) });
  });
  const patched: string[] = [];
  page.on("request", (r) => r.method() === "PATCH" && patched.push(r.url()));
  await page.goto(`/o/${slug}/s/pack/settings`);
  await page.getByRole("button", { name: "Replace the world" }).click();
  const dialog = page.getByRole("dialog", { name: "Replace the world?" });
  await dialog.getByRole("button", { name: "Library world", exact: true }).click();
  await dialog.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  await expect(dialog.getByText("The world was last opened in 1.21.5. 1.21.4 cannot open the world, so the server will not start.")).toBeVisible();
  await dialog.getByRole("button", { name: "Replace the world" }).click();
  await page.getByRole("dialog", { name: "Open a 1.21.5 world in 1.21.4?" }).getByRole("button", { name: "Replace the world anyway" }).click();
  await expect(dialog).toHaveCount(0);
  expect(Object.fromEntries(sent!)).toMatchObject({ world: "true", replace: "true", library: "w1", newerWorld: "true" });
  expect(patched).toEqual([]);
});

test("a modpack put on a version built for an older game than its world is asked about first", async ({ page, org, api }) => {
  const slug = await org([]);
  await packServer(page, api("owner"), slug, "pack", "26.3");
  const version = (id: string, number: string, game: string, type = "release") => ({ id, name: number, version_number: number, version_type: type, game_versions: [game], loaders: ["fabric"], date_published: "2026-09-20T10:00:00Z" });
  // Its newest is a beta for 26.3; its newest release, the one the panel picks for the best fit, is for 1.21.4.
  await page.route("**/api/v1/catalog/modrinth/projects/adventure-pack/versions*", (route) => route.fulfill({ json: [version("pv3", "3.0.0-beta", "26.3", "beta"), version("pv1", "1.0.0", "1.21.4")] }));
  const patched: { query: string; body: unknown }[] = [];
  await page.route(`**/api/v1/orgs/${slug}/servers/pack?*`, async (route) => {
    patched.push({ query: new URL(route.request().url()).search, body: route.request().postDataJSON() });
    await route.fulfill({ json: await api("owner").get(`/orgs/${slug}/servers/pack`) });
  });
  await page.route(`**/api/v1/orgs/${slug}/servers/pack`, async (route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    patched.push({ query: "", body: route.request().postDataJSON() });
    await route.fulfill({ json: await api("owner").get(`/orgs/${slug}/servers/pack`) });
  });
  await page.goto(`/o/${slug}/s/pack/content`);
  await page.getByRole("button", { name: "2.0.0" }).click();
  await page.getByRole("dialog").getByRole("button", { name: /1\.0\.0/ }).click();
  const ask = page.getByRole("dialog", { name: "Open a 26.3 world in 1.21.4?" });
  await ask.getByRole("button", { name: "Cancel" }).click();
  await expect(ask).toHaveCount(0);
  expect(patched).toEqual([]);
  // The best fit is the same version, and asked about the same.
  await page.getByRole("button", { name: "2.0.0" }).click();
  await page.getByRole("dialog").getByRole("button", { name: /Update to the best fit/ }).click();
  await ask.getByRole("button", { name: "Change to 1.0.0 anyway" }).click();
  await expect.poll(() => patched.length).toBe(1);
  expect(patched[0].query).toBe("?newerWorld=true");
  expect(patched[0].body).toMatchObject({ spec: { modpack: { project: "adventure-pack", version: "" } } });
});
