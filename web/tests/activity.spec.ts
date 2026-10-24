import { expect, test } from "./panel";

// What was last done to a server, and by whom, under its name: kept current
// as it happens, and all of it when pressed.

test("what was done to a server is said under its name, as it happens", async ({ page, as, api, org }) => {
  const slug = await org(["alpha", "beta"]);
  await as("owner");
  await page.goto(`/o/${slug}/s/alpha`);
  const line = page.getByRole("button", { name: /made it/ });
  await expect(line).toBeVisible();

  // Done elsewhere, it shows here without reloading.
  await api("owner").post(`/orgs/${slug}/servers/alpha/start`);
  await expect(page.getByRole("button", { name: /started it · / })).toBeVisible();
  // Not what was done to another server.
  await api("owner").post(`/orgs/${slug}/servers/beta/start`);
  await api("owner").post(`/orgs/${slug}/servers/alpha/stop`);
  await page.getByRole("button", { name: /stopped it · / }).click();
  const list = page.getByRole("dialog").getByRole("list", { name: "Activity" }).getByRole("listitem");
  await expect(list).toHaveCount(3);
  await expect(list.nth(0)).toContainText("stopped it");
  await expect(list.nth(1)).toContainText("started it");
  await expect(list.nth(2)).toContainText("made it");
});
