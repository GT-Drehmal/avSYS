import { expect, test } from "./panel";

test("several servers are selected and acted on together", async ({ page, as, org, api }) => {
  const slug = await org(["alpha", "bravo", "charlie"]);
  const states = async () => Object.fromEntries((await api("owner").get(`/orgs/${slug}/servers`)).map((s: any) => [s.name, s.spec.state]));
  await as("owner");
  await page.goto(`/o/${slug}`);
  const bar = page.getByRole("region", { name: "Selection" });
  await expect(bar).toHaveCount(0);

  // A range, with Shift.
  await page.getByLabel("Select alpha").click();
  await page.getByLabel("Select charlie").click({ modifiers: ["Shift"] });
  await expect(bar).toContainText("3 servers selected");
  await bar.getByRole("button", { name: "Start", exact: true }).click();
  await expect(bar).toHaveCount(0);
  expect(await states()).toEqual({ alpha: "Running", bravo: "Running", charlie: "Running" });

  // An action says how many of the selected it applies to.
  await page.getByLabel("Select bravo").click();
  await bar.getByRole("button", { name: "Stop", exact: true }).click();
  await expect(bar).toHaveCount(0);
  await page.getByLabel("Select every server").click();
  await expect(bar.getByRole("button", { name: "Stop 2 of 3" })).toBeVisible();
  await expect(bar.getByRole("button", { name: "Start 1 of 3" })).toBeVisible();

  // Escape lets go of them.
  await page.keyboard.press("Escape");
  await expect(bar).toHaveCount(0);
  await expect(page.getByLabel("Select alpha")).not.toBeChecked();
});

test("deleting several servers is typed to confirm, and names them", async ({ page, as, org, api }) => {
  const slug = await org(["alpha", "bravo", "charlie"]);
  await as("owner");
  await page.goto(`/o/${slug}`);
  await page.getByLabel("Select bravo").click();
  await page.getByLabel("Select charlie").click();
  await page.getByRole("region", { name: "Selection" }).getByRole("button", { name: "Delete", exact: true }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("bravo, charlie");
  const confirm = dialog.getByRole("button", { name: "Delete 2 servers" });
  await expect(confirm).toBeDisabled();
  // Nothing runs behind this panel, so there is nothing to take a last snapshot of.
  await dialog.getByRole("switch", { name: /final snapshot/ }).click();
  await dialog.getByRole("textbox").fill("delete 2 servers");
  await confirm.click();
  await expect(dialog).toHaveCount(0);
  await expect.poll(async () => (await api("owner").get(`/orgs/${slug}/servers`)).map((s: any) => s.name)).toEqual(["alpha"]);
});

test("a selection does not follow you into another organization", async ({ page, as }) => {
  // Both of the seeded organizations have a server called survival.
  await as("owner");
  await page.goto("/o/club");
  await expect(page.getByLabel("Select survival")).toBeVisible();
  await page.getByRole("button", { name: /Build Club/ }).click();
  await page.getByRole("menuitem", { name: /Friends/ }).click();
  await expect(page).toHaveURL(/\/o\/friends$/);
  await page.getByLabel("Select survival").click();
  await expect(page.getByRole("region", { name: "Selection" })).toBeVisible();
  await page.getByRole("button", { name: /Friends/ }).click();
  await page.getByRole("menuitem", { name: /Build Club/ }).click();
  await expect(page).toHaveURL(/\/o\/club$/);
  await expect(page.getByRole("region", { name: "Selection" })).toHaveCount(0);
  await expect(page.getByLabel("Select survival")).not.toBeChecked();
});

test("what each role may do to the list", async ({ page, as }) => {
  await as("viewer");
  await page.goto("/o/friends");
  await expect(page.getByRole("listitem").filter({ hasText: "Survival" })).toBeVisible();
  await expect(page.getByLabel("Select survival")).toHaveCount(0);
  await expect(page.getByRole("link", { name: "New server" })).toHaveCount(0);

  await as("operator");
  await page.reload();
  await page.getByLabel("Select survival").click();
  const bar = page.getByRole("region", { name: "Selection" });
  await expect(bar.getByRole("button", { name: /^Stop/ })).toBeVisible();
  await expect(bar.getByRole("button", { name: "Delete", exact: true })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "New server" })).toHaveCount(0);

  await as("admin");
  await page.reload();
  await page.getByLabel("Select survival").click();
  await expect(bar.getByRole("button", { name: "Delete", exact: true })).toBeVisible();
  await expect(page.getByRole("link", { name: "New server" })).toBeVisible();
});

test("a snapshot of several servers is asked for at once, and one that could not says why", async ({ page, as, org }) => {
  const slug = await org(["alpha", "bravo"]);
  await as("owner");
  // Nothing runs behind this panel to take them: alpha is taken, and bravo
  // is busy with something else.
  const asked: string[] = [];
  await page.route(`**/api/v1/orgs/${slug}/servers/*/snapshots`, (route) => {
    const server = new URL(route.request().url()).pathname.split("/")[6];
    asked.push(`${route.request().method()} ${server}`);
    return server === "bravo"
      ? route.fulfill({ status: 409, json: { code: "busy", message: "Something else is being done to this server. Try again when it has finished." } })
      : route.fulfill({ status: 202, json: { kind: "snapshot", org: slug, server, stage: "Starting", percent: 0 } });
  });
  await page.goto(`/o/${slug}`);
  await page.getByLabel("Select every server").click();
  const bar = page.getByRole("region", { name: "Selection" });
  await bar.getByRole("button", { name: "Snapshot", exact: true }).click();
  await expect(page.getByText("Taking a snapshot of alpha")).toBeVisible();
  await expect(page.getByText(/1 of 2 failed\. bravo: Something else is being done/)).toBeVisible();
  expect(asked.sort()).toEqual(["POST alpha", "POST bravo"]);
  // What failed stays selected.
  await expect(bar).toContainText("1 server selected");
  await expect(page.getByLabel("Select bravo")).toBeChecked();
});

test("one selected server can be duplicated from the list; several cannot", async ({ page, as, org }) => {
  const slug = await org(["alpha", "bravo"]);
  await as("owner");
  await page.goto(`/o/${slug}`);
  const bar = page.getByRole("region", { name: "Selection" });
  await page.getByLabel("Select alpha").click();
  await page.getByLabel("Select bravo").click();
  await expect(bar.getByRole("button", { name: "Duplicate", exact: true })).toHaveCount(0);
  await page.getByLabel("Select bravo").click();
  await bar.getByRole("button", { name: "Duplicate", exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/new\\?from=server:alpha$`));
  await expect(page.getByRole("heading", { name: "Duplicate alpha" })).toBeVisible();
});

test("an operator is not offered to duplicate", async ({ page, as }) => {
  await as("operator");
  await page.goto("/o/friends");
  await page.getByLabel("Select survival").click();
  const bar = page.getByRole("region", { name: "Selection" });
  await expect(bar.getByRole("button", { name: "Start", exact: true })).toBeVisible();
  await expect(bar.getByRole("button", { name: "Duplicate", exact: true })).toHaveCount(0);
});

test("Duplicate is not offered where the panel cannot take snapshots", async ({ page, as }) => {
  await as("owner");
  await page.route("**/api/v1/meta", async (route) => {
    const meta = await (await route.fetch()).json();
    return route.fulfill({ json: { ...meta, snapshots: false } });
  });
  await page.goto("/o/friends");
  await page.getByLabel("Select survival").click();
  const bar = page.getByRole("region", { name: "Selection" });
  await expect(bar.getByRole("button", { name: "Start", exact: true })).toBeVisible();
  await expect(bar.getByRole("button", { name: "Duplicate", exact: true })).toHaveCount(0);
});

test("the selection bar never covers the end of the list, however many rows it wraps to", async ({ page, as, org }) => {
  // Enough servers that the list runs past the bottom of the window.
  const names = ["alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet", "kilo", "lima"];
  const slug = await org(names);
  await as("owner");
  for (const width of [820, 1280, 390]) {
    await page.setViewportSize({ width, height: 600 });
    await page.goto(`/o/${slug}`);
    // One at a time: on a phone there is no box for all of them.
    for (const name of ["alpha", "lima"]) await page.getByLabel(`Select ${name}`, { exact: true }).click();
    const bar = page.getByRole("region", { name: "Selection" });
    await expect(bar).toContainText("2 servers selected");
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
    const last = page.getByRole("link", { name: /^lima / }).first();
    await expect(last).toBeVisible();
    // Settled: the bar's height is known once it has been laid out.
    await page.waitForTimeout(100);
    await page.evaluate(() => window.scrollTo(0, document.documentElement.scrollHeight));
    const row = await last.boundingBox();
    const box = await bar.boundingBox();
    expect(row && box && row.y + row.height <= box.y + 1, `at ${width} pixels the last row ends at ${row && row.y + row.height}, the bar starts at ${box?.y}`).toBe(true);
  }
});
