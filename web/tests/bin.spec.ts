import { expect, test } from "./panel";

// Recently deleted, in an organization of each test's own: deleting a
// snapshot changes what everyone else would see. Its snapshots are made by
// the test panel (cmd/avsys-uitest/snapshots.go), whose restic removes them
// as a real one would.
const seed = async (request: import("@playwright/test").APIRequestContext, org: string, server: string, count: number) => {
  const res = await request.post(`/uitest/snapshots?org=${org}&server=${server}&count=${count}`);
  expect(res.ok()).toBe(true);
  return ((await res.json()) as { ids: string[] }).ids;
};

test("a deleted snapshot waits in Recently deleted, and is put back", async ({ page, as, org, request, api }) => {
  const slug = await org(["keep"]);
  await seed(request, slug, "keep", 2);
  await as("owner");
  await page.goto(`/o/${slug}/backups`);
  await expect(page.getByText("2 snapshots of 1 server")).toBeVisible();
  await expect(page.getByRole("region", { name: "Recently deleted" })).toHaveCount(0);

  await page.getByRole("button", { expanded: false }).filter({ hasText: "keep" }).click();
  await page.getByRole("button", { name: /^Delete the snapshot of keep/ }).first().click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("It stays in Recently deleted for 7 days.");
  await dialog.getByRole("button", { name: "Delete snapshot" }).click();
  await expect(page.getByText("1 snapshot of 1 server")).toBeVisible();

  // Kept, with who deleted it and how long it has left, using storage.
  const bin = page.getByRole("region", { name: "Recently deleted" });
  await expect(bin).toContainText("Recently deleted · 1");
  await expect(bin).toContainText("Kept 7 days, then removed for good. They use storage until then.");
  await bin.getByRole("button", { name: /Recently deleted/, expanded: false }).click();
  const row = bin.getByRole("listitem");
  await expect(row).toHaveCount(1);
  await expect(row).toContainText("by owner");
  await expect(row).toContainText("7 days left");
  // Nowhere else: not on the server's own tab, and not to be made a server of.
  await page.goto(`/o/${slug}/s/keep/backups`);
  await expect(page.getByRole("button", { name: /^About the snapshot of keep/ })).toHaveCount(1);
  expect((await api("owner").get(`/orgs/${slug}/snapshots`)).snapshots).toHaveLength(1);

  await page.goto(`/o/${slug}/backups`);
  await bin.getByRole("button", { name: /Recently deleted/, expanded: false }).click();
  await bin.getByRole("button", { name: /^Put back the snapshot of keep/ }).click();
  await expect(page.getByText("Put back the snapshot of keep")).toBeVisible();
  await expect(page.getByText("2 snapshots of 1 server")).toBeVisible();
  await expect(bin).toHaveCount(0);
});

test("the panel's administrator empties Recently deleted; an organization's admin only puts back", async ({ page, as, org, request, api }) => {
  const slug = await org(["keep"]);
  await seed(request, slug, "keep", 3);
  const invite = await api("owner").post(`/orgs/${slug}/invites`, { role: "admin" });
  await api("admin").post(`/invites/${invite.token}/accept`);

  // Two deleted by the organization's admin, who can put them back and no more.
  await as("admin");
  await page.goto(`/o/${slug}/backups`);
  await page.getByRole("button", { name: "By time" }).click();
  await page.getByLabel(/^Select the snapshot of keep/).nth(0).click();
  await page.getByLabel(/^Select the snapshot of keep/).nth(1).click();
  await page.getByRole("region", { name: "Selection" }).getByRole("button", { name: "Delete", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("They stay in Recently deleted for 7 days.");
  await dialog.getByRole("button", { name: "Delete 2 snapshots" }).click();
  await expect(page.getByText("1 snapshot of 1 server")).toBeVisible();
  const bin = page.getByRole("region", { name: "Recently deleted" });
  await bin.getByRole("button", { name: /Recently deleted/, expanded: false }).click();
  await expect(bin.getByRole("button", { name: /^Put back the snapshot of keep/ })).toHaveCount(2);
  await expect(bin.getByRole("button", { name: "Empty" })).toHaveCount(0);
  await expect(bin.getByRole("button", { name: /for good$/ })).toHaveCount(0);

  // The panel's administrator removes one, then the rest, each asked first.
  await as("owner");
  await page.reload();
  await bin.getByRole("button", { name: /Recently deleted/, expanded: false }).click();
  await bin.getByRole("button", { name: /for good$/ }).first().click();
  await expect(dialog.getByRole("heading", { name: "Remove this snapshot for good?" })).toBeVisible();
  await expect(dialog).toContainText("This can't be undone.");
  await dialog.getByRole("button", { name: "Remove for good" }).click();
  await expect(bin).toContainText("Recently deleted · 1");
  await bin.getByRole("button", { name: "Empty" }).click();
  await expect(dialog.getByRole("heading", { name: "Empty Recently deleted?" })).toBeVisible();
  await dialog.getByRole("button", { name: "Empty", exact: true }).click();
  await expect(bin).toHaveCount(0);
  // Gone for good: not put back into the list, not in the repository.
  await expect(page.getByText("1 snapshot of 1 server")).toBeVisible();
  expect((await api("owner").get(`/orgs/${slug}/bin`)).snapshots).toHaveLength(0);
});
