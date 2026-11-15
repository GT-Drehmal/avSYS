import { expect, test } from "./panel";

// A cordoned node, as before maintenance: it takes no new servers, and the
// servers on it are moved off.
const GiB = 1024 ** 3;

test("the nodes page shows a cordoned node as cordoned, not down", async ({ page, as }) => {
  await as("owner");
  await page.route("**/api/v1/nodes", (route) =>
    route.fulfill({
      json: {
        nodes: [
          { name: "pc", ready: true, automatic: true, hosts: true, score: 5500, memory: 32 * GiB, free: 20 * GiB, cores: 16000, players: 0 },
          {
            name: "pi5",
            ready: false,
            cordoned: true,
            automatic: false,
            hosts: true,
            score: 0,
            memory: 8 * GiB,
            free: 4 * GiB,
            cores: 4000,
            players: 0,
            servers: [{ org: "friends", name: "survival", memory: "4Gi", phase: "Running" }],
          },
          { name: "old", ready: false, automatic: false, hosts: true, score: 0, memory: 8 * GiB, free: 8 * GiB, cores: 4000, players: 0 },
        ],
        measuring: false,
      },
    }),
  );
  await page.goto("/nodes");
  const row = (name: string) => page.getByRole("listitem").filter({ has: page.getByText(name, { exact: true }) });
  await expect(row("pi5")).toContainText("cordoned");
  await expect(row("pi5")).not.toContainText("down");
  await expect(row("old")).toContainText("down");
  await expect(row("pc")).not.toContainText("cordoned");
});

test("a server on a cordoned node is moved off it, and not to another cordoned node", async ({ page, as }) => {
  await as("owner");
  await page.route("**/api/v1/nodes?*", (route) =>
    route.fulfill({
      json: {
        automatic: "small",
        nodes: [
          { name: "fast", ready: false, cordoned: true, automatic: false, score: 2, memory: 16 * GiB, free: 12 * GiB },
          { name: "pi", ready: false, cordoned: true, automatic: false, score: 0.4, memory: 16 * GiB, free: 12 * GiB },
          { name: "small", ready: true, automatic: true, score: 1, memory: 16 * GiB, free: 12 * GiB },
        ],
      },
    }),
  );
  await page.route("**/api/v1/orgs/friends/servers/survival/placement", (route) => route.fulfill({ json: { node: "fast", local: true } }));
  const sent: unknown[] = [];
  await page.route("**/api/v1/orgs/friends/servers/survival/move", (route) => {
    sent.push(route.request().postDataJSON());
    return route.fulfill({ status: 202, json: { kind: "move", org: "friends", server: "survival", stage: "Starting", percent: 0, startedAt: new Date().toISOString() } });
  });
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByText("Runs on fast.")).toBeVisible();
  const to = page.getByLabel("Move to");
  // Another cordoned node is shown as such, with room or not, and cannot be
  // chosen.
  await expect(to.locator('option[value="pi"]')).toBeDisabled();
  await expect(to.locator('option[value="pi"]')).toContainText("cordoned");
  await expect(to.locator('option[value="small"]')).toBeEnabled();
  await to.selectOption("small");
  await page.getByRole("button", { name: "Move", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("its files move to small");
  await dialog.getByRole("button", { name: "Move", exact: true }).click();
  await expect.poll(() => sent).toEqual([{ node: "small" }]);
});
