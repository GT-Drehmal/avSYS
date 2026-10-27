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
async function library(page: Page, worlds: ReturnType<typeof world>[]) {
  const calls: { method: string; path: string; query: Record<string, string>; body: unknown }[] = [];
  await page.route(/\/api\/v1\/orgs\/friends\/worlds(\/.*)?(\?.*)?$/, async (route) => {
    const req = route.request();
    const url = new URL(req.url());
    const path = url.pathname.replace("/api/v1/orgs/friends", "");
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

test("a world is uploaded to the library, named as it calls itself, with its own picture", async ({ page, as }) => {
  await as("operator");
  const calls = await library(page, []);
  await page.goto("/o/friends/worlds");
  await expect(page.getByText("No worlds yet")).toBeVisible();
  await page.getByRole("button", { name: "Add world" }).click();
  const dialog = page.getByRole("dialog", { name: "Add a world" });
  await expect(dialog.getByRole("button", { name: "Add world" })).toBeDisabled();
  // A save made in the game, one folder down, with a picture of its own.
  const icon = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
  await dialog.getByLabel("World archive").setInputFiles({
    name: "island.zip",
    mimeType: "application/zip",
    buffer: zip({ "Island Save/level.dat": levelDat("1.21.4", "Island"), "Island Save/region/r.0.0.mca": "land", "Island Save/icon.png": icon }),
  });
  await expect(dialog.getByTestId("world-summary")).toContainText('World "Island" · last opened in 1.21.4');
  await expect(dialog.getByLabel("Name")).toHaveValue("Island");
  await dialog.getByLabel("Description").fill("Where we started");
  await dialog.getByRole("button", { name: "Add world" }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole("listitem", { name: "Island" })).toBeVisible();
  expect(calls[0]).toMatchObject({ method: "POST", path: "/worlds/upload", query: { name: "Island", description: "Where we started", root: "Island Save", version: "1.21.4", file: "island.zip" } });
  expect(calls[1]).toMatchObject({ method: "PUT", path: "/worlds/w9/icon" });
});

test("a server's world, or a snapshot's, is saved to the library", async ({ page, as }) => {
  await as("operator");
  const calls = await library(page, []);
  await page.goto("/o/friends/worlds");
  await page.getByRole("button", { name: "Add world" }).click();
  let dialog = page.getByRole("dialog", { name: "Add a world" });
  await dialog.getByRole("button", { name: "Server", exact: true }).click();
  await dialog.getByRole("button", { name: /Survival/ }).first().click();
  await expect(dialog.getByText("A snapshot of it is taken first")).toBeVisible();
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
