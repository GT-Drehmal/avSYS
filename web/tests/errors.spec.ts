import { expect, test } from "./panel";

test("an error on one page leaves the rest of the panel standing", async ({ page, as, pageErrors }) => {
  await as("owner");
  // A list of servers that is not what the page expects.
  await page.route("**/api/v1/orgs/friends/servers", (route) => route.fulfill({ status: 200, contentType: "application/json", body: JSON.stringify([{ name: "broken", org: "friends" }]) }));
  await page.goto("/o/friends");
  await expect(page.getByRole("alert")).toContainText("Something went wrong on this page");
  await expect(page.getByRole("link", { name: "Backups" })).toBeVisible();
  // Going somewhere else clears it.
  await page.getByRole("link", { name: "Backups" }).click();
  await expect(page.getByRole("heading", { name: "Backups" })).toBeVisible();
  await expect(page.getByRole("alert").filter({ hasText: "Something went wrong" })).toHaveCount(0);
  // The error was caught and shown, which is what was being tested.
  pageErrors.length = 0;
});

test("a part of the app that is gone reloads the page once, then says so, and never leaves it empty", async ({ page, as, pageErrors }) => {
  await as("owner");
  await page.goto("/o/friends/s/survival");
  await expect(page.getByRole("link", { name: "Terminal" })).toBeVisible();
  // As in a tab left open while the panel was updated: the file it asks for
  // is not there. In a real update the reload finds the new file under a new
  // name; here it stays gone, which is the case that could go round for ever.
  await page.route("**/assets/Terminal-*.js", (route) => route.fulfill({ status: 404, headers: { "Cache-Control": "no-store" }, body: "not found" }));
  let loads = 0;
  page.on("load", () => loads++);
  await page.getByRole("link", { name: "Terminal" }).click();
  await expect.poll(() => loads).toBe(1);
  await expect(page).toHaveURL(/\/terminal$/);
  await expect(page.getByRole("alert")).toContainText("This page needs reloading");
  // The server's name and its other tabs are still there around it.
  await expect(page.getByRole("heading", { name: "Survival" })).toBeVisible();
  await expect(page.getByRole("link", { name: "Console" })).toBeVisible();
  // And it does not go round again.
  await page.waitForTimeout(1500);
  expect(loads).toBe(1);
  // Another tab of the same server works.
  await page.getByRole("main").getByRole("link", { name: "Settings" }).click();
  await expect(page.getByRole("alert").filter({ hasText: "needs reloading" })).toHaveCount(0);
  pageErrors.length = 0;
});

test("a script that does not exist is not answered with the page", async ({ request }) => {
  const res = await request.get("/assets/Terminal-gone.js");
  expect(res.status()).toBe(404);
  // And is not kept: a browser that held on to it would not ask again after an update.
  expect(res.headers()["cache-control"]).toBe("no-store");
  const page = await request.get("/o/friends/s/anything");
  expect(page.status()).toBe(200);
  expect(await page.text()).toContain("<div id=\"root\">");
});
