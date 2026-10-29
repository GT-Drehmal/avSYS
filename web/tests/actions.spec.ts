import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// Start, stop and restart each wait for the one before to take. A game that
// is loading stops only once it has, which can take a minute, and a button
// clicked again meanwhile would undo it: Start appears where Stop was.

test.beforeEach(async ({ as }) => {
  await as("owner");
});

/**
 * survival as the panel would have it: its state as asked, and its phase as
 * the controller would say it, set by the test. Actions are recorded and
 * change its state, not its phase.
 */
async function survival(page: Page, phase: string, message = "") {
  const s = { state: "Running", phase, message, asked: [] as string[] };
  const shape = (server: any) => ({ ...server, spec: { ...server.spec, state: s.state }, status: { ...server.status, phase: s.phase, message: s.message } });
  await page.route("**/api/v1/orgs/friends/servers", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    return route.fulfill({ json: (await (await route.fetch()).json()).map((x: any) => (x.name === "survival" ? shape(x) : x)) });
  });
  await page.route("**/api/v1/orgs/friends/servers/survival", async (route) => route.fulfill({ json: shape(await (await route.fetch()).json()) }));
  await page.route(/\/api\/v1\/orgs\/friends\/servers\/survival\/(start|stop|restart)$/, async (route) => {
    const action = new URL(route.request().url()).pathname.split("/").pop() ?? "";
    s.asked.push(action);
    if (action === "stop") s.state = "Stopped";
    if (action === "start") s.state = "Running";
    const server = await (await page.request.get("/api/v1/orgs/friends/servers/survival")).json();
    return route.fulfill({ json: shape(server) });
  });
  return s;
}

const header = (page: Page) => page.locator("header, main").first();

test("a stop clicked twice does not start the server again: Start is offered once it has stopped", async ({ page }) => {
  const s = await survival(page, "Starting");
  await page.goto("/o/friends/s/survival");
  const stop = header(page).getByRole("button", { name: "Stop", exact: true });
  await stop.click();
  // Where Stop was, it says Stopping, and cannot be clicked.
  const stopping = header(page).getByRole("button", { name: "Stopping" });
  await expect(stopping).toBeVisible();
  await expect(stopping).toBeDisabled();
  await expect(header(page).getByRole("button", { name: "Start", exact: true })).toHaveCount(0);
  await stopping.click({ force: true });
  expect(s.asked).toEqual(["stop"]);

  // Still loading, it says why it takes a while.
  s.phase = "Stopping";
  s.message = "The game is still loading. The server stops once the game has loaded.";
  await page.reload();
  await expect(page.getByText("The game is still loading. The server stops once the game has loaded.")).toBeVisible();
  await expect(header(page).getByRole("button", { name: "Stopping" })).toBeDisabled();

  // Stopped, it can be started.
  s.phase = "Stopped";
  s.message = "";
  await page.reload();
  await header(page).getByRole("button", { name: "Start", exact: true }).click();
  await expect.poll(() => s.asked).toEqual(["stop", "start"]);
});

test("a restart is not asked for again while one is under way, but the server can still be stopped", async ({ page }) => {
  const s = await survival(page, "Running");
  await page.goto("/o/friends/s/survival");
  const restart = header(page).getByRole("button", { name: "Restart", exact: true });
  await restart.click();
  await expect.poll(() => s.asked).toEqual(["restart"]);
  // The game says Running until it has been told to stop: not again meanwhile.
  await expect(restart).toBeDisabled();
  // Starting again, still not.
  s.phase = "Starting";
  await page.reload();
  await expect(header(page).getByRole("button", { name: "Restart", exact: true })).toBeDisabled();
  await header(page).getByRole("button", { name: "Stop", exact: true }).click();
  await expect.poll(() => s.asked).toEqual(["restart", "stop"]);
});

test("a server that is running or has crashed can be restarted", async ({ page }) => {
  const s = await survival(page, "Error", "The server keeps crashing (3 restarts). Check the console for the cause.");
  await page.goto("/o/friends/s/survival");
  await header(page).getByRole("button", { name: "Restart", exact: true }).click();
  await expect.poll(() => s.asked).toEqual(["restart"]);
});

test("in the list, a stopping server has no Start, and a starting one no restart", async ({ page }) => {
  const s = await survival(page, "Starting");
  await page.goto("/o/friends");
  const row = page.getByRole("link", { name: /Survival/ }).first();
  await expect(row.getByRole("button", { name: "Restart survival" })).toBeDisabled();
  await row.getByRole("button", { name: "Stop", exact: true }).click();
  await expect(row.getByRole("button", { name: "Stopping" })).toBeDisabled();
  await expect(row.getByRole("button", { name: "Start", exact: true })).toHaveCount(0);
  expect(s.asked).toEqual(["stop"]);
});
