import { expect, type Page, test } from "./panel";

// Completing commands at the console, from the game's command tree for the
// server's version: the test panel has the one recorded for 1.21.4
// (tests/upstream/commands), and none for any other version. Its running
// servers answer help as a server with Yet Another Bingo did
// (tests/upstream/commands/help), so they have its commands too.

test.beforeEach(async ({ as }) => {
  await as("owner");
});

const line = (page: Page) => page.getByRole("combobox", { name: "Server command" });
const options = (page: Page) => page.getByRole("listbox", { name: "Suggestions" }).getByRole("option");
const chosen = (page: Page) => page.getByRole("listbox", { name: "Suggestions" }).getByRole("option", { selected: true });
const ghost = (page: Page) => page.getByTestId("ghost");
const hints = (page: Page) => page.getByTestId("hints");
const consoleBox = (page: Page) => page.locator("form").filter({ has: line(page) }).locator("xpath=..");

/** The console of the running Paper 1.21.4 server, once its commands are there. */
async function openConsole(page: Page) {
  await page.goto("/o/friends/s/survival");
  const box = line(page);
  await expect(box).toBeEnabled();
  // The first time anywhere, the panel makes the tree: asked again until it is there.
  await expect(async () => {
    await box.fill("");
    await box.pressSequentially("ti");
    await expect(options(page).first()).toBeVisible({ timeout: 1000 });
  }).toPass({ timeout: 15_000 });
  await box.fill("");
  return box;
}

test("commands that start with what is typed are offered, and Tab puts the chosen one in", async ({ page }) => {
  const box = await openConsole(page);
  await box.pressSequentially("ti");
  await expect(options(page)).toHaveText(["tick", "time", "title"]);
  await expect(chosen(page)).toHaveText("tick");
  // The chosen one shows faded after what is typed.
  await expect(ghost(page)).toHaveText("ck");
  await box.press("ArrowDown");
  await expect(chosen(page)).toHaveText("time");
  await expect(ghost(page)).toHaveText("me");
  await box.press("Tab");
  await expect(box).toHaveValue("time ");
  await expect(box).toBeFocused();
  // Its subcommands come next, each on its own.
  await expect(options(page)).toHaveText(["add", "query", "set"]);
  await box.press("ArrowUp");
  await expect(chosen(page)).toHaveText("set");
  await box.press("Tab");
  await expect(box).toHaveValue("time set ");
  await expect(options(page)).toHaveText(["day", "midnight", "night", "noon"]);
  // And the argument that can go there instead.
  await expect(hints(page).getByText("<time>", { exact: true })).toBeVisible();
});

test("undo takes a completion out, and keeps what was typed", async ({ page }) => {
  const box = await openConsole(page);
  await box.pressSequentially("wea");
  await box.press("Tab");
  await expect(box).toHaveValue("weather ");
  await box.press("ControlOrMeta+z");
  await expect(box).toHaveValue("wea");
  await expect(options(page)).toHaveText(["weather"]);
  // Redo puts it back.
  await box.press("ControlOrMeta+Shift+z");
  await expect(box).toHaveValue("weather ");
  // Two completions are taken out one at a time.
  await box.pressSequentially("c");
  await box.press("Tab");
  await expect(box).toHaveValue("weather clear ");
  await box.press("ControlOrMeta+z");
  await expect(box).toHaveValue("weather c");
  await box.press("ControlOrMeta+z");
  await expect(box).toHaveValue("weather ");
  await box.press("ControlOrMeta+z");
  await expect(box).toHaveValue("wea");
  // Typing somewhere else in the line at once is a step of its own.
  await box.fill("");
  await box.pressSequentially("say hello");
  await box.press("Home");
  await box.pressSequentially("x");
  await expect(box).toHaveValue("xsay hello");
  await box.press("ControlOrMeta+z");
  await expect(box).toHaveValue("say hello");
});

test("a click puts a suggestion in, and the line keeps the focus", async ({ page }) => {
  const box = await openConsole(page);
  await box.pressSequentially("game");
  await expect(options(page)).toHaveText(["gamemode", "gamerule"]);
  await options(page).filter({ hasText: "gamerule" }).click();
  await expect(box).toHaveValue("gamerule ");
  await expect(box).toBeFocused();
  await box.pressSequentially("keepInv");
  await box.press("Tab");
  await box.press("Tab");
  await expect(box).toHaveValue("gamerule keepInventory true ");
});

test("the argument expected is shown, with what it takes", async ({ page }) => {
  const box = await openConsole(page);
  await box.pressSequentially("give ");
  await expect(hints(page).getByText("<targets>", { exact: true })).toBeVisible();
  await expect(hints(page).getByText("players", { exact: true })).toBeVisible();
  // Who is on is offered, and the selectors.
  await expect(options(page)).toHaveText(["Steve", "Alex", "@a", "@p", "@r", "@s"]);
  await box.pressSequentially("Al");
  await box.press("Tab");
  await expect(box).toHaveValue("give Alex ");
  await expect(hints(page).getByText("<item>", { exact: true })).toBeVisible();
  // With nothing to offer, the argument shows faded in the line.
  await expect(ghost(page)).toHaveText("<item>");
  await box.fill("");
  await box.pressSequentially("say hello");
  await expect(hints(page).getByText("<message>", { exact: true })).toBeVisible();
  await expect(options(page)).toHaveCount(0);
});

test("all of it works from the keyboard alone", async ({ page }) => {
  const box = await openConsole(page);
  await page.getByRole("combobox", { name: "Log" }).focus();
  // Tab goes from the log menu, past the console, to the line.
  for (let i = 0; i < 10 && !(await box.evaluate((el) => el === document.activeElement)); i++) await page.keyboard.press("Tab");
  await expect(box).toBeFocused();
  await page.keyboard.type("li");
  await expect(options(page)).toHaveText(["list"]);
  await page.keyboard.press("Tab");
  await expect(box).toHaveValue("list ");
  // Escape closes the list; Enter runs the line as typed.
  await page.keyboard.type("u");
  await expect(options(page)).toHaveText(["uuids"]);
  await page.keyboard.press("Escape");
  await expect(options(page)).toHaveCount(0);
  await expect(ghost(page)).toHaveText("");
  await page.keyboard.press("Enter");
  await expect(page.getByText("> list u")).toBeVisible();
  await expect(box).toHaveValue("");
  // With no list, up and down go through the commands run before.
  await page.keyboard.press("ArrowUp");
  await expect(box).toHaveValue("list u");
  await expect(options(page)).toHaveCount(0);
  await page.keyboard.press("ArrowDown");
  await expect(box).toHaveValue("");
  // With nothing to complete, Tab leaves the line, as anywhere else.
  await page.keyboard.press("Tab");
  await expect(box).not.toBeFocused();
});

test("the commands of a server's mods are offered too, from its help", async ({ page }) => {
  const box = await openConsole(page);
  await box.pressSequentially("bingo di");
  await expect(options(page)).toHaveText(["difficulty"]);
  await box.press("Tab");
  await expect(box).toHaveValue("bingo difficulty ");
  await expect(options(page)).toHaveText(["easy", "extreme", "hard", "impossible", "medium"]);
  await box.pressSequentially("ha");
  await expect(options(page)).toHaveText(["hard"]);
  await expect(ghost(page)).toHaveText("rd");
  await box.press("Tab");
  await expect(box).toHaveValue("bingo difficulty hard ");
  // An argument is hinted by its name alone: what it takes is not known.
  await box.fill("");
  await box.pressSequentially("bingo timelimit ");
  await expect(options(page)).toHaveText(["off"]);
  await expect(hints(page)).toHaveText("<minutes>");
  await box.fill("");
  await box.pressSequentially("bingo shuffleteams ");
  await expect(ghost(page)).toHaveText("<number_of_teams>");
  // Any word goes for it, and nothing after it is taken for a mistake.
  await box.pressSequentially("4 ");
  await expect(page.getByRole("listbox")).toHaveCount(0);
  await expect(hints(page)).toHaveCount(0);
  // The game's own are as they were.
  await box.fill("");
  await box.pressSequentially("ti");
  await expect(options(page)).toHaveText(["tick", "time", "title"]);
});

test("a server of a version with no command list is typed at as before", async ({ page }) => {
  await page.goto("/o/club/s/classic");
  const box = line(page);
  await expect(box).toBeEnabled();
  await box.pressSequentially("ti");
  // Asked, and told there is none: nothing is offered, and nothing goes wrong.
  await page.waitForTimeout(500);
  await expect(page.getByRole("listbox")).toHaveCount(0);
  await expect(ghost(page)).toHaveText("");
  await box.press("Tab");
  await expect(box).toHaveValue("ti");
});

test("the list in both themes", async ({ page }) => {
  for (const scheme of ["dark", "light"] as const) {
    await page.emulateMedia({ colorScheme: scheme });
    const box = await openConsole(page);
    await box.pressSequentially("execute as @a run ti");
    await box.press("ArrowDown");
    await expect(chosen(page)).toHaveText("time");
    await consoleBox(page).screenshot({ path: test.info().outputPath(`completion-${scheme}.png`) });
    await box.fill("");
    await box.pressSequentially("give @p diamond ");
    await expect(ghost(page)).toHaveText("[<count>]");
    await consoleBox(page).screenshot({ path: test.info().outputPath(`hint-${scheme}.png`) });
  }
});
