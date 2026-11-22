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

test("with passwords off, two-factor stays, and a reset link only turns it off", async ({ page, as }) => {
  await as("owner");
  await passwordsOff(page);
  await page.goto("/account");
  await expect(page.getByRole("heading", { name: "Sessions" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Two-factor" })).toBeVisible();
  // No password to give for it, though the account has one from before.
  await page.locator("section").filter({ has: page.getByRole("heading", { name: "Two-factor" }) }).getByRole("button", { name: "Set up" }).click();
  await expect(page.getByRole("dialog").getByLabel("Password")).toHaveCount(0);
  await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();

  // The reset link is for two-factor alone.
  let asked: unknown = null;
  await page.route("**/api/v1/users/*/reset-link", async (route) => {
    asked = route.request().postDataJSON();
    await route.fulfill({ status: 201, json: { url: "http://127.0.0.1/reset/made-up", expiresAt: new Date().toISOString() } });
  });
  await page.goto("/o/friends/members");
  await page.getByRole("button", { name: "Reset sign-in for viewer" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("Reset two-factor for Viewer?")).toBeVisible();
  await expect(dialog.getByText("Makes a link that turns their two-factor off.")).toBeVisible();
  await expect(dialog.getByRole("switch")).toHaveCount(0);
  await dialog.getByRole("button", { name: "Make link" }).click();
  await expect.poll(() => asked).toEqual({ twoFactorOff: true });
  await expect(dialog.getByText("It works once, for 24 hours.")).toBeVisible();

  // The link's page turns it off, with no password to set.
  await page.route("**/api/v1/reset-links/two-factor-only", (route) =>
    route.request().method() === "GET"
      ? route.fulfill({ json: { username: "viewer", displayName: "Viewer", newPassword: false, twoFactorOff: true } })
      : route.fulfill({ json: { username: "viewer" } }),
  );
  await page.goto("/reset/two-factor-only");
  await expect(page.getByRole("heading", { name: "Turn off two-factor" })).toBeVisible();
  await expect(page.getByLabel("New password")).toHaveCount(0);
  await page.getByRole("button", { name: "Turn off two-factor" }).click();
  await expect(page.getByRole("heading", { name: "Two-factor is off" })).toBeVisible();

  // A link made before passwords were turned off, to set one, says why it does not work.
  await page.route("**/api/v1/reset-links/some-old-link", (route) =>
    route.fulfill({ status: 403, json: { code: "passwords_off", message: "Passwords are turned off here." } }),
  );
  await page.goto("/reset/some-old-link");
  await expect(page.getByRole("heading", { name: "Passwords are turned off here" })).toBeVisible();
  await expect(page.getByLabel("New password")).toHaveCount(0);
});

