import type { Page } from "@playwright/test";
import type { Invite } from "../src/lib/api";
import { expect, test } from "./panel";

// A panel on a home network is often reached over plain http at an address
// that is not the browser's own machine. There the page is not a secure
// context and has no navigator.clipboard, and the copy buttons have to copy
// another way. Here the panel is opened at such an address, and what the
// page asks of it is answered by the test panel.

const insecure = "http://avsys.test:8099";

/** What each copy put on the clipboard, as the page copied it. */
const copied = (page: Page) => page.evaluate(() => (window as unknown as { copied: string[] }).copied);

test.beforeEach(async ({ page, as, baseURL }) => {
  await as("owner");
  // Fetched here and handed to the page, which keeps its address: WebKit
  // would follow a request sent on elsewhere to where it went. The live
  // updates never end, so are not fetched: the page does without them.
  await page.route(`${insecure}/**`, async (route) => {
    const url = route.request().url().replace(insecure, baseURL ?? "");
    if (new URL(url).pathname === "/api/v1/events") return route.fulfill({ status: 204 });
    return route.fulfill({ response: await route.fetch({ url }) });
  });
  await page.addInitScript(() => {
    const w = window as unknown as { copied: string[] };
    w.copied = [];
    document.addEventListener(
      "copy",
      () => {
        const field = document.activeElement;
        if (field instanceof HTMLTextAreaElement) w.copied.push(field.value.slice(field.selectionStart, field.selectionEnd));
      },
      true,
    );
  });
});

test("the page under test has no clipboard API", async ({ page }) => {
  await page.goto(`${insecure}/o/friends`);
  await expect(page.getByRole("heading", { name: "Servers" })).toBeVisible();
  expect(await page.evaluate(() => [window.isSecureContext, !!navigator.clipboard])).toEqual([false, false]);
});

test("an address copies from a list over plain http, without opening the server", async ({ page }) => {
  await page.goto(`${insecure}/o/friends`);
  const address = page.getByRole("button", { name: "survival.friends.play.test" });
  await address.click();
  await expect.poll(() => copied(page)).toEqual(["survival.friends.play.test"]);
  await expect(page).toHaveURL(`${insecure}/o/friends`);
  await expect(address).toBeFocused();
  await expect(page.getByText("Could not copy")).toHaveCount(0);
});

test("an invite link copies from its dialog over plain http, and the dialog keeps the focus", async ({ page, api, org }) => {
  const slug = await org([]);
  await page.goto(`${insecure}/o/${slug}/members`);
  await page.getByRole("button", { name: "Invite", exact: true }).click();
  await page.getByRole("button", { name: "Create link" }).click();
  const dialog = page.getByRole("dialog", { name: "Invite link" });
  await expect(dialog).toBeVisible();
  const [invite] = await api("owner").get<Invite[]>(`/orgs/${slug}/invites`);
  const link = `${insecure}/invite/${invite.token}`;
  const copy = dialog.getByRole("button", { name: link });
  await copy.click();
  await expect.poll(() => copied(page)).toEqual([link]);
  await expect(copy).toBeFocused();
  await expect(page.getByText("Could not copy")).toHaveCount(0);
  // Still open, and closed as a dialog is.
  await page.keyboard.press("Escape");
  await expect(dialog).toBeHidden();

  // And from the list of links, once it is closed.
  await page.getByRole("button", { name: `…/invite/${invite.token.slice(0, 10)}…` }).click();
  await expect.poll(() => copied(page)).toEqual([link, link]);
});
