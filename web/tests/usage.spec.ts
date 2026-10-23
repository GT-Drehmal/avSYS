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

test("a server that has not run says so", async ({ page }) => {
  await usage(page, { available: true, limit: 4 * gib, every: 30, samples: [] });
  await page.goto("/o/friends/s/survival/usage");
  await expect(page.getByText("Usage shows here while the server runs.")).toBeVisible();
  await expect(page.getByRole("img")).toHaveCount(0);
});

test("a cluster that does not report memory and CPU still shows players", async ({ page }) => {
  const start = Date.now() - 3600_000;
  const samples = Array.from({ length: 120 }, (_, i) => ({ t: new Date(start + i * 30_000).toISOString(), players: i < 60 ? 2 : 5 }));
  await usage(page, { available: false, limit: 4 * gib, every: 30, samples });
  await page.goto("/o/friends/s/survival/usage");
  await expect(page.getByText("This cluster does not report memory and CPU.")).toBeVisible();
  await expect(page.getByRole("region", { name: "Players" }).getByRole("img")).toHaveAttribute("aria-label", "Players: 5 now, 5 at most");
  await expect(page.getByRole("region", { name: "Memory" })).toHaveCount(0);
  await expect(page.getByRole("region", { name: "CPU" })).toHaveCount(0);
});

test("memory and CPU start where the cluster began to report them, on the same hours as players", async ({ page }) => {
  const start = Date.now() - 3600_000;
  // The first half hour before metrics were there: players alone.
  const samples = Array.from({ length: 120 }, (_, i) => ({ t: new Date(start + i * 30_000).toISOString(), players: 1, ...(i >= 60 && { cpu: 500, memory: 2 * gib }) }));
  await usage(page, { available: true, limit: 4 * gib, every: 30, samples });
  await page.goto("/o/friends/s/survival/usage");
  await expect(page.getByRole("region", { name: "CPU" }).getByRole("img")).toHaveAttribute("aria-label", "CPU: 0.50 cores now, 0.50 cores at most");
  // Drawn from halfway across, not stretched over the whole chart.
  const line = page.getByRole("region", { name: "CPU" }).locator("path").nth(1);
  await expect.poll(async () => Number(((await line.getAttribute("d")) ?? "").match(/^M([\d.]+),/)?.[1])).toBeGreaterThan(290);
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

test("pointing at a moment shows what each chart was then", async ({ page }) => {
  const start = Date.now() - 2 * 3600_000;
  const samples = Array.from({ length: 240 }, (_, i) => ({ t: new Date(start + i * 30_000).toISOString(), cpu: 500, memory: (i < 120 ? 2 : 3) * gib, players: i < 120 ? 1 : 3 }));
  await usage(page, { available: true, limit: 4 * gib, every: 30, samples });
  await page.goto("/o/friends/s/survival/usage");
  const players = page.getByRole("region", { name: "Players" });
  const box = (await players.getByRole("img").boundingBox())!;
  // A quarter of the way along, in the first hour.
  await page.mouse.move(box.x + box.width / 4, box.y + box.height / 2);
  const time = new Date(start + 30 * 60_000).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  await expect(players.getByRole("tooltip")).toHaveText(`1 ${time}`);
  // The other charts mark the same moment.
  await expect(page.getByRole("region", { name: "Memory" }).getByRole("tooltip")).toHaveText(`2.0 GB ${time}`);
  await expect(page.getByRole("region", { name: "CPU" }).getByRole("tooltip")).toHaveText(`0.50 cores ${time}`);
  await page.mouse.move(box.x + box.width - 2, box.y + box.height / 2);
  await expect(players.getByRole("tooltip")).toContainText("3 ");
  await page.mouse.move(1, 1);
  await expect(page.getByRole("tooltip")).toHaveCount(0);
});

test("pointing at a time the server was not running shows nothing", async ({ page }) => {
  const start = Date.now() - 3 * 3600_000;
  const at = (minutes: number) => new Date(start + minutes * 60_000).toISOString();
  const up = (from: number) => Array.from({ length: 120 }, (_, i) => ({ t: at(from + i / 2), cpu: 500, memory: 2 * gib, players: 1 }));
  await usage(page, { available: true, limit: 4 * gib, every: 30, samples: [...up(0), ...up(120)] });
  await page.goto("/o/friends/s/survival/usage");
  const box = (await page.getByRole("region", { name: "CPU" }).getByRole("img").boundingBox())!;
  // Halfway along is in the hour it was stopped.
  await page.mouse.move(box.x + box.width / 2, box.y + box.height / 2);
  await page.mouse.move(box.x + box.width / 2 + 1, box.y + box.height / 2);
  await expect(page.getByRole("tooltip")).toHaveCount(0);
  await page.mouse.move(box.x + 4, box.y + box.height / 2);
  await expect(page.getByRole("tooltip")).toHaveCount(3);
});
