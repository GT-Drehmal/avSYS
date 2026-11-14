import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// The test panel has an OpenID Connect provider of its own, "Test ID",
// whose page signs in whoever is typed on it. Its "outsider" is linked to
// the panel's outsider.

/** On the provider's page, signs in as login. */
async function atProvider(page: Page, login: string) {
  await expect(page).toHaveTitle("Test ID");
  await page.getByLabel("Username").fill(login);
  await page.getByRole("button", { name: "Continue" }).click();
}

test("signing in through the install's own provider", async ({ page }) => {
  await page.goto("/login?next=/account");
  await page.getByRole("link", { name: "Continue with Test ID" }).click();
  await atProvider(page, "outsider");
  await expect(page).toHaveURL(/\/account$/);
  const me = await (await page.request.get("/api/v1/me")).json();
  expect(me.user.username).toBe("outsider");
  // The account shows the provider as linked.
  const signIn = page.locator("section").filter({ hasText: "Sign-in" });
  await expect(signIn.getByText("Test ID")).toBeVisible();
  await expect(signIn.getByText("outsider", { exact: true })).toBeVisible();
});

test("someone the panel does not know is not let in without an invite", async ({ page }) => {
  await page.goto("/login");
  await page.getByRole("link", { name: "Continue with Test ID" }).click();
  await atProvider(page, "stranger");
  await expect(page).toHaveURL(/\/login\?error=/);
  await expect(page.getByText("You need an invite link to create an account here.")).toBeVisible();
  // The password form is still there.
  await expect(page.getByLabel("Password")).toBeVisible();
});

test("a provider account that is someone else's is not linked", async ({ page, as }) => {
  await as("viewer");
  await page.goto("/account");
  const signIn = page.locator("section").filter({ hasText: "Sign-in" });
  await signIn.getByRole("link", { name: "Link account" }).click();
  await atProvider(page, "outsider");
  await expect(page).toHaveURL(/\/account\?error=/);
  await expect(signIn.getByText("That Test ID account is linked to someone else here.")).toBeVisible();
  await expect(signIn.getByRole("link", { name: "Link account" })).toBeVisible();
  const me = await (await page.request.get("/api/v1/me")).json();
  expect(me.user.username).toBe("viewer");
});
