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
  await dialog.getByLabel("Description").fill("Where we started");
  await dialog.getByRole("button", { name: "Add world" }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole("listitem", { name: "Island" })).toBeVisible();
  expect(calls[0]).toMatchObject({ method: "POST", path: "/worlds/upload", query: { name: "Island", description: "Where we started", root: "Island Save", version: "1.21.4", file: "island.zip", unpacked: expect.stringMatching(/^\d+$/) } });
  expect(calls[1]).toMatchObject({ method: "PUT", path: "/worlds/w9/icon" });
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
  await dialog.getByLabel("Description").fill("Smaller now");
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
  await expect(page.getByText("This world was last opened in 1.21.5, which is newer than 1.21.4.")).toBeVisible();
  await page.getByRole("button", { name: "Change" }).last().click();
  await expect(page.getByRole("searchbox", { name: "Search worlds" })).toBeVisible();
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
  await dialog.getByRole("button", { name: "Library", exact: true }).click();
  const replace = dialog.getByRole("button", { name: "Replace the world" });
  await expect(replace).toBeDisabled();
  await dialog.getByRole("list", { name: "Worlds" }).getByRole("button", { name: /Island/ }).click();
  await expect(dialog.getByTestId("world-summary")).toContainText("1.21.4 · 12 MB · uploaded");
  await replace.click();
  await expect(dialog).toHaveCount(0);
  expect(Object.fromEntries(sent!)).toMatchObject({ world: "true", replace: "true", library: "w1" });
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
