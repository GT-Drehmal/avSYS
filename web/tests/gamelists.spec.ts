import { expect, type Page, test } from "./panel";

// Players put on a server's whitelist or made operators in game, not by the
// panel, are shown in Settings under the list they are on. Each can be kept
// in the settings, or taken off.

test.beforeEach(async ({ as }) => {
  await as("owner");
});

type Lists = Record<"whitelist" | "ops", { known: boolean; inGame: { name: string; uuid?: string; bedrock?: boolean }[] }>;

/** Answers survival's lists from lists, taking off whom the page asks to, and keeps what is saved. */
async function survivalLists(page: Page, lists: Lists, refuse?: string) {
  const removed: string[] = [];
  const saved: { spec: { whitelist?: string[]; ops?: string[] } }[] = [];
  await page.route("**/api/v1/orgs/friends/servers/survival/lists", (route) => route.fulfill({ json: lists }));
  await page.route("**/api/v1/orgs/friends/servers/survival/lists/*/*", (route) => {
    const [list, name] = new URL(route.request().url()).pathname.split("/").slice(-2) as ["whitelist" | "ops", string];
    if (route.request().method() !== "DELETE") return route.fallback();
    if (refuse) return route.fulfill({ status: 409, json: { code: "member", message: refuse } });
    removed.push(`${list}/${decodeURIComponent(name)}`);
    lists[list].inGame = lists[list].inGame.filter((p) => p.name !== decodeURIComponent(name));
    return route.fulfill({ status: 204 });
  });
  await page.route("**/api/v1/orgs/friends/servers/survival", async (route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    saved.push(route.request().postDataJSON());
    return route.fulfill({ status: 503, json: { code: "test", message: "Not sent by the test." } });
  });
  return { removed, saved };
}

test("players added in game are shown under their list, and one is taken off", async ({ page }) => {
  const { removed } = await survivalLists(page, {
    whitelist: { known: true, inGame: [{ name: "Herobrine" }, { name: "Steve" }] },
    ops: { known: true, inGame: [{ name: "Notch" }] },
  });
  await page.goto("/o/friends/s/survival/settings");
  const whitelist = page.getByRole("list", { name: "Added in game to the whitelist" });
  const ops = page.getByRole("list", { name: "Added in game to the operators" });
  await expect(whitelist.getByRole("listitem")).toHaveText([/Herobrine/, /Steve/]);
  await expect(ops.getByRole("listitem")).toHaveText([/Notch/]);

  await page.getByRole("button", { name: "Remove Steve from the whitelist" }).click();
  await expect(whitelist.getByRole("listitem")).toHaveText([/Herobrine/]);
  expect(removed).toEqual(["whitelist/Steve"]);
  await page.getByRole("button", { name: "Remove Notch from the operators" }).click();
  await expect(ops).toHaveCount(0);
  expect(removed).toEqual(["whitelist/Steve", "ops/Notch"]);
  // Nothing else was changed.
  await expect(page.getByText("Unsaved changes")).toHaveCount(0);
});

test("a player added in game is kept by listing them in the settings", async ({ page }) => {
  const { removed, saved } = await survivalLists(page, {
    whitelist: { known: true, inGame: [{ name: "Herobrine" }] },
    ops: { known: true, inGame: [] },
  });
  await page.goto("/o/friends/s/survival/settings");
  await page.getByRole("button", { name: "Keep Herobrine on the whitelist" }).click();
  await expect(page.getByLabel("Whitelisted players")).toHaveValue("Herobrine");
  await expect(page.getByRole("list", { name: "Added in game to the whitelist" })).toHaveCount(0);
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect.poll(() => saved.length).toBe(1);
  expect(saved[0].spec.whitelist).toEqual(["Herobrine"]);
  expect(removed).toEqual([]);
  // Discarded, the player is back under what was added in game.
  await page.getByRole("button", { name: "Discard" }).click();
  await expect(page.getByRole("button", { name: "Keep Herobrine on the whitelist" })).toBeVisible();
});

test("a removal the panel refuses says why, and the player stays", async ({ page }) => {
  await survivalLists(page, { whitelist: { known: true, inGame: [{ name: "Steve" }] }, ops: { known: true, inGame: [] } }, "Steve is a member's player. Untick them under Members instead.");
  await page.goto("/o/friends/s/survival/settings");
  await page.getByRole("button", { name: "Remove Steve from the whitelist" }).click();
  await expect(page.getByText("Steve is a member's player. Untick them under Members instead.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Remove Steve from the whitelist" })).toBeEnabled();
});

test("lists that cannot be read show nothing more", async ({ page }) => {
  // This panel reaches no server: the lists are not known.
  const answered = page.waitForResponse((r) => r.url().endsWith("/servers/survival/lists"));
  await page.goto("/o/friends/s/survival/settings");
  const lists = (await (await answered).json()) as Lists;
  expect(lists.whitelist.known).toBe(false);
  expect(lists.ops.known).toBe(false);
  await expect(page.getByLabel("Whitelisted players")).toBeVisible();
  await expect(page.getByText("Added in game")).toHaveCount(0);
});

test("a Bedrock player added in game is shown by gamertag, marked Bedrock, and taken off by the name the game has", async ({ page }) => {
  const { removed, saved } = await survivalLists(page, {
    whitelist: { known: true, inGame: [{ name: ".Tim203", uuid: "00000000-0000-0000-0009-01f64f65c7c3", bedrock: true }, { name: "Steve" }] },
    ops: { known: true, inGame: [] },
  });
  await page.goto("/o/friends/s/survival/settings");
  const whitelist = page.getByRole("list", { name: "Added in game to the whitelist" });
  await expect(whitelist.getByRole("listitem")).toHaveText([/^Tim203Bedrock/, /^Steve/]);
  // Kept by the UUID Floodgate gives him: the image, as the server starts,
  // would look the name the game has for him up with Mojang, which has no
  // such player, and the server would not start.
  await page.getByRole("button", { name: "Keep Tim203 on the whitelist" }).click();
  await expect(page.getByLabel("Whitelisted players")).toHaveValue("00000000-0000-0000-0009-01f64f65c7c3");
  await expect(whitelist.getByRole("listitem")).toHaveText([/^Steve/]);
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect.poll(() => saved.length).toBe(1);
  expect(saved[0].spec.whitelist).toEqual(["00000000-0000-0000-0009-01f64f65c7c3"]);
  saved.length = 0;
  await page.getByRole("button", { name: "Discard" }).click();
  await page.getByRole("button", { name: "Remove Tim203 from the whitelist" }).click();
  await expect(whitelist.getByRole("listitem")).toHaveText([/Steve/]);
  expect(removed).toEqual(["whitelist/.Tim203"]);
  expect(saved).toEqual([]);
});
