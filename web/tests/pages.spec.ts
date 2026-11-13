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
  ["/o/friends/s/survival/usage", "Usage shows here while the server runs."],
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
  // The server's own picture: the faces of others on it may follow.
  const tile = (name: string) => row(name).locator("span.relative.inline-flex").first();
  await expect(row("Creative")).toContainText("C");
  // While survival's is on its way, no letter is shown for it.
  await expect(tile("Survival").locator("span[aria-hidden]")).toHaveText("");
  release();
  await expect(tile("Survival").locator("img")).toHaveCSS("opacity", "1");

  // Away and back: shown at once, with no plain tile first.
  await page.getByRole("link", { name: "Members" }).click();
  await page.getByRole("link", { name: "Servers" }).click();
  await expect(tile("Survival").locator("img")).toBeVisible();
  await expect(tile("Survival").locator("span[aria-hidden]")).toHaveCount(0);
});

test("the console says a server is starting, and only a stopped one is not running", async ({ page, as }) => {
  await as("owner");
  // As the panel answers while the game is not up.
  await page.route("**/api/v1/orgs/friends/servers/creative/console", (route) =>
    route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body: "event: waiting\ndata: null\n\n" }),
  );
  await page.goto("/o/friends/s/creative");
  await expect(page.getByText("The server is not running.")).toBeVisible();

  await page.route("**/api/v1/orgs/friends/servers", async (route) => {
    const list = await (await route.fetch()).json();
    return route.fulfill({ json: list.map((s: { name: string; status: object }) => (s.name === "creative" ? { ...s, status: { ...s.status, phase: "Starting" } } : s)) });
  });
  await page.reload();
  await expect(page.getByText("The server is starting.")).toBeVisible();
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
  await expect(page.getByText(`Your domains: ${mine}.`)).toBeVisible();
});

test("an organization's admin sees its domains and cannot change them", async ({ page, as }) => {
  await as("admin");
  await page.goto("/o/friends/settings");
  await expect(page.getByRole("heading", { name: "Domains" })).toBeVisible();
  await expect(page.getByText(/Set by the panel's administrator/)).toBeVisible();
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

test("the first load is the page opened, and the others follow when there is nothing else to do", async ({ page, as }) => {
  const fetched: string[] = [];
  page.on("request", (r) => {
    const name = /\/assets\/([A-Za-z]+)-.*\.js$/.exec(r.url())?.[1];
    if (name) fetched.push(name);
  });
  await page.goto("/login");
  await expect(page.getByRole("button", { name: /sign in|log in/i })).toBeVisible();
  // Signing in has not happened, so nothing of the panel itself is asked for.
  expect(fetched).not.toContain("Servers");
  expect(fetched).not.toContain("Backups");
  await as("owner");
  await page.goto("/o/friends");
  await expect(page.getByRole("heading", { name: "Servers" })).toBeVisible();
  await expect.poll(() => fetched, { timeout: 15_000 }).toEqual(expect.arrayContaining(["Servers", "Backups", "Worlds", "Templates"]));
  // The editor and the terminal wait until they are opened.
  expect(fetched).not.toContain("Terminal");
});

test("a page that could not be fetched, as after the panel was updated, is loaded again", async ({ page, as, browserName }) => {
  await as("owner");
  let refused = 0;
  await page.route("**/assets/Backups-*.js", (route) => {
    if (refused++ === 0) return route.abort();
    return route.continue();
  });
  await page.goto("/o/friends/backups");
  const backups = page.getByRole("heading", { name: "Backups" });
  // WebKit holds on to the failure of that address through reloads for a
  // while. An update does not meet that, as its parts have new addresses, so
  // all that is asked of it here is to say so rather than show nothing.
  if (browserName === "webkit") {
    await expect(backups.or(page.getByRole("heading", { name: "This page needs reloading" }))).toBeVisible({ timeout: 15_000 });
    return;
  }
  await expect(backups).toBeVisible({ timeout: 15_000 });
  expect(refused).toBeGreaterThan(1);
});

test("a page fetched ahead that fails to arrive leaves the page open alone", async ({ page, as }) => {
  await as("owner");
  let refused = 0;
  await page.route("**/assets/Worlds-*.js", (route) => {
    refused++;
    return route.abort();
  });
  let loads = 0;
  page.on("load", () => loads++);
  await page.goto("/o/friends");
  await expect(page.getByRole("heading", { name: "Servers" })).toBeVisible();
  // Something typed is still there once the fetch has failed.
  await page.getByLabel("Search servers").fill("kept");
  await expect.poll(() => refused, { timeout: 15_000 }).toBeGreaterThan(0);
  await page.waitForTimeout(1000);
  expect(loads).toBe(1);
  await expect(page.getByLabel("Search servers")).toHaveValue("kept");
});

test("a server's picture opens larger when clicked, for an admin to change or remove and for anyone to see", async ({ page, as }) => {
  await as("owner");
  const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
  const asked: string[] = [];
  await page.route("**/api/v1/orgs/friends/servers/survival/icon*", (route) => {
    const method = route.request().method();
    if (method === "GET") return route.fulfill({ status: 200, contentType: "image/png", body: png });
    asked.push(method);
    return route.fulfill({ status: 204 });
  });
  await page.goto("/o/friends/s/survival");
  await page.getByRole("button", { name: "Picture of Survival" }).click();
  const dialog = page.getByRole("dialog", { name: "Survival" });
  await expect(dialog.getByText("Shown in the server list after a restart.")).toBeVisible();
  await dialog.getByLabel("Picture for Survival").setInputFiles({ name: "new.png", mimeType: "image/png", buffer: png });
  await expect.poll(() => asked).toEqual(["PUT"]);
  await dialog.getByRole("button", { name: "Remove" }).click();
  await expect.poll(() => asked).toEqual(["PUT", "DELETE"]);
  await page.keyboard.press("Escape");

  // Anyone else sees it, with nothing to change it with.
  await as("viewer");
  await page.reload();
  await page.getByRole("button", { name: "Picture of Survival" }).click();
  const seen = page.getByRole("dialog", { name: "Survival" });
  await expect(seen.locator("img")).toBeVisible();
  await expect(seen.getByRole("button", { name: /Replace|Upload|Remove/ })).toHaveCount(0);
});

test("an organization's picture opens larger from its settings, for an owner to change or remove and for an admin to see", async ({ page, as }) => {
  await as("owner");
  const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
  const asked: string[] = [];
  // Friends has a picture, and changing it changes nothing anyone else sees.
  await page.route("**/api/v1/me", async (route) => {
    const me = await (await route.fetch()).json();
    return route.fulfill({ json: { ...me, orgs: me.orgs.map((o: { slug: string }) => (o.slug === "friends" ? { ...o, hasIcon: true } : o)) } });
  });
  await page.route("**/api/v1/orgs/friends/icon*", (route) => {
    const method = route.request().method();
    if (method === "GET") return route.fulfill({ status: 200, contentType: "image/png", body: png });
    asked.push(method);
    return route.fulfill({ status: 204 });
  });
  await page.goto("/o/friends/settings");
  await page.getByRole("button", { name: "Picture of Friends" }).click();
  const dialog = page.getByRole("dialog", { name: "Friends" });
  await expect(dialog.locator("img")).toBeVisible();
  await dialog.getByLabel("Picture for Friends").setInputFiles({ name: "new.png", mimeType: "image/png", buffer: png });
  await expect.poll(() => asked).toEqual(["PUT"]);
  await dialog.getByRole("button", { name: "Remove" }).click();
  await expect.poll(() => asked).toEqual(["PUT", "DELETE"]);
  await page.keyboard.press("Escape");

  // An admin sees it, with nothing to change it with: it is owners' to change.
  await as("admin");
  await page.reload();
  await expect(page.getByRole("heading", { name: "Domains" })).toBeVisible();
  await expect(page.getByRole("button", { name: /^(Replace|Upload|Remove)$/ })).toHaveCount(0);
  await page.getByRole("button", { name: "Picture of Friends" }).click();
  const seen = page.getByRole("dialog", { name: "Friends" });
  await expect(seen.locator("img")).toBeVisible();
  await expect(seen.getByRole("button", { name: /Replace|Upload|Remove/ })).toHaveCount(0);
});

test("a picture being changed on the page cannot be changed again in its dialog until that is done", async ({ page, as }) => {
  await as("owner");
  const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");
  await page.route("**/api/v1/me", async (route) => {
    const me = await (await route.fetch()).json();
    return route.fulfill({ json: { ...me, orgs: me.orgs.map((o: { slug: string }) => (o.slug === "friends" ? { ...o, hasIcon: true } : o)) } });
  });
  // The upload is held until the test says.
  let release = () => {};
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  const asked: string[] = [];
  await page.route("**/api/v1/orgs/friends/icon*", async (route) => {
    const method = route.request().method();
    if (method === "GET") return route.fulfill({ status: 200, contentType: "image/png", body: png });
    asked.push(method);
    if (method === "PUT") await held;
    return route.fulfill({ status: 204 });
  });
  await page.goto("/o/friends/settings");
  await page.getByLabel("Picture for Friends").setInputFiles({ name: "new.png", mimeType: "image/png", buffer: png });
  await expect.poll(() => asked).toEqual(["PUT"]);
  await page.getByRole("button", { name: "Picture of Friends" }).click();
  const dialog = page.getByRole("dialog", { name: "Friends" });
  await expect(dialog.getByRole("button", { name: "Remove" })).toBeDisabled();
  await expect(dialog.getByRole("button", { name: "Replace" })).toBeDisabled();
  release();
  await expect(dialog.getByRole("button", { name: "Remove" })).toBeEnabled();
  await dialog.getByRole("button", { name: "Remove" }).click();
  await expect.poll(() => asked).toEqual(["PUT", "DELETE"]);
});

test("your own picture opens larger from your account, to change or remove, and a photo is not drawn blocky", async ({ page, as }) => {
  await as("owner");
  // 128 pixels square, as an uploaded one is kept: shown at 192, it is smoothed, not drawn in blocks.
  const png = Buffer.from(
    "iVBORw0KGgoAAAANSUhEUgAAAIAAAACACAIAAABMXPacAAAAyElEQVR42u3RQQ0AAAjEsJOGNKQhDRnwaDIFa6pHh8UCAAAEAIAAABAAAAIAQAAACAAAAQAgAAAEAIAAABAAAAIAQAAACAAAAQAgAAAEAIAAABAAAAIAQAAACAAAAQAgAAAEAIAAABAAAAIAQAAACAAAAQAAwAUAAAQAgAAAEAAAAgBAAAAIAAABACAAAAQAgAAAEAAAAgBAAAAIAAABACAAAAQAgAAAEAAAAgBAAAAIAAABACAAAAQAgAAAEAAAAgBAAAAIwIcWRmIFoZah+J4AAAAASUVORK5CYII=",
    "base64",
  );
  const asked: string[] = [];
  // The owner has uploaded a picture, and changing it changes nothing anyone else sees.
  await page.route("**/api/v1/me", async (route) => {
    const me = await (await route.fetch()).json();
    return route.fulfill({ json: { ...me, user: { ...me.user, avatarUrl: `/api/v1/users/${me.user.id}/avatar` } } });
  });
  await page.route("**/api/v1/users/*/avatar*", (route) => route.fulfill({ status: 200, contentType: "image/png", body: png }));
  await page.route("**/api/v1/me/avatar", (route) => {
    asked.push(route.request().method());
    return route.fulfill({ status: 204 });
  });
  await page.goto("/account");
  await page.getByRole("button", { name: "Picture of owner" }).click();
  const dialog = page.getByRole("dialog", { name: "owner" });
  await expect.poll(() => dialog.locator("img").evaluate((img: HTMLImageElement) => img.complete && img.naturalWidth)).toBe(128);
  await expect(dialog.locator("img")).not.toHaveCSS("image-rendering", "pixelated");
  await dialog.getByLabel("Picture for owner").setInputFiles({ name: "new.png", mimeType: "image/png", buffer: png });
  await expect.poll(() => asked).toEqual(["PUT"]);
  await dialog.getByRole("button", { name: "Remove" }).click();
  await expect.poll(() => asked).toEqual(["PUT", "DELETE"]);
});

test("the console shows the log so far whole, at its end, and keeps it on screen when it reconnects", async ({ page, as }) => {
  await as("owner");
  // The stream, driven from here, one event at a time.
  await page.addInitScript(() => {
    const Real = window.EventSource;
    class Fake {
      listeners: Record<string, ((e: { data: string }) => void)[]> = {};
      addEventListener(type: string, f: (e: { data: string }) => void) {
        this.listeners[type] = [...(this.listeners[type] ?? []), f];
      }
      emit(type: string, data: unknown) {
        for (const f of this.listeners[type] ?? []) f({ data: JSON.stringify(data) });
      }
      close() {}
    }
    window.EventSource = function (url: string) {
      if (!url.includes("/console")) return new Real(url);
      const fake = new Fake();
      (window as unknown as { consoleStream: Fake }).consoleStream = fake;
      return fake;
    } as unknown as typeof EventSource;
  });
  await page.goto("/o/friends/s/survival");
  const emit = (type: string, data: unknown) => page.evaluate(([t, d]) => (window as unknown as { consoleStream: { emit: (t: unknown, d: unknown) => void } }).consoleStream.emit(t, d), [type, data]);
  await page.waitForFunction(() => "consoleStream" in window);
  const batch = (from: number, to: number) => Array.from({ length: to - from }, (_, i) => `[12:00:00 INFO]: line ${from + i}`);
  // Two batches of what is there already, a moment apart: neither shows until both can.
  const heldBack = await page.evaluate(async ([one, two]) => {
    const stream = (window as unknown as { consoleStream: { emit: (t: string, d: unknown) => void } }).consoleStream;
    stream.emit("reset", null);
    stream.emit("lines", one);
    await new Promise((r) => setTimeout(r, 40));
    const shown = document.body.innerText.includes("line 0");
    stream.emit("lines", two);
    return !shown;
  }, [batch(0, 200), batch(200, 400)]);
  expect(heldBack).toBe(true);
  await expect(page.getByText("line 399")).toBeInViewport();
  await expect(page.getByText("line 0", { exact: false }).first()).toBeAttached();
  // New output after that shows as it comes.
  await emit("lines", ["[12:00:01 INFO]: a new line"]);
  await expect(page.getByText("a new line")).toBeVisible();
  // Reconnected, what was there stays until the log so far has come again.
  await emit("reset", null);
  await expect(page.getByText("a new line")).toBeVisible();
  await emit("lines", ["[12:00:02 INFO]: after the reconnect"]);
  await expect(page.getByText("after the reconnect")).toBeVisible();
  await expect(page.getByText("a new line")).toHaveCount(0);
});

test("a command run while the log so far is coming in keeps its answer once the log shows", async ({ page, as }) => {
  await as("owner");
  await page.addInitScript(() => {
    const Real = window.EventSource;
    class Fake {
      listeners: Record<string, ((e: { data: string }) => void)[]> = {};
      addEventListener(type: string, f: (e: { data: string }) => void) {
        this.listeners[type] = [...(this.listeners[type] ?? []), f];
      }
      emit(type: string, data: unknown) {
        for (const f of this.listeners[type] ?? []) f({ data: JSON.stringify(data) });
      }
      close() {}
    }
    window.EventSource = function (url: string) {
      if (!url.includes("/console")) return new Real(url);
      const fake = new Fake();
      (window as unknown as { consoleStream: Fake }).consoleStream = fake;
      return fake;
    } as unknown as typeof EventSource;
  });
  await page.route("**/servers/*/console", (route) =>
    route.request().method() === "POST"
      ? route.fulfill({ status: 409, contentType: "application/json", body: JSON.stringify({ code: "busy", message: "A snapshot is being taken." }) })
      : route.continue(),
  );
  await page.clock.install();
  await page.goto("/o/friends/s/survival");
  await page.waitForFunction(() => "consoleStream" in window);
  const emit = (type: string, data: unknown) => page.evaluate(([t, d]) => (window as unknown as { consoleStream: { emit: (t: unknown, d: unknown) => void } }).consoleStream.emit(t, d), [type, data]);
  // Time stands still, so the log so far is still held while the command runs.
  await page.clock.pauseAt(Date.now() + 60_000);
  await emit("reset", null);
  await emit("lines", ["[12:00:00 INFO]: the log so far"]);
  const input = page.getByLabel("Server command");
  await input.fill("say hi");
  await input.press("Enter");
  await expect(page.getByText("A snapshot is being taken.")).toBeVisible();
  await expect(page.getByText("the log so far")).toHaveCount(0);
  // The hold ends: the log so far shows, and the command and its answer stay, after it.
  await page.clock.runFor(2000);
  await expect(page.getByText("the log so far")).toBeVisible();
  await expect(page.getByText("> say hi")).toBeVisible();
  await expect(page.getByText("A snapshot is being taken.")).toBeVisible();
  const text = await page.getByText("the log so far").locator("xpath=..").innerText();
  expect(text.indexOf("the log so far")).toBeLessThan(text.indexOf("> say hi"));
  expect(text.indexOf("> say hi")).toBeLessThan(text.indexOf("A snapshot is being taken."));
});

test("on a reconnect, what is on screen stays until the log so far starts coming", async ({ page, as }) => {
  await as("owner");
  await page.addInitScript(() => {
    const Real = window.EventSource;
    class Fake {
      listeners: Record<string, ((e: { data: string }) => void)[]> = {};
      addEventListener(type: string, f: (e: { data: string }) => void) {
        this.listeners[type] = [...(this.listeners[type] ?? []), f];
      }
      emit(type: string, data: unknown) {
        for (const f of this.listeners[type] ?? []) f({ data: JSON.stringify(data) });
      }
      close() {}
    }
    window.EventSource = function (url: string) {
      if (!url.includes("/console")) return new Real(url);
      const fake = new Fake();
      (window as unknown as { consoleStream: Fake }).consoleStream = fake;
      return fake;
    } as unknown as typeof EventSource;
  });
  await page.clock.install();
  await page.goto("/o/friends/s/survival");
  await page.waitForFunction(() => "consoleStream" in window);
  const emit = (type: string, data: unknown) => page.evaluate(([t, d]) => (window as unknown as { consoleStream: { emit: (t: unknown, d: unknown) => void } }).consoleStream.emit(t, d), [type, data]);
  await page.clock.pauseAt(Date.now() + 60_000);
  await emit("reset", null);
  await emit("lines", ["[12:00:00 INFO]: before the reconnect"]);
  await page.clock.runFor(2000);
  await expect(page.getByText("before the reconnect")).toBeVisible();
  // Reconnected, and nothing yet: a slow answer leaves the console as it was.
  await emit("reset", null);
  await page.clock.runFor(500);
  await expect(page.getByText("before the reconnect")).toBeVisible();
  // Then the log so far comes, and takes its place.
  await emit("lines", ["[12:00:01 INFO]: after the reconnect"]);
  await page.clock.runFor(2000);
  await expect(page.getByText("after the reconnect")).toBeVisible();
  await expect(page.getByText("before the reconnect")).toHaveCount(0);
});
