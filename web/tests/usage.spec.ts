import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// A server's Usage tab: what its game has used over the last hours.

test.beforeEach(async ({ as }) => {
  await as("viewer");
});

const gib = 1024 ** 3;

async function usage(page: Page, body: object) {
  await page.route("**/api/v1/orgs/friends/servers/survival/usage", (route) => route.fulfill({ json: body }));
}

test("memory, CPU and players are drawn over the last hours, memory against what it may use", async ({ page }) => {
  const start = Date.now() - 2 * 3600_000;
  const samples = Array.from({ length: 240 }, (_, i) => ({ t: new Date(start + i * 30_000).toISOString(), cpu: 400 + ((i + 1) % 10) * 100, memory: (2 + i / 240) * gib, players: i < 120 ? 1 : 3 }));
  await usage(page, { available: true, limit: 4 * gib, every: 30, samples });
  await page.goto("/o/friends/s/survival/usage");
  await expect(page.getByText("The last 2 hours.")).toBeVisible();
  const memory = page.getByRole("region", { name: "Memory" });
  await expect(memory).toContainText("4.0 GB allowed");
  await expect(memory.getByRole("img")).toHaveAttribute("aria-label", /^Memory: 3\.0 GB now, 3\.0 GB at most$/);
  await expect(page.getByRole("region", { name: "CPU" }).getByRole("img")).toHaveAttribute("aria-label", "CPU: 0.40 cores now, 1.30 cores at most");
  await expect(page.getByRole("region", { name: "Players" }).getByRole("img")).toHaveAttribute("aria-label", "Players: 3 now, 3 at most");
});

test("a cluster that does not report usage, or a server that has not run, says so", async ({ page }) => {
  await usage(page, { available: false, limit: 4 * gib, every: 30, samples: [] });
  await page.goto("/o/friends/s/survival/usage");
  await expect(page.getByText("This cluster does not report what servers use.")).toBeVisible();
  await page.unrouteAll({ behavior: "wait" });
  await usage(page, { available: true, limit: 4 * gib, every: 30, samples: [] });
  await page.reload();
  await expect(page.getByText("Usage shows here while the server runs.")).toBeVisible();
  await expect(page.getByRole("img")).toHaveCount(0);
});

test("a time the server was not running is left as a gap", async ({ page }) => {
  const start = Date.now() - 3 * 3600_000;
  const at = (minutes: number) => new Date(start + minutes * 60_000).toISOString();
  // An hour up, an hour stopped, an hour up again.
  const up = (from: number) => Array.from({ length: 120 }, (_, i) => ({ t: at(from + i / 2), cpu: 500, memory: 2 * gib, players: 1 }));
  await usage(page, { available: true, limit: 4 * gib, every: 30, samples: [...up(0), ...up(120)] });
  await page.goto("/o/friends/s/survival/usage");
  const line = page.getByRole("region", { name: "CPU" }).locator("path").nth(1);
  await expect.poll(async () => ((await line.getAttribute("d")) ?? "").match(/M/g)?.length).toBe(2);
});

test("a server that is not running now says what it used when it last ran", async ({ page }) => {
  const start = Date.now() - 4 * 3600_000;
  const samples = Array.from({ length: 60 }, (_, i) => ({ t: new Date(start + i * 30_000).toISOString(), cpu: 700, memory: 2 * gib, players: 2 }));
  await usage(page, { available: true, limit: 4 * gib, every: 30, samples });
  await page.goto("/o/friends/s/survival/usage");
  await expect(page.getByRole("region", { name: "Players" }).getByRole("img")).toHaveAttribute("aria-label", "Players: 2 when it last ran, 2 at most");
});
