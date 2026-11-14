import type { Page } from "@playwright/test";
import { expect, test, withOffsite } from "./panel";

// Which snapshots are in the offsite copy, on the panel that has one: of the
// six canned snapshots, the two taken a few hours ago are not there yet.

const filterBy = async (page: Page, ...choices: string[]) => {
  await page.getByRole("button", { name: "Filter", exact: true }).click();
  for (const choice of choices) await page.getByRole("menuitemcheckbox", { name: choice, exact: true }).click();
  await page.keyboard.press("Escape");
};

test.describe("with an offsite copy", () => {
  test.use({ baseURL: withOffsite });

  test.beforeEach(async ({ page, as }) => {
    await as("owner");
    await page.addInitScript(() => window.localStorage.removeItem("avsys.backups.view"));
  });

  test("by time, snapshots are narrowed to those offsite or only here, kept in the address", async ({ page }) => {
    await page.goto("/o/friends/backups");
    await page.getByRole("button", { name: "By time" }).click();
    const snapshots = page.getByRole("button", { name: /^About the snapshot of / });
    await expect(snapshots).toHaveCount(6);
    const rows = page.getByRole("listitem").filter({ has: snapshots });
    // Those offsite are marked.
    await expect(rows.filter({ has: page.getByRole("img", { name: "Offsite" }) })).toHaveCount(4);

    await filterBy(page, "Only here");
    await expect(snapshots).toHaveCount(2);
    await expect(page).toHaveURL(/offsite=no/);
    await expect(rows.getByRole("img", { name: "Offsite" })).toHaveCount(0);
    // Loaded afresh, it is as it was.
    await page.reload();
    await expect(snapshots).toHaveCount(2);
    await expect(page.getByRole("button", { name: "Stop filtering by Only here" })).toBeVisible();
    // Both is everything; then offsite alone.
    await filterBy(page, "Offsite");
    await expect(snapshots).toHaveCount(6);
    await page.getByRole("button", { name: "Stop filtering by Only here" }).click();
    await expect(snapshots).toHaveCount(4);
    await expect(rows.getByRole("img", { name: "Offsite" })).toHaveCount(4);
    await expect(rows.last()).toContainText("oldworld");
    await expect(page).toHaveURL(/\?offsite=yes$/);
    // Filters of different kinds narrow each other down.
    await filterBy(page, "creative");
    await expect(page.getByText("No matching snapshots.")).toBeVisible();

    // An address that asks for it opens the list by time.
    await page.goto("/o/friends/backups?offsite=no");
    await expect(page.getByRole("button", { name: "By time" })).toHaveAttribute("aria-pressed", "true");
    await expect(snapshots).toHaveCount(2);
  });

  test("a snapshot's details say whether it is in the offsite copy", async ({ page }) => {
    await page.goto("/o/friends/backups?offsite=no&server=creative");
    await page.getByRole("button", { name: /^About the snapshot of creative/ }).click();
    await expect(page.getByRole("dialog")).toContainText("Not in the offsite copy yet.");
    await page.keyboard.press("Escape");
    await page.goto("/o/friends/backups?server=oldworld");
    await page.getByRole("button", { name: /^About the snapshot of oldworld/ }).click();
    await expect(page.getByRole("dialog")).toContainText("In the offsite copy.");
  });
});

test("without an offsite copy, snapshots are neither marked nor narrowed by it", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/backups?offsite=no");
  await expect(page.getByRole("button", { name: "By time" })).toHaveAttribute("aria-pressed", "true");
  const snapshots = page.getByRole("button", { name: /^About the snapshot of / });
  await expect(snapshots).toHaveCount(6);
  await expect(page.getByRole("img", { name: "Offsite" })).toHaveCount(0);
  await page.getByRole("button", { name: "Filter", exact: true }).click();
  await expect(page.getByRole("menuitemcheckbox", { name: "creative", exact: true })).toBeVisible();
  await expect(page.getByRole("menuitemcheckbox", { name: "Only here", exact: true })).toHaveCount(0);
  await expect(page.getByRole("menuitemcheckbox", { name: "Offsite", exact: true })).toHaveCount(0);
  await expect(page.getByRole("menu")).not.toContainText("Where");
});
