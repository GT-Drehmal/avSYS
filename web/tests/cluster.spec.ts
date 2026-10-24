import { expect, test } from "./panel";

// The panel says when it cannot reach the cluster: what it shows of servers
// is what it last heard.

const stream = (cluster: object) => `event: cluster\ndata: ${JSON.stringify(cluster)}\n\n`;

test("a cluster that cannot be reached is said on every page, until it can again", async ({ page, as }) => {
  await as("viewer");
  const since = new Date(2026, 9, 21, 14, 27).toISOString();
  let body = stream({ reachable: false, since });
  await page.route("**/api/v1/events?*", (route) => route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body }));
  await page.goto("/o/friends");
  const notice = page.getByRole("alert").filter({ hasText: "Can't reach the cluster" });
  await expect(notice).toHaveText("Can't reach the cluster since 2:27 PM. Servers may not be as shown.");
  await page.getByRole("link", { name: "Survival" }).first().click();
  await expect(page.getByRole("heading", { name: "Survival" })).toBeVisible();
  await expect(notice).toBeVisible();
  // The stream is opened again, now saying it can be reached.
  body = stream({ reachable: true });
  await expect(notice).toHaveCount(0, { timeout: 15_000 });
});

test("a cluster that can be reached says nothing", async ({ page, as }) => {
  await as("viewer");
  await page.goto("/o/friends");
  await expect(page.getByRole("heading", { name: "Servers" })).toBeVisible();
  await page.waitForTimeout(500);
  await expect(page.getByRole("alert").filter({ hasText: "Can't reach the cluster" })).toHaveCount(0);
});
