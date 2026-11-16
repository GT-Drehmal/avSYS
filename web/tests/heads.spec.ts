import type { Locator, Page } from "@playwright/test";
import { expect, test } from "./panel";

// A player is shown by their head wherever they are named, as on the saved
// lists: the face of their skin, or their initial where there is none, or
// none could be had.

/** An 8 by 8 face, as the panel makes one from a skin. */
const face = Buffer.from(
  "iVBORw0KGgoAAAANSUhEUgAAAAgAAAAICAIAAABLbSncAAAAO0lEQVR4nGPQluPFihjwSWxpyUFDIAlMUQgCSfz//9/LpgfCBzKAXKgEBGU5GAARig6IKBqDAVk5siYAKhJMwx6sE6oAAAAASUVORK5CYII=",
  "base64",
);

const steve = "8667ba71-b85a-4004-af54-457a9734eed7";
const alex = "ec561538-f3fd-461d-aff5-086b22154bce";

/** Answers the heads of the players given, by UUID; anyone else's is not there, as for one with no skin. */
async function heads(page: Page, have: string[]) {
  const bare = new Set(have.map((u) => u.replaceAll("-", "")));
  await page.route("**/api/v1/players/*/head*", (route) => {
    const uuid = new URL(route.request().url()).pathname.split("/").at(-2) ?? "";
    if (bare.has(uuid.replaceAll("-", ""))) return route.fulfill({ body: face, contentType: "image/png" });
    return route.fulfill({ status: 404, json: { code: "not_found", message: "Not found." } });
  });
}

/** The head of the player of a UUID, drawn in. */
async function expectHead(scope: Locator, uuid: string) {
  const img = scope.locator(`img[src^="/api/v1/players/${uuid}/head"]`);
  await expect(img).toBeVisible();
  await expect.poll(() => img.evaluate((el: HTMLImageElement) => el.complete && el.naturalWidth)).toBe(8);
  // Beside their name, it says nothing more.
  await expect(img).toHaveAttribute("alt", "");
}

/** In place of a head that could not be had, the player's initial. */
async function expectInitial(scope: Locator, uuid: string | undefined, initial: string) {
  if (uuid) await expect(scope.locator(`img[src^="/api/v1/players/${uuid}/head"]`)).toHaveCount(0);
  await expect(scope.getByText(initial, { exact: true })).toBeVisible();
}

test.beforeEach(async ({ as }) => {
  await as("owner");
});

test("the players on a server are shown by their heads beside their names", async ({ page }) => {
  // Steve has a skin, and Alex's head cannot be had.
  await heads(page, [steve]);
  await page.goto("/o/friends/s/survival");
  const fact = page.locator("dl > div").filter({ has: page.locator("dt", { hasText: /^Players$/ }) });
  await expect(fact).toContainText("2 / 20");
  await expect(fact.getByText("Steve, Alex", { exact: true })).toBeVisible();
  await expect(fact.locator("dd > span")).toHaveAttribute("title", "Steve, Alex");
  await expectHead(fact, steve);
  await expectInitial(fact, alex, "A");
});

test("a server whose game names its players without their UUIDs shows their initials", async ({ page }) => {
  await heads(page, [steve]);
  // No live updates: they would bring the server as the panel has it.
  await page.route("**/api/v1/events?*", (route) => route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body: "retry: 60000\n\n" }));
  await page.route("**/api/v1/orgs/friends/servers", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const servers: { name: string; status: { players?: unknown } }[] = await (await route.fetch()).json();
    for (const s of servers) if (s.name === "survival") s.status.players = { online: 2, max: 20, names: ["Steve", "Alex"] };
    return route.fulfill({ json: servers });
  });
  await page.goto("/o/friends/s/survival");
  const fact = page.locator("dl > div").filter({ has: page.locator("dt", { hasText: /^Players$/ }) });
  await expect(fact.getByText("Steve, Alex", { exact: true })).toBeVisible();
  await expect(fact.locator("img")).toHaveCount(0);
  await expectInitial(fact, undefined, "S");
  await expectInitial(fact, undefined, "A");
});

test("each Minecraft account of one's own is shown by its head", async ({ page }) => {
  await heads(page, [steve]);
  await page.route("**/api/v1/me", async (route) => {
    const me = await (await route.fetch()).json();
    const accounts = [
      { uuid: steve.replaceAll("-", ""), name: "Steve", linkedAt: "", main: true, verified: true },
      { uuid: alex.replaceAll("-", ""), name: "Alex", linkedAt: "", main: false, verified: false },
    ];
    return route.fulfill({ json: { ...me, minecraft: accounts[0], minecraftAccounts: accounts } });
  });
  await page.goto("/account");
  const section = page.locator("section").filter({ hasText: "Minecraft accounts" });
  const [first, second] = [section.locator("li").nth(0), section.locator("li").nth(1)];
  await expect(first).toContainText("Steve");
  await expectHead(first, steve.replaceAll("-", ""));
  await expect(second).toContainText("Alex");
  await expectInitial(second, alex.replaceAll("-", ""), "A");
});

/** The members, with the viewer playing as Steve and an alt, and the operator as Alex. */
async function membersInGame(page: Page) {
  await page.route("**/api/v1/orgs/friends/members", async (route) => {
    const members = await (await route.fetch()).json();
    return route.fulfill({
      json: members.map((m: { username: string }) =>
        m.username === "viewer"
          ? {
              ...m,
              minecraftName: "Steve",
              minecraftAccounts: [
                { uuid: steve, name: "Steve", linkedAt: "", main: true, verified: true },
                { uuid: "b", name: "SteveAlt", linkedAt: "", main: false, verified: false },
              ],
            }
          : m.username === "operator"
            ? { ...m, minecraftName: "Alex", minecraftAccounts: [{ uuid: alex, name: "Alex", linkedAt: "", main: true, verified: true }] }
            : m,
      ),
    });
  });
}

test("a member's main Minecraft account is shown by its head", async ({ page }) => {
  await heads(page, [steve]);
  await membersInGame(page);
  await page.goto("/o/friends/members");
  const viewer = page.locator("main li").filter({ hasText: "Steve, SteveAlt in game" });
  await expectHead(viewer, steve);
  // Only the main one's.
  await expect(viewer.locator("img[src*='/players/']")).toHaveCount(1);
  const operator = page.locator("main li").filter({ hasText: "Alex in game" });
  await expectInitial(operator, alex, "A");
});

test("the members on a server's lists, and the players added in game, are shown by their heads", async ({ page }) => {
  await heads(page, [steve]);
  await membersInGame(page);
  await page.route("**/api/v1/orgs/friends/servers/survival/lists", (route) =>
    route.fulfill({
      json: {
        whitelist: { known: true, inGame: [{ name: "Herobrine" }, { name: "Steve", uuid: steve }] },
        ops: { known: true, inGame: [{ name: "Notch", uuid: "069a79f4-44e9-4726-a5be-fca90e38aaf5" }] },
      },
    }),
  );
  await page.goto("/o/friends/s/survival/settings");

  const whitelist = page.getByRole("list", { name: "Added in game to the whitelist" });
  await expectHead(whitelist.getByRole("listitem").filter({ hasText: "Steve" }), steve);
  // One the game has no UUID for, and one whose head cannot be had.
  await expectInitial(whitelist.getByRole("listitem").filter({ hasText: "Herobrine" }), undefined, "H");
  await expectInitial(page.getByRole("list", { name: "Added in game to the operators" }).getByRole("listitem"), "069a79f4-44e9-4726-a5be-fca90e38aaf5", "N");

  const viewer = page.getByRole("row").filter({ hasText: "Steve, SteveAlt" });
  await expectHead(viewer, steve);
  await expectInitial(page.getByRole("row").filter({ hasText: "Alex" }), alex, "A");
});
