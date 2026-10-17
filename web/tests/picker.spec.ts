import { content, expect, test } from "./panel";

// The picker for mods, against canned answers from Modrinth.
test.beforeEach(async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/s/creative/content");
  await page.getByRole("button", { name: /^Add/ }).first().click();
  await expect(page.getByRole("dialog")).toBeVisible();
});

test("it opens on what fits the server, and loads more when scrolled", async ({ page }) => {
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("30 results for Fabric 1.21.4")).toBeVisible();
  await expect(dialog.getByLabel("Sort by")).toHaveValue("downloads");
  const about = dialog.getByRole("button", { name: /^About / });
  await expect(about).toHaveCount(24);
  await about.last().scrollIntoViewIfNeeded();
  await expect(about).toHaveCount(30);
});

test("a category narrows it, and clearing brings the rest back", async ({ page }) => {
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: /Filters/ }).click();
  await dialog.getByRole("button", { name: "Adventure", exact: true }).click();
  await expect(dialog.getByText("3 results for Fabric 1.21.4")).toBeVisible();
  await expect(dialog.getByRole("button", { name: "About Waystones" })).toBeVisible();
  await dialog.getByRole("button", { name: "Clear filters" }).click();
  await expect(dialog.getByText("30 results for Fabric 1.21.4")).toBeVisible();
});

test("a closer look says what it needs, and going back keeps the search", async ({ page }) => {
  const dialog = page.getByRole("dialog");
  await dialog.getByPlaceholder(/Search mods/).fill("lith");
  await dialog.getByRole("button", { name: "About Lithium" }).click();
  await expect(dialog.getByRole("heading", { name: "Versions for Fabric 1.21.4" })).toBeVisible();
  await expect(dialog.getByText("2.0.0")).toBeVisible();
  await expect(dialog.getByText("Fabric API")).toBeVisible();
  await expect(dialog.getByText(/one other project/)).toBeVisible();
  await expect(dialog.getByRole("link", { name: /Open on Modrinth/ })).toHaveAttribute("href", "https://modrinth.com/mod/lithium");
  await dialog.getByRole("button", { name: "Results" }).click();
  await expect(dialog.getByPlaceholder(/Search mods/)).toHaveValue("lith");
});

// Each of these once left an empty window in one engine and not the other.
for (const [how, close] of [
  ["clicking outside it", (page) => page.mouse.click(20, 400)],
  ["pressing Escape", (page) => page.keyboard.press("Escape")],
] as const) {
  test(`closing it by ${how} leaves the page as it was`, async ({ page }) => {
    await expect(page.getByRole("dialog").getByText(/results for/)).toBeVisible();
    await close(page);
    await expect(page.getByRole("dialog")).toHaveCount(0);
    await expect(content(page).getByText("Creative").first()).toBeVisible();
    await expect(page.getByRole("button", { name: /^Add/ }).first()).toBeVisible();
  });
}

test("picking one adds it to the server", async ({ page, org, api, as }) => {
  // On a server of its own, so that nothing else sees the change.
  const slug = await org(["modded"]);
  await as("owner");
  await page.goto(`/o/${slug}/s/modded/content`);
  await page.getByRole("button", { name: /^Add/ }).first().click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: /Chunky/ }).first().click();
  await expect.poll(async () => (await api("owner").get(`/orgs/${slug}/servers/modded`)).spec.mods?.map((m: any) => m.project)).toEqual(["chunky"]);
  await page.keyboard.press("Escape");
  await expect(content(page).getByText("Chunky", { exact: true })).toBeVisible();
});
