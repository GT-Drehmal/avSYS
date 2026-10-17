import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// A person links every Minecraft account they play as, a main and alts: each
// is listed, one is the main one, and each can be unlinked.

type Account = { uuid: string; name: string; linkedAt: string; main: boolean };

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
      s.list.push({ uuid: `uuid-${name}`, name, linkedAt: new Date().toISOString(), main: s.list.length === 0 });
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
                { uuid: "a", name: "Vera", linkedAt: "", main: true },
                { uuid: "b", name: "VeraAlt", linkedAt: "", main: false },
              ],
            }
          : m,
      ),
    });
  });
  await page.goto("/o/friends/members");
  await expect(page.getByText("· Vera, VeraAlt in game")).toBeVisible();
});
