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
  await menu("Edit");
  let dialog = page.getByRole("dialog", { name: "Edit Lobby" });
  await dialog.getByLabel("Name").fill("Hub");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog).toHaveCount(0);
  expect(calls.at(-1)).toEqual({ method: "PATCH", path: "/templates/t1", body: { name: "Hub", description: "Where everyone starts." } });

  await menu("Update from a server");
  dialog = page.getByRole("dialog", { name: "Update Lobby" });
  await dialog.getByLabel("Server").selectOption("creative");
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
  await expect(page.getByRole("button", { name: "Save a server" })).toHaveCount(0);
});

test("with no templates the page says how to make one", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/templates");
  await expect(page.getByText("No templates yet")).toBeVisible();
  await expect(page.getByRole("button", { name: "Save a server" })).toBeEnabled();
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
