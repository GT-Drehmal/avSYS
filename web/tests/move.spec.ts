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
  await expect(page.getByRole("dialog")).toContainText("The server starts again on pi");
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
  await expect(page.getByText("No other node to move the server to.")).toBeVisible();
  await expect(page.getByLabel("Move to")).toHaveCount(0);
});

// The node its files are on is down: they cannot be copied, so the server is
// made again on another node from its newest snapshot, and says what is lost.
const newest = { id: "abcd1234ef567890", time: new Date(Date.now() - 3 * 3600_000).toISOString(), server: "survival", trigger: "scheduled", size: 1, added: 1, files: 1 };
const fastDown = { ...nodes, nodes: nodes.nodes.map((n) => (n.name === "fast" ? { ...n, ready: false } : n)) };

test("a server on a node that is down is moved from its newest snapshot, after saying what is lost", async ({ page }) => {
  await page.unroute("**/api/v1/nodes?*");
  await page.route("**/api/v1/nodes?*", (route) => route.fulfill({ json: fastDown }));
  await page.route("**/api/v1/orgs/friends/servers/survival/placement", (route) => route.fulfill({ json: { node: "fast", local: true, down: true, newest } }));
  const sent: unknown[] = [];
  await page.route("**/api/v1/orgs/friends/servers/survival/move", (route) => {
    sent.push(route.request().postDataJSON());
    return route.fulfill({ status: 202, json: { kind: "move", org: "friends", server: "survival", stage: "Starting", percent: 0, startedAt: new Date().toISOString() } });
  });
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByText("Runs on fast, which is down.")).toBeVisible();
  const said = page.getByTestId("node-down");
  await expect(said).toContainText("The server's files on fast can't be reached.");
  await expect(said).toContainText("taken 3 hours ago");
  await expect(said).toContainText("Changes since then are lost.");
  // Not a move that copies the files.
  await expect(page.getByRole("button", { name: "Move", exact: true })).toHaveCount(0);
  await page.getByLabel("Move to").selectOption("pi");
  await page.getByRole("button", { name: "Move from snapshot" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByRole("heading", { name: "Move survival to pi from a snapshot?" })).toBeVisible();
  await expect(dialog).toContainText("The server is made again on pi from its snapshot taken 3 hours ago");
  await expect(dialog).toContainText("Changes since then are lost. The files on fast are not used again.");
  await dialog.getByRole("button", { name: "Move from snapshot" }).click();
  await expect.poll(() => sent).toEqual([{ node: "pi", fromSnapshot: newest.id }]);
  await expect(page.getByText("Moving survival to pi")).toBeVisible();
});

test("a server on a node that is down with no snapshot to move from says why, and offers no move", async ({ page }) => {
  await page.unroute("**/api/v1/nodes?*");
  await page.route("**/api/v1/nodes?*", (route) => route.fulfill({ json: fastDown }));
  for (const unable of ["There is no snapshot of survival to make it again from.", "Snapshots cannot be read right now."]) {
    await page.unroute("**/api/v1/orgs/friends/servers/survival/placement");
    await page.route("**/api/v1/orgs/friends/servers/survival/placement", (route) => route.fulfill({ json: { node: "fast", local: true, down: true, unable } }));
    await page.goto("/o/friends/s/survival/settings");
    await expect(page.getByText("Runs on fast, which is down.")).toBeVisible();
    await expect(page.getByTestId("node-down")).toHaveText(`The server's files on fast can't be reached. ${unable}`);
    await expect(page.getByLabel("Move to")).toHaveCount(0);
  }
});
