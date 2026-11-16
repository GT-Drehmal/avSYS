import { expect, test } from "./panel";

// Instance settings: what the panel's administrator sets for the whole
// panel. What each part does is tested where it is (release.spec.ts,
// security.spec.ts, curseforge.spec.ts); here, the page itself.

test("the administrator opens Instance settings from the menu, with updates, sign-in and CurseForge", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends");
  await page.getByRole("button", { name: /owner/ }).click();
  await page.getByRole("menuitem", { name: "Instance settings" }).click();
  await expect(page).toHaveURL(/\/settings$/);
  await expect(page.getByRole("heading", { name: "Instance settings" })).toBeVisible();
  await expect(page.locator("main").getByRole("heading", { level: 2 })).toHaveText(["Updates", "Sign-in", "CurseForge"]);

  await expect(page.getByRole("region", { name: "Updates" })).toContainText("This is avSYS v0.1.0.");
  await expect(page.getByRole("switch", { name: "Install fixes by itself" })).toBeVisible();

  // How people sign in is as the install has it: shown, not changed here.
  const signIn = page.getByRole("region", { name: "Sign-in" });
  await expect(signIn).toContainText("Set when avSYS is installed.");
  await expect(signIn.getByRole("definition")).toHaveText(["On", "Test ID", "By invite"]);
  await expect(signIn.getByRole("switch", { name: "Require two-factor for administrators" })).toBeVisible();

  await expect(page.getByRole("region", { name: "CurseForge" }).getByLabel("API key")).toBeVisible();
});

test("with passwords off, there is no two-factor to require", async ({ page, as }) => {
  await as("owner");
  await page.route("**/api/v1/meta", async (route) => {
    const meta = await (await route.fetch()).json();
    await route.fulfill({ json: { ...meta, passwords: false, allowSignups: true } });
  });
  await page.goto("/settings");
  const signIn = page.getByRole("region", { name: "Sign-in" });
  await expect(signIn.getByRole("definition")).toHaveText(["Off", "Test ID", "Anyone can sign up"]);
  await expect(page.getByRole("switch", { name: /two-factor/ })).toHaveCount(0);
});

test("nobody else is offered Instance settings, and the page is for admins only", async ({ page, as }) => {
  for (const who of ["admin", "viewer"] as const) {
    await as(who);
    await page.goto("/o/friends");
    await page.getByRole("button", { name: new RegExp(who) }).click();
    await expect(page.getByRole("menuitem", { name: "Account" })).toBeVisible();
    await expect(page.getByRole("menuitem", { name: "Instance settings" })).toHaveCount(0);
    await page.keyboard.press("Escape");

    await page.goto("/settings");
    await expect(page.getByText("Admins only")).toBeVisible();
    await expect(page.locator("main").getByRole("region")).toHaveCount(0);
    await expect(page.locator("main").getByRole("switch")).toHaveCount(0);
  }
});

test("the panel's settings are for its administrator alone", async ({ api }) => {
  expect((await api("owner").get("/panel")).panel).toMatchObject({ version: "v0.1.0" });
  for (const who of ["admin", "viewer"] as const) {
    await expect(api(who).get("/panel")).rejects.toThrow(/403/);
  }
  // The nodes no longer carry them.
  const nodes = await api("owner").get("/nodes");
  expect(nodes.panel).toBeUndefined();
  expect(nodes.update).toBeUndefined();
});
