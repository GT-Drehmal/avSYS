import type { Page } from "@playwright/test";
import { expect, test, withMetrics } from "./panel";

// Against a panel on a cluster with a metrics API and two nodes, pc and
// pi5, which has sampled them and its running servers for the two hours
// before it started: what the Usage tab and the Nodes page draw from that.

test.use({ baseURL: withMetrics });

/** The figures said of a chart, as a screen reader is told them. */
const said = (page: Page, name: string) => page.getByRole("region", { name, exact: true }).getByRole("img");

test("a running server's memory and CPU are drawn from what the panel sampled, memory against what it may use", async ({ page, as }) => {
  await as("viewer");
  await page.goto("/o/friends/s/survival/usage");
  await expect(page.getByText(/^The last 2 hours\.$/)).toBeVisible();
  await expect(said(page, "Memory")).toHaveAttribute("aria-label", /^Memory: [\d.]+ GB now, [\d.]+ GB at most, 4\.0 GB allowed$/);
  await expect(said(page, "CPU")).toHaveAttribute("aria-label", /^CPU: [\d.]+ cores now, [\d.]+ cores at most$/);
  await expect(said(page, "Players")).toHaveAttribute("aria-label", "Players: 2 now, 2 at most");
  // In use is drawn as a line, under the dashed line of what it may use.
  const memory = page.getByRole("region", { name: "Memory" });
  await expect(memory.locator("path").nth(1)).toHaveAttribute("d", /^M[\d.]+,[\d.]+( L[\d.]+,[\d.]+){200,}/);
  await expect(memory.locator("line")).toHaveCount(1);
});

test("a server that is not running has nothing drawn", async ({ page, as }) => {
  await as("viewer");
  await page.goto("/o/friends/s/creative/usage");
  await expect(page.getByText("Usage shows here while the server runs.")).toBeVisible();
});

test("each node's memory and CPU are drawn against what it has, with the cluster's totals", async ({ page, as }) => {
  await as("owner");
  await page.goto("/nodes");
  await expect(page.getByTestId("cluster-total")).toHaveText(/^2 nodes · 2 players · [\d.]+ GB of 40\.0 GB memory in use · [\d.]+ of 20 cores$/);
  await expect(said(page, "Memory on pc")).toHaveAttribute("aria-label", /^Memory on pc: [\d.]+ GB now, [\d.]+ GB at most, 32\.0 GB in all$/);
  await expect(said(page, "CPU on pc")).toHaveAttribute("aria-label", /^CPU on pc: [\d.]+ cores now, [\d.]+ cores at most, 16 cores in all$/);
  await expect(said(page, "Memory on pi5")).toHaveAttribute("aria-label", /8\.0 GB in all$/);
  await expect(said(page, "CPU on pi5")).toHaveAttribute("aria-label", /4\.0 cores in all$/);

  // Pointing at a moment marks it on both of that node's charts, not the other node's.
  const box = (await said(page, "Memory on pc").boundingBox())!;
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await expect(page.getByRole("region", { name: "Memory on pc" }).getByRole("tooltip")).toBeVisible();
  await expect(page.getByRole("region", { name: "CPU on pc" }).getByRole("tooltip")).toBeVisible();
  await expect(page.getByRole("region", { name: "Memory on pi5" }).getByRole("tooltip")).toHaveCount(0);
});

for (const [name, path] of [
  ["Usage tab", "/o/friends/s/survival/usage"],
  ["Nodes page", "/nodes"],
] as const) {
  test(`the ${name} fits a phone's width`, async ({ page, as }) => {
    await as("owner");
    await page.setViewportSize({ width: 375, height: 800 });
    await page.goto(path);
    await expect(page.getByRole("region", { name: /^Memory/ }).first().getByRole("img")).toBeVisible();
    expect(await page.evaluate(() => document.documentElement.scrollWidth)).toBeLessThanOrEqual(375);
  });
}
