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

  const { spec } = await api("owner").get(`/orgs/${slug}/servers/weekend-world`);
  expect(spec).toMatchObject({ loader: "paper", version: "1.21.4", displayName: "Weekend World", autoWhitelist: "viewer" });
  expect(spec.properties).toMatchObject({ difficulty: "hard", "white-list": "true" });
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

test("duplicating a server starts from its settings and sends only what was changed", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/s/survival");
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByLabel("Short name")).toHaveValue("survival-copy");
  const more = dialog.getByRole("button", { name: /More options/ });
  await more.click();
  await expect(dialog.getByLabel("Game version")).toHaveValue("1.21.4");
  await dialog.getByLabel("Game version").selectOption("1.21.5");
  await dialog.getByLabel("Difficulty").selectOption("peaceful");
  await expect(more).toContainText("2 changed");

  // What would be sent is looked at and not sent: nothing here can copy files.
  const sent = page.waitForRequest((r) => r.method() === "POST" && r.url().endsWith("/orgs/friends/servers"));
  await page.route("**/api/v1/orgs/friends/servers", (route) =>
    route.request().method() === "POST" ? route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "test", message: "Not sent by the test." }) }) : route.continue(),
  );
  await dialog.getByRole("button", { name: "Duplicate" }).click();
  const body = (await sent).postDataJSON();
  expect(body).toMatchObject({ name: "survival-copy", fromServer: "survival" });
  expect(body.spec).toMatchObject({ version: "1.21.5", properties: { difficulty: "peaceful" } });
  expect(body.spec).not.toHaveProperty("autoOp");
  await expect(dialog.getByText("Not sent by the test.")).toBeVisible();
});
