import { expect, test } from "./panel";

const GiB = 1024 ** 3;
const listed = (over: Record<string, unknown> = {}) => ({
  nodes: [
    { name: "pc", ready: true, automatic: true, hosts: true, score: 5500, memory: 32 * GiB, free: 20 * GiB, cores: 16000, players: 0 },
    { name: "pi5", ready: false, automatic: false, hosts: true, score: 0, memory: 8 * GiB, free: 8 * GiB, cores: 4000, players: 0 },
  ],
  measuring: false,
  ...over,
});

test("the administrator keeps a node from taking new servers, and measures every node's speed", async ({ page, as }) => {
  await as("owner");
  let state = listed();
  await page.route("**/api/v1/nodes", (route) => route.fulfill({ json: state }));
  const switched: unknown[] = [];
  await page.route("**/api/v1/nodes/pc", async (route) => {
    switched.push(route.request().postDataJSON());
    state = { ...state, nodes: state.nodes.map((n) => (n.name === "pc" ? { ...n, automatic: false, hosts: false } : n)) };
    await route.fulfill({ status: 204 });
  });
  let measured = 0;
  await page.route("**/api/v1/nodes/measure", async (route) => {
    measured++;
    state = { ...state, measuring: true };
    await route.fulfill({ status: 202, json: { measuring: true } });
  });

  await page.goto("/nodes");
  await expect(page.getByText("speed 5500")).toBeVisible();
  await expect(page.getByText("speed not measured")).toBeVisible();
  await expect(page.getByText("down")).toBeVisible();

  await page.getByRole("switch", { name: /New servers on pc/ }).click();
  await expect(page.getByRole("switch", { name: /New servers on pc/ })).toHaveAttribute("aria-checked", "false");
  await expect(page.getByText("Only when picked by hand.")).toBeVisible();
  expect(switched).toEqual([{ automatic: false }]);

  await page.getByRole("button", { name: "Measure speed" }).click();
  await expect(page.getByText("Measuring every node. This takes a few minutes.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Measuring…" })).toBeDisabled();
  expect(measured).toBe(1);

  // Done, with one node that could not be.
  state = { ...state, measuring: false, measured: { at: new Date().toISOString(), failed: ["pi5"] } };
  await expect(page.getByText(/Could not measure pi5\./)).toBeVisible({ timeout: 10_000 });
  await expect(page.getByRole("button", { name: "Measure speed" })).toBeEnabled();
});

test("nodes are for the administrator only", async ({ page, as }) => {
  await as("admin");
  await page.goto("/nodes");
  await expect(page.getByText("Only the panel's administrator can see nodes.")).toBeVisible();
});

test("each node says what it is using and how many are playing on it, and the cluster in all", async ({ page, as }) => {
  await as("owner");
  const pc = { name: "pc", ready: true, automatic: true, hosts: true, score: 5500, memory: 32 * GiB, free: 20 * GiB, cores: 16000, players: 3 };
  const servers = [
    { org: "friends", name: "survival", memory: "8Gi", phase: "Running", players: 3 },
    { org: "friends", name: "creative", memory: "4Gi", phase: "Stopped" },
  ];
  const pi5 = { name: "pi5", ready: true, automatic: true, hosts: true, score: 0, memory: 8 * GiB, free: 8 * GiB, cores: 4000, players: 0 };
  let state: object = { nodes: [{ ...pc, servers, inUse: { cpu: 2500, memory: 11.5 * GiB } }, { ...pi5, inUse: { cpu: 100, memory: 0.5 * GiB } }], measuring: false };
  await page.route("**/api/v1/nodes", (route) => route.fulfill({ json: state }));
  await page.goto("/nodes");
  await expect(page.getByTestId("cluster-total")).toHaveText("2 nodes · 3 players · 12.0 GB of 40.0 GB memory in use");
  await expect(page.getByTestId("node-use").first()).toHaveText("In use: 11.5 GB memory, 2.5 of 16 cores · 3 players");
  await expect(page.getByRole("link", { name: /friends\/survival/ })).toContainText("8Gi · 3 players");
  await expect(page.getByRole("link", { name: /friends\/creative/ })).not.toContainText("player");

  // A cluster that does not say what nodes use: players only.
  state = { nodes: [{ ...pc, servers }, pi5], measuring: false };
  await page.reload();
  await expect(page.getByTestId("cluster-total")).toHaveText("2 nodes · 3 players");
  await expect(page.getByTestId("node-use").first()).toHaveText("3 players");
});
