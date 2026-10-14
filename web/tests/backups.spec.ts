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
});
