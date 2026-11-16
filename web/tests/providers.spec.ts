import type { Locator, Page } from "@playwright/test";
import { expect, test } from "./panel";

// Each sign-in provider is shown with its mark wherever it is named: on the
// sign-in page, in the account's sign-ins and in Instance settings. The
// test panel has only its own OpenID Connect provider ("Test ID"); the
// others are added by answering /meta as an install with them would.

const providers = [
  { id: "github", name: "GitHub" },
  { id: "discord", name: "Discord" },
  { id: "google", name: "Google" },
  { id: "oidc", name: "Test ID" },
];

async function allProviders(page: Page) {
  await page.route("**/api/v1/meta", async (route) => {
    const meta = await (await route.fetch()).json();
    await route.fulfill({ json: { ...meta, providers } });
  });
}

/** The provider's mark inside where, hidden from assistive technology. */
async function expectIcon(where: Locator, id: string) {
  const icon = where.locator(`svg[data-provider-icon="${id}"]`);
  await expect(icon).toBeVisible();
  await expect(icon).toHaveAttribute("aria-hidden", "true");
  const box = await icon.boundingBox();
  expect(box?.width).toBe(16);
  expect(box?.height).toBe(16);
}

/** The colour each part of the provider's mark is filled with. */
const fills = (where: Locator, id: string) =>
  where.locator(`svg[data-provider-icon="${id}"] path`).evaluateAll((paths) => paths.map((p) => getComputedStyle(p).fill));

const googleColours = ["rgb(234, 67, 53)", "rgb(66, 133, 244)", "rgb(251, 188, 5)", "rgb(52, 168, 83)"];

for (const scheme of ["dark", "light"] as const) {
  test(`the sign-in page shows each provider's mark, ${scheme}`, async ({ page }) => {
    await page.emulateMedia({ colorScheme: scheme });
    await allProviders(page);
    await page.goto("/login");
    for (const p of providers) {
      const link = page.getByRole("link", { name: `Continue with ${p.name}`, exact: true });
      await expect(link).toBeVisible();
      await expect(link).toHaveText(`Continue with ${p.name}`);
      await expectIcon(link, p.id);
      // Left of the text, and in line with it.
      const icon = (await link.locator("svg").boundingBox())!;
      const text = await link.evaluate((a) => {
        const r = document.createRange();
        r.setStartAfter(a.querySelector("svg")!);
        r.setEndAfter(a.lastChild!);
        const b = r.getBoundingClientRect();
        return { x: b.x, y: b.y, height: b.height };
      });
      expect(icon.x + icon.width).toBeLessThanOrEqual(text.x);
      expect(Math.abs(icon.y + icon.height / 2 - (text.y + text.height / 2))).toBeLessThanOrEqual(1.5);
    }
    // GitHub's and Discord's are drawn in the button's text colour, so they follow the theme.
    for (const id of ["github", "discord"]) {
      const link = page.locator(`a:has(svg[data-provider-icon="${id}"])`);
      const colour = await link.evaluate((a) => getComputedStyle(a).color);
      expect(await fills(link, id)).toEqual([colour]);
    }
    // Google's "G" keeps its own four colours in either theme.
    expect(await fills(page.locator("a", { hasText: "Google" }), "google")).toEqual(googleColours);
  });
}

test("the account's sign-ins show each provider's mark", async ({ page, as }) => {
  await as("owner");
  await allProviders(page);
  await page.goto("/account");
  const list = page.locator("section").filter({ has: page.getByRole("heading", { name: "Sign-in", exact: true }) }).getByRole("listitem");
  await expect(list).toHaveCount(providers.length);
  for (const [i, p] of providers.entries()) {
    await expect(list.nth(i)).toContainText(p.name);
    await expectIcon(list.nth(i), p.id);
  }
});

test("Instance settings shows each provider with its mark", async ({ page, as }) => {
  await as("owner");
  await allProviders(page);
  await page.goto("/settings");
  const signIn = page.getByRole("region", { name: "Sign-in" });
  const ways = signIn.getByRole("definition").nth(1).getByRole("listitem");
  await expect(ways).toHaveText(providers.map((p) => p.name));
  for (const [i, p] of providers.entries()) await expectIcon(ways.nth(i), p.id);
});
