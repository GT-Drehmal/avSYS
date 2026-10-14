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

test("a part of the app that is gone reloads the page once, and never leaves it empty", async ({ page, as, pageErrors }) => {
  await as("owner");
  await page.goto("/o/friends/s/survival");
  await expect(page.getByRole("link", { name: "Terminal" })).toBeVisible();
  // As in a tab left open while the panel was updated: the file it asks for
  // is not there.
  let asked = 0;
  await page.route("**/assets/Terminal-*.js", (route) =>
    asked++ === 0 ? route.fulfill({ status: 404, headers: { "Cache-Control": "no-store" }, body: "not found" }) : route.continue(),
  );
  let loads = 0;
  page.on("load", () => loads++);
  await page.getByRole("link", { name: "Terminal" }).click();
  await expect.poll(() => loads).toBe(1);
  await expect(page).toHaveURL(/\/terminal$/);
  // Whatever the reload found, the server's page is there around it. (In
  // Chromium the file is asked for again and the terminal opens. WebKit
  // answers the second request from memory, which a real update, where the
  // file has a new name, does not run into; it then says to reload by hand.)
  await expect(page.getByRole("link", { name: "Terminal" })).toBeVisible();
  await expect(page.getByRole("heading", { name: "Survival" })).toBeVisible();
  // And it does not go round again.
  await page.waitForTimeout(1500);
  expect(loads).toBe(1);
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
