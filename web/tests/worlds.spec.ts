import type { Page } from "@playwright/test";
import { levelDat, zip } from "./archives";
import { expect, test } from "./panel";

// The library of worlds. Its worlds are kept with snapshots, which need
// restic: here the panel's answers stand in.

const world = (id: string, name: string, extra: Record<string, unknown> = {}) => ({
  id,
  name,
  description: "",
  createdBy: "owner",
  createdAt: new Date(Date.now() - 86_400_000).toISOString(),
  updatedAt: new Date().toISOString(),
  root: "world",
  version: "1.21.4",
  size: 12 << 20,
  source: "upload",
  ready: true,
  ...extra,
});

/** The library the panel would have, with what is asked of it noted. */
async function library(page: Page, worlds: ReturnType<typeof world>[], org = "friends") {
  const calls: { method: string; path: string; query: Record<string, string>; body: unknown }[] = [];
  await page.route(new RegExp(`/api/v1/orgs/${org}/worlds(/.*)?(\\?.*)?$`), async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname.replace(`/api/v1/orgs/${org}`, "");
    if (req.method() === "GET" && path === "/worlds") return route.fulfill({ json: worlds });
    if (req.method() === "GET" && path.endsWith("/icon")) return route.fulfill({ status: 404, json: { code: "not_found", message: "No picture." } });
    let body: unknown = null;
    try {
      body = req.postDataJSON();
    } catch {
      body = req.postDataBuffer()?.length ?? null;
    }
    calls.push({ method: req.method(), path, query: Object.fromEntries(url.searchParams), body });
    if (req.method() === "POST") {
      const made = world("w9", url.searchParams.get("name") ?? (body as { name: string }).name, path.endsWith("/upload") ? {} : { ready: false, saving: true });
      worlds.push(made);
      return route.fulfill({ status: path.endsWith("/upload") ? 201 : 202, json: made });
    }
    if (req.method() === "DELETE" && path.match(/^\/worlds\/[^/]+$/)) {
      worlds.splice(worlds.findIndex((w) => path.endsWith(w.id)), 1);
      return route.fulfill({ status: 204 });
    }
    if (req.method() === "PUT" || req.method() === "DELETE") return route.fulfill({ status: 204 });
    return route.fulfill({ json: worlds[0] });
  });
  return calls;
}

test("the library lists its worlds, found by searching", async ({ page, as }) => {
  await as("viewer");
  await library(page, [world("w1", "Island", { description: "A small one" }), world("w2", "Arena", { source: "server:arena", version: "1.21.1" }), world("w3", "Lobby", { ready: false, saving: true })]);
  await page.goto("/o/friends");
  await page.getByRole("link", { name: "Worlds" }).click();
  await expect(page.getByRole("heading", { name: "Worlds" })).toBeVisible();
  const list = page.getByRole("list", { name: "Worlds" });
  await expect(list.getByRole("listitem")).toHaveCount(3);
  await expect(list.getByRole("listitem", { name: "Island" })).toContainText("A small one");
  await expect(list.getByRole("listitem", { name: "Arena" })).toContainText("1.21.1 · 12 MB · from arena");
  await expect(list.getByRole("listitem", { name: "Lobby" })).toContainText("Saving");
  // A viewer sees them, and nothing to change them with.
  await expect(page.getByRole("button", { name: "Add world" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /Actions for/ })).toHaveCount(0);
  await page.getByRole("searchbox", { name: "Search worlds" }).fill("arena");
  await expect(list.getByRole("listitem")).toHaveCount(1);
  await page.getByRole("searchbox", { name: "Search worlds" }).fill("nothing like it");
  await expect(page.getByText("No worlds match.")).toBeVisible();
});

test("a world is uploaded to the library, named as its file is, with its own picture", async ({ page, as }) => {
  await as("operator");
  const calls = await library(page, []);
  await page.goto("/o/friends/worlds");
  await expect(page.getByText("No worlds yet")).toBeVisible();
  await page.getByRole("button", { name: "Add world" }).click();
  const dialog = page.getByRole("dialog", { name: "Add a world" });
  await expect(dialog.getByRole("button", { name: "Add world" })).toBeDisabled();
  // A save made in the game, one folder down, with a picture of its own.
  const icon = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
  // What it calls itself is coloured as the game shows it: said plainly.
  await dialog.getByLabel("World archive").setInputFiles({
    name: "Island Adventure.zip",
    mimeType: "application/zip",
    buffer: zip({ "Island Save/level.dat": levelDat("1.21.4", "§6§lIsland§r Save"), "Island Save/region/r.0.0.mca": "land", "Island Save/icon.png": icon }),
  });
  await expect(dialog.getByTestId("world-summary")).toContainText('World "Island Save" · last opened in 1.21.4');
  await expect(dialog.getByTestId("world-summary").locator("span[style]")).toHaveText("Island");
  await expect(dialog.getByTestId("world-summary").locator("span[style]")).toHaveCSS("font-weight", "700");
  await expect(dialog.getByLabel("Name")).toHaveValue("Island Adventure");
  // A name typed in is kept when another file is chosen.
  await dialog.getByLabel("Name").fill("Island");
  await dialog.getByLabel("World archive").setInputFiles({
    name: "island.zip",
    mimeType: "application/zip",
    buffer: zip({ "Island Save/level.dat": levelDat("1.21.4", "Island"), "Island Save/region/r.0.0.mca": "land", "Island Save/icon.png": icon }),
  });
  await expect(dialog.getByTestId("world-summary")).toContainText('World "Island"');
  await expect(dialog.getByLabel("Name")).toHaveValue("Island");
  await dialog.getByLabel("Notes").fill("Where we started");
  await dialog.getByRole("button", { name: "Add world" }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole("listitem", { name: "Island" })).toBeVisible();
  expect(calls[0]).toMatchObject({ method: "POST", path: "/worlds/upload", query: { name: "Island", description: "Where we started", root: "Island Save", version: "1.21.4", file: "island.zip" } });
  // How large it is unpacked is the panel's to find out, as it unpacks it.
  expect(calls[0].query.unpacked).toBeUndefined();
  expect(calls[1]).toMatchObject({ method: "PUT", path: "/worlds/w9/icon" });
});

test("a name already taken is said before a file is sent", async ({ page, as }) => {
  await as("operator");
  const calls = await library(page, [world("w1", "Island")]);
  const parts: string[] = [];
  await page.route(/\/api\/v1\/orgs\/friends\/uploads/, (route) => {
    parts.push(route.request().url());
    return route.fulfill({ json: { received: 0 } });
  });
  await page.goto("/o/friends/worlds");
  await page.getByRole("button", { name: "Add world" }).click();
  const dialog = page.getByRole("dialog", { name: "Add a world" });
  await dialog.getByLabel("World archive").setInputFiles({ name: "island.zip", mimeType: "application/zip", buffer: zip({ "Save/level.dat": levelDat("1.21.4", "Save") }) });
  await expect(dialog.getByTestId("world-summary")).toBeVisible();
  await dialog.getByLabel("Name").fill("ISLAND");
  await dialog.getByRole("button", { name: "Add world" }).click();
  await expect(dialog.getByText("There is already a world called ISLAND.")).toBeVisible();
  expect(parts).toEqual([]);
  expect(calls).toEqual([]);
});

test("a world's own picture goes with it, not with another archive or a server's world", async ({ page, as }) => {
  await as("operator");
  const calls = await library(page, []);
  await page.goto("/o/friends/worlds");
  await page.getByRole("button", { name: "Add world" }).click();
  const dialog = page.getByRole("dialog", { name: "Add a world" });
  const icon = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
  await dialog.getByLabel("World archive").setInputFiles({ name: "a.zip", mimeType: "application/zip", buffer: zip({ "A/level.dat": levelDat("1.21.4", "A"), "A/icon.png": icon }) });
  await expect(dialog.getByRole("button", { name: "Remove" })).toBeVisible();
  // Another without one: none.
  await dialog.getByLabel("World archive").setInputFiles({ name: "b.zip", mimeType: "application/zip", buffer: zip({ "B/level.dat": levelDat("1.21.4", "B") }) });
  await expect(dialog.getByTestId("world-summary")).toContainText('World "B"');
  await expect(dialog.getByRole("button", { name: "Remove" })).toHaveCount(0);
  await dialog.getByLabel("World archive").setInputFiles({ name: "a.zip", mimeType: "application/zip", buffer: zip({ "A/level.dat": levelDat("1.21.4", "A"), "A/icon.png": icon }) });
  await expect(dialog.getByRole("button", { name: "Remove" })).toBeVisible();
  // A server's world brings the server's.
  await dialog.getByRole("button", { name: "Server", exact: true }).click();
  await dialog.getByRole("button", { name: /Survival/ }).first().click();
  await dialog.getByLabel("Name").fill("Spawn");
  await dialog.getByRole("button", { name: "Add world" }).click();
  await expect(dialog).toHaveCount(0);
  expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual(["POST /worlds"]);
});

test("a world that could not be saved says why, until it is deleted", async ({ page, as }) => {
  await as("admin");
  await library(page, [world("w1", "Nothing", { ready: false, error: "The world is not among the server's files." })]);
  await page.goto("/o/friends/worlds");
  const row = page.getByRole("listitem", { name: "Nothing" });
  await expect(row).toContainText("Not saved: The world is not among the server's files.");
  await expect(row.getByRole("button", { name: "New server" })).toBeDisabled();
  await row.getByRole("button", { name: "Actions for Nothing" }).click();
  await expect(page.getByRole("menuitem", { name: "Download" })).toHaveAttribute("data-disabled", "");
  await page.getByRole("menuitem", { name: "Delete" }).click();
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(page.getByText("No worlds yet")).toBeVisible();
});

test("a world kept as an archive says so while it is unpacked, and why it could not be, and is used as it is meanwhile", async ({ page, as }) => {
  await as("admin");
  const worlds = [
    world("w1", "Drehmal", { kept: "archive", size: 4 * 2 ** 30, unpacking: true }),
    world("w2", "Broken", { kept: "archive", unpackError: "The archive has no folder Drehmal." }),
    world("w3", "Island", { kept: "folders" }),
  ];
  await library(page, worlds);
  await page.goto("/o/friends/worlds");
  const unpacking = page.getByRole("listitem", { name: "Drehmal" });
  await expect(unpacking).toContainText("1.21.4 · 4.0 GB · uploaded");
  await expect(unpacking).toContainText("Unpacking");
  await expect(unpacking.getByRole("button", { name: "New server" })).toBeEnabled();
  const broken = page.getByRole("listitem", { name: "Broken" });
  await expect(broken).toContainText("Could not unpack: The archive has no folder Drehmal.");
  await expect(broken.getByRole("button", { name: "New server" })).toBeEnabled();
  await broken.getByRole("button", { name: "Actions for Broken" }).click();
  await expect(page.getByRole("menuitem", { name: "Download" })).not.toHaveAttribute("data-disabled", "");
  await page.keyboard.press("Escape");
  const kept = page.getByRole("listitem", { name: "Island" });
  await expect(kept).toContainText("1.21.4 · 12 MB · uploaded");
  await expect(kept).not.toContainText("unpack");
  // Once it is unpacked, it says so on its own: the list is asked again
  // while one is being unpacked.
  worlds[0] = world("w1", "Drehmal", { kept: "folders", size: 7 * 2 ** 30, unpacked: 7 * 2 ** 30 });
  await expect(unpacking).toContainText("1.21.4 · 7.0 GB · uploaded", { timeout: 10_000 });
  await expect(unpacking).not.toContainText("Unpacking");
});

test("a new server from a world kept as its own files has room for the world, and for it to grow", async ({ page, as }) => {
  await as("owner");
  await library(page, [world("w1", "Island", { kept: "folders", size: 8 * 2 ** 30, unpacked: 8 * 2 ** 30 })]);
  await page.goto("/o/friends/worlds");
  await page.getByRole("listitem", { name: "Island" }).getByRole("button", { name: "New server" }).click();
  await expect(page.getByRole("heading", { name: "New server" })).toBeVisible();
  await expect(page.getByTestId("world-summary")).toContainText("1.21.4 · 8.0 GB · uploaded");
  // Not room for an archive besides: its files go straight where the world goes.
  await expect(page.getByLabel("Storage (GB)")).toHaveValue("12");
});

test("a server's world, or a snapshot's, is saved to the library", async ({ page, as }) => {
  await as("operator");
  const calls = await library(page, []);
  await page.goto("/o/friends/worlds");
  await page.getByRole("button", { name: "Add world" }).click();
  let dialog = page.getByRole("dialog", { name: "Add a world" });
  await dialog.getByRole("button", { name: "Server", exact: true }).click();
  await dialog.getByRole("button", { name: /Survival/ }).first().click();
  await expect(dialog.getByText("A snapshot of the server is taken first")).toBeVisible();
  await expect(dialog.getByLabel("Name")).toHaveValue("Survival");
  await dialog.getByLabel("Name").fill("Spawn");
  await dialog.getByRole("button", { name: "Add world" }).click();
  await expect(dialog).toHaveCount(0);
  // Saving, until it is.
  await expect(page.getByRole("listitem", { name: "Spawn" })).toContainText("Saving");
  expect(calls[0]).toMatchObject({ method: "POST", path: "/worlds", body: { name: "Spawn", description: "", server: "survival" } });

  await page.getByRole("button", { name: "Add world" }).click();
  dialog = page.getByRole("dialog", { name: "Add a world" });
  await dialog.getByRole("button", { name: "Snapshot", exact: true }).click();
  await dialog.getByRole("button", { name: /oldworld/ }).first().click();
  await dialog.getByRole("list", { name: "Snapshots" }).getByRole("button").first().click();
  await expect(dialog.getByLabel("Name")).toHaveValue("oldworld");
  await dialog.getByRole("button", { name: "Add world" }).click();
  await expect(dialog).toHaveCount(0);
  expect(calls[1]).toMatchObject({ method: "POST", path: "/worlds", body: { name: "oldworld", snapshot: expect.any(String) } });
});

test("a world is renamed, and deleted by an admin", async ({ page, as }) => {
  await as("admin");
  const calls = await library(page, [world("w1", "Island")]);
  await page.goto("/o/friends/worlds");
  await page.getByRole("button", { name: "Actions for Island" }).click();
  await page.getByRole("menuitem", { name: "Edit" }).click();
  const dialog = page.getByRole("dialog", { name: "Edit Island" });
  await dialog.getByLabel("Name").fill("Atoll");
  await dialog.getByLabel("Notes").fill("Smaller now");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog).toHaveCount(0);
  expect(calls[0]).toMatchObject({ method: "PATCH", path: "/worlds/w1", body: { name: "Atoll", description: "Smaller now" } });

  await page.getByRole("button", { name: "Actions for Island" }).click();
  await page.getByRole("menuitem", { name: "Delete" }).click();
  await page.getByRole("button", { name: "Delete", exact: true }).click();
  await expect(page.getByText("No worlds yet")).toBeVisible();
  expect(calls.at(-1)).toMatchObject({ method: "DELETE", path: "/worlds/w1" });
});

test("an operator adds and changes worlds, but does not delete them", async ({ page, as }) => {
  await as("operator");
  await library(page, [world("w1", "Island")]);
  await page.goto("/o/friends/worlds");
  await page.getByRole("button", { name: "Actions for Island" }).click();
  await expect(page.getByRole("menuitem", { name: "Edit" })).toBeVisible();
  await expect(page.getByRole("menuitem", { name: "Download" })).toBeVisible();
  await expect(page.getByRole("menuitem", { name: "Delete" })).toHaveCount(0);
});

test("a new server starts with a world from the library, picked where it is kept", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await library(page, [world("w1", "Island", { size: 3 * 2 ** 30, unpacked: 9 * 2 ** 30 })], slug);
  let sent: URLSearchParams | null = null;
  await page.route(`**/api/v1/orgs/${slug}/servers/island/import?*`, async (route) => {
    sent = new URL(route.request().url()).searchParams;
    await route.fulfill({ json: await api("owner").get(`/orgs/${slug}/servers/island`) });
  });
  await page.goto(`/o/${slug}/worlds`);
  await page.getByRole("listitem", { name: "Island" }).getByRole("button", { name: "New server" }).click();
  await expect(page.getByRole("heading", { name: "New server" })).toBeVisible();
  const worldChoice = page.getByRole("group", { name: "World", exact: true });
  await expect(worldChoice.getByRole("button", { name: "Library world" })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByTestId("world-summary")).toContainText("1.21.4 · 3.0 GB · uploaded");
  await expect(page.getByLabel("Display name")).toHaveValue("Island");
  // Room for it, unpacked beside its archive.
  await expect(page.getByLabel("Storage (GB)")).toHaveValue("12");
  // Made with it, not generated.
  await page.getByRole("button", { name: /World options and more/ }).click();
  await expect(page.getByLabel("Seed")).toBeDisabled();
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/island$`));
  await expect(page.getByText("Island is set up")).toBeVisible();
  expect(Object.fromEntries(sent!)).toMatchObject({ world: "true", library: "w1" });
});

test("a world from the library newer than the game picked is warned of, and one must be picked", async ({ page, as }) => {
  await as("owner");
  await library(page, [world("w1", "Island", { version: "1.21.5" }), world("w2", "Lobby", { ready: false, saving: true })]);
  await page.goto("/o/friends/new");
  await page.getByLabel("Display name").fill("Picked");
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  await page.getByRole("group", { name: "World", exact: true }).getByRole("button", { name: "Library world" }).click();
  const picker = page.getByRole("list", { name: "Worlds" });
  // One still being saved cannot be picked.
  await expect(picker.getByRole("button", { name: /Lobby/ })).toBeDisabled();
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page.getByText("Pick a world from the library.")).toBeVisible();
  await picker.getByRole("button", { name: /Island/ }).click();
  await expect(page.getByText("The world was last opened in 1.21.5. 1.21.4 cannot open the world, so the server will not start.")).toBeVisible();
  await page.getByRole("button", { name: "Change" }).last().click();
  await expect(page.getByRole("searchbox", { name: "Search worlds" })).toBeVisible();
});

test("a new server with a world from the library newer than the game picked is asked about first, and the world sent saying so", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await library(page, [world("w1", "Island", { version: "1.21.5" })], slug);
  let sent: URLSearchParams | null = null;
  await page.route(`**/api/v1/orgs/${slug}/servers/island/import?*`, async (route) => {
    sent = new URL(route.request().url()).searchParams;
    await route.fulfill({ json: await api("owner").get(`/orgs/${slug}/servers/island`) });
  });
  await page.goto(`/o/${slug}/new`);
  await page.getByLabel("Display name").fill("Island");
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  await page.getByRole("group", { name: "World", exact: true }).getByRole("button", { name: "Library world" }).click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  const ask = page.getByRole("dialog", { name: "Open a 1.21.5 world in 1.21.4?" });
  await expect(ask).toContainText("The world was last opened in 1.21.5. 1.21.4 cannot open the world, so the server will not start.");
  // Nothing is made unless asked to.
  await ask.getByRole("button", { name: "Cancel" }).click();
  await expect(ask).toHaveCount(0);
  expect(await api("owner").get(`/orgs/${slug}/servers`)).toEqual([]);
  // On the version it was last opened in, there is nothing to ask.
  await page.getByLabel("Game version").selectOption("1.21.5");
  await expect(page.getByText(/cannot open the world/)).toHaveCount(0);
  await page.getByLabel("Game version").selectOption("1.21.4");
  await page.getByRole("button", { name: "Create server" }).click();
  await ask.getByRole("button", { name: "Create server anyway" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/island$`));
  await expect(page.getByText("Island is set up")).toBeVisible();
  expect(Object.fromEntries(sent!)).toMatchObject({ world: "true", library: "w1", newerWorld: "true" });
});

test("a template is given a world from the library, kept with it", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await library(page, [world("w1", "Island")], slug);
  let put: string | null = null;
  await page.route(new RegExp(`/api/v1/orgs/${slug}/templates/[^/]+/world\\?`), (route) => {
    put = new URL(route.request().url()).search;
    return route.fulfill({ json: {} });
  });
  await page.goto(`/o/${slug}/templates/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  await page.getByLabel("Name", { exact: true }).fill("Island base");
  await page.getByRole("group", { name: "World", exact: true }).getByRole("button", { name: "Library world" }).click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  await page.getByRole("button", { name: "Create template" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/templates$`));
  expect(put).toBe("?library=w1");
});

test("a stopped server's world is replaced with one from the library", async ({ page, as }) => {
  await as("owner");
  await library(page, [world("w1", "Island")]);
  let sent: URLSearchParams | null = null;
  await page.route("**/api/v1/orgs/friends/servers/creative/import?*", async (route) => {
    sent = new URL(route.request().url()).searchParams;
    await route.fulfill({ status: 202, json: await (await page.request.get("/api/v1/orgs/friends/servers/creative")).json() });
  });
  await page.goto("/o/friends/s/creative/settings");
  await page.getByRole("button", { name: "Replace the world" }).click();
  const dialog = page.getByRole("dialog", { name: "Replace the world?" });
  await dialog.getByRole("button", { name: "Library world", exact: true }).click();
  const replace = dialog.getByRole("button", { name: "Replace the world" });
  await expect(replace).toBeDisabled();
  await dialog.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  await expect(dialog.getByTestId("world-summary")).toContainText("1.21.4 · 12 MB · uploaded");
  await replace.click();
  await expect(dialog).toHaveCount(0);
  expect(Object.fromEntries(sent!)).toMatchObject({ world: "true", replace: "true", library: "w1" });
  expect(sent!.get("newerWorld")).toBeNull();
});

test("a stopped server's world replaced with one from the library from a newer game is asked about first", async ({ page, as }) => {
  await as("owner");
  await library(page, [world("w1", "Island", { version: "26.3" })]);
  let sent: URLSearchParams | null = null;
  await page.route("**/api/v1/orgs/friends/servers/creative/import?*", async (route) => {
    sent = new URL(route.request().url()).searchParams;
    await route.fulfill({ status: 202, json: await (await page.request.get("/api/v1/orgs/friends/servers/creative")).json() });
  });
  await page.goto("/o/friends/s/creative/settings");
  await page.getByRole("button", { name: "Replace the world" }).click();
  const dialog = page.getByRole("dialog", { name: "Replace the world?" });
  await dialog.getByRole("button", { name: "Library world", exact: true }).click();
  await dialog.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  await expect(dialog.getByText("The world was last opened in 26.3. 1.21.4 cannot open the world, so the server will not start.")).toBeVisible();
  await dialog.getByRole("button", { name: "Replace the world" }).click();
  const ask = page.getByRole("dialog", { name: "Open a 26.3 world in 1.21.4?" });
  await ask.getByRole("button", { name: "Cancel" }).click();
  await expect(ask).toHaveCount(0);
  expect(sent).toBeNull();
  await dialog.getByRole("button", { name: "Replace the world" }).click();
  await ask.getByRole("button", { name: "Replace the world anyway" }).click();
  await expect(dialog).toHaveCount(0);
  expect(Object.fromEntries(sent!)).toMatchObject({ world: "true", replace: "true", library: "w1", newerWorld: "true" });
});

test("a server's world is saved to the library from its Backups tab, named after it unless named otherwise", async ({ page, as }) => {
  await as("operator");
  const calls = await library(page, []);
  await page.goto("/o/friends/s/survival/backups");
  // Making templates is admins', saving worlds operators' too.
  await expect(page.getByRole("button", { name: "Save as template" })).toHaveCount(0);
  await page.getByRole("button", { name: "Save world" }).click();
  const dialog = page.getByRole("dialog", { name: "Save world to library" });
  await expect(dialog).toContainText("A snapshot of the server is taken first");
  await expect(dialog.getByLabel("Name")).toHaveAttribute("placeholder", "Survival");
  await dialog.getByRole("button", { name: "Save", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByText("Saving Survival to Worlds")).toBeVisible();
  expect(calls[0]).toMatchObject({ method: "POST", path: "/worlds", body: { name: "Survival", description: "", server: "survival" } });
});

test("a snapshot's world is saved to the library from its details", async ({ page, as }) => {
  await as("owner");
  const calls = await library(page, []);
  await page.goto("/o/friends/s/survival/backups");
  await page.getByRole("button", { name: /^About the snapshot of survival/ }).first().click();
  await page.getByRole("dialog").getByRole("button", { name: "Save world" }).click();
  const dialog = page.getByRole("dialog", { name: "Save world to library" });
  await dialog.getByLabel("Name").fill("Before the war");
  await dialog.getByRole("button", { name: "Save", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  expect(calls[0]).toMatchObject({ method: "POST", path: "/worlds", body: { name: "Before the war", snapshot: expect.any(String) } });
});

test("an operator saves a snapshot's world from its details, which offer nothing else of an admin's", async ({ page, as }) => {
  await as("operator");
  const calls = await library(page, []);
  for (const at of ["/o/friends/s/survival/backups", "/o/friends/backups"]) {
    await page.goto(at);
    // The organization's, one list of them all.
    if (at === "/o/friends/backups") await page.getByRole("group", { name: "Arrange by" }).getByRole("button", { name: "By time" }).click();
    await page.getByRole("button", { name: /^About the snapshot of survival/ }).first().click();
    const details = page.getByRole("dialog");
    for (const admins of ["New server", "Save as template", "Delete", "Restore"]) await expect(details.getByRole("button", { name: admins, exact: true })).toHaveCount(0);
    await details.getByRole("button", { name: "Save world" }).click();
    const dialog = page.getByRole("dialog", { name: "Save world to library" });
    await dialog.getByRole("button", { name: "Save", exact: true }).click();
    await expect(dialog).toHaveCount(0);
  }
  expect(calls.map((c) => (c.body as { snapshot?: string }).snapshot)).toEqual([expect.any(String), expect.any(String)]);
});

test("a viewer has no world to save", async ({ page, as }) => {
  await as("viewer");
  await page.goto("/o/friends/s/survival/backups");
  await expect(page.getByText(/snapshots?, the newest/)).toBeVisible();
  await expect(page.getByRole("button", { name: "Save world" })).toHaveCount(0);
});

test("a new server asked for with a world that cannot be used yet says so, rather than making a new one", async ({ page, as }) => {
  await as("owner");
  await library(page, [world("w1", "Island", { ready: false, saving: true }), world("w2", "Nothing", { ready: false, error: "The world is not among the server's files." })]);
  await page.goto("/o/friends/new?world=w1");
  await expect(page.getByText("Island is still being saved. This page continues once the world is ready.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Create server" })).toHaveCount(0);
  await page.goto("/o/friends/new?world=w2");
  await expect(page.getByText("Nothing was not saved: The world is not among the server's files.")).toBeVisible();
  await page.goto("/o/friends/new?world=gone");
  await expect(page.getByText("There is no such world in the library.")).toBeVisible();
});

test("several worlds are selected and deleted together, by an admin", async ({ page, as }) => {
  await as("admin");
  const calls = await library(page, [world("w1", "Island"), world("w2", "Arena"), world("w3", "Lobby")]);
  await page.goto("/o/friends/worlds");
  await page.getByLabel("Select every world").click();
  const bar = page.getByRole("region", { name: "Selection" });
  await expect(bar).toContainText("3 worlds selected");
  await page.getByLabel("Select Lobby").click();
  await bar.getByRole("button", { name: "Delete", exact: true }).click();
  await page.getByRole("button", { name: "Delete 2 worlds" }).click();
  await expect(page.getByText("Deleted 2 worlds")).toBeVisible();
  expect(calls.filter((c) => c.method === "DELETE").map((c) => c.path).sort()).toEqual(["/worlds/w1", "/worlds/w2"]);
  await expect(page.getByRole("listitem", { name: "Lobby" })).toBeVisible();
});

test("an operator has no worlds to select", async ({ page, as }) => {
  await as("operator");
  await library(page, [world("w1", "Island"), world("w2", "Arena")]);
  await page.goto("/o/friends/worlds");
  await expect(page.getByRole("listitem", { name: "Island" })).toBeVisible();
  await expect(page.getByLabel(/^Select /)).toHaveCount(0);
});

test("a world's picture that comes late does not land on what was picked since", async ({ page, as }) => {
  await as("owner");
  await library(page, [world("w1", "Island"), world("w2", "Arena")]);
  const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
  // Held until the test says, and answered with Island's picture.
  let release = () => {};
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  let asked = false;
  await page.route("**/api/v1/orgs/friends/worlds/w1/icon*", async (route) => {
    asked = true;
    await held;
    await route.fulfill({ status: 200, contentType: "image/png", body: png });
  });
  await page.route("**/api/v1/orgs/friends/worlds/w2/icon*", (route) => route.fulfill({ status: 404, json: { code: "not_found", message: "None." } }));
  await page.goto("/o/friends/new");
  const picture = page.getByRole("button", { name: "Replace" });
  const choices = page.getByRole("group", { name: "World", exact: true });
  await choices.getByRole("button", { name: "Library world" }).click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  await expect.poll(() => asked).toBe(true);
  // Arena, which has no picture, is picked while Island's is on its way.
  await page.getByRole("button", { name: "Change" }).last().click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Arena/ }).click();
  await expect(page.getByText("Arena").first()).toBeVisible();
  const landed = page.waitForResponse("**/worlds/w1/icon*");
  release();
  await landed;
  await page.waitForTimeout(200);
  await expect(picture).toHaveCount(0);
});

test("a world's picture opens larger when clicked, for an operator to change or remove and for a viewer to see", async ({ page, as }) => {
  await as("operator");
  const calls = await library(page, [world("w1", "Island")]);
  const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
  await page.route("**/api/v1/orgs/friends/worlds/w1/icon*", (route) => (route.request().method() === "GET" ? route.fulfill({ status: 200, contentType: "image/png", body: png }) : route.fallback()));
  await page.goto("/o/friends/worlds");
  const row = page.getByRole("listitem", { name: "Island" });
  await row.getByRole("button", { name: "Picture of Island" }).click();
  const dialog = page.getByRole("dialog", { name: "Island" });
  await expect(dialog.locator("img")).toHaveCSS("image-rendering", "pixelated");
  await dialog.getByLabel("Picture for Island").setInputFiles({ name: "new.png", mimeType: "image/png", buffer: png });
  await expect.poll(() => calls.map((c) => `${c.method} ${c.path}`)).toEqual(["PUT /worlds/w1/icon"]);
  await dialog.getByRole("button", { name: "Remove" }).click();
  await expect.poll(() => calls.map((c) => `${c.method} ${c.path}`)).toEqual(["PUT /worlds/w1/icon", "DELETE /worlds/w1/icon"]);
  await page.keyboard.press("Escape");

  // A viewer sees it, with nothing to change it with.
  await as("viewer");
  await page.reload();
  await row.getByRole("button", { name: "Picture of Island" }).click();
  const seen = page.getByRole("dialog", { name: "Island" });
  await expect(seen.locator("img")).toBeVisible();
  await expect(seen.getByRole("button", { name: /Replace|Upload|Remove/ })).toHaveCount(0);
});

test("a world's picture goes when the world does, and comes back with it, whatever the server runs", async ({ page, as }) => {
  await as("owner");
  await library(page, [world("w1", "Island")]);
  const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
  await page.route("**/api/v1/orgs/friends/worlds/w1/icon*", (route) => route.fulfill({ status: 200, contentType: "image/png", body: png }));
  await page.goto("/o/friends/new");
  const picture = page.getByRole("button", { name: "Replace" });
  const type = page.getByRole("group", { name: "Server type" });
  await type.getByRole("button", { name: /^Paper/ }).click();
  await page.getByRole("group", { name: "World", exact: true }).getByRole("button", { name: "Library world" }).click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  await expect(picture).toBeVisible();
  // A modpack server starts with the world too.
  await page.getByRole("button", { name: "Modpack", exact: true }).click();
  await expect(picture).toBeVisible();
  const choices = page.getByRole("group", { name: "World", exact: true });
  await choices.getByRole("button", { name: "New world" }).click();
  await expect(picture).toHaveCount(0);
  await choices.getByRole("button", { name: "Library world" }).click();
  await expect(picture).toBeVisible();
  await page.getByRole("button", { name: "Game version", exact: true }).click();
  await expect(picture).toBeVisible();
});

test("a new server looks as the world it is made with does, unless given a picture of its own", async ({ page, as }) => {
  await as("owner");
  await library(page, [world("w1", "Island")]);
  const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
  // The library's world has a picture.
  await page.route("**/api/v1/orgs/friends/worlds/w1/icon*", (route) => route.fulfill({ status: 200, contentType: "image/png", body: png }));
  await page.goto("/o/friends/new");
  const picture = page.getByRole("button", { name: "Replace" });
  const choices = page.getByRole("group", { name: "World", exact: true });
  // A save of one's own, with its picture.
  await choices.getByRole("button", { name: "Upload world" }).click();
  await page.getByLabel("World archive").setInputFiles({ name: "save.zip", mimeType: "application/zip", buffer: zip({ "Save/level.dat": levelDat("1.21.4", "Save"), "Save/icon.png": png }) });
  await expect(page.getByTestId("world-summary")).toContainText("Last opened in 1.21.4");
  await expect(picture).toBeVisible();
  // A new world has none.
  await choices.getByRole("button", { name: "New world" }).click();
  await expect(picture).toHaveCount(0);
  // The library's, with its.
  await choices.getByRole("button", { name: "Library world" }).click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  await expect(picture).toBeVisible();
  // One chosen here stays, whatever world is picked.
  await page.getByRole("button", { name: "Remove", exact: true }).click();
  await expect(picture).toHaveCount(0);
  await page.getByRole("button", { name: "Change" }).last().click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  await expect(picture).toHaveCount(0);
});

// A resource pack uploaded to a world: required, and what players are asked.
const mapPack = { url: "", sha1: "a".repeat(40), enforce: true, prompt: "For the map", uploaded: { name: "map.zip", size: 3 << 20, time: new Date().toISOString() }, project: null };

test("a map uploaded with its resource pack beside it is added with the pack, unless switched off", async ({ page, as }) => {
  await as("operator");
  const calls = await library(page, []);
  await page.goto("/o/friends/worlds");
  for (const [use, files] of [
    [true, { "Island Save/level.dat": levelDat("1.21.4", "Island"), "Island Save/resources.zip": "PK pack" }],
    [false, { "Arena/level.dat": levelDat("1.21.4", "Arena"), "resources.zip": "PK pack" }],
  ] as const) {
    await page.getByRole("button", { name: "Add world" }).click();
    const dialog = page.getByRole("dialog", { name: "Add a world" });
    await dialog.getByLabel("World archive").setInputFiles({ name: "map.zip", mimeType: "application/zip", buffer: zip(files) });
    const usePack = dialog.getByRole("switch", { name: "Use the map's resource pack" });
    await expect(usePack).toHaveAttribute("aria-checked", "true");
    await expect(dialog).toContainText("resources.zip · 7 B");
    if (!use) await usePack.click();
    await dialog.getByLabel("Name").fill(use ? "Island" : "Arena");
    await dialog.getByRole("button", { name: "Add world" }).click();
    await expect(dialog).toHaveCount(0);
  }
  expect(calls.map((c) => `${c.method} ${c.path} ${c.query.name ?? ""}`)).toEqual(["POST /worlds/upload Island", "PUT /worlds/w9/resource-pack resources.zip", "POST /worlds/upload Arena"]);
  // One with none has nothing to switch off.
  await page.getByRole("button", { name: "Add world" }).click();
  const dialog = page.getByRole("dialog", { name: "Add a world" });
  await dialog.getByLabel("World archive").setInputFiles({ name: "plain.zip", mimeType: "application/zip", buffer: zip({ "Plain/level.dat": levelDat("1.21.4", "Plain") }) });
  await expect(dialog.getByTestId("world-summary")).toContainText('World "Plain"');
  await expect(dialog.getByRole("switch", { name: "Use the map's resource pack" })).toHaveCount(0);
});

test("a world is given a resource pack from an address, required, and has it taken off again", async ({ page, as }) => {
  await as("operator");
  const calls = await library(page, [world("w1", "Island"), world("w2", "Arena", { resourcePack: mapPack })]);
  await page.goto("/o/friends/worlds");
  await expect(page.getByRole("listitem", { name: "Arena" })).toContainText("with a resource pack");
  await expect(page.getByRole("listitem", { name: "Island" })).not.toContainText("resource pack");
  await page.getByRole("button", { name: "Actions for Island" }).click();
  await page.getByRole("menuitem", { name: "Edit" }).click();
  let dialog = page.getByRole("dialog", { name: "Edit Island" });
  await expect(dialog.getByRole("switch", { name: "Required" })).toHaveCount(0);
  await dialog.getByRole("button", { name: "Add resource pack" }).click();
  const picker = page.getByRole("dialog");
  await picker.getByRole("button", { name: "From URL" }).click();
  await picker.getByLabel("Download URL").fill("https://example.com/map.zip");
  await picker.getByLabel("SHA-1").fill("ab".repeat(20));
  await picker.getByRole("button", { name: "Use this pack" }).click();
  dialog = page.getByRole("dialog", { name: "Edit Island" });
  const pack = dialog.getByRole("region", { name: "Resource pack" });
  await expect(pack).toContainText("map");
  await expect(pack).toContainText("https://example.com/map.zip");
  await dialog.getByRole("switch", { name: "Required" }).click();
  await dialog.getByLabel("Prompt").fill("For the map");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog).toHaveCount(0);
  expect(calls[0]).toMatchObject({ method: "PATCH", path: "/worlds/w1", body: { name: "Island", resourcePack: { url: "https://example.com/map.zip", sha1: "ab".repeat(20), enforce: true, prompt: "For the map" } } });

  // One it has, taken off.
  await page.getByRole("button", { name: "Actions for Arena" }).click();
  await page.getByRole("menuitem", { name: "Edit" }).click();
  dialog = page.getByRole("dialog", { name: "Edit Arena" });
  await expect(dialog.getByRole("region", { name: "Resource pack" })).toContainText("map");
  await expect(dialog.getByRole("switch", { name: "Required" })).toHaveAttribute("aria-checked", "true");
  await expect(dialog.getByLabel("Prompt")).toHaveValue("For the map");
  await dialog.getByRole("region", { name: "Resource pack" }).getByRole("button", { name: "Remove", exact: true }).click();
  await expect(dialog.getByRole("button", { name: "Add resource pack" })).toBeVisible();
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog).toHaveCount(0);
  expect(calls[1]).toMatchObject({ method: "PATCH", path: "/worlds/w2", body: { resourcePack: null } });
});

test("a world is given a resource pack file, uploaded once it is saved", async ({ page, as }) => {
  await as("operator");
  const calls = await library(page, [world("w1", "Island")]);
  await page.goto("/o/friends/worlds");
  await page.getByRole("button", { name: "Actions for Island" }).click();
  await page.getByRole("menuitem", { name: "Edit" }).click();
  let dialog = page.getByRole("dialog", { name: "Edit Island" });
  await dialog.getByRole("button", { name: "Add resource pack" }).click();
  const picker = page.getByRole("dialog");
  await picker.getByRole("button", { name: "Upload", exact: true }).click();
  await picker.getByLabel("Resource pack file").setInputFiles({ name: "island.zip", mimeType: "application/zip", buffer: Buffer.from("PK zip") });
  dialog = page.getByRole("dialog", { name: "Edit Island" });
  await expect(dialog.getByRole("region", { name: "Resource pack" })).toContainText("island");
  // Nothing is sent until it is saved.
  expect(calls).toEqual([]);
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog).toHaveCount(0);
  expect(calls.map((c) => `${c.method} ${c.path} ${c.query.name ?? ""}`.trim())).toEqual(["PUT /worlds/w1/resource-pack island.zip", "PATCH /worlds/w1"]);
  expect(calls[1].body).toMatchObject({ resourcePack: { enforce: false, prompt: "" } });
});

/** What is asked of a server being made, by what and how. One given a resource pack has the one given. */
async function watchServer(page: Page, slug: string, name: string, pack?: Record<string, unknown>) {
  const asked: { method: string; path: string; query: Record<string, string> }[] = [];
  await page.route(`**/api/v1/orgs/${slug}/servers/${name}/**`, async (route) => {
    const req = route.request();
    if (req.method() === "GET") return route.fallback();
    const url = new URL(req.url());
    asked.push({ method: req.method(), path: url.pathname.split(`/servers/${name}`)[1], query: Object.fromEntries(url.searchParams) });
    const server = await (await page.request.get(`/api/v1/orgs/${slug}/servers/${name}`)).json();
    if (pack && url.pathname.endsWith("/resource-pack")) server.spec.resourcePack = pack;
    return route.fulfill({ json: server });
  });
  return asked;
}

test("a world's resource pack is in the new server form with it, and goes to the server", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await library(page, [world("w1", "Island", { resourcePack: mapPack }), world("w2", "Arena")], slug);
  const asked = await watchServer(page, slug, "island", { ...mapPack, url: `https://panel.example/packs/${mapPack.sha1}.zip` });
  await page.goto(`/o/${slug}/worlds`);
  await page.getByRole("listitem", { name: "Island" }).getByRole("button", { name: "New server" }).click();
  await expect(page.getByTestId("world-summary")).toContainText("with a resource pack");
  // What a server can be given shows once its type is picked.
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  const packs = page.getByRole("list", { name: "Packs, the top one first" });
  await expect(packs).toContainText("map");
  await expect(packs).toContainText("the world's");
  await expect(page.getByRole("switch", { name: "Required" })).toHaveAttribute("aria-checked", "true");
  await expect(page.getByLabel("Prompt")).toHaveValue("For the map");
  // Another world has none: Island's goes. Picked again, it comes back.
  await page.getByRole("button", { name: "Change" }).last().click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Arena/ }).click();
  await expect(packs).toHaveCount(0);
  await page.getByRole("button", { name: "Change" }).last().click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  await expect(packs).toContainText("map");
  // A new world has none either.
  await page.getByRole("group", { name: "World", exact: true }).getByRole("button", { name: "New world" }).click();
  await expect(packs).toHaveCount(0);
  await page.getByRole("group", { name: "World", exact: true }).getByRole("button", { name: "Library world" }).click();
  await expect(packs).toContainText("map");
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page.getByText("Island is set up")).toBeVisible();
  expect(asked).toEqual([
    { method: "PUT", path: "/import", query: expect.objectContaining({ world: "true", library: "w1" }) },
    { method: "PUT", path: "/resource-pack", query: { world: "w1", wait: "true" } },
    { method: "POST", path: "/start", query: {} },
  ]);
});

test("a world's resource pack taken off in the new server form is not given", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await library(page, [world("w1", "Island", { resourcePack: mapPack })], slug);
  const asked = await watchServer(page, slug, "island");
  await page.goto(`/o/${slug}/new?world=w1`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  await page.getByRole("button", { name: "Remove map" }).click();
  await expect(page.getByRole("list", { name: "Packs, the top one first" })).toHaveCount(0);
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page.getByText("Island is set up")).toBeVisible();
  expect(asked.map((a) => `${a.method} ${a.path}`)).toEqual(["PUT /import", "POST /start"]);
});

test("a template given a world from the library keeps the world's resource pack with it", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await library(page, [world("w1", "Island", { resourcePack: mapPack })], slug);
  const put: string[] = [];
  await page.route(new RegExp(`/api/v1/orgs/${slug}/templates/[^/]+/world(\\?.*)?$`), (route) => {
    const req = route.request();
    put.push(`${req.method()} ${new URL(req.url()).search}${req.method() === "PATCH" ? JSON.stringify(req.postDataJSON()) : ""}`);
    return route.fulfill({ json: {} });
  });
  await page.goto(`/o/${slug}/templates/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  await page.getByLabel("Name", { exact: true }).fill("Island base");
  await page.getByRole("group", { name: "World", exact: true }).getByRole("button", { name: "Library world" }).click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  await expect(page.getByRole("list", { name: "Packs, the top one first" })).toContainText("map");
  // Not required for this template's servers.
  await page.getByRole("switch", { name: "Required" }).click();
  await page.getByRole("button", { name: "Create template" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/templates$`));
  expect(put).toEqual(["PUT ?library=w1&pack=true", 'PATCH {"resourcePack":{"enforce":false,"prompt":"For the map"}}']);
});

test("a stopped server's world replaced with one from the library takes its resource pack, unless switched off", async ({ page, as }) => {
  await as("owner");
  await library(page, [world("w1", "Island", { resourcePack: mapPack })]);
  const sent: Record<string, string>[] = [];
  await page.route("**/api/v1/orgs/friends/servers/creative/import?*", async (route) => {
    sent.push(Object.fromEntries(new URL(route.request().url()).searchParams));
    await route.fulfill({ status: 202, json: await (await page.request.get("/api/v1/orgs/friends/servers/creative")).json() });
  });
  await page.goto("/o/friends/s/creative/settings");
  for (const use of [true, false]) {
    await page.getByRole("button", { name: "Replace the world" }).click();
    const dialog = page.getByRole("dialog", { name: "Replace the world?" });
    await dialog.getByRole("button", { name: "Library world", exact: true }).click();
    await dialog.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
    const usePack = dialog.getByRole("switch", { name: "Use the world's resource pack" });
    await expect(usePack).toHaveAttribute("aria-checked", "true");
    if (!use) await usePack.click();
    await dialog.getByRole("button", { name: "Replace the world" }).click();
    await expect(dialog).toHaveCount(0);
  }
  expect(sent[0]).toMatchObject({ library: "w1", replace: "true", pack: "true" });
  expect(sent[1]).toMatchObject({ library: "w1", replace: "true" });
  expect(sent[1].pack).toBeUndefined();
});

test("a world's resource pack the form has already is not added twice, and a template keeps the one pack it has", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  const fromUrl = { url: "https://example.com/map.zip", sha1: "ab".repeat(20), enforce: false, project: null, uploaded: null };
  await library(page, [world("w1", "Island", { resourcePack: fromUrl }), world("w2", "Arena", { resourcePack: mapPack })], slug);
  const addUrl = async () => {
    await page.getByRole("button", { name: "Add resource pack" }).click();
    const picker = page.getByRole("dialog");
    await picker.getByRole("button", { name: "From URL" }).click();
    await picker.getByLabel("Download URL").fill(fromUrl.url);
    await picker.getByLabel("SHA-1").fill(fromUrl.sha1);
    await picker.getByRole("button", { name: /Use this pack|Add/ }).last().click();
    await expect(picker).toHaveCount(0);
  };
  const packs = page.getByRole("list", { name: "Packs, the top one first" }).getByRole("listitem");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  await addUrl();
  await expect(packs).toHaveCount(1);
  // The same pack, with Island: once.
  await page.getByRole("group", { name: "World", exact: true }).getByRole("button", { name: "Library world" }).click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  await expect(page.getByTestId("world-summary")).toContainText("with a resource pack");
  await expect(packs).toHaveCount(1);
  // Another, with Arena: both.
  await page.getByRole("button", { name: "Change" }).last().click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Arena/ }).click();
  await expect(packs).toHaveCount(2);

  // A template has one pack: one it has stays, and the world's is not kept.
  const put: string[] = [];
  await page.route(new RegExp(`/api/v1/orgs/${slug}/templates/[^/]+/world(\\?.*)?$`), (route) => {
    put.push(`${route.request().method()} ${new URL(route.request().url()).search}`);
    return route.fulfill({ json: {} });
  });
  await page.goto(`/o/${slug}/templates/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  await page.getByLabel("Name", { exact: true }).fill("Arena base");
  await addUrl();
  await page.getByRole("group", { name: "World", exact: true }).getByRole("button", { name: "Library world" }).click();
  await page.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Arena/ }).click();
  await expect(page.getByTestId("world-summary")).toContainText("with a resource pack");
  await expect(packs).toHaveCount(1);
  await expect(packs).toContainText("map");
  await expect(packs).not.toContainText("the world's");
  await page.getByRole("button", { name: "Create template" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/templates$`));
  expect(put).toEqual(["PUT ?library=w2"]);
});

test("a world shows the datapacks that come with it, each with what it says of itself and its picture", async ({ page, as }) => {
  // The panel's own world, as it read it (see cmd/avsys-uitest/library.go).
  await as("viewer");
  await page.goto("/o/friends/worlds");
  const row = page.getByRole("list", { name: "Worlds" }).getByRole("listitem", { name: "Drehmal" });
  await expect(row).toContainText("1.20.1 · 1.5 GB · uploaded · 4 datapacks");
  await row.getByRole("button", { name: "About Drehmal" }).click();
  const dialog = page.getByRole("dialog", { name: "Drehmal" });
  await expect(dialog).toContainText("4 datapacks");
  const packs = dialog.getByRole("list", { name: "Datapacks" }).getByRole("listitem");
  // Bukkit's own is not one; the rest by name, each on one line.
  await expect(packs).toHaveText([/bern_trades\s*Villager trades of Bern/, /drehmal_core\s*Quests, bosses and lore of Drehmal/, /hi_drehmal\s*Hi, Drehmal/, /mael\s*Mael's music discs/]);
  // Those with a picture show it; the rest a mark of their own.
  for (const name of ["drehmal_core", "mael"]) {
    const img = packs.filter({ hasText: name }).locator("img");
    await expect(img).toHaveAttribute("src", /\/datapacks\/icon\?name=/);
    await expect.poll(() => img.evaluate((i: HTMLImageElement) => i.complete && i.naturalWidth)).toBe(64);
  }
  for (const name of ["bern_trades", "hi_drehmal"]) await expect(packs.filter({ hasText: name }).locator("img")).toHaveCount(0);
  // Read-only: nothing to change them with.
  await expect(dialog.getByRole("button", { name: /Remove|Add/ })).toHaveCount(0);
});

test("a world without datapacks says so, and one whose are being read says that", async ({ page, as }) => {
  await as("viewer");
  const worlds = [world("w1", "Island", { datapacks: 0 }), world("w2", "Arena", { readingDatapacks: true })];
  await library(page, worlds);
  await page.goto("/o/friends/worlds");
  const list = page.getByRole("list", { name: "Worlds" });
  await expect(list.getByRole("listitem", { name: "Island" })).not.toContainText("datapack");
  await list.getByRole("button", { name: "About Island" }).click();
  await expect(page.getByRole("dialog", { name: "Island" }).getByRole("region", { name: "Datapacks" })).toContainText("None.");
  await page.keyboard.press("Escape");
  await list.getByRole("button", { name: "About Arena" }).click();
  const dialog = page.getByRole("dialog", { name: "Arena" });
  await expect(dialog.getByRole("region", { name: "Datapacks" })).toContainText("Reading");
  // Read meanwhile: the page asks again, and shows them.
  await page.route(/\/api\/v1\/orgs\/friends\/worlds\/w2\/datapacks$/, (route) => route.fulfill({ json: [{ name: "arena.zip", title: "arena", about: "Rounds and resets" }] }));
  worlds[1] = world("w2", "Arena", { datapacks: 1 });
  await expect(dialog.getByRole("list", { name: "Datapacks" }).getByRole("listitem")).toHaveText([/arena\s*Rounds and resets/]);
  await page.keyboard.press("Escape");
  await expect(list.getByRole("listitem", { name: "Arena" })).toContainText("1 datapack");
});
