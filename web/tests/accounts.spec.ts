import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// A person links every Minecraft account they play as, a main and alts: each
// is listed, one is the main one, and each can be unlinked.

type Account = { uuid: string; name: string; linkedAt: string; main: boolean; verified: boolean };

/** The account routes, answered here as the panel would, with what was asked listed. */
async function accounts(page: Page, start: Account[]) {
  const s = { list: [...start], asked: [] as string[] };
  const reorder = () => s.list.sort((a, b) => Number(b.main) - Number(a.main));
  await page.route("**/api/v1/me", async (route) => {
    const me = await (await route.fetch()).json();
    return route.fulfill({ json: { ...me, minecraft: s.list[0], minecraftAccounts: s.list } });
  });
  await page.route("**/api/v1/me/minecraft-accounts**", async (route) => {
    const req = route.request();
    const path = new URL(req.url()).pathname.replace("/api/v1/me/minecraft-accounts", "");
    s.asked.push(`${req.method()} ${path}`.trim());
    if (req.method() === "POST" && path === "") {
      const { name } = req.postDataJSON();
      s.list.push({ uuid: `uuid-${name}`, name, linkedAt: new Date().toISOString(), main: s.list.length === 0, verified: false });
    } else if (req.method() === "POST" && path.endsWith("/verify")) {
      const uuid = path.split("/")[1];
      const a = s.list.find((x) => x.uuid === uuid);
      const code = req.postDataJSON()?.code;
      if (!code) return route.fulfill({ json: { name: a?.name, address: "verify.mc.example.org", until: new Date(Date.now() + 600_000).toISOString() } });
      if (code !== "ABCD-EFGH") return route.fulfill({ status: 400, json: { code: "wrong_code", message: "Wrong code. Try again." } });
      if (a) a.verified = true;
      return route.fulfill({ json: { name: a?.name, verified: true } });
    } else if (req.method() === "POST" && path.endsWith("/main")) {
      const uuid = path.split("/")[1];
      for (const a of s.list) a.main = a.uuid === uuid;
    } else if (req.method() === "DELETE") {
      const uuid = path.split("/")[1];
      const gone = s.list.find((a) => a.uuid === uuid);
      s.list = s.list.filter((a) => a.uuid !== uuid);
      if (gone?.main && s.list.length > 0) s.list[0].main = true;
    }
    reorder();
    return route.fulfill({ json: s.list });
  });
  return s;
}

test.beforeEach(async ({ as }) => {
  await as("owner");
});

test("several accounts are linked, one is the main one, and each can be unlinked", async ({ page }) => {
  const s = await accounts(page, []);
  await page.goto("/account");
  const section = page.locator("section").filter({ hasText: "Minecraft accounts" });
  await section.getByLabel("Minecraft username").fill("Steve");
  await section.getByRole("button", { name: "Link", exact: true }).click();
  await expect(section.getByText("Steve", { exact: true })).toBeVisible();
  await expect(section.getByText("main", { exact: true })).toBeVisible();
  // Another, beside it.
  await expect(section.getByLabel("Minecraft username")).toHaveAttribute("placeholder", "Another name you play as");
  await section.getByLabel("Minecraft username").fill("SteveAlt");
  await section.getByRole("button", { name: "Link", exact: true }).click();
  await expect(section.getByText("SteveAlt", { exact: true })).toBeVisible();
  await expect(section.getByRole("button", { name: "Make main" })).toHaveCount(1);

  await section.getByRole("button", { name: "Make main" }).click();
  await expect(section.locator("li").first()).toContainText("SteveAlt");
  await expect(section.locator("li").first()).toContainText("main");

  await section.getByRole("button", { name: "Unlink SteveAlt" }).click();
  await expect(section.getByText("SteveAlt", { exact: true })).toHaveCount(0);
  // The one left is the main one.
  await expect(section.locator("li")).toHaveCount(1);
  await expect(section.locator("li").first()).toContainText("main");
  expect(s.asked).toEqual(["POST", "POST", "POST /uuid-SteveAlt/main", "DELETE /uuid-SteveAlt"]);
});

test("a member's accounts are all shown beside them", async ({ page }) => {
  await page.route("**/api/v1/orgs/friends/members", async (route) => {
    const members = await (await route.fetch()).json();
    return route.fulfill({
      json: members.map((m: { username: string }) =>
        m.username === "viewer"
          ? {
              ...m,
              minecraftName: "Vera",
              minecraftAccounts: [
                { uuid: "a", name: "Vera", linkedAt: "", main: true, verified: true },
                { uuid: "b", name: "VeraAlt", linkedAt: "", main: false, verified: false },
              ],
            }
          : m,
      ),
    });
  });
  await page.goto("/o/friends/members");
  await expect(page.getByText("· Vera, VeraAlt in game")).toBeVisible();
});

test("an account is verified with the code the game shows", async ({ page }) => {
  const s = await accounts(page, [{ uuid: "uuid-Steve", name: "Steve", linkedAt: new Date().toISOString(), main: true, verified: false }]);
  await page.goto("/account");
  const section = page.locator("section").filter({ hasText: "Minecraft accounts" });
  await section.getByRole("button", { name: "Verify" }).click();
  await expect(section.getByText("In Minecraft, sign in as Steve and join verify.mc.example.org. Enter the code it shows.")).toBeVisible();
  expect(s.asked).toContain("POST /uuid-Steve/verify");
  // A wrong code is said so, and the box stays.
  await section.getByLabel("Code").fill("WXYZ-WXYZ");
  await section.getByRole("button", { name: "Verify" }).click();
  await expect(page.getByText("Wrong code. Try again.")).toBeVisible();
  await expect(section.getByLabel("Code")).toBeVisible();
  await expect(section.getByText("verified", { exact: true })).toHaveCount(0);
  // The right one.
  await section.getByLabel("Code").fill("ABCD-EFGH");
  await section.getByRole("button", { name: "Verify" }).click();
  await expect(page.getByText("Steve is verified")).toBeVisible();
  await expect(section.getByText("verified", { exact: true })).toBeVisible();
  await expect(section.getByLabel("Code")).toHaveCount(0);
  await expect(section.getByRole("button", { name: "Verify" })).toHaveCount(0);
});

test("a check can be put away", async ({ page }) => {
  await accounts(page, [{ uuid: "uuid-Steve", name: "Steve", linkedAt: new Date().toISOString(), main: true, verified: false }]);
  await page.goto("/account");
  const section = page.locator("section").filter({ hasText: "Minecraft accounts" });
  await section.getByRole("button", { name: "Verify" }).click();
  await section.getByRole("button", { name: "Cancel" }).click();
  await expect(section.getByLabel("Code")).toHaveCount(0);
  await expect(section.getByRole("button", { name: "Verify" })).toHaveCount(1);
});
