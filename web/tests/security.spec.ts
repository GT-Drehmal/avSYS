import type { APIRequestContext, Page } from "@playwright/test";
import { createHmac, randomUUID } from "node:crypto";
import { type Api, expect, test } from "./panel";

// Sessions, two-factor and reset links, on accounts made for each test, so
// that what one test turns on no other sees.

const password = "a fine password";

/** A new person with a password, in a new organization of the owner's, signed in on this page. */
async function newPerson(page: Page, api: (who: "owner") => Api, org: (servers: string[]) => Promise<string>) {
  const slug = await org([]);
  const invite = await api("owner").post<{ token: string }>(`/orgs/${slug}/invites`, { role: "viewer", expiresInHours: 1, maxUses: 1 });
  const username = `u${randomUUID().slice(0, 10)}`;
  await signUp(page.request, username, invite.token);
  return { username, slug };
}

async function signUp(request: APIRequestContext, username: string, invite: string) {
  const res = await request.post("/api/v1/auth/register", { headers: { "X-Avsys": "1" }, data: { username, password, invite } });
  expect(res.ok(), await res.text()).toBe(true);
}

async function signIn(request: APIRequestContext, username: string, pw = password) {
  const res = await request.post("/api/v1/auth/login", { headers: { "X-Avsys": "1" }, data: { username, password: pw } });
  expect(res.ok(), await res.text()).toBe(true);
}

const signedIn = async (request: APIRequestContext) => (await request.get("/api/v1/me")).ok();

/** The code an authenticator app shows for a base32 secret (RFC 6238: SHA-1, six digits, 30 seconds). */
function totp(secret: string, at = Date.now()) {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const c of secret.replace(/=+$/, "")) bits += alphabet.indexOf(c).toString(2).padStart(5, "0");
  const key = Buffer.from(bits.match(/.{8}/g)!.map((b) => parseInt(b, 2)));
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(at / 1000 / 30)));
  const mac = createHmac("sha1", key).update(counter).digest();
  const offset = mac[mac.length - 1] & 0xf;
  return String((mac.readUInt32BE(offset) & 0x7fffffff) % 1_000_000).padStart(6, "0");
}

test("two-factor is set up with a code, then asked for after the password", async ({ page, api, org }) => {
  const { username } = await newPerson(page, api, org);
  await page.goto("/account");
  const section = page.locator("section").filter({ has: page.getByRole("heading", { name: "Two-factor" }) });
  await section.getByRole("button", { name: "Set up" }).click();
  const dialog = page.getByRole("dialog");
  // The password first; a wrong one is said so.
  await dialog.getByLabel("Password").fill("not it");
  await dialog.getByRole("button", { name: "Continue" }).click();
  await expect(page.getByText("The current password is wrong.")).toBeVisible();
  await dialog.getByLabel("Password").fill(password);
  const setup = page.waitForResponse((r) => r.url().endsWith("/api/v1/me/two-factor/setup") && r.ok());
  await dialog.getByRole("button", { name: "Continue" }).click();
  const { secret } = await (await setup).json();
  await expect(dialog.getByRole("img", { name: "QR code" })).toBeVisible();
  await expect(dialog.getByText(secret)).toBeVisible();
  // A wrong code, then the right one.
  await dialog.getByLabel("Code").fill("000000");
  await dialog.getByRole("button", { name: "Turn on" }).click();
  await expect(page.getByText("Wrong code. Try again.")).toBeVisible();
  await dialog.getByLabel("Code").fill(totp(secret));
  await dialog.getByRole("button", { name: "Turn on" }).click();
  const codes = dialog.getByRole("list", { name: "Recovery codes" }).getByRole("listitem");
  await expect(codes).toHaveCount(10);
  const recovery = (await codes.first().textContent())!;
  await dialog.getByRole("button", { name: "Done" }).click();
  await expect(section.getByText("On", { exact: true })).toBeVisible();
  await expect(section.getByText("10 recovery codes left")).toBeVisible();

  // Signing in again asks for a code after the password.
  await page.request.post("/api/v1/auth/logout", { headers: { "X-Avsys": "1" } });
  await page.goto("/login?next=/account");
  await page.getByLabel("Username").fill(username);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByLabel("Code")).toBeVisible();
  expect(await signedIn(page.request)).toBe(false);
  await page.getByLabel("Code").fill("123456");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByText("Wrong code. Try again.")).toBeVisible();
  // A recovery code does, once.
  await page.getByLabel("Code").fill(recovery);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).toHaveURL(/\/account$/);
  await expect(section.getByText("9 recovery codes left")).toBeVisible();

  // Turning it off takes the password.
  await section.getByRole("button", { name: "Turn off" }).click();
  await page.getByRole("dialog").getByLabel("Password").fill(password);
  await page.getByRole("dialog").getByRole("button", { name: "Turn off" }).click();
  await expect(section.getByRole("button", { name: "Set up" })).toBeVisible();
});

test("going back from the code starts the sign-in again", async ({ page, api, org }) => {
  const { username } = await newPerson(page, api, org);
  const setup = await page.request.post("/api/v1/me/two-factor/setup", { headers: { "X-Avsys": "1" }, data: { password } });
  const { secret } = await setup.json();
  expect((await page.request.post("/api/v1/me/two-factor", { headers: { "X-Avsys": "1" }, data: { code: totp(secret) } })).ok()).toBe(true);
  await page.request.post("/api/v1/auth/logout", { headers: { "X-Avsys": "1" } });
  await page.goto("/login");
  await page.getByLabel("Username").fill(username);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await page.getByRole("button", { name: "Back" }).click();
  await expect(page.getByLabel("Password")).toHaveValue("");
  await expect(page.getByLabel("Username")).toHaveValue(username);
  await expect(page.getByLabel("Code")).toHaveCount(0);
});

test("sessions are listed, and signed out one by one or all but this one", async ({ page, browser, api, org, baseURL }) => {
  const { username } = await newPerson(page, api, org);
  const phone = await browser.newContext({ baseURL, userAgent: "Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.0 Mobile/15E148 Safari/604.1" });
  try {
    await signIn(phone.request, username);
    await page.goto("/account");
    const section = page.locator("section").filter({ has: page.getByRole("heading", { name: "Sessions" }) });
    const rows = section.getByRole("listitem");
    await expect(rows).toHaveCount(2);
    await expect(section.getByText("this device")).toHaveCount(1);
    const phoneRow = rows.filter({ hasText: "Safari on iOS" });
    await expect(phoneRow).toBeVisible();
    await expect(phoneRow.getByText("this device")).toHaveCount(0);
    // This one has no button to sign out: the menu does that.
    await expect(rows.filter({ hasText: "this device" }).getByRole("button")).toHaveCount(0);

    await phoneRow.getByRole("button", { name: "Sign out Safari on iOS" }).click();
    await expect(rows).toHaveCount(1);
    expect(await signedIn(phone.request)).toBe(false);
    expect(await signedIn(page.request)).toBe(true);

    // Everywhere else.
    await signIn(phone.request, username);
    await page.reload();
    await expect(rows).toHaveCount(2);
    await section.getByRole("button", { name: "Sign out everywhere else" }).click();
    await expect(rows).toHaveCount(1);
    await expect(section.getByRole("button", { name: "Sign out everywhere else" })).toBeDisabled();
    expect(await signedIn(phone.request)).toBe(false);

    // Changing the password does the same.
    await signIn(phone.request, username);
    const signInSection = page.locator("section").filter({ has: page.getByRole("heading", { name: "Sign-in" }) });
    await signInSection.getByLabel("Current password").fill(password);
    await signInSection.getByLabel("New password").fill("another fine password");
    await signInSection.getByRole("button", { name: "Change password" }).click();
    await expect(page.getByText("Password updated")).toBeVisible();
    expect(await signedIn(phone.request)).toBe(false);
    expect(await signedIn(page.request)).toBe(true);
  } finally {
    await phone.close();
  }
});

test("the administrator makes a reset link, and the member sets a new password with it", async ({ page, api, org, pageAs }) => {
  const { username, slug } = await newPerson(page, api, org);
  // Two-factor on, and a phone lost.
  const { secret } = await (await page.request.post("/api/v1/me/two-factor/setup", { headers: { "X-Avsys": "1" }, data: { password } })).json();
  expect((await page.request.post("/api/v1/me/two-factor", { headers: { "X-Avsys": "1" }, data: { code: totp(secret) } })).ok()).toBe(true);

  // An organization's admin who is not the panel's has no such button.
  const owner = await pageAs("owner");
  await owner.goto(`/o/${slug}/members`);
  await owner.getByRole("button", { name: `Reset sign-in for ${username}` }).click();
  const dialog = owner.getByRole("dialog");
  await dialog.getByRole("switch", { name: /Turn off two-factor/ }).click();
  await dialog.getByRole("button", { name: "Make link" }).click();
  await expect(dialog.getByText("It works once, for 24 hours.")).toBeVisible();
  const link = (await dialog.getByRole("button", { name: /\/reset\// }).textContent())!;
  const path = new URL(link).pathname;

  await page.goto(path);
  await expect(page.getByRole("heading", { name: "Set a new password" })).toBeVisible();
  await page.getByLabel("New password").fill("a brand new password");
  await page.getByRole("button", { name: "Set password" }).click();
  await expect(page.getByRole("heading", { name: "Password set" })).toBeVisible();
  expect(await signedIn(page.request)).toBe(false);

  // The new password signs in, with no code: two-factor was turned off.
  await page.getByRole("link", { name: "Sign in" }).click();
  await page.getByLabel("Username").fill(username);
  await page.getByLabel("Password").fill("a brand new password");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page).not.toHaveURL(/\/login/);
  expect(await signedIn(page.request)).toBe(true);

  // The link works once.
  await page.goto(path);
  await expect(page.getByRole("heading", { name: "This link is no longer valid" })).toBeVisible();
});

test("only the panel's administrator may reset someone's sign-in", async ({ page, as, pageAs }) => {
  await as("admin");
  await page.goto("/o/friends/members");
  await expect(page.getByLabel("Role of viewer")).toBeVisible();
  await expect(page.getByRole("button", { name: /Reset sign-in/ })).toHaveCount(0);
  const owner = await pageAs("owner");
  await owner.goto("/o/friends/members");
  await expect(owner.getByRole("button", { name: "Reset sign-in for viewer" })).toBeVisible();
  // Not for themselves: that is the account page.
  await expect(owner.getByRole("button", { name: "Reset sign-in for owner" })).toHaveCount(0);
});

test("the administrator can require two-factor of administrators; others cannot", async ({ page, as, pageAs }) => {
  await as("owner");
  // Answered here: the setting is the whole panel's, and other tests sign
  // in as the owner with a password.
  let asked: unknown = null;
  await page.route("**/api/v1/settings/admin-two-factor", async (route) => {
    asked = route.request().postDataJSON();
    await route.fulfill({ status: 204 });
  });
  await page.goto("/account");
  const toggle = page.getByRole("switch", { name: /Require for administrators/ });
  await expect(toggle).toHaveAttribute("aria-checked", "false");
  await toggle.click();
  await expect.poll(() => asked).toEqual({ required: true });

  const viewer = await pageAs("viewer");
  await viewer.goto("/account");
  await expect(viewer.getByRole("heading", { name: "Two-factor" })).toBeVisible();
  await expect(viewer.getByRole("switch", { name: /Require for administrators/ })).toHaveCount(0);
});

test("an administrator who has to set up two-factor is sent to do that first", async ({ page, as }) => {
  await as("owner");
  await page.route("**/api/v1/me", async (route) => {
    const me = await (await route.fetch()).json();
    return route.fulfill({ json: { ...me, mustSetUpTwoFactor: true } });
  });
  await page.goto("/o/friends");
  await expect(page).toHaveURL(/\/two-factor$/);
  await expect(page.getByRole("heading", { name: "Set up two-factor" })).toBeVisible();
  await expect(page.getByText("Administrators here need two-factor to sign in.")).toBeVisible();
  await expect(page.getByLabel("Password")).toBeVisible();
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();
  // Nothing else is shown around it.
  await expect(page.getByRole("link", { name: "Servers" })).toHaveCount(0);
});

test("an administrator who need not set it up is not kept on that page", async ({ page, as }) => {
  await as("owner");
  await page.goto("/two-factor");
  await expect(page).not.toHaveURL(/\/two-factor$/);
});

test("a reset link that is not one says so", async ({ page }) => {
  await page.goto("/reset/not-a-real-link");
  await expect(page.getByRole("heading", { name: "This link is no longer valid" })).toBeVisible();
  await expect(page.getByText("Ask for a new one.")).toBeVisible();
});
