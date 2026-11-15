import { randomUUID } from "node:crypto";
import type { Invite, Member } from "../src/lib/api";
import { expect, test } from "./panel";

// An invite link is how anyone gets into an organization here: someone new
// makes an account with it, someone with one joins with a click or signs in
// first. A link that has run out, been used up or never was says so plainly.

const newName = () => `n${randomUUID().replaceAll("-", "").slice(0, 10)}`;

test("someone new makes an account with an invite and is in with its role", async ({ page, api, org, password }) => {
  const slug = await org([]);
  const invite = await api("owner").post<Invite>(`/orgs/${slug}/invites`, { role: "operator", maxUses: 1 });
  await page.goto(`/invite/${invite.token}`);
  await expect(page.getByRole("heading", { name: `Join Test ${slug}` })).toBeVisible();
  await expect(page.getByText("You have been invited as operator")).toBeVisible();

  const name = newName();
  await page.getByLabel("Username").fill(name);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page).toHaveURL(`/o/${slug}`);
  await expect(page.getByRole("heading", { name: "Servers" })).toBeVisible();

  const members = await api("owner").get<Member[]>(`/orgs/${slug}/members`);
  expect(members.find((m) => m.username === name)?.role).toBe("operator");
  const [used] = await api("owner").get<Invite[]>(`/orgs/${slug}/invites`);
  expect(used).toMatchObject({ token: invite.token, uses: 1 });

  // It was for one person: the next to follow it is told it is spent.
  await page.context().clearCookies();
  await page.goto(`/invite/${invite.token}`);
  await expect(page.getByRole("heading", { name: "This invite is no longer valid" })).toBeVisible();
});

test("someone signed in joins with one click", async ({ page, as, api, org }) => {
  const slug = await org([]);
  const invite = await api("owner").post<Invite>(`/orgs/${slug}/invites`, { role: "viewer" });
  await as("outsider");
  await page.goto(`/invite/${invite.token}`);
  await expect(page.getByText("Signed in as outsider")).toBeVisible();
  await page.getByRole("button", { name: `Join Test ${slug}` }).click();
  await expect(page).toHaveURL(`/o/${slug}`);
  const members = await api("owner").get<Member[]>(`/orgs/${slug}/members`);
  expect(members.find((m) => m.username === "outsider")?.role).toBe("viewer");
});

test("someone with an account signs in from the invite and then joins", async ({ page, api, org, password }) => {
  const slug = await org([]);
  const invite = await api("owner").post<Invite>(`/orgs/${slug}/invites`, { role: "admin" });
  await page.goto(`/invite/${invite.token}`);
  await page.getByRole("button", { name: "Already have an account? Sign in" }).click();
  await page.getByLabel("Username").fill("viewer");
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  // Back at the invite, now signed in.
  await expect(page).toHaveURL(`/invite/${invite.token}`);
  await expect(page.getByText("Signed in as viewer")).toBeVisible();
  await page.getByRole("button", { name: `Join Test ${slug}` }).click();
  await expect(page).toHaveURL(`/o/${slug}`);
  const members = await api("owner").get<Member[]>(`/orgs/${slug}/members`);
  expect(members.find((m) => m.username === "viewer")?.role).toBe("admin");
});

test("a link that never was says so plainly", async ({ page }) => {
  await page.goto("/invite/not-an-invite");
  await expect(page.getByRole("heading", { name: "This invite is no longer valid" })).toBeVisible();
  await expect(page.getByText("The invite may have expired or been revoked. Ask for a new one.")).toBeVisible();
  await expect(page.getByLabel("Username")).toHaveCount(0);
  await page.getByRole("link", { name: "Go to avSYS" }).click();
  await expect(page).toHaveURL(/\/login/);
});

test("an expired link says so plainly", async ({ page, api }) => {
  const invites = await api("owner").get<Invite[]>("/orgs/friends/invites");
  const expired = invites.find((i) => i.expiresAt && new Date(i.expiresAt) < new Date());
  expect(expired, "the test panel has an expired invite").toBeDefined();
  await page.goto(`/invite/${expired?.token}`);
  await expect(page.getByRole("heading", { name: "This invite is no longer valid" })).toBeVisible();
  await expect(page.getByLabel("Username")).toHaveCount(0);
});

test("an invite used up while its page is open says so on signing up, and makes no account", async ({ page, api, org, password }) => {
  const slug = await org([]);
  const invite = await api("owner").post<Invite>(`/orgs/${slug}/invites`, { role: "operator", maxUses: 1 });
  await page.goto(`/invite/${invite.token}`);
  await expect(page.getByRole("heading", { name: `Join Test ${slug}` })).toBeVisible();
  // Someone else takes it first.
  await api("outsider").post(`/invites/${invite.token}/accept`);

  const name = newName();
  await page.getByLabel("Username").fill(name);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Create account" }).click();
  await expect(page.getByText("This invite is no longer valid. Ask for a new one.")).toBeVisible();
  await expect(page).toHaveURL(`/invite/${invite.token}`);
  // Nothing was made: the name is still free to sign in as nobody.
  const res = await page.request.post("/api/v1/auth/login", { headers: { "X-Avsys": "1" }, data: { username: name, password } });
  expect(res.status()).toBe(401);
  const members = await api("owner").get<Member[]>(`/orgs/${slug}/members`);
  expect(members.map((m) => m.username)).not.toContain(name);
});

test("joining with an invite used up while its page is open says so", async ({ page, as, api, org }) => {
  const slug = await org([]);
  const invite = await api("owner").post<Invite>(`/orgs/${slug}/invites`, { role: "operator", maxUses: 1 });
  await as("viewer");
  await page.goto(`/invite/${invite.token}`);
  await expect(page.getByText("Signed in as viewer")).toBeVisible();
  await api("outsider").post(`/invites/${invite.token}/accept`);
  await page.getByRole("button", { name: `Join Test ${slug}` }).click();
  await expect(page.getByText("This invite is no longer valid. Ask for a new one.")).toBeVisible();
  await expect(page).toHaveURL(`/invite/${invite.token}`);
});
