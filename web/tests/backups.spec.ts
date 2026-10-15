import { expect, test } from "./panel";

// The organization's Backups page, against six canned snapshots: four of
// survival, one of creative (whose daily one is overdue), and the last one
// of a server that has since been deleted.
test.beforeEach(async ({ page, as }) => {
  await as("owner");
  // Each test starts from the default arrangement, whatever one before it chose.
  await page.addInitScript(() => window.localStorage.removeItem("avsys.backups.view"));
  await page.goto("/o/friends/backups");
  await expect(page.getByText("6 snapshots of 3 servers")).toBeVisible();
});

test("by server: a closed row each, the one that needs looking at first", async ({ page }) => {
  const rows = page.getByRole("button", { expanded: false }).filter({ hasText: "Newest" });
  await expect(rows).toHaveCount(3);
  // Creative's daily snapshot is overdue, so it leads; the deleted server is last, being the oldest.
  await expect(rows.nth(0)).toContainText("Creative");
  await expect(rows.nth(0)).toContainText(/last backup/);
  await expect(rows.nth(1)).toContainText("Survival");
  await expect(rows.nth(1)).toContainText("4 snapshots");
  await expect(rows.nth(2)).toContainText("oldworld");
  await expect(rows.nth(2)).toContainText("deleted server");
  // Closed, so none of their snapshots are on the page.
  await expect(page.getByRole("button", { name: /^About the snapshot of / })).toHaveCount(0);
});

test("a server is opened to see its snapshots, and one of them to see what it holds", async ({ page }) => {
  await page.getByRole("button", { expanded: false }).filter({ hasText: "Survival" }).click();
  const snapshots = page.getByRole("button", { name: /^About the snapshot of survival/ });
  await expect(snapshots).toHaveCount(4);
  await expect(page.getByRole("link", { name: "Open survival's Backups tab" })).toHaveAttribute("href", "/o/friends/s/survival/backups");

  await snapshots.first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Snapshot of survival" })).toBeVisible();
  await expect(dialog).toContainText("Kept by the schedule, as the newest of its day.");
  await expect(dialog).toContainText("Paper 1.21.4");
  await expect(dialog).toContainText("difficulty=hard");
  await expect(dialog.getByText("LuckPerms", { exact: true })).toBeVisible();
  await page.keyboard.press("Escape");
  await expect(dialog).toHaveCount(0);

  // A snapshot taken by hand is nobody's to remove but a person's.
  await snapshots.nth(1).click();
  await expect(dialog).toContainText("Taken by hand, by owner");
  await expect(dialog).toContainText("Until it is deleted by hand.");
});

test("by server, each server that exists is snapshotted from its row, one with none yet too", async ({ page }) => {
  const asked: string[] = [];
  await page.route("**/api/v1/orgs/friends/servers/*/snapshots", (route) => {
    if (route.request().method() !== "POST") return route.continue();
    asked.push(new URL(route.request().url()).pathname);
    return route.fulfill({ status: 202, json: { kind: "snapshot", org: "friends", server: "survival", stage: "Starting", percent: 0, startedAt: new Date().toISOString() } });
  });
  // A server with no snapshot yet has a row of its own, to take its first.
  const lobby = page.getByRole("listitem").filter({ hasText: "Lobby" });
  await expect(lobby).toContainText("No snapshots yet");
  await expect(lobby.getByRole("button", { expanded: false })).toHaveCount(0);
  await lobby.getByRole("button", { name: "Snapshot lobby now" }).click();
  await expect.poll(() => asked).toEqual(["/api/v1/orgs/friends/servers/lobby/snapshots"]);

  // Pressing it does not open or close the row.
  const survival = page.getByRole("listitem").filter({ hasText: "Survival" });
  await survival.getByRole("button", { name: "Snapshot survival now" }).click();
  await expect.poll(() => asked.length).toBe(2);
  await expect(survival.getByRole("button", { expanded: false })).toBeVisible();

  // A deleted server has nothing to snapshot.
  await expect(page.getByRole("listitem").filter({ hasText: "oldworld" }).getByRole("button", { name: /^Snapshot / })).toHaveCount(0);
});

test("by time: everything in order, each with its server, and narrowed by server or reason", async ({ page }) => {
  await page.getByRole("button", { name: "By time" }).click();
  const snapshots = page.getByRole("button", { name: /^About the snapshot of / });
  await expect(snapshots).toHaveCount(6);
  // Newest first, whichever server they are of.
  const rows = page.getByRole("listitem").filter({ has: snapshots });
  await expect(rows.first()).toContainText(/survival|creative/);
  await expect(rows.last()).toContainText("oldworld");

  await page.getByLabel("Server", { exact: true }).selectOption("creative");
  await expect(snapshots).toHaveCount(1);
  await page.getByLabel("Server", { exact: true }).selectOption("");
  await page.getByLabel("Why it was taken").selectOption("final");
  await expect(snapshots).toHaveCount(1);
  await expect(rows.first()).toContainText("oldworld");
  await page.getByLabel("Why it was taken").selectOption("copy");
  await expect(page.getByText("No snapshot matches that.")).toBeVisible();
});

test("the arrangement chosen is remembered", async ({ page }) => {
  await page.getByRole("button", { name: "By time" }).click();
  await expect(page.getByRole("button", { name: /^About the snapshot of / })).toHaveCount(6);
  // Loaded afresh, without the clearing that the other tests start with.
  const again = await page.context().newPage();
  await again.goto("/o/friends/backups");
  await expect(again.getByRole("button", { name: "By time" })).toHaveAttribute("aria-pressed", "true");
  await expect(again.getByRole("button", { name: /^About the snapshot of / })).toHaveCount(6);
  await again.close();
});

test("several snapshots, of more than one server, are deleted in one request", async ({ page }) => {
  await page.getByRole("button", { name: "By time" }).click();
  await page.getByLabel(/^Select the snapshot of survival/).first().click();
  await page.getByLabel(/^Select the snapshot of oldworld/).click();
  const bar = page.getByRole("region", { name: "Selection" });
  await expect(bar).toContainText("2 snapshots selected");
  await bar.getByRole("button", { name: "Delete", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("1 of survival, 1 of oldworld");

  // What is asked for is looked at and answered here: the snapshots are canned.
  let asked: { method: string; ids: string[] } | undefined;
  await page.route("**/api/v1/orgs/friends/snapshots", async (route) => {
    if (route.request().method() !== "DELETE") return route.continue();
    asked = { method: "DELETE", ids: route.request().postDataJSON().ids };
    await route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify({ deleted: 2 }) });
  });
  await dialog.getByRole("button", { name: "Delete 2 snapshots" }).click();
  await expect(page.getByText("Deleted 2 snapshots")).toBeVisible();
  expect(asked?.ids).toHaveLength(2);
  await expect(bar).toHaveCount(0);
});

test("a viewer sees the snapshots and is offered nothing to do to them", async ({ page, as }) => {
  await as("viewer");
  await page.reload();
  await page.getByRole("button", { expanded: false }).filter({ hasText: "Survival" }).click();
  await expect(page.getByRole("button", { name: /^About the snapshot of survival/ })).toHaveCount(4);
  await expect(page.getByLabel(/^Select the snapshot of /)).toHaveCount(0);
  await expect(page.getByRole("button", { name: /^New server from the snapshot/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /^Delete the snapshot/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /^Snapshot .* now$/ })).toHaveCount(0);
});

test("a server's picture is its own while it exists, and its newest snapshot's once it is gone", async ({ page }) => {
  // Answered here: what reads a picture out of a snapshot is restic, and this
  // panel's restic is a stand-in.
  const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAEElEQVR4nGM4oREFRAwQCgAl3gUpEzqvXwAAAABJRU5ErkJggg==", "base64");
  const asked: string[] = [];
  await page.route("**/api/v1/orgs/friends/snapshots/*/icon*", (route) => {
    asked.push(new URL(route.request().url()).pathname);
    return route.fulfill({ contentType: "image/png", body: png });
  });
  await page.reload();
  const gone = page.getByRole("button", { expanded: false }).filter({ hasText: "oldworld" });
  await expect(gone.locator('img[src*="/snapshots/"]')).toBeVisible();
  // Those that exist show their own, and no snapshot is read for them.
  await expect(page.getByRole("button", { expanded: false }).filter({ hasText: "Survival" }).locator('img[src*="/snapshots/"]')).toHaveCount(0);
  expect(new Set(asked).size).toBe(1);

  // By time, the deleted server's rows have the same picture.
  await page.getByRole("button", { name: "By time" }).click();
  await expect(page.locator('img[src*="/snapshots/"]')).toHaveCount(1);

  // A snapshot's own details show the picture among its files.
  await page.getByRole("button", { name: /^About the snapshot of survival/ }).first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.locator('img[src*="/snapshots/"]')).toBeVisible();
});

test("a snapshot of a server that exists can be restored from here, and one of a deleted server cannot", async ({ page }) => {
  const asked: string[] = [];
  await page.route("**/api/v1/orgs/friends/servers/*/snapshots/*/restore", (route) => {
    asked.push(new URL(route.request().url()).pathname);
    return route.fulfill({ status: 202, json: { kind: "restore", org: "friends", server: "survival", stage: "Stopping the server", percent: 0 } });
  });
  await page.getByRole("button", { expanded: false }).filter({ hasText: "oldworld" }).click();
  await expect(page.getByRole("button", { name: /^Restore oldworld/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /^New server from the snapshot of oldworld/ })).toBeVisible();

  await page.getByRole("button", { expanded: false }).filter({ hasText: "Survival" }).click();
  const restore = page.getByRole("button", { name: /^Restore survival to the snapshot from/ });
  await expect(restore).toHaveCount(4);
  await restore.first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Restore survival?" })).toBeVisible();
  await expect(dialog).toContainText("saved as a snapshot first");
  await dialog.getByRole("button", { name: "Restore", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  expect(asked).toHaveLength(1);
  expect(asked[0]).toMatch(/^\/api\/v1\/orgs\/friends\/servers\/survival\/snapshots\/[0-9a-f]+\/restore$/);

  // By time too, and from a snapshot's details.
  await page.getByRole("button", { name: "By time" }).click();
  await expect(page.getByRole("button", { name: /^Restore oldworld/ })).toHaveCount(0);
  await page.getByRole("button", { name: /^About the snapshot of survival/ }).first().click();
  await expect(page.getByRole("dialog").getByRole("button", { name: "Restore", exact: true })).toBeVisible();
});

test("a server's own Backups tab restores with the same question", async ({ page }) => {
  let asked = "";
  await page.route("**/api/v1/orgs/friends/servers/survival/snapshots/*/restore", (route) => {
    asked = new URL(route.request().url()).pathname;
    return route.fulfill({ status: 202, json: { kind: "restore", org: "friends", server: "survival", stage: "Stopping the server", percent: 0 } });
  });
  await page.goto("/o/friends/s/survival/backups");
  await page.getByRole("button", { name: /^Restore survival to the snapshot from/ }).first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Restore survival?" })).toBeVisible();
  await dialog.getByRole("button", { name: "Restore", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  expect(asked).toMatch(/\/servers\/survival\/snapshots\/[0-9a-f]+\/restore$/);
});

test("a server is snapshotted from its header on any tab, and its Backups tab has no second button", async ({ page, as }) => {
  const asked: string[] = [];
  await page.route("**/api/v1/orgs/friends/servers/survival/snapshots", (route) => {
    if (route.request().method() !== "POST") return route.continue();
    asked.push(route.request().url());
    return route.fulfill({ status: 202, json: { kind: "snapshot", org: "friends", server: "survival", stage: "Starting", percent: 0, startedAt: new Date().toISOString() } });
  });
  await page.goto("/o/friends/s/survival");
  await page.getByRole("button", { name: "Snapshot", exact: true }).click();
  await expect(page.getByText("Taking a snapshot of survival")).toBeVisible();
  expect(asked).toHaveLength(1);

  await page.goto("/o/friends/s/survival/backups");
  await expect(page.getByRole("button", { name: "Snapshot", exact: true })).toHaveCount(1);

  await as("viewer");
  await page.goto("/o/friends/s/survival");
  await expect(page.getByRole("button", { name: "Snapshot", exact: true })).toHaveCount(0);
});

test("the header's Snapshot waits while one is being taken, as the event stream says, on any tab", async ({ page }) => {
  const op = { kind: "snapshot", org: "friends", server: "survival", stage: "Copying files", percent: 40, startedAt: new Date().toISOString() };
  let running = false;
  await page.route("**/api/v1/events", (route) =>
    route.fulfill({
      status: 200,
      headers: { "Content-Type": "text/event-stream" },
      body: running ? `event: operation.progress\ndata: ${JSON.stringify(op)}\n\n` : "event: waiting\ndata: null\n\n",
    }),
  );
  await page.route("**/api/v1/orgs/friends/servers/survival/operation", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify(running ? op : null) }));
  await page.goto("/o/friends/s/survival");
  const snapshot = page.getByRole("button", { name: "Snapshot", exact: true });
  await expect(snapshot).toBeEnabled();
  // The stream is opened again, now saying a snapshot of it is under way,
  // and that an old snapshot was deleted meanwhile, which is not its end.
  running = true;
  await expect(snapshot).toBeDisabled({ timeout: 15_000 });
});

test("a snapshot deleted while one is being taken does not end it; its own end does", async ({ page }) => {
  const op = { kind: "snapshot", org: "friends", server: "survival", stage: "Copying files", percent: 40, startedAt: new Date().toISOString() };
  const deleted = { kind: "delete", org: "friends", server: "survival" };
  const done = { kind: "snapshot", org: "friends", server: "survival" };
  let body = `event: operation.progress\ndata: ${JSON.stringify(op)}\n\nevent: operation.done\ndata: ${JSON.stringify(deleted)}\n\n`;
  await page.route("**/api/v1/events", (route) => route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body }));
  // The panel's own record says the same as the stream.
  await page.route("**/api/v1/orgs/friends/servers/survival/operation", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify(body.includes("progress") ? op : null) }));
  await page.goto("/o/friends/s/survival");
  const snapshot = page.getByRole("button", { name: "Snapshot", exact: true });
  await expect(snapshot).toBeDisabled();
  body = `event: operation.done\ndata: ${JSON.stringify(done)}\n\n`;
  await expect(snapshot).toBeEnabled({ timeout: 15_000 });
});

test("a server's header fits a phone, its buttons wrapping", async ({ page }) => {
  await page.setViewportSize({ width: 390, height: 800 });
  await page.goto("/o/friends/s/survival");
  await expect(page.getByRole("button", { name: "Snapshot", exact: true })).toBeVisible();
  expect(await page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
});

test("by server, a list of servers that could not be loaded is said, not taken for none", async ({ page }) => {
  await page.route("**/api/v1/orgs/friends/servers", (route) => route.fulfill({ status: 500, json: { code: "test", message: "Not today." } }));
  await page.reload();
  await expect(page.getByText(/The list of servers could not be loaded/)).toBeVisible({ timeout: 15_000 });
  await expect(page.getByRole("button", { name: /^Snapshot .* now$/ })).toHaveCount(0);
});

test("a snapshot shown as running on a tab left earlier is let go of when the stream reconnects", async ({ page }) => {
  const op = { kind: "snapshot", org: "friends", server: "survival", stage: "Copying files", percent: 40, startedAt: new Date().toISOString() };
  let running = true;
  await page.route("**/api/v1/orgs/friends/servers/survival/snapshots", async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    const list = await (await route.fetch()).json();
    return route.fulfill({ json: { ...list, operations: running ? [op] : [] } });
  });
  // Each time the stream is opened it ends at once, and is opened again.
  await page.route("**/api/v1/events", (route) => route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body: "event: waiting\ndata: null\n\n" }));
  await page.goto("/o/friends/s/survival/backups");
  const snapshot = page.getByRole("button", { name: "Snapshot", exact: true });
  await expect(snapshot).toBeDisabled();
  // It finished while nothing was said of it, and the Console tab does not ask.
  running = false;
  await page.getByRole("link", { name: "Console" }).click();
  await expect(snapshot).toBeEnabled({ timeout: 15_000 });
});

test("a server's page opened while a snapshot of it is being taken waits for it, on any tab", async ({ page }) => {
  const op = { kind: "snapshot", org: "friends", server: "survival", stage: "Copying files", percent: 40, startedAt: new Date().toISOString() };
  await page.route("**/api/v1/orgs/friends/servers/survival/operation", (route) => route.fulfill({ json: op }));
  await page.goto("/o/friends/s/survival");
  await expect(page.getByRole("button", { name: "Snapshot", exact: true })).toBeDisabled();
});

test("coming back to a server's page asks again what is being done to it", async ({ page }) => {
  const op = { kind: "snapshot", org: "friends", server: "survival", stage: "Starting", percent: 0, startedAt: new Date().toISOString() };
  let running = false;
  await page.route("**/api/v1/orgs/friends/servers/survival/operation", (route) => route.fulfill({ contentType: "application/json", body: JSON.stringify(running ? op : null) }));
  await page.goto("/o/friends/s/survival");
  const snapshot = page.getByRole("button", { name: "Snapshot", exact: true });
  await expect(snapshot).toBeEnabled();
  await page.getByRole("link", { name: "Servers", exact: true }).first().click();
  // Started from elsewhere, with nothing said on the stream yet.
  running = true;
  await page.getByRole("link", { name: /Survival/ }).first().click();
  await expect(snapshot).toBeDisabled();
});
