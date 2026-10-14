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
  await page.getByLabel("Picture for Lobby").setInputFiles({ name: "icon.png", mimeType: "image/png", buffer: Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNkYPhfDwAChwGA60e6kgAAAABJRU5ErkJggg==", "base64") });
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
