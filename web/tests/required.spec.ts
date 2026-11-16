import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// A form sent with something it needs left out says so in the panel's own
// words, under what is missing, in place of the browser's bubble, and goes
// to it. The button to send it stays one that can be pressed.

/** Whether the browser was kept from showing its own bubble on the next form sent with a problem. */
async function bubbleKept(page: Page, send: () => Promise<void>) {
  const kept = page.evaluate(
    () =>
      new Promise<boolean>((resolve) =>
        // Before every other listener; read once they all have run.
        window.addEventListener("invalid", (e) => setTimeout(() => resolve(e.defaultPrevented)), { capture: true, once: true }),
      ),
  );
  await send();
  return kept;
}

const focused = (page: Page) => page.evaluate(() => document.activeElement?.getAttribute("aria-label") ?? document.activeElement?.id ?? "");

test("the new-server form says what is missing under it, and goes to the first", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  let sent = 0;
  await page.route(`**/api/v1/orgs/${slug}/servers`, (route) => (route.request().method() === "POST" ? (sent++, route.abort()) : route.fallback()));
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  const create = page.getByRole("button", { name: "Create server" });
  const eula = page.getByRole("checkbox", { name: /End User License Agreement/ });
  const name = page.getByLabel("Display name");

  // The EULA not accepted.
  await name.fill("Weekend World");
  await expect(create).toBeEnabled();
  expect(await bubbleKept(page, () => create.click())).toBe(true);
  await expect(page.getByText("Check this box to accept the EULA.")).toBeVisible();
  await expect(eula).toBeFocused();
  await expect(eula).toBeInViewport();

  // No name either: both are said, and the first, higher up, is gone to.
  await name.fill("");
  await page.getByLabel("Short name").fill("");
  await create.click();
  await expect(page.getByText("Give it a name.")).toBeVisible();
  await expect(page.getByText("Give it a short name.")).toBeVisible();
  await expect(page.getByText("Check this box to accept the EULA.")).toBeVisible();
  await expect(name).toBeFocused();
  await expect(name).toHaveAttribute("aria-invalid", "true");

  // Each goes once it is put right.
  await name.fill("Weekend World");
  await expect(page.getByText("Give it a name.")).toHaveCount(0);
  await expect(name).not.toHaveAttribute("aria-invalid");
  await page.getByLabel("Short name").fill("Not This");
  await expect(page.getByText("Give it a short name.")).toHaveCount(0);
  await create.click();
  await expect(page.getByText("Use lowercase letters, digits and -, starting with a letter.")).toBeVisible();
  await page.getByLabel("Short name").fill("weekend-world");
  await expect(page.getByText("Use lowercase letters, digits and -, starting with a letter.")).toHaveCount(0);
  await eula.check();
  await expect(page.getByText("Check this box to accept the EULA.")).toHaveCount(0);
  expect(sent).toBe(0);
  await create.click();
  await expect.poll(() => sent).toBe(1);
});

test("a form whose control has no label above it says so under the control too", async ({ page, as }) => {
  await as("viewer");
  await page.route("**/api/v1/me/tokens", (route) => (route.request().method() === "POST" ? route.abort() : route.fallback()));
  await page.goto("/account");
  const create = page.getByRole("button", { name: "Create token" });
  await expect(create).toBeEnabled();
  expect(await bubbleKept(page, () => create.click())).toBe(true);
  const section = page.locator("section").filter({ has: create });
  await expect(section.getByText("Give it a name.")).toBeVisible();
  expect(await focused(page)).toBe("Token name");
  await page.getByLabel("Token name").fill("CI");
  await expect(section.getByText("Give it a name.")).toHaveCount(0);
});

test("signing in with nothing typed says what to type", async ({ page }) => {
  await page.goto("/login");
  const signIn = page.getByRole("button", { name: "Sign in" });
  expect(await bubbleKept(page, () => signIn.click())).toBe(true);
  await expect(page.getByText("Enter your username.")).toBeVisible();
  await expect(page.getByText("Enter your password.")).toBeVisible();
  await expect(page.getByLabel("Username")).toBeFocused();
});
