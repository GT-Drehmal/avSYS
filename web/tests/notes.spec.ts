import { expect, test } from "./panel";

// Notes on servers, templates and worlds, which the panel keeps for those
// who look after them, and the names and colours of snapshots.

test("a server's notes are written on the new-server form, shown on its page and the list, and changed in Settings", async ({ page, as, org, api }) => {
  const slug = await org(["other"]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  await page.getByLabel("Display name").fill("Event");
  // Said to be the panel's own, beside the message players see.
  await expect(page.getByText("Only seen in the panel.")).toBeVisible();
  await page.getByLabel("Notes").fill("For the Halloween event.\nDelete in November.");
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/event$`));

  // Kept by the panel, and nowhere the game reads.
  const made = await api("owner").get(`/orgs/${slug}/servers/event`);
  expect(made.notes).toBe("For the Halloween event.\nDelete in November.");
  expect(JSON.stringify(made.spec)).not.toContain("Halloween");
  // Under its name, line for line.
  await expect(page.getByText("For the Halloween event.")).toBeVisible();
  await expect(page.getByText("Delete in November.")).toBeVisible();

  // On the list, and found by them.
  await page.goto(`/o/${slug}`);
  const rows = page.getByRole("listitem").filter({ has: page.getByRole("link") });
  await expect(rows.filter({ hasText: "Event" })).toContainText("For the Halloween event.");
  await page.getByRole("searchbox", { name: "Search servers" }).fill("halloween");
  await expect(rows).toHaveCount(1);
  await expect(rows.first()).toContainText("Event");

  // Changed in Settings, on their own.
  await page.goto(`/o/${slug}/s/event/settings`);
  await expect(page.getByLabel("Notes")).toHaveValue("For the Halloween event.\nDelete in November.");
  const sent = page.waitForRequest((r) => r.method() === "PATCH" && r.url().endsWith(`/orgs/${slug}/servers/event`));
  await page.getByLabel("Notes").fill("Kept for screenshots.");
  await page.getByRole("button", { name: "Save changes" }).click();
  expect((await sent).postDataJSON()).toEqual({ notes: "Kept for screenshots." });
  await expect(page.getByText("Kept for screenshots.").first()).toBeVisible();
  expect((await api("owner").get(`/orgs/${slug}/servers/event`)).notes).toBe("Kept for screenshots.");

  // With other settings, together.
  const both = page.waitForRequest((r) => r.method() === "PATCH" && r.url().endsWith(`/orgs/${slug}/servers/event`));
  await page.getByLabel("Notes").fill("");
  await page.getByLabel("Display name").fill("Event over");
  await page.getByRole("button", { name: "Save changes" }).click();
  const body = (await both).postDataJSON();
  expect(body.notes).toBe("");
  expect(body.spec).toMatchObject({ displayName: "Event over" });
  await expect.poll(async () => (await api("owner").get(`/orgs/${slug}/servers/event`)).notes).toBe("");
});

test("a copy starts with its original's notes, which can be changed before it is made", async ({ page, as, api }) => {
  // Its own organization's server would do as well; survival has none, and
  // a copy of it is never sent here.
  const slug = "friends";
  await as("owner");
  const notes = "The main world.";
  await page.route(`**/api/v1/orgs/${slug}/servers`, async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const list = await (await route.fetch()).json();
    await route.fulfill({ json: list.map((s: { name: string }) => (s.name === "survival" ? { ...s, notes } : s)) });
  });
  await page.goto(`/o/${slug}/new?from=server:survival`);
  await expect(page.getByLabel("Notes")).toHaveValue(notes);
  const sent = page.waitForRequest((r) => r.method() === "POST" && r.url().endsWith(`/orgs/${slug}/servers`));
  await page.route(`**/api/v1/orgs/${slug}/servers`, (route) =>
    route.request().method() === "POST" ? route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "test", message: "Not sent by the test." }) }) : route.fallback(),
  );
  await page.getByLabel("Notes").fill("A copy to test on.");
  await page.getByRole("button", { name: "Duplicate" }).last().click();
  expect((await sent).postDataJSON()).toMatchObject({ fromServer: "survival", notes: "A copy to test on." });
  // Nothing was made.
  expect((await api("owner").get(`/orgs/${slug}/servers`)).some((s: { name: string }) => s.name === "survival-copy")).toBe(false);
});

test("a template's notes are written and changed on its form, and a server made from it starts with them", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/templates/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  await page.getByLabel("Name", { exact: true }).fill("Minigame");
  await page.getByLabel("Notes").fill("Small, resets nightly.");
  await page.getByRole("button", { name: "Create template" }).click();
  const row = page.getByRole("listitem", { name: "Minigame" });
  await expect(row).toContainText("Small, resets nightly.");

  // The same field when it is changed.
  await row.getByRole("button", { name: "Actions for Minigame" }).click();
  await page.getByRole("menuitem", { name: "Edit" }).click();
  await expect(page.getByLabel("Notes")).toHaveValue("Small, resets nightly.");
  await page.getByLabel("Notes").fill("Small, resets weekly.");
  await page.getByRole("button", { name: "Save" }).click();
  await expect(page.getByRole("listitem", { name: "Minigame" })).toContainText("Small, resets weekly.");

  // A server made from it has them to start with.
  await page.getByRole("listitem", { name: "Minigame" }).getByRole("button", { name: "New server" }).click();
  await expect(page.getByLabel("Notes")).toHaveValue("Small, resets weekly.");
});

test("a world is given notes as it is added, as the other forms have them", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/worlds`);
  await page.getByRole("button", { name: "Add world" }).first().click();
  const dialog = page.getByRole("dialog", { name: "Add a world" });
  await expect(dialog.getByLabel("Notes")).toBeVisible();
  await expect(dialog.getByText("Only seen in the panel.")).toBeVisible();
});
