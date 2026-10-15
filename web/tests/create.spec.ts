import { expect, test } from "./panel";

test("a new server is made from the form", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);

  // Nothing is chosen for you, and it says so when you try without.
  await page.getByLabel("Display name").fill("Weekend World");
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page.getByText("Choose a server type.")).toBeVisible();

  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await expect(page.getByLabel("Short name")).toHaveValue("weekend-world");
  await page.getByLabel("Game version").selectOption("1.21.4");
  await page.getByLabel("Difficulty").selectOption("hard");
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/weekend-world$`));

  // Left alone, it is its organization's own: members are let in, and it
  // follows the organization's snapshots.
  const { spec } = await api("owner").get(`/orgs/${slug}/servers/weekend-world`);
  expect(spec).toMatchObject({ loader: "paper", version: "1.21.4", displayName: "Weekend World", autoWhitelist: "viewer", expose: { type: "Router" } });
  expect(spec.properties).toMatchObject({ difficulty: "hard", "white-list": "true", "enforce-whitelist": "true" });
  expect(spec.properties).not.toHaveProperty("motd");
  expect(spec).not.toHaveProperty("backups");
  expect(spec.sleep.enabled ?? false).toBe(false);
  // With no picture to wait for, it starts at once.
  expect(spec.state).toBe("Running");
});

test("everything a server can be given later can be given when it is made", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Vanilla/ }).click();
  await page.getByLabel("Display name").fill("Lobby");
  await page.getByLabel("Message of the day").fill("Come on in");
  await page.getByLabel("Who can join").selectOption("admin");
  await page.getByLabel("Operators by role").selectOption("owner");
  await page.getByRole("button", { name: /How it is reached, and sleep/ }).click();
  await page.getByRole("switch", { name: /Sleep when empty/ }).click();
  await page.getByLabel("After minutes with no players").fill("5");
  await page.getByLabel("Scheduled snapshots").selectOption("off");
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();

  // A picture, which goes to the server once it exists and before its game
  // first starts: the server is made stopped, and the panel starts it when
  // the picture is in place. Nothing runs behind this panel, so that part is
  // answered for it, and the server stays as it was made.
  await page.getByLabel("Picture for Lobby").setInputFiles({ name: "icon.png", mimeType: "image/png", buffer: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAEElEQVR4nGM4oREFRAwQCgAl3gUpEzqvXwAAAABJRU5ErkJggg==", "base64") });
  await expect(page.getByRole("button", { name: "Replace" })).toBeVisible();
  let pictured = "";
  await page.route(`**/api/v1/orgs/${slug}/servers/lobby/icon?wait=true&start=true`, async (route) => {
    // What was sent is not looked into: WebKit does not show a test the body of a file.
    pictured = `${route.request().method()} ${route.request().headers()["content-type"] ?? ""}`;
    await route.fulfill({ status: 204 });
  });

  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/lobby$`));
  await expect(page.getByText("Lobby has its picture")).toBeVisible();
  expect(pictured).toBe("PUT image/png");

  const { spec } = await api("owner").get(`/orgs/${slug}/servers/lobby`);
  expect(spec).toMatchObject({ loader: "vanilla", state: "Stopped", autoWhitelist: "admin", autoOp: "owner", sleep: { enabled: true, idleMinutes: 5 }, backups: { enabled: false } });
  expect(spec.properties).toMatchObject({ motd: "Come on in", "white-list": "true" });
});

test("a server open to anyone has no whitelist, and one that is not routed does not sleep", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Vanilla/ }).click();
  await page.getByLabel("Display name").fill("Open");
  await page.getByLabel("Who can join").selectOption("anyone");
  await page.getByRole("button", { name: /How it is reached, and sleep/ }).click();
  await page.getByRole("switch", { name: /Sleep when empty/ }).click();
  await page.getByLabel("How it is reached").selectOption("NodePort");
  await expect(page.getByRole("switch", { name: /Sleep when empty/ })).toBeDisabled();
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/open$`));
  const { spec } = await api("owner").get(`/orgs/${slug}/servers/open`);
  expect(spec.expose.type).toBe("NodePort");
  expect(spec.sleep.enabled ?? false).toBe(false);
  expect(spec).not.toHaveProperty("autoWhitelist");
  expect(spec.properties ?? {}).not.toHaveProperty("white-list");
});

test("the game versions offered are the ones the server type has", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/new");
  const type = page.getByRole("group", { name: "Server type" });
  const versions = page.getByLabel("Game version").locator("option");
  await type.getByRole("button", { name: /^Folia/ }).click();
  // "Latest" names a version too, so each is matched whole.
  await expect(versions.filter({ hasText: /^26\.2$/ })).toHaveCount(1);
  await expect(versions.filter({ hasText: /^26\.3$/ })).toHaveCount(0);
  await type.getByRole("button", { name: /^Paper/ }).click();
  await expect(versions.filter({ hasText: /^26\.3$/ })).toHaveCount(1);
});

test("duplicating a server is the same page, filled in from the original, and sends only what was changed", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/s/survival");
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  await expect(page).toHaveURL(/\/o\/friends\/new\?from=server:survival$/);
  await expect(page.getByRole("heading", { name: "Duplicate survival" })).toBeVisible();

  // The sections a new server has, in the same order.
  const sections = page.locator("form h2");
  await expect(sections).toHaveText(["Starting point", "Name and picture", "Content", "World", "Players", "Resources", "Network", "Backups", "Duplicate"]);

  // What it starts from, and nothing more.
  await expect(page.getByText("Paper 1.21.4", { exact: true })).toBeVisible();
  await expect(page.getByText(/as it is now|is not touched/)).toHaveCount(0);

  // Filled in from the original, with a name of its own.
  await expect(page.getByLabel("Short name")).toHaveValue("survival-copy");
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.4");
  await expect(page.getByLabel("Memory (GB)")).toHaveValue("4");
  await expect(page.getByText("LuckPerms", { exact: true })).toBeVisible();

  await page.getByLabel("Game version").selectOption("1.21.5");
  await page.getByLabel("Difficulty").selectOption("peaceful");
  await page.getByLabel("Who can join").selectOption("admin");

  // What would be sent is looked at and not sent: nothing here can copy files.
  const sent = page.waitForRequest((r) => r.method() === "POST" && r.url().endsWith("/orgs/friends/servers"));
  await page.route("**/api/v1/orgs/friends/servers", (route) =>
    route.request().method() === "POST" ? route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "test", message: "Not sent by the test." }) }) : route.continue(),
  );
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  const body = (await sent).postDataJSON();
  expect(body).toMatchObject({ name: "survival-copy", fromServer: "survival" });
  expect(body.spec).toMatchObject({ version: "1.21.5", autoWhitelist: "admin", properties: { difficulty: "peaceful", "white-list": "true" } });
  // What was left alone is not named, so that it comes from the original.
  for (const untouched of ["autoOp", "sleep", "backups", "mods"]) expect(body.spec).not.toHaveProperty(untouched);
  await expect(page.getByText("Not sent by the test.")).toBeVisible();
});

test("a copy can be given other content before it is made", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/new?from=server:survival");
  await expect(page.getByRole("heading", { name: "Duplicate survival" })).toBeVisible();

  await page.getByRole("button", { name: "Remove LuckPerms" }).click();
  await expect(page.getByText("No plugins or datapacks.")).toBeVisible();
  // Put back as it was, and so not sent.
  await page.getByRole("button", { name: "Put back as survival has it" }).click();
  await expect(page.getByText("LuckPerms", { exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Remove LuckPerms" }).click();
  await page.getByRole("button", { name: "Add plugins or datapacks" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "From URL" }).click();
  // Files are uploaded once the server exists: there is nowhere to put one yet.
  await expect(dialog.getByRole("button", { name: "Upload" })).toHaveCount(0);
  await dialog.getByLabel("Download URL").fill("https://example.org/chunky-1.4.jar");
  await dialog.getByRole("button", { name: "Add", exact: true }).click();
  await page.keyboard.press("Escape");
  await expect(page.getByText("chunky-1.4.jar", { exact: true })).toBeVisible();

  const sent = page.waitForRequest((r) => r.method() === "POST" && r.url().endsWith("/orgs/friends/servers"));
  await page.route("**/api/v1/orgs/friends/servers", (route) =>
    route.request().method() === "POST" ? route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "test", message: "Not sent by the test." }) }) : route.continue(),
  );
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  const body = (await sent).postDataJSON();
  expect(body.spec.mods).toEqual([{ source: "url", kind: "plugin", project: "https://example.org/chunky-1.4.jar", title: "chunky-1.4.jar" }]);
});

test("a server made from a snapshot shows the picture the snapshot holds", async ({ page, as }) => {
  await as("owner");
  const id = "abcd1234";
  const icon = Buffer.from(
    "iVBORw0KGgoAAAANSUhEUgAAAAIAAAACCAIAAAD91JpzAAAAFklEQVR4nGP4z8DAwMDAxMDAwMDAAAANHQEDasKb6QAAAABJRU5ErkJggg==",
    "base64",
  );
  const asked: string[] = [];
  await page.route(`**/api/v1/orgs/friends/snapshots/${id}`, (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        id, time: new Date(Date.now() - 3600_000).toISOString(), server: "survival", trigger: "manual", size: 1 << 20, added: 1024, files: 10,
        spec: { displayName: "Survival", version: "1.21.4", loader: "paper", eula: true, resources: { memory: "4Gi" }, storage: { size: "10Gi" }, mods: [] },
        retention: { scheduled: true, kept: ["daily"] },
      }),
    }),
  );
  await page.route(`**/api/v1/orgs/friends/snapshots/${id}/icon*`, (route) => {
    asked.push(route.request().url());
    return route.fulfill({ contentType: "image/png", body: icon });
  });
  // The server's picture as it is now is not what the snapshot holds.
  await page.route("**/api/v1/orgs/friends/servers/survival/icon*", (route) => route.abort());

  await page.goto(`/o/friends/new?from=snapshot:${id}`);
  await expect(page.getByRole("heading", { name: "New server from a snapshot of survival" })).toBeVisible();
  await expect(page.getByText(/^Snapshot from 1 hour ago · .* · Paper 1\.21\.4$/)).toBeVisible();
  // On the starting point, and next to the name.
  await expect(page.locator(`img[src*="/snapshots/${id}/icon"]`)).toHaveCount(2);
  for (const img of await page.locator(`img[src*="/snapshots/${id}/icon"]`).all()) await expect(img).toBeVisible();
  expect(asked.length).toBeGreaterThan(0);
});

test("a name that snapshots of a deleted server are under is not suggested, and says why", async ({ page, as }) => {
  await as("owner");
  // A survival-copy was deleted, with a last snapshot of it.
  await page.route("**/api/v1/orgs/friends/snapshots", (route) =>
    route.fulfill({
      contentType: "application/json",
      body: JSON.stringify({
        snapshots: [{ id: "f1a2b3c4d5e6", time: new Date().toISOString(), server: "survival-copy", trigger: "final", size: 1024, added: 1024, files: 3 }],
        usage: { bytes: 1024, snapshots: 1 },
      }),
    }),
  );
  await page.goto("/o/friends/new?from=server:survival");
  await expect(page.getByLabel("Short name")).toHaveValue("survival-copy-2");
  await expect(page.getByText("It cannot be changed later.")).toBeVisible();
  await page.getByLabel("Short name").fill("survival-copy");
  await expect(page.getByText("Snapshots of an earlier server named survival-copy go with the name. Choose another.")).toBeVisible();
  await page.getByLabel("Short name").fill("survival");
  await expect(page.getByText("There is already a server named survival.")).toBeVisible();

  await page.goto("/o/friends/new");
  await page.getByLabel("Display name").fill("Survival copy");
  await expect(page.getByLabel("Short name")).toHaveValue("survival-copy");
  await expect(page.getByText(/Snapshots of an earlier server named survival-copy go with the name/)).toBeVisible();
});

test("a copy opens where any new server does, with how its copying goes on every tab", async ({ page, as }) => {
  await as("owner");
  // Nothing here can copy files: the copy is lobby, made a moment ago and
  // held until its files are in, with its copying a third done.
  const held = { phase: "Stopped", message: "Made from a snapshot, and held stopped until all of its files are in place." };
  await page.route("**/api/v1/orgs/friends/servers", async (route) => {
    if (route.request().method() === "POST") {
      const list = await (await route.fetch({ method: "GET" })).json();
      const lobby = list.find((s: { name: string }) => s.name === "lobby");
      return route.fulfill({ status: 201, contentType: "application/json", body: JSON.stringify({ ...lobby, status: { ...lobby.status, ...held } }) });
    }
    const list = await (await route.fetch()).json();
    return route.fulfill({ json: list.map((s: { name: string; status: object }) => (s.name === "lobby" ? { ...s, status: { ...s.status, ...held } } : s)) });
  });
  await page.route("**/api/v1/orgs/friends/servers/lobby/snapshots", (route) =>
    route.fulfill({ json: { available: true, snapshots: [], operations: [{ kind: "create", org: "friends", server: "lobby", stage: "Copying files", percent: 33 }] } }),
  );

  await page.goto("/o/friends/new?from=server:survival");
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  await expect(page).toHaveURL(/\/o\/friends\/s\/lobby$/);
  const progress = page.getByRole("progressbar", { name: "Creation of lobby: Copying files" });
  await expect(progress).toBeVisible();
  // Above every tab, and once only on its Backups tab.
  await page.getByRole("link", { name: "Files", exact: true }).click();
  await expect(page).toHaveURL(/\/s\/lobby\/files$/);
  await expect(progress).toBeVisible();
  await page.locator('a[href="/o/friends/s/lobby/backups"]').click();
  await expect(page).toHaveURL(/\/s\/lobby\/backups$/);
  await expect(progress).toHaveCount(1);
});

test("a copy keeps the picture its files bring, or is given its own, or none", async ({ page, as }) => {
  await as("owner");
  const sent: { picture?: string | null }[] = [];
  await page.route("**/api/v1/orgs/friends/servers", (route) => {
    if (route.request().method() !== "POST") return route.continue();
    sent.push(route.request().postDataJSON());
    return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "test", message: "Not sent by the test." }) });
  });
  await page.goto("/o/friends/new?from=server:survival");
  const duplicate = page.getByRole("button", { name: "Duplicate", exact: true });

  // Left alone, nothing is said of it: it keeps the one its files bring.
  await duplicate.click();
  await expect(page.getByText("Not sent by the test.")).toBeVisible();
  expect(sent.at(-1)).not.toHaveProperty("picture");

  // A large one of its own is made small enough to send.
  const big = await page.evaluate(async () => {
    const canvas = document.createElement("canvas");
    canvas.width = 2000;
    canvas.height = 1000;
    const ctx = canvas.getContext("2d");
    if (ctx) {
      ctx.fillStyle = "#c33";
      ctx.fillRect(0, 0, 2000, 1000);
    }
    return canvas.toDataURL("image/png").split(",")[1];
  });
  await page.getByLabel("Picture for survival copy").setInputFiles({ name: "big.png", mimeType: "image/png", buffer: Buffer.from(big, "base64") });
  await expect(page.getByText("Put in place before it first starts.")).toBeVisible();
  await duplicate.click();
  await expect.poll(() => sent.length).toBe(2);
  const own = sent[1].picture;
  expect(typeof own).toBe("string");
  const png = Buffer.from(own as string, "base64");
  expect(png.subarray(1, 4).toString()).toBe("PNG");
  // 256 pixels on its longer side.
  expect([png.readUInt32BE(16), png.readUInt32BE(20)]).toEqual([256, 128]);

  // Taken away, it is asked to have none.
  await page.getByRole("button", { name: "Remove", exact: true }).click();
  await expect(page.getByText("It will have none.")).toBeVisible();
  await duplicate.click();
  await expect.poll(() => sent.length).toBe(3);
  expect(sent[2].picture).toBeNull();
});

test("a copy is the same form: its world's making shown as it was, and its other properties there to change", async ({ page, as }) => {
  await as("owner");
  const properties = { difficulty: "hard", "level-type": "flat", "view-distance": "12", "spawn-protection": "0" };
  await page.route("**/api/v1/orgs/friends/servers", async (route) => {
    if (route.request().method() === "POST") {
      return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "test", message: "Not sent by the test." }) });
    }
    const list = await (await route.fetch()).json();
    return route.fulfill({ json: list.map((s: { name: string; spec: object }) => (s.name === "survival" ? { ...s, spec: { ...s.spec, properties } } : s)) });
  });
  await page.goto("/o/friends/new?from=server:survival");
  await expect(page.getByLabel("Difficulty")).toHaveValue("hard");
  await page.getByRole("button", { name: /Player limit, PvP and more/ }).click();
  // Made already, so shown as it was made and not to be changed.
  await expect(page.getByLabel("World type")).toHaveValue("flat");
  await expect(page.getByLabel("World type")).toBeDisabled();
  await expect(page.getByLabel("Seed")).toBeDisabled();
  const other = page.getByLabel("Other properties");
  await expect(other).toHaveValue("view-distance=12\nspawn-protection=0");

  await other.fill("view-distance=6\nmax-tick-time=-1");
  const sent = page.waitForRequest((r) => r.method() === "POST" && r.url().endsWith("/orgs/friends/servers"));
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  const body = (await sent).postDataJSON();
  // Changed, added, and taken away; what was left alone is not named.
  expect(body.spec.properties).toEqual({ "view-distance": "6", "max-tick-time": "-1", "spawn-protection": null });
});

test("picking the latest release for a copy already on it changes nothing", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/new?from=server:survival");
  const latestOption = page.getByLabel("Game version").locator('option[value="LATEST"]');
  await expect(latestOption).toHaveText(/\(.+\)/);
  const latest = /\((.+)\)/.exec((await latestOption.textContent()) ?? "")?.[1] ?? "";
  expect(latest).not.toBe("");
  // Survival, as if it were on the latest release already.
  await page.route("**/api/v1/orgs/friends/servers", async (route) => {
    if (route.request().method() === "POST") {
      return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "test", message: "Not sent by the test." }) });
    }
    const list = await (await route.fetch()).json();
    return route.fulfill({ json: list.map((s: { name: string; spec: object }) => (s.name === "survival" ? { ...s, spec: { ...s.spec, version: latest } } : s)) });
  });
  await page.reload();
  await page.getByLabel("Game version").selectOption("LATEST");
  // The starting point says what the original runs, not what was picked.
  await expect(page.getByText(`Paper ${latest}`, { exact: true })).toBeVisible();
  await expect(page.getByText("A world opened in a newer version does not open in an older one again.")).toHaveCount(0);
  const sent = page.waitForRequest((r) => r.method() === "POST" && r.url().endsWith("/orgs/friends/servers"));
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  expect((await sent).postDataJSON().spec).not.toHaveProperty("version");

  // Another version is a change, and says what that means.
  await page.getByLabel("Game version").selectOption("1.21.4");
  await expect(page.getByText("A world opened in a newer version does not open in an older one again.")).toBeVisible();
});

test("the new-server page has the same sections in the same order", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/new");
  await expect(page.locator("form h2")).toHaveText(["Software", "Name and picture", "Content", "World", "Players", "Resources", "Network", "Backups", "Minecraft EULA"]);
});

test("a server that is not there cannot be copied", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/new?from=server:nobody");
  await expect(page.getByText("There is no server named nobody to copy.")).toBeVisible();
});

test("with the list of servers unavailable, nothing is guessed about the original", async ({ page, as }) => {
  await as("owner");
  await page.route("**/api/v1/orgs/friends/servers", (route) => route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "unavailable", message: "The cluster did not answer." }) }));
  await page.goto("/o/friends/new?from=server:survival");
  await expect(page.getByText("The cluster did not answer.")).toBeVisible({ timeout: 20_000 });
  await expect(page.getByText(/There is no server named/)).toHaveCount(0);
  await expect(page.getByLabel("Short name")).toHaveCount(0);
});

test("a restore's progress shows before the server is held, once, and on every tab", async ({ page, as }) => {
  await as("owner");
  // A restore begins by snapshotting what is there; the server is not held yet.
  await page.route("**/api/v1/orgs/friends/servers/creative/snapshots", (route) =>
    route.fulfill({ json: { available: true, snapshots: [], operations: [{ kind: "restore", org: "friends", server: "creative", stage: "Saving what is there now", percent: 10 }] } }),
  );
  await page.goto("/o/friends/s/creative/backups");
  const progress = page.getByRole("progressbar", { name: /^Restore of creative/ });
  await expect(progress).toHaveCount(1);
  await page.getByRole("link", { name: "Files", exact: true }).click();
  await expect(page).toHaveURL(/\/s\/creative\/files$/);
  await expect(progress).toBeVisible();
});

test("a copy's picture can be taken away even when it could not be shown", async ({ page, as }) => {
  await as("owner");
  const sent: { picture?: string | null }[] = [];
  // Survival's picture cannot be read just now.
  await page.route("**/api/v1/orgs/friends/servers/survival/icon*", (route) => route.fulfill({ status: 503, json: { code: "unavailable", message: "Not now." } }));
  await page.route("**/api/v1/orgs/friends/servers", (route) => {
    if (route.request().method() !== "POST") return route.continue();
    sent.push(route.request().postDataJSON());
    return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "test", message: "Not sent by the test." }) });
  });
  await page.goto("/o/friends/new?from=server:survival");
  await expect(page.getByText("None could be shown for it.")).toBeVisible();
  await page.getByRole("button", { name: "Remove", exact: true }).click();
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  await expect.poll(() => sent.length).toBe(1);
  expect(sent[0].picture).toBeNull();
});

const twoNodes = {
  automatic: "fast",
  nodes: [
    { name: "fast", ready: true, automatic: true, score: 2, memory: 16 * 1024 ** 3, free: 12 * 1024 ** 3, servers: [{ org: "friends", name: "survival", memory: "4Gi", phase: "Running" }] },
    { name: "pi", ready: true, automatic: false, score: 0.4, memory: 3 * 1024 ** 3, free: 2.5 * 1024 ** 3, servers: [] },
  ],
};

test("a node can be chosen for a new server, any that has room, slow ones included", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await page.route("**/api/v1/nodes?*", (route) => route.fulfill({ json: twoNodes }));
  const sent: { node?: string }[] = [];
  await page.route(`**/api/v1/orgs/${slug}/servers`, (route) => {
    if (route.request().method() !== "POST") return route.continue();
    sent.push(route.request().postDataJSON());
    return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "test", message: "Not sent by the test." }) });
  });
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Vanilla/ }).click();
  await page.getByLabel("Display name").fill("Small");
  const choice = page.getByLabel("Node", { exact: true });
  await expect(choice.locator("option").first()).toHaveText("Automatic (now fast)");
  // 4 GB does not fit on the small one; 2 GB does.
  await expect(choice.locator('option[value="pi"]')).toBeDisabled();
  await page.getByLabel("Memory (GB)").fill("2");
  await expect(choice.locator('option[value="pi"]')).toBeEnabled();
  await choice.selectOption("pi");
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect.poll(() => sent.length).toBe(1);
  expect(sent[0].node).toBe("pi");
});

test("with one node, the node is still shown", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await page.route("**/api/v1/nodes?*", (route) => route.fulfill({ json: { automatic: "fast", nodes: [twoNodes.nodes[0]] } }));
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Vanilla/ }).click();
  const choice = page.getByLabel("Node", { exact: true });
  await expect(choice.locator("option")).toHaveText(["Automatic (now fast)", /^fast · speed 2\.00/]);
  await expect(page.getByText("The only node there is.")).toBeVisible();
  // Changing the memory asks again, and the field stays.
  await page.getByLabel("Memory (GB)").fill("2");
  await expect(choice).toBeVisible();
});

test("a copy of a server kept to some nodes is kept to them, unless Automatic is chosen", async ({ page, as }) => {
  await as("owner");
  await page.route("**/api/v1/nodes?*", (route) => route.fulfill({ json: twoNodes }));
  await page.route("**/api/v1/orgs/friends/servers", async (route) => {
    if (route.request().method() === "POST") {
      sent.push(route.request().postDataJSON());
      return route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "test", message: "Not sent by the test." }) });
    }
    const list = await (await route.fetch()).json();
    return route.fulfill({ json: list.map((s: { name: string; spec: object }) => (s.name === "survival" ? { ...s, spec: { ...s.spec, nodeSelector: { disk: "ssd" } } } : s)) });
  });
  const sent: { node?: string; spec: { nodeSelector?: object } }[] = [];
  await page.goto("/o/friends/new?from=server:survival");
  const choice = page.getByLabel("Node", { exact: true });
  await expect(choice.locator("option").first()).toHaveText("As the original: nodes with disk=ssd");
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  await expect.poll(() => sent.length).toBe(1);
  expect(sent[0]).not.toHaveProperty("node");
  expect(sent[0].spec).not.toHaveProperty("nodeSelector");
  await choice.selectOption({ label: "Automatic (now fast)" });
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  await expect.poll(() => sent.length).toBe(2);
  expect(sent[1]).not.toHaveProperty("node");
  expect(sent[1].spec.nodeSelector).toEqual({});

  // With one node, the original's nodes can still be let go of.
  await page.unroute("**/api/v1/nodes?*");
  await page.route("**/api/v1/nodes?*", (route) => route.fulfill({ json: { automatic: "fast", nodes: twoNodes.nodes.slice(0, 1) } }));
  await page.reload();
  await expect(page.getByLabel("Node", { exact: true }).locator("option")).toHaveText(["As the original: nodes with disk=ssd", "Automatic (now fast)", /^fast/]);
});

test("the panel's administrator sees the nodes and what runs on each; nobody else is offered them", async ({ page, as }) => {
  await page.route("**/api/v1/nodes", (route) => route.fulfill({ json: twoNodes }));
  await as("owner");
  await page.goto("/o/friends");
  await page.getByRole("button", { name: /owner/ }).click();
  await page.getByRole("menuitem", { name: "Nodes" }).click();
  await expect(page).toHaveURL(/\/nodes$/);
  await expect(page.getByRole("heading", { name: "Nodes" })).toBeVisible();
  await expect(page.getByRole("link", { name: /friends\/survival/ })).toBeVisible();
  await expect(page.getByText("only when chosen")).toBeVisible();

  await as("admin");
  await page.goto("/o/friends");
  await page.getByRole("button", { name: /admin/ }).click();
  await expect(page.getByRole("menuitem", { name: "Account" })).toBeVisible();
  await expect(page.getByRole("menuitem", { name: "Nodes" })).toHaveCount(0);
});
