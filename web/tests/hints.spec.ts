import type { Locator, Page } from "@playwright/test";
import { content, expect, test } from "./panel";

// A few words where a control has none beside it, shown by pointing at it,
// moving to it with the keyboard, or tapping it; examples in boxes left
// empty; and links to the pages that tell what a setting's entries are.

/** The one tooltip shown, if any. A hidden one is not in the page as a person sees it. */
const tooltip = (page: Page) => page.getByRole("tooltip");

/** Moves the keyboard's focus to `to` from the control before it, as Tab does. */
async function tabTo(page: Page, from: Locator, to: Locator) {
  await from.focus();
  await page.keyboard.press("Tab");
  await expect(to).toBeFocused();
}

/** The row of a server on the Servers page, and the dot at its start, which a screen reader is not told of: the row says the status in words. */
const server = (page: Page, name: string) => {
  const row = content(page).getByRole("listitem").filter({ has: page.getByRole("link", { name: new RegExp(`^${name}`) }) });
  return { row, link: row.getByRole("link").first(), dot: row.locator("[data-status-dot]") };
};

test("a server's dot says what it means when pointed at, or while the keyboard is on its row", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends");
  const { row, link, dot } = server(page, "Survival");
  await expect(dot).toBeVisible();
  // Its link is named for the server first, not for the dot.
  await expect(link).toHaveAccessibleName(/^Survival\b/);
  await expect(tooltip(page)).toHaveCount(0);

  await dot.hover();
  await expect(tooltip(page)).toHaveText("Online");
  await page.mouse.move(0, 0);
  await expect(tooltip(page)).toHaveCount(0);

  await tabTo(page, row.getByRole("checkbox", { name: "Select survival" }), link);
  await expect(tooltip(page)).toHaveText("Online");
  await page.keyboard.press("Escape");
  await expect(tooltip(page)).toHaveCount(0);
  // On along the row, it goes.
  await page.keyboard.press("Shift+Tab");
  await page.keyboard.press("Tab");
  await expect(tooltip(page)).toHaveText("Online");
  await page.keyboard.press("Tab");
  await expect(link).not.toBeFocused();
  await expect(tooltip(page)).toHaveCount(0);

  // Others say what they are too.
  await server(page, "Lobby").dot.hover();
  await expect(tooltip(page)).toHaveText("Asleep");
});

test.describe("on a touch screen", () => {
  test.use({ hasTouch: true });

  test("a tap on a server's dot says what it means, and does not open the server", async ({ page, as }) => {
    await as("owner");
    await page.goto("/o/friends");
    await server(page, "Survival").dot.tap();
    await expect(tooltip(page)).toHaveText("Online");
    await expect(page).toHaveURL(/\/o\/friends$/);
    // A tap elsewhere puts it away.
    await page.getByRole("heading", { name: "Servers" }).tap();
    await expect(tooltip(page)).toHaveCount(0);
  });
});

test("each role a member can be given says in a few words what it can do", async ({ page, as }) => {
  await as("owner");
  const sent: unknown[] = [];
  await page.route(/\/api\/v1\/orgs\/friends\/members\/[^/]+$/, (route) => {
    if (route.request().method() !== "PATCH") return route.continue();
    sent.push(route.request().postDataJSON());
    return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "test", message: "Not sent by the test." }) });
  });
  await page.goto("/o/friends/members");
  const role = page.getByRole("button", { name: "Role of viewer" });
  await expect(role).toHaveText("Viewer");

  await role.click();
  const menu = page.getByRole("menu");
  const said = {
    Viewer: "Sees servers and consoles",
    Operator: "Starts servers, runs commands",
    Admin: "Sets up servers and members",
    Owner: "Everything, even other owners",
  };
  for (const [name, about] of Object.entries(said)) {
    const choice = menu.getByRole("menuitemradio", { name, exact: true });
    await expect(choice).toHaveAccessibleDescription(about);
    await expect(choice.getByText(about)).toBeVisible();
  }
  await expect(menu.getByRole("menuitemradio", { name: "Viewer", exact: true })).toBeChecked();
  await page.keyboard.press("Escape");
  await expect(menu).toHaveCount(0);
  expect(sent).toEqual([]);

  // From the keyboard: opened, gone through, and one picked.
  await role.focus();
  await page.keyboard.press("Enter");
  await expect(menu.getByRole("menuitemradio", { name: "Viewer", exact: true })).toBeFocused();
  await page.keyboard.press("ArrowDown");
  await expect(menu.getByRole("menuitemradio", { name: "Operator", exact: true })).toBeFocused();
  await page.keyboard.press("Enter");
  await expect.poll(() => sent).toEqual([{ role: "operator" }]);
});

test("the word beside a member's tick in a server's players says what it means", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/s/survival/settings");
  // Operators and above are operators here, by role.
  const ownerOp = page.getByRole("checkbox", { name: /^Operator: Owner$/ });
  await expect(ownerOp).toBeChecked();
  await expect(ownerOp).toHaveAccessibleDescription("Follows their role");

  await page.getByRole("row", { name: /^Owner/ }).getByText("by role").last().hover();
  await expect(tooltip(page)).toHaveText("Follows their role");
  await page.mouse.move(0, 0);
  await expect(tooltip(page)).toHaveCount(0);

  await tabTo(page, page.getByRole("checkbox", { name: /^Whitelisted: Owner$/ }), ownerOp);
  await expect(tooltip(page)).toHaveText("Follows their role");
  await page.keyboard.press("Space");
  await expect(ownerOp).not.toBeChecked();
  await expect(tooltip(page)).toHaveText("Taken off by hand");
  await expect(ownerOp).toHaveAccessibleDescription("Taken off by hand");

  const viewerOp = page.getByRole("checkbox", { name: /^Operator: Viewer$/ });
  await viewerOp.check();
  await expect(viewerOp).toHaveAccessibleDescription("Added by hand");
});

const example = "Steve\nAlex\n069a79f4-44e9-4726-a5be-fca90e38aaf5";

test("a server's player boxes show what to type in them, in its settings and when it is made", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/s/survival/settings");
  for (const box of ["Whitelisted players", "Operators"]) await expect(page.getByLabel(box, { exact: true })).toHaveAttribute("placeholder", example);
  await expect(page.getByText("Usernames or UUIDs, one per line.")).toHaveCount(0);

  await page.goto("/o/friends/new?from=server:survival");
  for (const box of ["Whitelisted players", "Operators"]) await expect(page.getByLabel(box, { exact: true })).toHaveAttribute("placeholder", example);
});

test("a saved list's box shows what to type, for each edition", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/players`);
  await page.getByRole("button", { name: "New list" }).click();
  const dialog = page.getByRole("dialog", { name: "New list" });
  await expect(dialog.getByLabel("Players")).toHaveAttribute("placeholder", "Steve\nAlex");
  await dialog.getByRole("button", { name: "Bedrock" }).click();
  await expect(dialog.getByLabel("Players")).toHaveAttribute("placeholder", "Steve\nAlex Smith");
});

/** Says a link opens the page in a tab of its own, without saying where it came from. */
async function opensApart(link: Locator, href: string) {
  await expect(link).toHaveAttribute("href", href);
  await expect(link).toHaveAttribute("target", "_blank");
  await expect(link).toHaveAttribute("rel", "noreferrer");
  await expect(link).toHaveText("Docs");
}

test("server.properties and the image's options link to where they are told of, in settings and when a server is made", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/s/survival/settings");
  await opensApart(page.getByRole("link", { name: "Docs: server.properties" }), "https://minecraft.wiki/w/Server.properties");
  await opensApart(page.getByRole("link", { name: "Docs: itzg/minecraft-server image" }), "https://docker-minecraft-server.readthedocs.io/");

  await page.goto("/o/friends/new?from=server:survival");
  await page.getByRole("button", { name: /Player limit, PvP and more/ }).click();
  await opensApart(page.getByRole("link", { name: "Docs: server.properties" }), "https://minecraft.wiki/w/Server.properties");
});
