import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// A running game reads its picture only as it starts: a picture changed
// while it runs waits for a restart, as new settings do. A server that is
// not running shows it as it is.

const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");

/**
 * A friends server whose picture can be changed, every change kept; once
 * one is made, the server is said to wait for a restart for it, as the
 * panel says of a running one.
 */
async function pictured(page: Page, server: string) {
  const asked: string[] = [];
  await page.route(`**/api/v1/orgs/friends/servers/${server}/icon*`, (route) => {
    const method = route.request().method();
    if (method === "GET") return route.fulfill({ status: 200, contentType: "image/png", body: png });
    asked.push(method);
    return route.fulfill({ status: 204 });
  });
  await page.route("**/api/v1/orgs/friends/servers", async (route) => {
    const servers = await (await route.fetch()).json();
    if (asked.length === 0 || route.request().method() !== "GET") return route.fulfill({ json: servers });
    return route.fulfill({
      json: servers.map((s: { name: string; status: Record<string, unknown> }) => (s.name === server ? { ...s, status: { ...s.status, pendingRestart: true, pendingPicture: true } } : s)),
    });
  });
  return asked;
}

test("a running server's picture, changed, says players see it after a restart, and the server waits for one", async ({ page, as }) => {
  await as("owner");
  const asked = await pictured(page, "survival");
  await page.goto("/o/friends/s/survival");
  await expect(page.getByText("Restart to apply pending changes.")).toHaveCount(0);
  await page.getByRole("button", { name: "Picture of Survival" }).click();
  const dialog = page.getByRole("dialog", { name: "Survival" });
  await expect(dialog.getByText("Shown in the server list.")).toBeVisible();
  await expect(dialog.getByText("Players see it after a restart.")).toHaveCount(0);
  await dialog.getByLabel("Picture for Survival").setInputFiles({ name: "new.png", mimeType: "image/png", buffer: png });
  await expect.poll(() => asked).toEqual(["PUT"]);
  // Said once, in place of what it said before.
  await expect(dialog.getByText("Players see it after a restart.")).toHaveCount(1);
  await expect(dialog.getByText("Shown in the server list.")).toHaveCount(0);
  // Taken off, the same, still once.
  await dialog.getByRole("button", { name: "Remove" }).click();
  await expect.poll(() => asked).toEqual(["PUT", "DELETE"]);
  await expect(dialog.getByText("Players see it after a restart.")).toHaveCount(1);
  await page.keyboard.press("Escape");

  // The server waits for a restart, as for new settings, here and in the list.
  await page.reload();
  const bar = page.getByText("Restart to apply pending changes.");
  await expect(bar).toBeVisible();
  await expect(page.getByRole("button", { name: "Restart now" })).toBeVisible();
  await page.goto("/o/friends");
  await expect(page.getByText("restart to apply changes")).toBeVisible();
});

test("a stopped server's picture, changed, says nothing of a restart", async ({ page, as }) => {
  await as("owner");
  const asked: string[] = [];
  await page.route("**/api/v1/orgs/friends/servers/creative/icon*", (route) => {
    const method = route.request().method();
    if (method === "GET") return route.fulfill({ status: 200, contentType: "image/png", body: png });
    asked.push(method);
    return route.fulfill({ status: 204 });
  });
  await page.goto("/o/friends/s/creative/settings");
  const picker = page.getByLabel("Picture for Creative").locator("..");
  await expect(picker.getByText("Shown in the server list.")).toBeVisible();
  await page.getByLabel("Picture for Creative").setInputFiles({ name: "new.png", mimeType: "image/png", buffer: png });
  await expect.poll(() => asked).toEqual(["PUT"]);
  await page.waitForTimeout(300);
  await expect(page.getByText("Players see it after a restart.")).toHaveCount(0);
  await expect(picker.getByText("Shown in the server list.")).toBeVisible();
  await expect(page.getByText("Restart to apply pending changes.")).toHaveCount(0);
});

test("a running server's picture, changed in its settings, says players see it after a restart", async ({ page, as }) => {
  await as("owner");
  const asked = await pictured(page, "survival");
  await page.goto("/o/friends/s/survival/settings");
  await page.getByLabel("Picture for Survival").setInputFiles({ name: "new.png", mimeType: "image/png", buffer: png });
  await expect.poll(() => asked).toEqual(["PUT"]);
  await expect(page.getByText("Players see it after a restart.")).toHaveCount(1);
});
