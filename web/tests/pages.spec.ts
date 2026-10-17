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
  ["/o/friends/s/survival/content", "LuckPerms"],
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

test("earlier logs and crash reports can be opened from the console, searched and downloaded", async ({ page, as }) => {
  await as("viewer");
  const base = "**/api/v1/orgs/friends/servers/survival/logs";
  await page.route(base, (route) =>
    route.fulfill({
      json: {
        logs: [
          { name: "logs/latest.log", kind: "current", size: 2048, modTime: new Date().toISOString() },
          { name: "crash-reports/crash-2026-10-14_03.00.00-server.txt", kind: "crash", size: 9000, modTime: "2026-10-14T03:00:00Z" },
          { name: "logs/2026-10-14-1.log.gz", kind: "log", size: 1800, modTime: "2026-10-14T01:00:00Z" },
        ],
      },
    }),
  );
  // An earlier run of 2500 lines, read from its end a page at a time, with one error in it.
  const all = Array.from({ length: 2500 }, (_, i) => (i === 99 ? "[01:00:00] [Server thread/ERROR]: Something broke" : `[01:00:00] [Server thread/INFO]: line ${i + 1}`));
  const asked: string[] = [];
  let failNext = false;
  await page.route(`${base}/file*`, (route) => {
    const url = new URL(route.request().url());
    if (failNext && url.searchParams.has("before")) return route.fulfill({ status: 502, json: { code: "workspace", message: "The workspace is gone." } });
    asked.push(url.search);
    const q = url.searchParams.get("q");
    if (q) {
      const hits = all.flatMap((text, i) => (text.toLowerCase().includes(q.toLowerCase()) ? [{ n: i + 1, text, match: true }] : []));
      return route.fulfill({ json: { name: "logs/2026-10-14-1.log.gz", total: all.length, truncated: false, firstError: 100, lines: hits, search: q } });
    }
    if (url.searchParams.has("from")) {
      const from = Number(url.searchParams.get("from"));
      const lines = all.slice(from - 1, from - 1 + 2000).map((text, i) => ({ n: from + i, text }));
      return route.fulfill({ json: { name: "logs/2026-10-14-1.log.gz", total: all.length, truncated: false, firstError: 100, lines } });
    }
    const before = Number(url.searchParams.get("before") ?? 0) || all.length + 1;
    const start = Math.max(0, before - 1 - 2000);
    const lines = all.slice(start, before - 1).map((text, i) => ({ n: start + i + 1, text }));
    return route.fulfill({ json: { name: "logs/2026-10-14-1.log.gz", total: all.length, truncated: false, firstError: 100, lines, more: start > 0 } });
  });

  await page.goto("/o/friends/s/survival");
  const picker = page.getByLabel("Log", { exact: true });
  await expect(picker.locator("option")).toHaveCount(4);
  await picker.selectOption("logs/2026-10-14-1.log.gz");
  await expect(page.getByText("line 2500", { exact: false })).toBeVisible();
  await expect(page.getByText("Something broke")).toHaveCount(0);
  // The first error is before the page read: it is gone to, and read on from.
  await page.getByRole("button", { name: "First error" }).click();
  await expect(page.getByText("Something broke")).toBeInViewport();
  expect(asked.some((q) => q.includes("from=80"))).toBe(true);
  await expect(page.locator('[data-n="80"]')).toBeVisible();
  await page.getByRole("button", { name: "Load later lines" }).click();
  await expect(page.locator('[data-n="2500"]')).toHaveCount(1);
  await expect(page.getByRole("button", { name: "Load later lines" })).toHaveCount(0);
  await page.getByRole("button", { name: "Load earlier lines" }).click();
  await expect(page.locator('[data-n="1"]')).toHaveCount(1);
  // All read, and back to the end from there.
  await page.getByRole("button", { name: "Go to the end" }).click();
  await expect(page.locator('[data-n="1"]')).toHaveCount(0);
  await expect(page.getByText("Something broke")).toHaveCount(0);
  // A page that cannot be read says so.
  failNext = true;
  await page.getByRole("button", { name: "Load earlier lines" }).click();
  await expect(page.getByText("The workspace is gone.")).toBeVisible();
  failNext = false;
  // Back to the end, read from there as before.
  await picker.selectOption("");
  await picker.selectOption("logs/2026-10-14-1.log.gz");
  await expect(page.getByText("Something broke")).toHaveCount(0);
  await page.getByRole("button", { name: "Load earlier lines" }).click();
  await expect(page.getByText("Something broke")).toBeVisible();
  expect(asked.some((q) => q.includes("before=501"))).toBe(true);

  await page.getByLabel("Search this log").fill("ERROR");
  await page.keyboard.press("Enter");
  await expect(page.locator("[data-n]")).toHaveCount(1);
  await expect(page.locator('[data-n="100"]')).toContainText("Something broke");
  await expect(page.getByRole("button", { name: "First error" })).toBeDisabled();
  await page.getByLabel("Search this log").fill("no line says this");
  await page.keyboard.press("Enter");
  await expect(page.getByText("Nothing in this log matches")).toBeVisible();

  await expect(page.getByRole("link", { name: "Download" })).toHaveAttribute("href", /download=gz$/);
  await expect(page.getByRole("link", { name: "Unpacked" })).toHaveAttribute("href", /download=text$/);

  // Back to the game as it runs.
  await picker.selectOption("");
  await expect(page.getByLabel("Search this log")).toHaveCount(0);
});

test("a page open while the panel is updated says so, and offers to reload", async ({ page, as }) => {
  await as("viewer");
  let updated = false;
  let answered = 0;
  await page.route("**/api/v1/meta", async (route) => {
    const meta = await (await route.fetch()).json();
    await route.fulfill({ json: updated ? { ...meta, version: "sha-next" } : meta });
    answered++;
  });
  await page.goto("/o/friends");
  await expect(page.getByRole("heading", { name: "Servers" })).toBeVisible();
  // Both looks at the version as the page loads are answered first: one
  // still on its way when the tab comes back is not asked again.
  await expect.poll(() => answered).toBeGreaterThanOrEqual(2);
  await expect(page.getByText(/avSYS was updated/)).toHaveCount(0);
  updated = true;
  // Coming back to the tab asks again.
  await page.evaluate(() => window.dispatchEvent(new Event("visibilitychange")));
  await expect(page.getByRole("status").filter({ hasText: "avSYS was updated to sha-next." })).toBeVisible();
  await page.getByRole("button", { name: "Reload" }).click();
  await expect(page.getByText(/avSYS was updated/)).toHaveCount(0);
});

test("an organization's domains are set by the panel's administrator, and shown to its members", async ({ page, as, org }) => {
  const a = await org([]);
  const b = await org([]);
  // Of this organization: both engines run at once against one panel.
  const mine = `play.${a}.example`;
  await as("owner");
  await page.goto(`/o/${a}/settings`);
  // Written as a wildcard too, it is the same domain.
  await page.getByLabel("One per line").fill(`${mine}\n*.${mine.toUpperCase()}`);
  await page.getByRole("button", { name: "Save domains" }).click();
  await expect(page.getByText("Domains saved")).toBeVisible();
  await expect(page.getByLabel("One per line")).toHaveValue(mine);

  // Another organization cannot take one overlapping it.
  await page.goto(`/o/${b}/settings`);
  await page.getByLabel("One per line").fill(`mc.${mine}`);
  await page.getByRole("button", { name: "Save domains" }).click();
  await expect(page.getByText(`mc.${mine} overlaps ${mine}`, { exact: false })).toBeVisible();

  // The new-server form says which are its own.
  await page.goto(`/o/${a}/new`);
  await expect(page.getByText(`This organization's domains: ${mine}.`)).toBeVisible();
});

test("an organization's admin sees its domains and cannot change them", async ({ page, as }) => {
  await as("admin");
  await page.goto("/o/friends/settings");
  await expect(page.getByRole("heading", { name: "Domains" })).toBeVisible();
  await expect(page.getByText(/Given by the panel's administrator/)).toBeVisible();
  await expect(page.getByLabel("One per line")).toHaveCount(0);
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

test("a server's content is its Content tab, and a link to where it was before still finds it", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/s/survival");
  await page.getByRole("link", { name: "Content", exact: true }).click();
  await expect(page).toHaveURL(/\/o\/friends\/s\/survival\/content$/);
  await expect(content(page).getByText("LuckPerms").first()).toBeVisible();
  await page.goto("/o/friends/s/survival/mods");
  await expect(page).toHaveURL(/\/o\/friends\/s\/survival\/content$/);
  await expect(content(page).getByText("LuckPerms").first()).toBeVisible();
});
