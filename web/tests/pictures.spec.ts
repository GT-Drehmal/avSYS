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

/**
 * Holds every picture the page reads as it is read, until the returned
 * function is called: a large one takes that long.
 */
async function slowPictures(page: Page) {
  await page.addInitScript(() => {
    const w = window as unknown as { pictureRead: Promise<void>; readPictures: () => void };
    w.pictureRead = new Promise<void>((resolve) => (w.readPictures = resolve));
    const read = window.createImageBitmap.bind(window) as (...args: unknown[]) => Promise<ImageBitmap>;
    window.createImageBitmap = (async (...args: unknown[]) => {
      await w.pictureRead;
      return read(...args);
    }) as typeof window.createImageBitmap;
  });
  return () => page.evaluate(() => (window as unknown as { readPictures: () => void }).readPictures());
}

test("a picture still being read holds off every other change to it, here and in its dialog, so the last one asked for is what stays", async ({ page, as }) => {
  await as("owner");
  const read = await slowPictures(page);
  const sent = await sentPictures(page);
  const asked: string[] = [];
  await page.route("**/api/v1/me", async (route) => {
    const me = await (await route.fetch()).json();
    return route.fulfill({ json: { ...me, orgs: me.orgs.map((o: { slug: string }) => (o.slug === "friends" ? { ...o, hasIcon: true } : o)) } });
  });
  await page.route("**/api/v1/orgs/friends/icon*", (route) => {
    const method = route.request().method();
    if (method === "GET") return route.fulfill({ status: 200, contentType: "image/png", body: png });
    asked.push(method);
    return route.fulfill({ status: 204 });
  });
  await page.goto("/o/friends/settings");
  const picker = page.getByLabel("Picture for Friends").locator("..");
  await page.getByLabel("Picture for Friends").setInputFiles({ name: "big.webp", mimeType: "image/webp", buffer: webp });
  // Being read: nothing else can be done to it, on the page or in its dialog.
  await expect(picker.getByRole("button", { name: "Remove" })).toBeDisabled();
  await expect(picker.getByRole("button", { name: "Replace" })).toBeDisabled();
  await page.getByRole("button", { name: "Picture of Friends" }).click();
  const dialog = page.getByRole("dialog", { name: "Friends" });
  await expect(dialog.getByRole("button", { name: "Remove" })).toBeDisabled();
  await dialog.getByLabel("Picture for Friends").setInputFiles({ name: "other.png", mimeType: "image/png", buffer: png });
  await drop(page, dialog.getByRole("button", { name: "Replace" }), [{ name: "third.png", type: "image/png", bytes: png }]);
  await page.waitForTimeout(300);
  expect(asked).toEqual([]);

  // Read, it is the one sent, once; then taken away, it stays away.
  await read();
  await expect.poll(() => asked).toEqual(["PUT"]);
  expect(await sent()).toEqual([[200, 200]]);
  await dialog.getByRole("button", { name: "Remove" }).click();
  await expect.poll(() => asked).toEqual(["PUT", "DELETE"]);
  await page.waitForTimeout(500);
  expect(asked).toEqual(["PUT", "DELETE"]);
});

test("a copy's picture still being read when it is taken away stays taken away", async ({ page, as }) => {
  await as("owner");
  const read = await slowPictures(page);
  const made: { picture?: string | null }[] = [];
  await page.route("**/api/v1/orgs/friends/servers", (route) => {
    if (route.request().method() !== "POST") return route.continue();
    made.push(route.request().postDataJSON());
    return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "test", message: "Not sent by the test." }) });
  });
  await page.goto("/o/friends/new?from=server:survival");
  await page.getByLabel("Picture for survival copy").setInputFiles({ name: "big.webp", mimeType: "image/webp", buffer: webp });
  await page.getByRole("button", { name: "Remove", exact: true }).click();
  await expect(page.getByText("No picture.")).toBeVisible();
  // Read now, it is too late: it was taken away after it was chosen.
  await read();
  await page.waitForTimeout(500);
  await expect(page.getByText("No picture.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Choose a picture" })).toBeVisible();
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  await expect.poll(() => made.length).toBe(1);
  expect(made[0].picture).toBeNull();
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
