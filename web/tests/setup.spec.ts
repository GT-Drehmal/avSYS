import { expect, test, totp, watchErrors } from "./panel";

// A panel just installed, with nobody in it: whoever opens it first sets it
// up, and is its administrator. Nobody after them can make an account
// without an invite.

test("the first account is the administrator, and signups then close", async ({ page, browser, freshPanel, pageErrors }) => {
  const url = await freshPanel();
  await page.goto(url);
  await expect(page).toHaveURL(`${url}/login?next=%2F`);
  await expect(page.getByRole("heading", { name: "Set up avSYS" })).toBeVisible();
  await expect(page.getByText("This account will be the administrator.")).toBeVisible();
  await page.getByLabel("Username").fill("founder");
  await page.getByLabel("Password").fill("a password long enough");
  await page.getByRole("button", { name: "Create account" }).click();

  // Admins and owners have to use two-factor on a panel just installed,
  // and the administrator always does: set up first.
  await expect(page).toHaveURL(`${url}/two-factor`);
  await expect(page.getByRole("heading", { name: "Set up two-factor" })).toBeVisible();
  await page.getByLabel("Password").fill("a password long enough");
  const setup = page.waitForResponse((r) => r.url().endsWith("/api/v1/me/two-factor/setup") && r.ok());
  await page.getByRole("button", { name: "Continue" }).click();
  const { secret } = await (await setup).json();
  await page.getByLabel("Code").fill(totp(secret));
  await page.getByRole("button", { name: "Turn on" }).click();
  await expect(page.getByRole("list", { name: "Recovery codes" }).getByRole("listitem")).toHaveCount(10);
  await page.getByRole("button", { name: "Done" }).click();

  // In, as the administrator, with nothing yet but the first organization to make.
  await expect(page.getByText("Create your first organization")).toBeVisible();
  const me = await (await page.request.get(`${url}/api/v1/me`)).json();
  expect(me.user).toMatchObject({ username: "founder", isAdmin: true });
  await page.getByRole("button", { name: "New organization" }).click();
  await page.getByLabel("Name", { exact: true }).fill("Home Lab");
  await expect(page.getByLabel("Short name")).toHaveValue("home-lab");
  await page.getByRole("button", { name: "Create organization" }).click();
  await expect(page).toHaveURL(`${url}/o/home-lab`);

  // Someone else who opens the panel now can only sign in.
  const other = await browser.newContext();
  const stranger = await other.newPage();
  watchErrors(stranger, pageErrors);
  await stranger.goto(url);
  await expect(stranger.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await expect(stranger.getByText("Accounts here are created from an invite link.")).toBeVisible();
  await expect(stranger.getByRole("button", { name: /Create an account/ })).toHaveCount(0);
  const signup = await stranger.request.post(`${url}/api/v1/auth/register`, {
    headers: { "X-Avsys": "1" },
    data: { username: "latecomer", password: "a password long enough" },
  });
  expect(signup.status()).toBe(403);
  expect(await signup.json()).toMatchObject({ code: "signup_closed" });
  const meta = await (await stranger.request.get(`${url}/api/v1/meta`)).json();
  expect(meta.setupRequired).toBe(false);

  // The administrator signs in again with what they set up: the password,
  // then a code (the next one: the one shown was used).
  await stranger.getByLabel("Username").fill("founder");
  await stranger.getByLabel("Password").fill("a password long enough");
  await stranger.getByRole("button", { name: "Sign in" }).click();
  await stranger.getByLabel("Code").fill(totp(secret, Date.now() + 30_000));
  await stranger.getByRole("button", { name: "Sign in" }).click();
  await expect(stranger).toHaveURL(`${url}/o/home-lab`);
  await other.close();
});
