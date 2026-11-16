import type { Locator } from "@playwright/test";
import { expect, test } from "./panel";

// The mark is a quartz gate with amethyst light inside, drawn on whole
// pixels. A browser that cannot show the drawn icon (Safari) is given
// pictures of it, and each is really there: an address with nothing behind
// it would be answered with the page.

test("the tab icon is there in each form a browser asks for", async ({ page }) => {
  await page.goto("/login");
  const icons = [
    ['link[rel="icon"][type="image/svg+xml"]', "/favicon.svg", "image/svg+xml"],
    ['link[rel="icon"][sizes="32x32"]', "/favicon-32.png", "image/png"],
    ['link[rel="icon"][sizes="16x16"]', "/favicon-16.png", "image/png"],
    ['link[rel="apple-touch-icon"]', "/apple-touch-icon.png", "image/png"],
  ];
  for (const [selector, href, type] of icons) {
    await expect(page.locator(selector)).toHaveAttribute("href", href);
    const res = await page.request.get(href);
    expect(res.status(), href).toBe(200);
    expect(res.headers()["content-type"], href).toBe(type);
  }
  const svg = await (await page.request.get("/favicon.svg")).text();
  expect(svg).toContain("prefers-color-scheme: light");
});

/** The mark is drawn at a size where each of its cells is two whole pixels. */
async function expectMark(where: Locator) {
  const mark = where.getByRole("img", { name: "avSYS" });
  await expect(mark).toBeVisible();
  const box = await mark.boundingBox();
  expect(box?.height).toBe(32);
  const fills = await mark.locator("path").evaluateAll((paths) => paths.slice(0, 2).map((p) => getComputedStyle(p).fill));
  expect(fills).toEqual(["rgb(238, 243, 242)", "rgb(185, 140, 255)"]);
}

test("the sidebar shows the mark", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends");
  await expectMark(page.locator("aside"));
});

test("the sign-in page shows the mark", async ({ page }) => {
  await page.goto("/login");
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await expectMark(page.locator("body"));
});
