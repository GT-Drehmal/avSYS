import type { APIRequestContext, Page } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { type Api, expect, test, totp, turnOnTwoFactor, watchErrors } from "./panel";

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
  // Password managers find the code box by its name and autocomplete.
  await expect(dialog.getByLabel("Code")).toHaveAttribute("name", "totp");
  await expect(dialog.getByLabel("Code")).toHaveAttribute("autocomplete", "one-time-code");
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
  await expect(page.getByLabel("Code")).toHaveAttribute("name", "totp");
  await expect(page.getByLabel("Code")).toHaveAttribute("autocomplete", "one-time-code");
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
  // Signed in, the panel goes on to their organization: once there, so
  // that going to the link is not cut short by that.
  await expect(page).toHaveURL(new RegExp(`/o/${slug}$`));
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

test("the administrator chooses who has to use two-factor in Instance settings; others cannot", async ({ page, as, pageAs }) => {
  await as("owner");
  // Answered here: the setting is the whole panel's, and other tests sign
  // in as the owner with a password.
  let asked: unknown = null;
  let level = "nobody";
  await page.route("**/api/v1/settings/two-factor", async (route) => {
    asked = route.request().postDataJSON();
    level = (asked as { level: string }).level;
    await route.fulfill({ status: 204 });
  });
  await page.route("**/api/v1/me", async (route) => {
    const me = await (await route.fetch()).json();
    await route.fulfill({ json: { ...me, twoFactorLevel: level } });
  });
  await page.goto("/settings");
  const signIn = page.getByRole("region", { name: "Sign-in" });
  const choice = signIn.getByRole("button", { name: "Require two-factor for" });
  await expect(choice).toHaveText("Nobody");
  await expect(signIn.getByText("By highest role in any organization. Includes you, unless Nobody.")).toBeVisible();
  await choice.click();
  const menu = page.getByRole("menu");
  const said = {
    Nobody: "Up to each person.",
    "Admins and owners": "Of any organization.",
    "Operators and up": "Operators, admins and owners.",
    Everyone: "Every account.",
  };
  await expect(menu.getByRole("menuitemradio")).toHaveText(Object.entries(said).map(([name, about]) => name + about));
  for (const [name, about] of Object.entries(said)) {
    await expect(menu.getByRole("menuitemradio", { name, exact: true })).toHaveAccessibleDescription(about);
  }
  await expect(menu.getByRole("menuitemradio", { name: "Nobody", exact: true })).toBeChecked();
  await menu.getByRole("menuitemradio", { name: "Operators and up" }).click();
  await expect.poll(() => asked).toEqual({ level: "operators" });
  await expect(choice).toHaveText("Operators and up");
  // It is the whole panel's, so not on the administrator's own account page.
  await page.goto("/account");
  await expect(page.getByRole("heading", { name: "Two-factor" })).toBeVisible();
  await expect(page.getByRole("button", { name: /Require/ })).toHaveCount(0);

  const viewer = await pageAs("viewer");
  await viewer.goto("/account");
  await expect(viewer.getByRole("heading", { name: "Two-factor" })).toBeVisible();
  await expect(viewer.getByRole("button", { name: /Require/ })).toHaveCount(0);
  await viewer.goto("/settings");
  await expect(viewer.getByText("Admins only")).toBeVisible();
});

test("someone who has to set up two-factor is sent to do that first", async ({ page, as }) => {
  await as("owner");
  await page.route("**/api/v1/me", async (route) => {
    const me = await (await route.fetch()).json();
    return route.fulfill({ json: { ...me, mustSetUpTwoFactor: true, twoFactorRequired: true } });
  });
  await page.goto("/o/friends");
  await expect(page).toHaveURL(/\/two-factor$/);
  await expect(page.getByRole("heading", { name: "Set up two-factor" })).toBeVisible();
  await expect(page.getByText("You need two-factor to use this panel.")).toBeVisible();
  await expect(page.getByLabel("Password")).toBeVisible();
  await expect(page.getByRole("button", { name: "Sign out" })).toBeVisible();
  // Nothing else is shown around it.
  await expect(page.getByRole("link", { name: "Servers" })).toHaveCount(0);
});

test("someone who comes to need two-factor while the panel is open is sent to set it up", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends");
  const members = page.getByRole("link", { name: "Members" }).first();
  await expect(members).toBeVisible();
  // Made an admin somewhere, say: from then on the panel answers so.
  await page.route("**/api/v1/orgs/friends/members", (route) => route.fulfill({ status: 403, json: { code: "two_factor_required", message: "Set up two-factor to continue." } }));
  await page.route("**/api/v1/me", async (route) => {
    const me = await (await route.fetch()).json();
    return route.fulfill({ json: { ...me, mustSetUpTwoFactor: true, twoFactorRequired: true } });
  });
  await members.click();
  await expect(page).toHaveURL(/\/two-factor$/);
  await expect(page.getByRole("heading", { name: "Set up two-factor" })).toBeVisible();
});

test("someone with no password sets up two-factor with a recent sign-in, or signs in again first", async ({ page, as }) => {
  await as("outsider");
  let recent = false;
  await page.route("**/api/v1/me", async (route) => {
    const me = await (await route.fetch()).json();
    return route.fulfill({ json: { ...me, hasPassword: false, twoFactor: false, recentSignIn: recent } });
  });
  // Not really set up: what matters is that no password is asked for.
  let asked: unknown = null;
  await page.route("**/api/v1/me/two-factor/setup", async (route) => {
    asked = route.request().postDataJSON();
    await route.fulfill({ status: 403, json: { code: "recent_sign_in", message: "Sign in again to do this." } });
  });
  await page.goto("/account");
  const section = page.locator("section").filter({ has: page.getByRole("heading", { name: "Two-factor" }) });
  await section.getByRole("button", { name: "Set up" }).click();
  const dialog = page.getByRole("dialog");
  // Signed in a while ago: again first, through the provider they have, back here.
  await expect(dialog.getByText("Sign in again to continue.")).toBeVisible();
  const again = dialog.getByRole("link", { name: "Continue with Test ID" });
  await expect(again).toHaveAttribute("href", "/api/v1/auth/oidc/start?next=%2Faccount");
  await expect(dialog.getByLabel("Password")).toHaveCount(0);
  await dialog.getByRole("button", { name: "Cancel" }).click();

  // Signed in a moment ago: no password, and on.
  recent = true;
  await page.reload();
  await section.getByRole("button", { name: "Set up" }).click();
  await expect(dialog.getByText("You need an authenticator app on your phone.")).toBeVisible();
  await expect(dialog.getByLabel("Password")).toHaveCount(0);
  await dialog.getByRole("button", { name: "Continue" }).click();
  await expect.poll(() => asked).toEqual({ password: "" });
  // The panel says it was too long ago after all: sign in again.
  await expect(dialog.getByRole("link", { name: "Continue with Test ID" })).toBeVisible();
});

test("an admin of an organization who signs in through a provider sets up two-factor, then is asked for a code", async ({ page, browser, freshPanel, pageErrors }) => {
  // A panel of its own, as just installed: admins and owners have to.
  const url = await freshPanel();
  const headers = { "X-Avsys": "1" };
  const founder = await page.request.post(`${url}/api/v1/auth/register`, { headers, data: { username: "founder", password } });
  expect(founder.ok(), await founder.text()).toBe(true);
  await turnOnTwoFactor(page.request, url, password);
  expect((await page.request.post(`${url}/api/v1/orgs`, { headers, data: { name: "Home", slug: "home" } })).ok()).toBe(true);
  const invite = await (await page.request.post(`${url}/api/v1/orgs/home/invites`, { headers, data: { role: "admin" } })).json();

  // Someone new joins as an admin through the provider.
  const theirs = await browser.newContext();
  const them = await theirs.newPage();
  watchErrors(them, pageErrors);
  await them.goto(`${url}/invite/${invite.token}`);
  await them.getByRole("link", { name: "Continue with Test ID" }).click();
  await expect(them).toHaveTitle("Test ID");
  await them.getByLabel("Username").fill("ada");
  await them.getByRole("button", { name: "Continue" }).click();

  // Asked to set up two-factor before anything else, with no password to give.
  await expect(them).toHaveURL(`${url}/two-factor`);
  await expect(them.getByRole("heading", { name: "Set up two-factor" })).toBeVisible();
  await expect(them.getByLabel("Password")).toHaveCount(0);
  const setup = them.waitForResponse((r) => r.url().endsWith("/api/v1/me/two-factor/setup") && r.ok());
  await them.getByRole("button", { name: "Continue" }).click();
  const { secret } = await (await setup).json();
  await them.getByLabel("Code").fill(totp(secret));
  await them.getByRole("button", { name: "Turn on" }).click();
  const codes = them.getByRole("list", { name: "Recovery codes" }).getByRole("listitem");
  await expect(codes).toHaveCount(10);
  const recovery = (await codes.first().textContent())!;
  await them.getByRole("button", { name: "Done" }).click();
  await expect(them).toHaveURL(`${url}/o/home`);

  // The next sign-in through the provider asks for a code after it.
  await them.request.post(`${url}/api/v1/auth/logout`, { headers });
  await them.goto(`${url}/login`);
  await them.getByRole("link", { name: "Continue with Test ID" }).click();
  await them.getByLabel("Username").fill("ada");
  await them.getByRole("button", { name: "Continue" }).click();
  await expect(them.getByLabel("Code")).toBeVisible();
  expect((await them.request.get(`${url}/api/v1/me`)).ok()).toBe(false);
  await them.getByLabel("Code").fill("000000");
  await them.getByRole("button", { name: "Sign in" }).click();
  await expect(them.getByText("Wrong code. Try again.")).toBeVisible();
  // The next code: the one shown was used.
  await them.getByLabel("Code").fill(totp(secret, Date.now() + 30_000));
  await them.getByRole("button", { name: "Sign in" }).click();
  await expect(them).toHaveURL(`${url}/o/home`);
  const me = await (await them.request.get(`${url}/api/v1/me`)).json();
  expect(me).toMatchObject({ user: { username: "ada" }, twoFactor: true, twoFactorRequired: true, mustSetUpTwoFactor: false });

  // And again with a recovery code; going back from the code starts over.
  await them.request.post(`${url}/api/v1/auth/logout`, { headers });
  await them.goto(`${url}/login`);
  await them.getByRole("link", { name: "Continue with Test ID" }).click();
  await them.getByLabel("Username").fill("ada");
  await them.getByRole("button", { name: "Continue" }).click();
  await them.getByRole("button", { name: "Back" }).click();
  await expect(them.getByRole("link", { name: "Continue with Test ID" })).toBeVisible();
  await expect(them.getByLabel("Code")).toHaveCount(0);
  await them.getByRole("link", { name: "Continue with Test ID" }).click();
  await them.getByLabel("Username").fill("ada");
  await them.getByRole("button", { name: "Continue" }).click();
  await them.getByLabel("Code").fill(recovery);
  await them.getByRole("button", { name: "Sign in" }).click();
  await expect(them).toHaveURL(`${url}/o/home`);
  await theirs.close();
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
