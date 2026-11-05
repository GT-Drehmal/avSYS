import { expect, type Page, test } from "./panel";

// Every list is put in order and narrowed the same way, from beside its
// search: kept in the page's address, and with only what is shown to tick.

const sortBy = async (page: Page, now: string, by: string) => {
  await page.getByRole("button", { name: `Sort: ${now}` }).click();
  await page.getByRole("menuitemradio", { name: by, exact: true }).click();
};
const filterBy = async (page: Page, ...choices: string[]) => {
  await page.getByRole("button", { name: "Filter", exact: true }).click();
  for (const choice of choices) await page.getByRole("menuitemcheckbox", { name: choice, exact: true }).click();
  await page.keyboard.press("Escape");
};

test("servers are put in order, and what is chosen stays in the address", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends");
  const rows = page.getByRole("main").getByRole("listitem");
  await expect(rows).toHaveCount(4);
  const names = () => rows.locator("a").evaluateAll((links) => links.map((a) => (a.textContent ?? "").split(/Paper|Fabric|Vanilla|Purpur/)[0].trim()));
  await expect(rows.first()).toContainText("Broken");
  await sortBy(page, "Name", "Players");
  await expect(rows.first()).toContainText("Survival");
  await expect(page).toHaveURL(/\?sort=players$/);
  await sortBy(page, "Players", "Status");
  await expect(rows.first()).toContainText("Survival");
  await expect(rows.last()).toContainText("Creative");
  const inOrder = await names();
  // Loaded afresh, it is as it was.
  await page.reload();
  await expect(page.getByRole("button", { name: "Sort: Status" })).toBeVisible();
  await expect.poll(names).toEqual(inOrder);
  // The first is what there is without asking.
  await sortBy(page, "Status", "Name");
  await expect(page).toHaveURL(/\/o\/friends$/);
});

test("servers are narrowed, by several things at once, and each can be taken off", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends");
  const rows = page.getByRole("main").getByRole("listitem");
  await expect(rows).toHaveCount(4);
  await filterBy(page, "Online", "Offline");
  await expect(rows).not.toHaveCount(4);
  const both = await rows.count();
  expect(both).toBeGreaterThanOrEqual(2);
  await expect(page).toHaveURL(/status=Running&status=Stopped/);
  // Of one kind, any of them; of different kinds, all of them.
  await filterBy(page, "Paper");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Survival");
  // With the search too.
  await page.getByLabel("Search servers").fill("creative");
  await expect(page.getByText("No servers match.")).toBeVisible();
  await page.getByLabel("Search servers").press("Escape");
  await page.getByRole("button", { name: "Stop filtering by Paper" }).click();
  await expect(rows).toHaveCount(both);
  await page.getByRole("button", { name: "Stop filtering by Online" }).click();
  await page.getByRole("button", { name: "Stop filtering by Offline" }).click();
  await expect(rows).toHaveCount(4);
  await expect(page).toHaveURL(/\/o\/friends$/);
});

test("what is ticked follows the list as it is shown", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends?sort=status");
  const rows = page.getByRole("main").getByRole("listitem");
  await expect(rows).toHaveCount(4);
  const boxes = rows.getByRole("checkbox");
  // A range runs along the order shown, not along the names.
  await boxes.nth(0).click();
  await boxes.nth(2).click({ modifiers: ["Shift"] });
  await expect(page.getByText("3 servers selected")).toBeVisible();
  await expect(boxes.nth(3)).not.toBeChecked();
  await page.keyboard.press("Escape");
  // Everything ticked is everything shown, and one filtered out is let go.
  await filterBy(page, "Fabric");
  await expect(rows).toHaveCount(1);
  await page.getByLabel("Select every server").click();
  await expect(page.getByText("1 server selected")).toBeVisible();
  await page.getByRole("button", { name: "Stop filtering by Fabric" }).click();
  await expect(rows).toHaveCount(4);
  await expect(page.getByText("1 server selected")).toBeVisible();
  await filterBy(page, "Vanilla");
  await expect(page.getByText(/servers? selected/)).toHaveCount(0);
});

test("an address that asks for what is not there still works, and can be cleared", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends?sort=nothing&software=Forge");
  await expect(page.getByRole("button", { name: "Sort: Name" })).toBeVisible();
  await expect(page.getByText("No servers match.")).toBeVisible();
  await page.getByRole("button", { name: "Stop filtering by Forge" }).click();
  await expect(page.getByRole("main").getByRole("listitem")).toHaveCount(4);
});

test("on a phone, sorting and filtering share one menu", async ({ page, as }) => {
  await as("owner");
  await page.setViewportSize({ width: 375, height: 700 });
  await page.goto("/o/friends");
  await expect(page.getByRole("button", { name: /^Sort:/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Filter", exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "Sort and filter" }).click();
  await page.getByRole("menuitemcheckbox", { name: "Fabric", exact: true }).click();
  await page.keyboard.press("Escape");
  await expect(page.getByRole("main").getByRole("listitem")).toHaveCount(1);
  await page.getByRole("button", { name: "Sort and filter" }).click();
  await page.getByRole("menuitemradio", { name: "Players" }).click();
  await expect(page).toHaveURL(/sort=players/);
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow).toBeLessThanOrEqual(0);
});

test("templates are put in order and narrowed", async ({ page, as }) => {
  await as("owner");
  const snapshot = (time: string, size: number) => ({ id: time, time, server: "x", trigger: "template", loader: "paper", version: "1.21.4", size, added: size, files: 1 });
  const at = "2026-01-01T00:00:00Z";
  const t = (id: string, name: string, extra: Record<string, unknown>) => ({ id, name, description: "", createdBy: "owner", createdAt: at, updatedAt: at, versions: [], settingsOnly: false, ...extra });
  const first = snapshot("2026-02-01T00:00:00Z", 100);
  const second = snapshot("2026-03-01T00:00:00Z", 10);
  await page.route(/\/api\/v1\/orgs\/friends\/templates$/, (route) =>
    route.fulfill({
      json: [
        t("t1", "Arena", { current: first, versions: [first] }),
        t("t2", "Build", { current: second, versions: [second], world: "own" }),
        t("t3", "Plain", { settingsOnly: true, spec: { loader: "fabric", version: "1.21.4" } }),
      ],
    }),
  );
  await page.goto("/o/friends/templates");
  const rows = page.getByRole("list", { name: "Templates" }).getByRole("listitem");
  await expect(rows).toHaveCount(3);
  await sortBy(page, "Name", "Size");
  await expect(rows.first()).toContainText("Arena");
  await sortBy(page, "Size", "Last saved");
  await expect(rows.first()).toContainText("Build");
  await filterBy(page, "Settings only");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Plain");
  await page.getByRole("button", { name: "Stop filtering by Settings only" }).click();
  await filterBy(page, "With its own world");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Build");
  await page.getByRole("button", { name: "Stop filtering by With its own world" }).click();
  await filterBy(page, "Fabric");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Plain");
});

test("worlds are put in order and narrowed, game versions as numbers", async ({ page, as }) => {
  await as("owner");
  const w = (id: string, name: string, extra: Record<string, unknown>) => ({ id, name, description: "", createdBy: "owner", createdAt: "2026-01-01T00:00:00Z", updatedAt: "2026-01-01T00:00:00Z", root: "", size: 1, source: "upload", ready: true, ...extra });
  await page.route(/\/api\/v1\/orgs\/friends\/worlds$/, (route) =>
    route.fulfill({
      json: [
        w("w1", "Arena", { version: "1.9.4", size: 500 }),
        w("w2", "Build", { version: "1.21.4", source: "server:build", createdAt: "2026-03-01T00:00:00Z" }),
        w("w3", "Crater", { source: "snapshot:crater", size: 9 }),
      ],
    }),
  );
  await page.goto("/o/friends/worlds");
  const rows = page.getByRole("list", { name: "Worlds" }).getByRole("listitem");
  await expect(rows).toHaveCount(3);
  await sortBy(page, "Name", "Game version");
  await expect(rows.first()).toContainText("Build");
  await expect(rows.last()).toContainText("Crater");
  await sortBy(page, "Game version", "Size");
  await expect(rows.first()).toContainText("Arena");
  await sortBy(page, "Size", "Date added");
  await expect(rows.first()).toContainText("Build");
  await filterBy(page, "From a snapshot");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Crater");
});

test("members are put in order and narrowed", async ({ page, as }) => {
  await as("owner");
  const m = (userId: string, displayName: string, role: string, joinedAt: string, minecraftName?: string) => ({ userId, username: userId, displayName, avatarUrl: "", role, joinedAt, minecraftName });
  await page.route(/\/api\/v1\/orgs\/friends\/members$/, (route) =>
    route.fulfill({
      json: [m("a", "Ada", "viewer", "2026-01-01T00:00:00Z", "ada_mc"), m("b", "Bea", "owner", "2026-02-01T00:00:00Z"), m("c", "Cyd", "admin", "2026-03-01T00:00:00Z")],
    }),
  );
  await page.goto("/o/friends/members");
  const rows = page.getByRole("main").getByRole("listitem").filter({ hasText: /Ada|Bea|Cyd/ });
  await expect(rows).toHaveCount(3);
  await expect(rows.first()).toContainText("Ada");
  await sortBy(page, "Name", "Role");
  await expect(rows.first()).toContainText("Bea");
  await expect(rows.last()).toContainText("Ada");
  await sortBy(page, "Role", "Joined");
  await expect(rows.first()).toContainText("Cyd");
  await filterBy(page, "Admin");
  await expect(rows).toHaveCount(1);
  await page.getByRole("button", { name: "Stop filtering by Admin" }).click();
  await filterBy(page, "Linked");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Ada");
});

test("an address with a filter left empty shows everything, on every list", async ({ page, as }) => {
  await as("owner");
  for (const [path, heading] of [
    ["/o/friends?status=&software", "Servers"],
    ["/o/friends/members?role=&minecraft", "Members"],
    ["/o/friends/templates?kind=", "Templates"],
    ["/o/friends/worlds?source=&version=", "Worlds"],
    ["/o/friends/backups?reason=&age=", "Backups"],
  ]) {
    await page.goto(path);
    await expect(page.getByRole("heading", { name: heading, exact: true })).toBeVisible();
    await expect(page.getByRole("alert")).toHaveCount(0);
    await expect(page.getByRole("button", { name: /^Stop filtering by/ })).toHaveCount(0);
  }
  await expect(page.getByRole("main").getByRole("listitem")).not.toHaveCount(0);
});
