import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// An install can turn passwords off, so that everyone signs in through a
// provider. The test panel has passwords on; what the pages do with them
// off is seen by answering /meta as such a panel would.

async function passwordsOff(page: Page, more: Record<string, unknown> = {}) {
  await page.route("**/api/v1/meta", async (route) => {
    const meta = await (await route.fetch()).json();
    return route.fulfill({ json: { ...meta, passwords: false, ...more } });
  });
}

test("with passwords on, the sign-in page has both", async ({ page }) => {
  await page.goto("/login");
  await expect(page.getByRole("link", { name: "Continue with Test ID" })).toBeVisible();
  await expect(page.getByLabel("Password")).toBeVisible();
  await expect(page.getByText("or", { exact: true })).toBeVisible();
});

test("with passwords off, the sign-in page offers only the providers", async ({ page }) => {
  await passwordsOff(page);
  await page.goto("/login?next=/account");
  await expect(page.getByRole("link", { name: "Continue with Test ID" })).toBeVisible();
  await expect(page.getByLabel("Password")).toHaveCount(0);
  await expect(page.getByLabel("Username")).toHaveCount(0);
  await expect(page.getByText("or", { exact: true })).toHaveCount(0);
  // And signing in through one works.
  await page.getByRole("link", { name: "Continue with Test ID" }).click();
  await expect(page).toHaveTitle("Test ID");
  await page.getByLabel("Username").fill("outsider");
  await page.getByRole("button", { name: "Continue" }).click();
  await expect(page).toHaveURL(/\/account$/);
  // The account page has no password to set.
  const signIn = page.locator("section").filter({ hasText: "Sign-in" });
  await expect(signIn.getByText("Test ID")).toBeVisible();
  await expect(signIn.getByLabel(/password/i)).toHaveCount(0);
  await expect(signIn.getByRole("button", { name: /password/i })).toHaveCount(0);
});

test("with passwords off and open signups, there is no form to create an account", async ({ page }) => {
  await passwordsOff(page, { allowSignups: true });
  await page.goto("/login");
  await expect(page.getByRole("link", { name: "Continue with Test ID" })).toBeVisible();
  await expect(page.getByRole("button", { name: /create an account/i })).toHaveCount(0);
});

test("the first run with passwords off asks for the link from the log", async ({ page }) => {
  await passwordsOff(page, { setupRequired: true });
  await page.goto("/login");
  await expect(page.getByRole("heading", { name: "Set up avSYS" })).toBeVisible();
  await expect(page.getByText("Open the setup link from the panel's log.")).toBeVisible();
  await expect(page.getByRole("link", { name: /Continue with/ })).toHaveCount(0);
  await expect(page.getByLabel("Password")).toHaveCount(0);

  await page.goto("/login?setup=abc123");
  const link = page.getByRole("link", { name: "Continue with Test ID" });
  await expect(link).toBeVisible();
  await expect(link).toHaveAttribute("href", /[?&]setup=abc123(&|$)/);
  await expect(page.getByText("Open the setup link from the panel's log.")).toHaveCount(0);
  await expect(page.getByLabel("Password")).toHaveCount(0);
});

test("the first run with passwords on still makes the first account with a password", async ({ page }) => {
  await page.route("**/api/v1/meta", async (route) => {
    const meta = await (await route.fetch()).json();
    return route.fulfill({ json: { ...meta, setupRequired: true } });
  });
  await page.goto("/login");
  await expect(page.getByRole("heading", { name: "Set up avSYS" })).toBeVisible();
  await expect(page.getByLabel("Password")).toBeVisible();
  await expect(page.getByRole("button", { name: "Create account" })).toBeVisible();
});
