import { content, expect, test, type Role } from "./panel";

// Every page, opened cold by someone who may see all of it and by someone
// who may only look. Each has to draw something of its own: a window with
// nothing in it has no text to find, and an error thrown on the way fails
// the test by itself.
const pages: [path: string, shows: string | RegExp][] = [
  ["/o/friends", "Survival"],
  ["/o/friends/backups", "Backups"],
  ["/o/friends/members", "Members"],
  ["/o/friends/settings", /Settings|Organization/],
  ["/account", "Account"],
  ["/o/friends/s/survival", "Survival"],
  ["/o/friends/s/survival/mods", "LuckPerms"],
  ["/o/friends/s/survival/backups", /snapshot/i],
  ["/o/friends/s/survival/settings", "Survival"],
  // These two are loaded on demand, from files of their own.
  ["/o/friends/s/survival/files", "Survival"],
  ["/o/friends/s/survival/terminal", "Survival"],
];

for (const who of ["owner", "viewer"] satisfies Role[]) {
  for (const [path, shows] of pages) {
    test(`${path} opens for the ${who}`, async ({ page, as }) => {
      await as(who);
      await page.goto(path);
      await expect(content(page).getByText(shows).first()).toBeVisible();
      await expect(page.getByRole("alert").filter({ hasText: /went wrong|needs reloading/ })).toHaveCount(0);
    });
  }
}

test("the new-server page opens for the owner", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/new");
  await expect(page.getByRole("heading", { name: "New server" })).toBeVisible();
  await expect(page.getByRole("group", { name: "Server type" }).getByRole("button")).not.toHaveCount(0);
});

test("the servers list shows each state a server can be in", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends");
  const row = (name: string) => page.getByRole("listitem").filter({ hasText: name });
  await expect(row("Survival")).toContainText("Online");
  await expect(row("Survival")).toContainText("2 / 20");
  await expect(row("Creative")).toContainText("Offline");
  await expect(row("Lobby")).toContainText("Asleep");
  await expect(row("Broken")).toContainText("Error");
});

test("someone who is in no organization is not shown one", async ({ page, as }) => {
  await as("outsider");
  await page.goto("/o/friends");
  await expect(content(page).getByText("Survival")).toHaveCount(0);
});

test("signing in with a password", async ({ page, password }) => {
  await page.goto("/o/friends");
  await expect(page).toHaveURL(/\/login/);
  await page.getByLabel("Username").fill("owner");
  await page.getByLabel("Password").fill("not the password");
  await page.getByRole("button", { name: /sign in/i }).click();
  await expect(page.getByText(/incorrect|wrong|invalid/i)).toBeVisible();
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: /sign in/i }).click();
  await expect(page).toHaveURL(/\/o\/friends$/);
  await expect(content(page).getByText("Survival").first()).toBeVisible();
});
