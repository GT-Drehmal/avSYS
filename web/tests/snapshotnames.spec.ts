import { expect, test, withMetrics } from "./panel";

// Snapshots are named, and marked with a colour, in the panel. Against the
// panel with a metrics API, whose snapshots no other test looks at: each
// browser names one of its own, and takes the name away again.

test.use({ baseURL: withMetrics });

test("a snapshot is named and coloured, shown and found by its name, and kept by the schedule", async ({ page, as, api }, testInfo) => {
  await as("owner");
  // Survival's scheduled snapshots, oldest last: one for each browser.
  const which = testInfo.project.name === "webkit" ? 3 : 2;
  const name = `Before 1.22 ${testInfo.project.name}`;
  await page.goto("/o/friends/backups?server=survival");
  const snapshots = page.getByRole("button", { name: /^About the snapshot of survival/ });
  await expect(snapshots).toHaveCount(4);
  const row = page.getByRole("listitem").filter({ has: snapshots }).nth(which);
  const id = (await api("owner").get("/orgs/friends/servers/survival/snapshots")).snapshots[which].id as string;

  await row.getByRole("button", { name: /^Name the snapshot of survival/ }).click();
  const dialog = page.getByRole("dialog", { name: "Name this snapshot" });
  await expect(dialog.getByText("A named snapshot is kept until you delete it.")).toBeVisible();
  await dialog.getByLabel("Name").fill(name);
  await dialog.getByRole("radio", { name: "Teal" }).click();
  await expect(dialog.getByRole("radio", { name: "Teal" })).toHaveAttribute("aria-checked", "true");
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog).toHaveCount(0);

  // Its name first, with its colour, then when it was taken.
  const named = page.getByRole("listitem").filter({ hasText: name });
  await expect(named).toHaveCount(1);
  await expect(named.getByRole("img", { name: "Teal" })).toBeVisible();
  expect((await api("owner").get(`/orgs/friends/snapshots/${id}`)).name).toBe(name);

  // Found by its name.
  await page.goto("/o/friends/backups?sort=newest");
  await page.getByRole("searchbox", { name: "Search backups" }).fill(name);
  await expect(page.getByRole("button", { name: /^About the snapshot of / })).toHaveCount(1);
  await expect(named).toBeVisible();

  // The schedule passes over it.
  await named.getByRole("button", { name: /^About the snapshot of survival/ }).click();
  const details = page.getByRole("dialog", { name: "Snapshot of survival" });
  await expect(details).toContainText(name);
  await expect(details).toContainText("Until you delete it, as it is named.");
  await page.keyboard.press("Escape");

  // A restore says which one it goes back to.
  await named.getByRole("button", { name: /^Restore survival/ }).click();
  await expect(page.getByRole("dialog", { name: "Restore survival?" })).toContainText(`Back to “${name}”`);
  await page.getByRole("dialog", { name: "Restore survival?" }).getByRole("button", { name: "Cancel" }).click();

  // On the server's own Backups tab too.
  await page.goto("/o/friends/s/survival/backups");
  await expect(page.getByRole("listitem").filter({ hasText: name })).toHaveCount(1);

  // And where a server is made from a snapshot: its server found by it, and
  // it by its name.
  await page.goto("/o/friends/new");
  await page.getByRole("group", { name: "Start from" }).getByRole("button", { name: "Snapshot", exact: true }).click();
  await page.getByLabel("Search servers").fill(name);
  const servers = page.getByRole("list", { name: "Servers with snapshots" }).getByRole("button");
  await expect(servers).toHaveCount(1);
  await servers.first().click();
  await page.getByLabel("Search snapshots").fill(name);
  const picks = page.getByRole("list", { name: "Snapshots" }).getByRole("button");
  await expect(picks).toHaveCount(1);
  await expect(picks.first()).toContainText(name);

  // Its name taken away, it is as it was.
  await page.goto("/o/friends/backups?server=survival");
  await page.getByRole("listitem").filter({ hasText: name }).getByRole("button", { name: /^Name the snapshot of survival/ }).click();
  await expect(dialog.getByLabel("Name")).toHaveValue(name);
  await dialog.getByLabel("Name").fill("");
  await dialog.getByRole("radio", { name: "None" }).click();
  await dialog.getByRole("button", { name: "Save" }).click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByRole("listitem").filter({ hasText: name })).toHaveCount(0);
  const after = await api("owner").get(`/orgs/friends/snapshots/${id}`);
  expect(after.name).toBeUndefined();
  expect(after.color).toBeUndefined();
});

test("only admins name snapshots", async ({ page, as }) => {
  await as("operator");
  await page.goto("/o/friends/backups?server=survival");
  await expect(page.getByRole("button", { name: /^About the snapshot of survival/ })).toHaveCount(4);
  await expect(page.getByRole("button", { name: /^Name the snapshot of / })).toHaveCount(0);
});
