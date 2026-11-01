import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// Templates are saved from servers and made into new ones. Saving takes a
// snapshot, which needs a server's files: here the panel's answers stand in.

const version = (id: string, time: string, by = "owner") => ({ id, time, server: "template-t1", trigger: "template", by, version: "1.21.4", loader: "paper", size: 300 << 20, added: 1 << 20, files: 600 });

/** The template the panel would have, answered on every page. */
async function lobby(page: Page, versions = [version("b".repeat(64), new Date().toISOString()), version("a".repeat(64), new Date(Date.now() - 86_400_000).toISOString())]) {
  const survival = await (await page.request.get("/api/v1/orgs/friends/servers/survival")).json();
  const template = { id: "t1", name: "Lobby", description: "Where everyone starts.", createdBy: "owner", createdAt: versions.at(-1)!.time, updatedAt: versions[0].time, current: versions[0], versions };
  const calls: { method: string; path: string; body: unknown }[] = [];
  await page.route(/\/api\/v1\/orgs\/friends\/templates(\/.*)?(\?.*)?$/, async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname.replace("/api/v1/orgs/friends", "");
    if (req.method() !== "GET") {
      calls.push({ method: req.method(), path, body: req.postDataJSON?.() ?? null });
      return route.fulfill({ status: req.method() === "DELETE" && path === "/templates/t1" ? 204 : 200, json: template });
    }
    if (path === "/templates") return route.fulfill({ json: [template] });
    return route.fulfill({ json: { ...template, spec: survival.spec } });
  });
  return { template, calls };
}

test("a server is saved as a template from its Backups tab", async ({ page, as }) => {
  await as("owner");
  let sent: unknown = null;
  await page.route("**/api/v1/orgs/friends/templates", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    sent = route.request().postDataJSON();
    return route.fulfill({ status: 202, json: { id: "t1", name: "Lobby", description: "", versions: [], current: null } });
  });
  await page.goto("/o/friends/s/survival/backups");
  await page.getByRole("button", { name: "Save as template" }).click();
  const dialog = page.getByRole("dialog", { name: "Save as template" });
  // Named after the server unless named otherwise.
  await expect(dialog.getByLabel("Name")).toHaveAttribute("placeholder", "Survival");
  await dialog.getByLabel("Name").fill("Lobby");
  await dialog.getByLabel("Description").fill("Where everyone starts.");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog).toHaveCount(0);
  expect(sent).toEqual({ name: "Lobby", description: "Where everyone starts.", server: "survival" });
});

test("templates are listed, and a new server is made from one with the form filled in from it", async ({ page, as }) => {
  await as("owner");
  const { template } = await lobby(page);
  let made: Record<string, unknown> | null = null;
  await page.route("**/api/v1/orgs/friends/servers", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    made = route.request().postDataJSON();
    return route.fulfill({ status: 202, json: await (await page.request.get("/api/v1/orgs/friends/servers/survival")).json() });
  });
  await page.goto("/o/friends");
  await page.getByRole("link", { name: "Templates" }).click();
  const row = page.getByRole("listitem", { name: "Lobby" });
  await expect(row).toContainText("Where everyone starts.");
  await expect(row).toContainText("Paper 1.21.4 · 300 MB");
  await expect(row).toContainText("2 versions");
  await row.getByRole("button", { name: "New server" }).click();
  await expect(page.getByRole("heading", { name: "New server from Lobby" })).toBeVisible();
  await expect(page.getByText("template", { exact: true })).toBeVisible();
  // Named after the template, and given an address of its own.
  await expect(page.getByLabel("Display name")).toHaveValue("Lobby");
  await page.getByRole("button", { name: "Create server" }).click();
  const open = page.getByRole("button", { name: "Create without a whitelist" });
  await expect.poll(async () => made !== null || (await open.isVisible())).toBe(true);
  if (made === null) await open.click();
  await expect.poll(() => made?.fromTemplate).toBe(template.id);
  // There is a server named lobby already: a name of its own.
  expect(made!.name).toBe("lobby-2");
  expect(made!.fromSnapshot).toBeUndefined();
});

test("a template is edited, updated from a server, gone back to an earlier version of, and deleted", async ({ page, as }) => {
  await as("owner");
  const { template, calls } = await lobby(page);
  await page.goto("/o/friends/templates");
  const row = page.getByRole("listitem", { name: "Lobby" });
  const menu = async (item: string) => {
    await row.getByRole("button", { name: "Actions for Lobby" }).click();
    await page.getByRole("menuitem", { name: item }).click();
  };
  // Edited in the same form as a server, filled in as servers are made from it.
  await menu("Edit");
  await expect(page).toHaveURL(/\/templates\/t1\/edit$/);
  await expect(page.getByRole("heading", { name: "Edit Lobby" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Change", exact: true })).toHaveCount(0);
  await page.getByLabel("Name", { exact: true }).fill("Hub");
  await page.getByLabel("Memory (GB)").fill("6");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(/\/o\/friends\/templates$/);
  expect(calls.at(-1)).toMatchObject({ method: "PATCH", path: "/templates/t1", body: { name: "Hub", description: "Where everyone starts.", changes: { resources: { memory: "6144Mi" } } } });

  await menu("New version");
  let dialog = page.getByRole("dialog", { name: "Update Lobby" });
  await dialog.getByRole("list", { name: "Servers" }).getByRole("button", { name: /creative/ }).click();
  await expect(dialog).toContainText("becomes the newest version of Lobby");
  await dialog.getByRole("button", { name: "Update" }).click();
  await expect(dialog).toHaveCount(0);
  expect(calls.at(-1)).toEqual({ method: "POST", path: "/templates/t1/versions", body: { server: "creative" } });

  await menu("Versions");
  dialog = page.getByRole("dialog", { name: "Versions of Lobby" });
  const rows = dialog.getByRole("listitem");
  await expect(rows).toHaveCount(2);
  await expect(rows.first()).toContainText("used");
  await rows.nth(1).getByRole("button", { name: "Use" }).click();
  await expect.poll(() => calls.at(-1)).toEqual({ method: "PATCH", path: "/templates/t1", body: { current: template.versions[1].id } });
  await rows.nth(1).getByRole("button", { name: /Delete the version/ }).click();
  await expect.poll(() => calls.at(-1)?.path).toBe(`/templates/t1/versions/${template.versions[1].id}`);
  await page.keyboard.press("Escape");

  await menu("Delete");
  dialog = page.getByRole("dialog", { name: "Delete Lobby?" });
  await expect(dialog).toContainText("Its 2 versions are deleted too.");
  await dialog.getByRole("button", { name: "Delete" }).click();
  await expect.poll(() => calls.at(-1)).toEqual({ method: "DELETE", path: "/templates/t1", body: null });
});

test("a viewer sees the templates, and nothing to change them with", async ({ page, as }) => {
  await as("viewer");
  await lobby(page);
  await page.goto("/o/friends/templates");
  const row = page.getByRole("listitem", { name: "Lobby" });
  await expect(row).toBeVisible();
  await expect(row.getByRole("button")).toHaveCount(0);
  await expect(page.getByRole("link", { name: "New template" })).toHaveCount(0);
});

test("with no templates the page says how to make one", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/templates");
  await expect(page.getByText("No templates yet")).toBeVisible();
  await expect(page.getByRole("link", { name: "New template" })).toBeVisible();
});

test("a new template starts from a server picked from the servers, each with its picture, then filled in from it", async ({ page, as }) => {
  await as("owner");
  const calls: unknown[] = [];
  await page.route(/\/api\/v1\/orgs\/friends\/templates$/, (route) => {
    if (route.request().method() !== "POST") return route.fulfill({ json: [] });
    calls.push(route.request().postDataJSON());
    return route.fulfill({ status: 202, json: {} });
  });
  const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAFklEQVR4nGP4z8DAwMDAxMDAwMDAAAANHQEDasKb6QAAAABJRU5ErkJggg==", "base64");
  await page.route("**/api/v1/orgs/friends/servers/survival/icon*", (route) => route.fulfill({ contentType: "image/png", body: png }));
  await page.goto("/o/friends/templates");
  await page.getByRole("link", { name: "New template" }).click();
  await page.getByRole("group", { name: "Start from" }).getByRole("button", { name: "Server", exact: true }).click();
  await expect(page).toHaveURL(/\/templates\/new\?start=server$/);
  const survival = page.getByRole("list", { name: "Servers" }).getByRole("button", { name: /survival/ });
  await expect(survival).toContainText("Paper 1.21.4");
  await expect(survival.locator('img[src*="/servers/survival/icon"]')).toHaveCount(1);
  await survival.click();
  // The form, filled in from it, to change before it is made.
  await expect(page).toHaveURL(/\/templates\/new\?from=server%3Asurvival$/);
  await expect(page.getByRole("heading", { name: "New template" })).toBeVisible();
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue("Survival");
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.4");
  await expect(page.getByText("LuckPerms", { exact: true })).toBeVisible();
  // What is each server's own is not asked.
  await expect(page.getByLabel("Short name")).toHaveCount(0);
  await expect(page.getByLabel("Hostname")).toHaveCount(0);
  // Its world kept, unless its servers are to start one of their own.
  await expect(page.getByRole("group", { name: "Its world" }).getByRole("button", { name: "Keep world" })).toHaveAttribute("aria-pressed", "true");
  await page.getByLabel("Difficulty").selectOption("peaceful");
  await page.getByRole("button", { name: "Create template" }).click();
  await expect(page).toHaveURL(/\/o\/friends\/templates$/);
  expect(calls).toHaveLength(1);
  expect(calls[0]).toMatchObject({ name: "Survival", description: "", server: "survival", changes: { properties: { difficulty: "peaceful" } } });
  // What was left alone is not named, so that it comes from the server.
  expect((calls[0] as { changes: object }).changes).not.toHaveProperty("mods");
  // And the list asked for again: the starting point changes back.
  await page.goto("/o/friends/templates/new?from=server%3Asurvival");
  await page.getByRole("button", { name: "Change", exact: true }).click();
  await expect(page).toHaveURL(/\/templates\/new\?start=server$/);
});

test("the form from a template shows the version used now, after another was picked", async ({ page, as }) => {
  await as("owner");
  const survival = await (await page.request.get("/api/v1/orgs/friends/servers/survival")).json();
  const newer = version("b".repeat(64), new Date().toISOString());
  const older = { ...version("a".repeat(64), new Date(Date.now() - 86_400_000).toISOString()), version: "1.21.1" };
  let used = newer;
  const template = () => ({ id: "t1", name: "Lobby", description: "", createdBy: "owner", createdAt: older.time, updatedAt: newer.time, current: used, versions: [newer, older] });
  await page.route(/\/api\/v1\/orgs\/friends\/templates(\/.*)?$/, async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname;
    if (req.method() === "PATCH") {
      used = older;
      return route.fulfill({ json: template() });
    }
    if (path.endsWith("/templates")) return route.fulfill({ json: [template()] });
    return route.fulfill({ json: { ...template(), spec: { ...survival.spec, version: used.version } } });
  });
  await page.goto("/o/friends/templates");
  const row = page.getByRole("listitem", { name: "Lobby" });
  await row.getByRole("button", { name: "New server" }).click();
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.4");
  await page.goBack();
  await row.getByRole("button", { name: "Actions for Lobby" }).click();
  await page.getByRole("menuitem", { name: "Versions" }).click();
  await page.getByRole("dialog").getByRole("listitem").nth(1).getByRole("button", { name: "Use" }).click();
  await expect(page.getByRole("dialog").getByRole("listitem").nth(1)).toContainText("used");
  await page.keyboard.press("Escape");
  await row.getByRole("button", { name: "New server" }).click();
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.1");
});

test("a template saved while the event stream was away is shown once it is back", async ({ page, as }) => {
  await as("owner");
  let saved = false;
  const template = () => ({ id: "t1", name: "Lobby", description: "", createdBy: "owner", createdAt: new Date().toISOString(), updatedAt: new Date().toISOString(), current: saved ? version("b".repeat(64), new Date().toISOString()) : null, versions: saved ? [version("b".repeat(64), new Date().toISOString())] : [] });
  await page.route("**/api/v1/orgs/friends/templates", (route) => route.fulfill({ json: [template()] }));
  // Each time the stream is opened it ends at once, and is opened again.
  await page.route("**/api/v1/events?*", (route) => route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body: "event: waiting\ndata: null\n\n" }));
  await page.goto("/o/friends/templates");
  const row = page.getByRole("listitem", { name: "Lobby" });
  await expect(row).toContainText("Saving");
  await expect(row.getByRole("button", { name: "New server" })).toBeDisabled();
  // Its end was never heard.
  saved = true;
  await expect(row).toContainText("Paper 1.21.4", { timeout: 15_000 });
  await expect(row.getByRole("button", { name: "New server" })).toBeEnabled();
});

test("a template is made from settings alone, and a new server made from it has them", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/templates`);
  await page.getByRole("link", { name: "New template" }).click();
  await expect(page.getByRole("heading", { name: "New template" })).toBeVisible();
  // Nothing that is a server's own: its files, its name in addresses, its address, its node, the EULA.
  // A world it can have, kept with it, as a new server can.
  await expect(page.getByRole("button", { name: "Import" })).toHaveCount(0);
  await expect(page.getByRole("group", { name: "Its world" }).getByRole("button", { name: "Upload world" })).toBeVisible();
  for (const gone of ["Short name", "Hostname"]) await expect(page.getByLabel(gone)).toHaveCount(0);
  await expect(page.getByRole("checkbox", { name: /End User License Agreement/ })).toHaveCount(0);

  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  await page.getByLabel("Name", { exact: true }).fill("Survival base");
  await page.getByLabel("Description").fill("Hard, with more memory.");
  await page.getByLabel("Difficulty").selectOption("hard");
  await page.getByLabel("Memory (GB)").fill("6");
  await page.getByRole("button", { name: "Create template" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/templates$`));
  const row = page.getByRole("listitem", { name: "Survival base" });
  await expect(row).toContainText("Hard, with more memory.");
  await expect(row).toContainText("Paper 1.21.4 · settings only");

  await row.getByRole("button", { name: "New server" }).click();
  await expect(page.getByRole("heading", { name: "New server from Survival base" })).toBeVisible();
  // A new server, filled in with it, and every bit of it can be changed.
  await expect(page.getByLabel("Display name")).toHaveValue("Survival base");
  await expect(page.getByLabel("Short name")).toHaveValue("survival-base");
  await expect(page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.4");
  await expect(page.getByLabel("Difficulty")).toHaveValue("hard");
  await expect(page.getByLabel("Memory (GB)")).toHaveValue("6");
  await page.getByLabel("Short name").fill("weekend");
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/weekend$`));
  const { spec } = await api("owner").get(`/orgs/${slug}/servers/weekend`);
  expect(spec).toMatchObject({ loader: "paper", version: "1.21.4", displayName: "Survival base", resources: { memory: "6Gi" }, eula: true });
  expect(spec.properties).toMatchObject({ difficulty: "hard" });
});

test("a template of settings alone is changed in the same form", async ({ page, as, org, api }) => {
  const slug = await org([]);
  const made = await api("owner").post(`/orgs/${slug}/templates`, {
    name: "Modded",
    description: "",
    spec: { loader: "fabric", version: "1.21.4", resources: { memory: "4Gi" }, storage: { size: "10Gi" }, properties: { pvp: "false" } },
  });
  await as("owner");
  await page.goto(`/o/${slug}/templates`);
  const row = page.getByRole("listitem", { name: "Modded" });
  await row.getByRole("button", { name: "Actions for Modded" }).click();
  // It has no versions to go back to.
  await expect(page.getByRole("menuitem", { name: "Versions" })).toHaveCount(0);
  await page.getByRole("menuitem", { name: "Edit" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/templates/${made.id}/edit$`));
  await expect(page.getByRole("heading", { name: "Edit Modded" })).toBeVisible();
  await expect(page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Fabric/ })).toHaveAttribute("aria-pressed", "true");
  await page.getByLabel("Name", { exact: true }).fill("Modded plus");
  await page.getByLabel("Memory (GB)").fill("8");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/templates$`));
  await expect(page.getByRole("listitem", { name: "Modded plus" })).toBeVisible();
  const now = await api("owner").get(`/orgs/${slug}/templates/${made.id}`);
  expect(now).toMatchObject({ name: "Modded plus", settingsOnly: true, spec: { loader: "fabric", resources: { memory: "8Gi" }, properties: { pvp: "false" } } });
});

test("the new-server form starts from a template picked in it", async ({ page, as }) => {
  await as("owner");
  await lobby(page);
  await page.goto("/o/friends/new");
  const start = page.getByRole("group", { name: "Start from" });
  await start.getByRole("button", { name: "Template", exact: true }).click();
  const list = page.getByRole("list", { name: "Templates" });
  await expect(list).toContainText("Paper 1.21.4 · 300 MB");
  await page.getByLabel("Search templates").fill("lobby");
  await expect(list.getByRole("button")).toHaveCount(1);
  // The rest is filled in from the one picked.
  await expect(page.getByLabel("Display name")).toBeHidden();
  await list.getByRole("button", { name: /Lobby/ }).click();
  await expect(page.getByRole("heading", { name: "New server from Lobby" })).toBeVisible();
  await expect(page.getByLabel("Display name")).toHaveValue("Lobby");
  // Back to the form as it was, and on to another way to start.
  await page.goBack();
  await start.getByRole("button", { name: "Template", exact: true }).click();
  await start.getByRole("button", { name: "New", exact: true }).click();
  await expect(page.getByRole("list", { name: "Templates" })).toHaveCount(0);
  await expect(page.getByLabel("Display name")).toBeVisible();
});

test("the new-server form says where templates are made when there are none", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("group", { name: "Start from" }).getByRole("button", { name: "Template", exact: true }).click();
  const none = page.getByText("No templates yet.");
  await expect(none).toBeVisible();
  await none.getByRole("link", { name: "Templates" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/templates$`));
});

test("a pack joined from several shows each of them in the form, the top one first", async ({ page, as }) => {
  await as("owner");
  const survival = await (await page.request.get("/api/v1/orgs/friends/servers/survival")).json();
  const v = version("b".repeat(64), new Date().toISOString());
  const pack = (n: number, title: string) => ({ sha1: String(n).repeat(40), size: 1 << 20, name: `${title}.zip`, project: { source: "modrinth", id: title.toLowerCase().replace(/ /g, "-"), version: "v", versionName: `${title} 1.0`, title } });
  const template = { id: "t1", name: "Lobby", description: "", createdBy: "owner", createdAt: v.time, updatedAt: v.time, current: v, versions: [v], settingsOnly: false };
  const resourcePack = { url: "http://panel.test/packs/joined.zip", sha1: "9".repeat(40), uploaded: { size: 3 << 20, time: v.time }, stack: [pack(1, "Colorful Grasses"), pack(2, "Dark Mode"), pack(3, "Fresh Animations")] };
  await page.route(/\/api\/v1\/orgs\/friends\/templates(\/.*)?$/, (route) =>
    route.fulfill({ json: new URL(route.request().url()).pathname.endsWith("/templates") ? [template] : { ...template, spec: { ...survival.spec, resourcePack } } }),
  );
  await page.goto("/o/friends/new?from=template:t1");
  const packs = page.getByRole("list", { name: "Packs, the top one first" }).getByRole("listitem");
  await expect(packs).toHaveCount(3);
  await expect(packs.nth(0)).toContainText("Colorful Grasses");
  await expect(packs.nth(0)).toContainText("Colorful Grasses 1.0");
  await expect(packs.nth(2)).toContainText("Fresh Animations");
  await expect(page.getByText("Joined into one pack.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Move Dark Mode up" })).toBeEnabled();
});

test("a new template starts from a snapshot, its server picked first", async ({ page, as }) => {
  await as("owner");
  const sent: unknown[] = [];
  await page.route(/\/api\/v1\/orgs\/friends\/templates$/, (route) => {
    if (route.request().method() !== "POST") return route.fulfill({ json: [] });
    sent.push(route.request().postDataJSON());
    return route.fulfill({ status: 201, json: {} });
  });
  await page.goto("/o/friends/templates/new?start=snapshot");
  await page.getByRole("list", { name: "Servers with snapshots" }).getByRole("button", { name: /Survival/ }).click();
  const snapshots = page.getByRole("list", { name: "Snapshots" }).getByRole("button");
  await expect(snapshots).toHaveCount(4);
  await snapshots.nth(1).click();
  const list = await (await page.request.get("/api/v1/orgs/friends/snapshots")).json();
  const survival = list.snapshots.filter((s: { server: string }) => s.server === "survival");
  await expect(page).toHaveURL(new RegExp(`/templates/new\\?from=snapshot%3A${survival[1].id}$`));
  await expect(page.getByText(/^Snapshot from/)).toBeVisible();
  await page.getByLabel("Name", { exact: true }).fill("Before the war");
  await page.getByRole("button", { name: "Create template" }).click();
  await expect(page).toHaveURL(/\/o\/friends\/templates$/);
  expect(sent).toHaveLength(1);
  expect(sent[0]).toMatchObject({ name: "Before the war", description: "", snapshot: survival[1].id });
});

test("a snapshot is saved as a template from its details", async ({ page, as }) => {
  await as("owner");
  const sent: unknown[] = [];
  await page.route(/\/api\/v1\/orgs\/friends\/templates$/, (route) => {
    if (route.request().method() !== "POST") return route.fulfill({ json: [] });
    sent.push(route.request().postDataJSON());
    return route.fulfill({ status: 201, json: {} });
  });
  await page.goto("/o/friends/s/survival/backups");
  await page.getByRole("button", { name: /^About the snapshot of survival/ }).first().click();
  await page.getByRole("dialog").getByRole("button", { name: "Save as template" }).click();
  const dialog = page.getByRole("dialog", { name: "Save as template" });
  // The snapshot is said, and not picked again.
  await expect(dialog).toContainText("Survival, 3 hours ago");
  await expect(dialog.getByRole("group", { name: "Save from" })).toHaveCount(0);
  await dialog.getByLabel("Name").fill("Before the war");
  await dialog.getByRole("button", { name: "Save", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  expect(sent).toEqual([{ name: "Before the war", description: "", snapshot: expect.any(String) }]);
});

test("a template is duplicated in the form, filled in from it: one saved from a server with its files, one of settings with them", async ({ page, as }) => {
  await as("owner");
  const survival = await (await page.request.get("/api/v1/orgs/friends/servers/survival")).json();
  const v = version("b".repeat(64), new Date().toISOString());
  const saved = { id: "t1", name: "Lobby", description: "Where everyone starts.", createdBy: "owner", createdAt: v.time, updatedAt: v.time, current: v, versions: [v], settingsOnly: false, extraFlags: "-Xss1m" };
  // With what the form has no place for, kept as it is.
  const spec = {
    loader: "paper",
    version: "1.21.4",
    loaderVersion: "120",
    resources: { memory: "4Gi", cpu: "2" },
    storage: { size: "10Gi", storageClassName: "fast" },
    opMembers: { also: ["u1"] },
    whitelistMembers: { except: ["u2"] },
  };
  const settings = { id: "t2", name: "Skyblock", description: "", createdBy: "owner", createdAt: v.time, updatedAt: v.time, current: null, versions: [], settingsOnly: true, spec };
  const sent: unknown[] = [];
  await page.route(/\/api\/v1\/orgs\/friends\/templates(\/t[12])?$/, (route) => {
    const path = new URL(route.request().url()).pathname;
    if (route.request().method() === "POST") {
      sent.push(route.request().postDataJSON());
      return route.fulfill({ status: 201, json: {} });
    }
    if (path.endsWith("/t1")) return route.fulfill({ json: { ...saved, spec: { ...survival.spec, displayName: "Lobby" } } });
    if (path.endsWith("/t2")) return route.fulfill({ json: settings });
    return route.fulfill({ json: [saved, settings] });
  });
  await page.goto("/o/friends/templates");
  const duplicate = async (name: string) => {
    await page.getByRole("button", { name: `Actions for ${name}` }).click();
    await page.getByRole("menuitem", { name: "Duplicate" }).click();
    await expect(page.getByRole("heading", { name: "New template" })).toBeVisible();
  };
  await duplicate("Lobby");
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue("Lobby copy");
  await expect(page.getByText("LuckPerms", { exact: true })).toBeVisible();
  // The flags it was given are shown, to change or take off.
  await page.getByRole("button", { name: /Launch flags/ }).click();
  await expect(page.getByLabel("Extra JVM flags")).toHaveValue("-Xss1m");
  await page.getByLabel("Memory (GB)").fill("8");
  await page.getByRole("button", { name: "Create template" }).click();
  await expect(page).toHaveURL(/\/o\/friends\/templates$/);
  await duplicate("Skyblock");
  await expect(page.getByLabel("Name", { exact: true })).toHaveValue("Skyblock copy");
  await page.getByLabel("Name", { exact: true }).fill("Skyblock hard");
  await page.getByRole("button", { name: "Create template" }).click();
  await expect(page).toHaveURL(/\/o\/friends\/templates$/);
  expect(sent).toHaveLength(2);
  expect(sent[0]).toMatchObject({ name: "Lobby copy", template: "t1", changes: { resources: { memory: "8192Mi" } } });
  expect(sent[1]).toMatchObject({ name: "Skyblock hard", spec: { loader: "paper", version: "1.21.4", loaderVersion: "120", resources: { cpu: "2" }, storage: { storageClassName: "fast" }, opMembers: { also: ["u1"] }, whitelistMembers: { except: ["u2"] } } });
  expect(sent[1]).not.toHaveProperty("template");
});

test("a template says whether its servers keep its world or start a new one, and a server made from it can say otherwise", async ({ page, as }) => {
  await as("owner");
  const sent: unknown[] = [];
  await page.route(/\/api\/v1\/orgs\/friends\/templates$/, (route) => {
    if (route.request().method() !== "POST") return route.fulfill({ json: [] });
    sent.push(route.request().postDataJSON());
    return route.fulfill({ status: 202, json: {} });
  });
  await page.goto("/o/friends/templates/new?from=server%3Asurvival");
  const worldChoice = page.getByRole("group", { name: "Its world" });
  await expect(worldChoice.getByRole("button", { name: "Keep world" })).toHaveAttribute("aria-pressed", "true");
  await worldChoice.getByRole("button", { name: "New world" }).click();
  await page.getByRole("button", { name: "Create template" }).click();
  await expect(page).toHaveURL(/\/o\/friends\/templates$/);
  expect(sent[0]).toMatchObject({ server: "survival", changes: { world: "new" } });

  // A server made from one whose servers start a new world starts one, unless it keeps the template's.
  const { template } = await lobby(page);
  await page.route("**/api/v1/orgs/friends/templates/t1", async (route) => {
    const survival = await (await page.request.get("/api/v1/orgs/friends/servers/survival")).json();
    return route.fulfill({ json: { ...template, world: "new", spec: survival.spec } });
  });
  const made = () => page.waitForRequest((r) => r.method() === "POST" && r.url().endsWith("/orgs/friends/servers"));
  await page.route("**/api/v1/orgs/friends/servers", (route) =>
    route.request().method() === "POST" ? route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "test", message: "Not sent by the test." }) }) : route.continue(),
  );
  await page.goto("/o/friends/new?from=template:t1");
  await expect(page.getByRole("group", { name: "Its world" }).getByRole("button", { name: "New world" })).toHaveAttribute("aria-pressed", "true");
  // Asked first, as it would be open to anyone: either way, it is sent.
  const create = async () => {
    const request = made();
    await page.getByRole("button", { name: "Create server" }).click();
    await page.getByRole("button", { name: "Create without a whitelist" }).click();
    return (await request).postDataJSON();
  };
  expect(await create()).toMatchObject({ fromTemplate: "t1", world: "new" });
  await page.getByRole("group", { name: "Its world" }).getByRole("button", { name: "Keep world" }).click();
  expect(await create()).toMatchObject({ fromTemplate: "t1", world: "kept" });

  // One with a world of its own: kept, its version's is.
  await page.route("**/api/v1/orgs/friends/templates/t1", async (route) => {
    const survival = await (await page.request.get("/api/v1/orgs/friends/servers/survival")).json();
    return route.fulfill({ json: { ...template, world: "own", ownWorld: { root: "Island", name: "Island", version: "1.21.4", file: "island.zip", size: 4096 }, spec: survival.spec } });
  });
  await page.goto("/o/friends/new?from=template:t1");
  await expect(page.getByRole("group", { name: "Its world" }).getByRole("button", { name: "Upload world" })).toHaveAttribute("aria-pressed", "true");
  await page.getByRole("group", { name: "Its world" }).getByRole("button", { name: "Keep world" }).click();
  expect(await create()).toMatchObject({ fromTemplate: "t1", world: "kept" });
});

test("a template takes one resource pack from an address: no more are added to it", async ({ page, as, org, api }) => {
  const slug = await org([]);
  const made = await api("owner").post(`/orgs/${slug}/templates`, {
    name: "Packed",
    description: "",
    spec: { loader: "paper", version: "1.21.4", resources: { memory: "4Gi" }, storage: { size: "10Gi" }, resourcePack: { url: "https://example.com/pack.zip", sha1: "a".repeat(40) } },
  });
  await as("owner");
  await page.goto(`/o/${slug}/templates/${made.id}/edit`);
  const packs = page.getByRole("region", { name: "Resource packs" });
  await expect(packs.getByText("pack", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Add resource pack" })).toBeDisabled();
  // Taken off, another can be given.
  await packs.getByRole("button", { name: "Remove pack" }).click();
  await expect(page.getByRole("button", { name: "Add resource pack" })).toBeEnabled();
});

test("several templates are selected and deleted together", async ({ page, as, org, api }) => {
  const slug = await org([]);
  for (const name of ["Alpha", "Beta", "Gamma"]) {
    await api("owner").post(`/orgs/${slug}/templates`, { name, description: "", spec: { loader: "paper", version: "1.21.4", resources: { memory: "4Gi" }, storage: { size: "10Gi" } } });
  }
  await as("owner");
  await page.goto(`/o/${slug}/templates`);
  await page.getByLabel("Select Alpha").click();
  await page.getByLabel("Select Gamma", { exact: true }).click({ modifiers: ["Shift"] });
  const bar = page.getByRole("region", { name: "Selection" });
  await expect(bar).toContainText("3 templates selected");
  await page.getByLabel("Select Beta").click();
  await bar.getByRole("button", { name: "Delete", exact: true }).click();
  await page.getByRole("button", { name: "Delete 2 templates" }).click();
  await expect(page.getByText("Deleted 2 templates")).toBeVisible();
  await expect(page.getByRole("listitem", { name: "Beta" })).toBeVisible();
  await expect(page.getByRole("listitem", { name: "Alpha" })).toHaveCount(0);
  await expect(page.getByRole("listitem", { name: "Gamma" })).toHaveCount(0);
});

test("only admins select templates", async ({ page, as }) => {
  await as("owner");
  await lobby(page);
  await page.goto("/o/friends/templates");
  await expect(page.getByLabel("Select Lobby")).toBeVisible();
  await as("operator");
  await page.reload();
  await expect(page.getByRole("listitem", { name: "Lobby" })).toBeVisible();
  await expect(page.getByLabel("Select Lobby")).toHaveCount(0);
});
