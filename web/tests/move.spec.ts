import { expect, test } from "./panel";

// A server's Node section in its settings, against canned nodes: it is on
// "fast", and "pi" and "small" are the others.
const nodes = {
  automatic: "fast",
  nodes: [
    { name: "fast", ready: true, automatic: true, score: 2, memory: 16 * 1024 ** 3, free: 12 * 1024 ** 3 },
    { name: "pi", ready: true, automatic: false, score: 0.4, memory: 8 * 1024 ** 3, free: 6 * 1024 ** 3 },
    { name: "small", ready: true, automatic: true, score: 1, memory: 2 * 1024 ** 3, free: 1 * 1024 ** 3 },
  ],
};

test.beforeEach(async ({ page, as }) => {
  await as("owner");
  await page.route("**/api/v1/nodes?*", (route) => route.fulfill({ json: nodes }));
});

test("a server is moved to another node that has room, after saying what that does", async ({ page }) => {
  await page.route("**/api/v1/orgs/friends/servers/survival/placement", (route) => route.fulfill({ json: { node: "fast", local: true } }));
  const sent: unknown[] = [];
  await page.route("**/api/v1/orgs/friends/servers/survival/move", (route) => {
    sent.push(route.request().postDataJSON());
    return route.fulfill({ status: 202, json: { kind: "move", org: "friends", server: "survival", stage: "Starting", percent: 0, startedAt: new Date().toISOString() } });
  });
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByText("Runs on fast.")).toBeVisible();
  const to = page.getByLabel("Move to");
  // Not the node it is on, and not one without room for its 4 GB.
  await expect(to.locator('option[value="fast"]')).toHaveCount(0);
  await expect(to.locator('option[value="small"]')).toBeDisabled();
  await expect(page.getByRole("button", { name: "Move", exact: true })).toBeDisabled();
  await to.selectOption("pi");
  await page.getByRole("button", { name: "Move", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Move survival to pi?" })).toBeVisible();
  await expect(dialog).toContainText("its files move to pi");
  await dialog.getByRole("button", { name: "Move", exact: true }).click();
  await expect.poll(() => sent).toEqual([{ node: "pi" }]);
  await expect(page.getByText("Moving survival to pi")).toBeVisible();
});

test("a server whose files are not tied to a node is only started again there", async ({ page }) => {
  await page.route("**/api/v1/orgs/friends/servers/survival/placement", (route) => route.fulfill({ json: { node: "fast", local: false } }));
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByText("Runs on fast.")).toBeVisible();
  await page.getByLabel("Move to").selectOption("pi");
  await page.getByRole("button", { name: "Move", exact: true }).click();
  await expect(page.getByRole("dialog")).toContainText("It starts again on pi");
});

test("a server kept to nodes by labels set outside the panel is not offered a move", async ({ page }) => {
  await page.route("**/api/v1/orgs/friends/servers/survival/placement", (route) => route.fulfill({ json: { node: "fast", local: true, labels: { disk: "ssd" } } }));
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByText(/Kept to nodes labelled/)).toContainText("disk=ssd");
  await expect(page.getByLabel("Move to")).toHaveCount(0);
});

test("with one node there is nowhere to move it", async ({ page }) => {
  await page.unroute("**/api/v1/nodes?*");
  await page.route("**/api/v1/nodes?*", (route) => route.fulfill({ json: { automatic: "fast", nodes: nodes.nodes.slice(0, 1) } }));
  await page.route("**/api/v1/orgs/friends/servers/survival/placement", (route) => route.fulfill({ json: { node: "fast", local: true } }));
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByText("No other node to move it to.")).toBeVisible();
  await expect(page.getByLabel("Move to")).toHaveCount(0);
});
