import { readFileSync } from "node:fs";
import type { Locator, Page } from "@playwright/test";
import { expect, test } from "./panel";

// Every picture is chosen with the same chooser: any picture the browser can
// show will do, chosen, dropped or pasted, and what is sent is a PNG of its
// middle, square.

// 300 by 200, red on its left half and blue on its right, as the panel's own
// tests have it.
const webp = readFileSync("../internal/api/testdata/pictures/lossy.webp");
const lossless = readFileSync("../internal/api/testdata/pictures/lossless.webp");
const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");

/**
 * The pictures the page sends, as the width and height each PNG says it
 * has, or null for one that is not a PNG. Read in the page as they are
 * sent: a browser does not always say what a file it sent held.
 */
async function sentPictures(page: Page) {
  await page.addInitScript(() => {
    const sent: (number[] | null)[] = [];
    (window as unknown as { sentPictures: typeof sent }).sentPictures = sent;
    const fetched = window.fetch;
    window.fetch = async (input, init) => {
      if (init?.method === "PUT" && init.body instanceof Blob && /\/(icon|avatar)$/.test(String(input))) {
        const bytes = new Uint8Array(await init.body.arrayBuffer());
        const view = new DataView(bytes.buffer);
        const isPNG = bytes.length >= 24 && bytes[0] === 0x89 && String.fromCharCode(...bytes.subarray(1, 4)) === "PNG";
        sent.push(isPNG ? [view.getUint32(16), view.getUint32(20)] : null);
      }
      return fetched(input, init);
    };
  });
  return () => page.evaluate(() => (window as unknown as { sentPictures: (number[] | null)[] }).sentPictures);
}

/** Drops files, made in the page, on target: dragged over it first, as a hand does. */
async function drop(page: Page, target: Locator, files: { name: string; type: string; bytes: Buffer }[], check?: () => Promise<void>) {
  const dataTransfer = await page.evaluateHandle((files) => {
    const d = new DataTransfer();
    for (const f of files) d.items.add(new File([new Uint8Array(f.bytes)], f.name, { type: f.type }));
    return d;
  }, files.map((f) => ({ ...f, bytes: [...f.bytes] })));
  await target.dispatchEvent("dragenter", { dataTransfer });
  await target.dispatchEvent("dragover", { dataTransfer });
  await check?.();
  await target.dispatchEvent("drop", { dataTransfer });
}

/** The server Survival of friends, with a picture, that takes another. */
async function survival(page: Page) {
  await page.route("**/api/v1/orgs/friends/servers/survival/icon*", (route) =>
    route.request().method() === "GET" ? route.fulfill({ status: 200, contentType: "image/png", body: png }) : route.fulfill({ status: 204 }),
  );
}

test("a WebP is taken for an organization's picture, sent as a square PNG", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  const sent = await sentPictures(page);
  await page.goto(`/o/${slug}/settings`);
  await page.getByLabel(`Picture for Test ${slug}`).setInputFiles({ name: "logo.webp", mimeType: "image/webp", buffer: webp });
  await expect.poll(sent).toEqual([[200, 200]]);
  // The panel took it: it is shown, as the panel drew it.
  const shown = page.getByRole("button", { name: `Picture of Test ${slug}` }).locator("img");
  await expect.poll(() => shown.evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth)).toBe(128);
});

test("a WebP is taken for a server's picture, sent as a square PNG", async ({ page, as }) => {
  await as("owner");
  await survival(page);
  const sent = await sentPictures(page);
  await page.goto("/o/friends/s/survival");
  await page.getByRole("button", { name: "Picture of Survival" }).click();
  const dialog = page.getByRole("dialog", { name: "Survival" });
  await dialog.getByLabel("Picture for Survival").setInputFiles({ name: "icon.webp", mimeType: "image/webp", buffer: lossless });
  await expect.poll(sent).toEqual([[200, 200]]);
});

test("a picture dropped on an organization's picture is used, the first among several, and something else is not", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  const sent = await sentPictures(page);
  await page.goto(`/o/${slug}/settings`);
  const target = page.getByRole("button", { name: `Picture of Test ${slug}` });

  // Not a picture: said so, and nothing is sent.
  await drop(page, target, [{ name: "notes.txt", type: "text/plain", bytes: Buffer.from("hello") }], async () => {
    await expect(page.getByText("Drop to use")).toBeVisible();
  });
  await expect(page.getByText("Not a picture.")).toBeVisible();
  await expect(page.getByText("Drop to use")).toHaveCount(0);
  await page.waitForTimeout(300);
  expect(await sent()).toEqual([]);

  // Several: the first picture among them.
  await drop(page, target, [
    { name: "notes.txt", type: "text/plain", bytes: Buffer.from("hello") },
    { name: "logo.webp", type: "image/webp", bytes: webp },
    { name: "other.png", type: "image/png", bytes: png },
  ]);
  await expect.poll(sent).toEqual([[200, 200]]);
  await expect.poll(() => target.locator("img").evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth)).toBe(128);

  // On the dialog it opens, the dialog's own picker takes it, and only that one.
  await target.click();
  const dialog = page.getByRole("dialog", { name: `Test ${slug}` });
  await drop(page, dialog.getByRole("button", { name: "Replace" }), [{ name: "icon.png", type: "image/png", bytes: png }], async () => {
    await expect(dialog.getByText("Drop to use")).toBeVisible();
  });
  await expect.poll(sent).toEqual([
    [200, 200],
    [1, 1],
  ]);
  await page.waitForTimeout(300);
  expect(await sent()).toHaveLength(2);
});

test("a picture dropped on a server's picture is used, and something else is not", async ({ page, as }) => {
  await as("owner");
  await survival(page);
  const sent = await sentPictures(page);
  await page.goto("/o/friends/s/survival/settings");
  // The picker under General, whose picture also opens a dialog.
  const picker = page.getByLabel("Picture for Survival").locator("..");
  await drop(page, picker, [{ name: "world.zip", type: "application/zip", bytes: Buffer.from("PK") }], async () => {
    await expect(picker.getByText("Drop to use")).toBeVisible();
  });
  await expect(page.getByText("Not a picture.")).toBeVisible();
  await page.waitForTimeout(300);
  expect(await sent()).toEqual([]);
  await drop(page, picker, [{ name: "icon.webp", type: "image/webp", bytes: webp }]);
  await expect.poll(sent).toEqual([[200, 200]]);
  await expect(picker.getByText("Drop to use")).toHaveCount(0);
});

test("a picture pasted while a picture's buttons have focus is used", async ({ page, as }) => {
  await as("owner");
  await survival(page);
  const sent = await sentPictures(page);
  await page.goto("/o/friends/s/survival/settings");
  const upload = page.getByLabel("Picture for Survival").locator("..").getByRole("button", { name: /^(Upload|Replace)$/ });
  await upload.focus();
  const pasted = await upload.evaluate((el, bytes) => {
    const d = new DataTransfer();
    d.items.add(new File([new Uint8Array(bytes)], "icon.webp", { type: "image/webp" }));
    const e = new ClipboardEvent("paste", { clipboardData: d, bubbles: true, cancelable: true });
    // Where the browser does not take what it is given, there is nothing to paste.
    if (!e.clipboardData?.files.length) return false;
    el.dispatchEvent(e);
    return true;
  }, [...webp]);
  test.skip(!pasted, "this browser makes no paste with files of its own");
  await expect.poll(sent).toEqual([[200, 200]]);
});
