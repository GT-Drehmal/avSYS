import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// The file editor saves only over the file it opened.

test.beforeEach(async ({ as }) => {
  await as("owner");
});

/**
 * survival's files: server.properties, as the panel would serve it, with a
 * tag for what it holds. Saves are kept, with the tag each was sent with;
 * one sent with another tag than the file's is refused, as the panel does.
 */
async function files(page: Page) {
  const state = { text: "motd=hello\n", gone: false, saves: [] as { text: string; tag: string | null }[] };
  const tagOf = (text: string) => `"${text.length}-${[...text].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 7)}"`;
  await page.route("**/api/v1/orgs/friends/servers/survival/files?*", (route) =>
    route.fulfill({ json: { path: "/", entries: [{ name: "server.properties", size: state.text.length, dir: false, modTime: new Date().toISOString() }] } }),
  );
  await page.route("**/api/v1/orgs/friends/servers/survival/files/content?*", async (route) => {
    const req = route.request();
    if (req.method() === "GET") {
      if (state.gone) return route.fulfill({ status: 404, json: { code: "not_found", message: "No such file." } });
      return route.fulfill({ body: state.text, headers: { "Content-Type": "application/octet-stream", ETag: tagOf(state.text) } });
    }
    const tag = (await req.headerValue("If-Match")) ?? null;
    const text = req.postData() ?? "";
    state.saves.push({ text, tag });
    if (tag && state.gone) return route.fulfill({ status: 412, json: { code: "removed", message: "This file was removed since you opened it." } });
    if (tag && tag !== tagOf(state.text)) return route.fulfill({ status: 412, json: { code: "changed", message: "This file was changed since you opened it." } });
    state.text = text;
    state.gone = false;
    return route.fulfill({ status: 204, headers: { ETag: tagOf(text) } });
  });
  return { state, tagOf };
}

async function edit(page: Page, text: string) {
  await page.locator(".cm-content").click();
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.type(text);
}

const open = (page: Page) => page.goto("/o/friends/s/survival/files?file=%2Fserver.properties");

test("a save is sent with the tag of the file opened, and the next with that of the one saved", async ({ page }) => {
  const { state, tagOf } = await files(page);
  await open(page);
  await expect(page.locator(".cm-content")).toContainText("motd=hello");
  await edit(page, "pvp=false");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByText("unsaved")).toHaveCount(0);
  await edit(page, "\ndifficulty=hard");
  await page.keyboard.press("ControlOrMeta+s");
  await expect.poll(() => state.saves.length).toBe(2);
  expect(state.saves[0].tag).toBe(tagOf("motd=hello\n"));
  expect(state.saves[1].tag).toBe(tagOf("motd=hello\npvp=false"));
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("a file changed since it was opened is not saved over; reloaded, it shows the change", async ({ page }) => {
  const { state } = await files(page);
  await open(page);
  await expect(page.locator(".cm-content")).toContainText("motd=hello");
  // Someone else saves it meanwhile.
  state.text = "motd=from a friend\n";
  await edit(page, "pvp=false");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("This file was changed since you opened it.");
  expect(state.text).toBe("motd=from a friend\n");
  await expect(page.getByText("unsaved")).toBeVisible();
  await page.getByRole("button", { name: "Reload" }).click();
  await expect(page.locator(".cm-content")).toHaveText("motd=from a friend");
  await expect(page.getByText("unsaved")).toHaveCount(0);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("saved anyway, it is written over the change", async ({ page }) => {
  const { state } = await files(page);
  await open(page);
  await expect(page.locator(".cm-content")).toContainText("motd=hello");
  state.text = "motd=from a friend\n";
  await edit(page, "pvp=false");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByRole("button", { name: "Save anyway" }).click();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(page.getByText("unsaved")).toHaveCount(0);
  expect(state.saves.at(-1)).toEqual({ text: "motd=hello\npvp=false", tag: null });
  expect(state.text).toBe("motd=hello\npvp=false");
});

test("a file removed since it was opened is said so, and can be saved again", async ({ page }) => {
  const { state } = await files(page);
  await open(page);
  await expect(page.locator(".cm-content")).toContainText("motd=hello");
  state.gone = true;
  await edit(page, "pvp=false");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("This file was removed since you opened it.");
  await expect(page.getByRole("button", { name: "Reload" })).toHaveCount(0);
  await page.getByRole("button", { name: "Save anyway" }).click();
  await expect(page.getByRole("alert")).toHaveCount(0);
  expect(state.gone).toBe(false);
});

test("a file read again behind the editor, as on reconnecting, is not what a save is checked against", async ({ page }) => {
  const { state, tagOf } = await files(page);
  await open(page);
  await expect(page.locator(".cm-content")).toContainText("motd=hello");
  state.text = "motd=from a friend\n";
  await page.context().setOffline(true);
  await page.context().setOffline(false);
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await edit(page, "pvp=false");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("This file was changed since you opened it.");
  expect(state.saves[0].tag).toBe(tagOf("motd=hello\n"));
  expect(state.text).toBe("motd=from a friend\n");
});

test("a reload that fails keeps what was typed", async ({ page }) => {
  const { state } = await files(page);
  await open(page);
  await expect(page.locator(".cm-content")).toContainText("motd=hello");
  state.text = "motd=from a friend\n";
  await edit(page, "pvp=false");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  // Then removed too, before it is reloaded.
  state.gone = true;
  await page.getByRole("button", { name: "Reload" }).click();
  await expect(page.getByText("No such file.")).toBeVisible();
  await expect(page.locator(".cm-content")).toContainText("pvp=false");
  await expect(page.getByText("unsaved")).toBeVisible();
  await page.getByRole("button", { name: "Save anyway" }).click();
  await expect(page.getByText("unsaved")).toHaveCount(0);
  expect(state.text).toBe("motd=hello\npvp=false");
});
