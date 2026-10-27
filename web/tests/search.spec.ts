import { expect, test } from "./panel";

// Each list a page has is searched from a box above it: by any of the words
// typed, in any case, and kept in the page's address.

test("servers are searched by name, software and state, and only those shown can be ticked", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends");
  const rows = page.getByRole("main").getByRole("listitem");
  await expect(rows).toHaveCount(4);
  const search = page.getByLabel("Search servers");
  await search.fill("fabric");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Creative");
  await expect(page).toHaveURL(/\?q=fabric$/);
  await search.fill("paper ONLINE");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Survival");
  // Every one shown, and only those, is ticked.
  await page.getByLabel("Select every server").click();
  await expect(page.getByText("1 server selected")).toBeVisible();
  await search.fill("nothing like this");
  await expect(page.getByText("No servers match.")).toBeVisible();
  // Escape empties it.
  await search.press("Escape");
  await expect(rows).toHaveCount(4);
  await expect(page).toHaveURL(/\/o\/friends$/);
});

test("a search is kept on going back to the page", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends?q=lobby");
  await expect(page.getByLabel("Search servers")).toHaveValue("lobby");
  await page.getByRole("link", { name: /^Lobby/ }).click();
  await expect(page).toHaveURL(/\/s\/lobby$/);
  await page.goBack();
  await expect(page.getByLabel("Search servers")).toHaveValue("lobby");
  await expect(page.getByRole("main").getByRole("listitem")).toHaveCount(1);
});

test("templates are searched by name and description", async ({ page, as }) => {
  await as("owner");
  const t = (id: string, name: string, description: string) => ({ id, name, description, createdBy: "owner", createdAt: new Date().toISOString(), updatedAt: new Date().toISOString(), versions: [], settingsOnly: true, spec: { loader: "paper", version: "1.21.4" } });
  await page.route(/\/api\/v1\/orgs\/friends\/templates$/, (route) => route.fulfill({ json: [t("t1", "Lobby", "Where everyone starts."), t("t2", "Skyblock", "One island each.")] }));
  await page.goto("/o/friends/templates");
  const rows = page.getByRole("list", { name: "Templates" }).getByRole("listitem");
  await expect(rows).toHaveCount(2);
  await page.getByLabel("Search templates").fill("island");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Skyblock");
  await page.getByLabel("Search templates").fill("zzz");
  await expect(page.getByText("No templates match.")).toBeVisible();
});

test("backups are searched by server, and by why and when each was taken", async ({ page, as }) => {
  await as("owner");
  await page.addInitScript(() => window.localStorage.removeItem("avsys.backups.view"));
  await page.goto("/o/friends/backups");
  await expect(page.getByText("6 snapshots of 3 servers")).toBeVisible();
  const groups = page.getByRole("button", { expanded: false }).filter({ hasText: "Newest" });
  await expect(groups).toHaveCount(3);
  const search = page.getByLabel("Search backups");
  // A deleted server is found as one.
  await search.fill("deleted");
  await expect(groups).toHaveCount(1);
  await expect(groups.first()).toContainText("oldworld");
  await search.fill("surv");
  await expect(groups).toHaveCount(1);
  await expect(groups.first()).toContainText("Survival");
  // By time, the same search, and why each was taken.
  await page.getByRole("button", { name: "By time" }).click();
  await expect(page.getByRole("button", { name: /^About the snapshot of / })).toHaveCount(4);
  await search.fill("survival by owner");
  await expect(page.getByRole("button", { name: /^About the snapshot of / })).toHaveCount(1);
  await search.fill("zzz");
  await expect(page.getByText("No matching snapshots.")).toBeVisible();
  await page.getByRole("button", { name: "By server" }).click();
  await expect(page.getByText("No servers match.")).toBeVisible();
});

test("members are searched by name and role", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/members");
  const rows = page.getByRole("main").getByRole("list").first().getByRole("listitem");
  // The owner, an admin, an operator and a viewer.
  await expect(rows).toHaveCount(4);
  const search = page.getByLabel("Search members");
  await search.fill("viewer");
  await expect(rows).toHaveCount(1);
  await search.fill("you");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("owner");
  await search.fill("zzz");
  await expect(page.getByText("No members match.")).toBeVisible();
});
