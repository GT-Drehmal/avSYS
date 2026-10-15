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

test("a picture still on its way is not stood in for by a letter, and one seen before is there at once", async ({ page, as }) => {
  await as("owner");
  const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAEElEQVR4nGM4oREFRAwQCgAl3gUpEzqvXwAAAABJRU5ErkJggg==", "base64");
  let release: () => void = () => {};
  const held = new Promise<void>((resolve) => (release = resolve));
  // Survival has a picture, slow to come; creative has none.
  await page.route("**/api/v1/orgs/friends/servers/survival/icon*", async (route) => {
    await held;
    await route.fulfill({ contentType: "image/png", body: png });
  });
  await page.route("**/api/v1/orgs/friends/servers/creative/icon*", (route) => route.fulfill({ status: 404, json: { code: "not_found", message: "Not found." } }));
  await page.goto("/o/friends");
  const row = (name: string) => page.getByRole("link", { name: new RegExp(`^${name}`) }).first();
  await expect(row("Creative")).toContainText("C");
  // While survival's is on its way, no letter is shown for it.
  await expect(row("Survival").locator("span[aria-hidden]").first()).toHaveText("");
  release();
  await expect(row("Survival").locator("img")).toHaveCSS("opacity", "1");

  // Away and back: shown at once, with no plain tile first.
  await page.getByRole("link", { name: "Members" }).click();
  await page.getByRole("link", { name: "Servers" }).click();
  await expect(row("Survival").locator("img")).toBeVisible();
  await expect(row("Survival").locator("span[aria-hidden]")).toHaveCount(0);
});

test("the console says a server is starting, and only a stopped one is not running", async ({ page, as }) => {
  await as("owner");
  // As the panel answers while the game is not up.
  await page.route("**/api/v1/orgs/friends/servers/creative/console", (route) =>
    route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body: "event: waiting\ndata: null\n\n" }),
  );
  await page.goto("/o/friends/s/creative");
  await expect(page.getByText("This server is not running. Its log appears here once it starts.")).toBeVisible();

  await page.route("**/api/v1/orgs/friends/servers", async (route) => {
    const list = await (await route.fetch()).json();
    return route.fulfill({ json: list.map((s: { name: string; status: object }) => (s.name === "creative" ? { ...s, status: { ...s.status, phase: "Starting" } } : s)) });
  });
  await page.reload();
  await expect(page.getByText("The game is starting. Its log appears here as soon as it is up.")).toBeVisible();
});

test("members can be put on a server's whitelist and operators one by one, beside their role", async ({ page, as, api }) => {
  await as("owner");
  const members: { userId: string; username: string }[] = await api("owner").get("/orgs/friends/members");
  const id = (name: string) => members.find((m) => m.username === name)!.userId;
  const sent: { spec: Record<string, unknown> }[] = [];
  await page.route("**/api/v1/orgs/friends/servers/survival", (route) => {
    if (route.request().method() !== "PATCH") return route.continue();
    sent.push(route.request().postDataJSON());
    return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "test", message: "Not sent by the test." }) });
  });
  await page.goto("/o/friends/s/survival/settings");
  // Nobody is whitelisted by role here; operators and above are operators.
  const viewerWhitelisted = page.getByRole("checkbox", { name: /^Whitelisted: Viewer$/ });
  const ownerOp = page.getByRole("checkbox", { name: /^Operator: Owner$/ });
  await expect(viewerWhitelisted).not.toBeChecked();
  await expect(ownerOp).toBeChecked();
  await expect(page.getByRole("checkbox", { name: /^Operator: Viewer$/ })).not.toBeChecked();
  await viewerWhitelisted.check();
  await ownerOp.uncheck();
  await expect(page.getByRole("row", { name: /^Viewer/ })).toContainText("picked");
  await expect(page.getByRole("row", { name: /^Owner/ })).toContainText("left out");
  await page.getByRole("button", { name: /^Save/ }).click();
  await expect.poll(() => sent.length).toBe(1);
  expect(sent[0].spec.whitelistMembers).toEqual({ also: [id("viewer")], except: [] });
  expect(sent[0].spec.opMembers).toEqual({ also: [], except: [id("owner")] });
});
