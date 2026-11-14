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

test("once the cluster can be reached again, servers are asked for again, and not what the panel told meanwhile", async ({ page, as }) => {
  await as("owner");
  const asked = { servers: 0, snapshots: 0 };
  page.on("request", (r) => {
    const path = new URL(r.url()).pathname;
    if (path === "/api/v1/orgs/friends/servers") asked.servers++;
    if (path === "/api/v1/orgs/friends/snapshots") asked.snapshots++;
  });
  // One stream, which says the cluster is back, and no other.
  let streams = 0;
  await page.route("**/api/v1/events?*", (route) =>
    streams++ === 0
      ? route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body: `${stream({ reachable: true })}event: resync\ndata: {"cluster":true}\n\n` })
      : route.abort(),
  );
  await page.goto("/o/friends/backups");
  await expect(page.getByRole("heading", { name: "Backups" })).toBeVisible();
  await expect.poll(() => asked.servers).toBe(2);
  await page.waitForTimeout(500);
  expect(asked.snapshots).toBe(1);
});
