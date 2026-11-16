import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// The test panel runs v0.1.0, installed with the chart, and GitHub (as the
// panel's stand-in for it answers) has v0.2.0 out. The panel has its own
// Deployment, so it can move itself to v0.2.0.

/** Answers /panel as the panel does, changed by what is given. Only what is asked of it: what is set there goes on. */
async function panelAs(page: Page, change: (json: any) => void) {
  await page.route("**/api/v1/panel", async (route) => {
    if (route.request().method() !== "GET") return route.fallback();
    const json = await (await route.fetch()).json();
    change(json);
    await route.fulfill({ json });
  });
}

/** Where the panel cannot update itself, as without its own Deployment. */
const cannotUpdate = (json: any) => {
  json.panel = { ...json.panel, canUpdate: false };
};

test("the administrator is told of a newer release in Instance settings, with an Update button", async ({ page, as }) => {
  await as("owner");
  // The panel asks GitHub, never the browser.
  const asked: string[] = [];
  page.on("request", (r) => {
    if (new URL(r.url()).hostname.endsWith("github.com")) asked.push(r.url());
  });
  await page.goto("/settings");
  const notice = page.getByRole("region", { name: "New release" });
  await expect(notice).toContainText("avSYS v0.2.0 is out.");
  await expect(notice.getByRole("link", { name: "What's new" })).toHaveAttribute("href", "https://github.com/GT-Drehmal/avSYS/releases/tag/v0.2.0");
  await expect(notice.getByRole("button", { name: "Update" })).toBeVisible();
  // It updates itself: no command to run.
  await expect(notice).not.toContainText("run:");
  expect(asked).toEqual([]);
});

test("where the panel cannot update itself, it gives the command that upgrades", async ({ page, as }) => {
  await as("owner");
  await panelAs(page, cannotUpdate);
  await page.goto("/settings");
  const notice = page.getByRole("region", { name: "New release" });
  await expect(notice).toContainText("To upgrade, run:");
  await expect(notice.getByRole("button", { name: /helm upgrade/ })).toHaveText(
    "helm upgrade avsys oci://ghcr.io/gt-drehmal/charts/avsys -n avsys --reset-then-reuse-values --version 0.2.0",
  );
  await expect(notice.getByRole("button", { name: "Update", exact: true })).toHaveCount(0);
  await expect(notice.getByRole("link", { name: "install.yaml" })).toHaveCount(0);
  // Nor can it install fixes by itself.
  await expect(page.getByRole("region", { name: "Updates" })).toContainText("This is avSYS v0.1.0.");
  await expect(page.getByRole("switch", { name: "Install fixes by itself" })).toHaveCount(0);
});

test("a panel installed from install.yaml is told to bring the new one over", async ({ page, as }) => {
  await as("owner");
  await panelAs(page, (json) => {
    cannotUpdate(json);
    json.update = {
      ...json.update,
      command: "kubectl apply -f install.yaml",
      manifest: "https://github.com/GT-Drehmal/avSYS/releases/download/v0.2.0/install.yaml",
    };
  });
  await page.goto("/settings");
  const notice = page.getByRole("region", { name: "New release" });
  await expect(notice).toContainText("Copy your changes into the new install.yaml, then run:");
  await expect(notice.getByRole("link", { name: "install.yaml" })).toHaveAttribute("href", "https://github.com/GT-Drehmal/avSYS/releases/download/v0.2.0/install.yaml");
  await expect(notice.getByRole("button", { name: "kubectl apply -f install.yaml" })).toBeVisible();
});

test("where how the panel was installed is not known, the notice has no command", async ({ page, as }) => {
  await as("owner");
  await panelAs(page, (json) => {
    cannotUpdate(json);
    json.update = { version: json.update.version, notes: json.update.notes };
  });
  await page.goto("/settings");
  const notice = page.getByRole("region", { name: "New release" });
  await expect(notice).toContainText("avSYS v0.2.0 is out.");
  await expect(notice).not.toContainText("run:");
});

test("with no newer release, there is no notice", async ({ page, as }) => {
  await as("owner");
  await panelAs(page, (json) => {
    delete json.update;
  });
  await page.goto("/settings");
  await expect(page.getByRole("region", { name: "Updates" })).toContainText("This is avSYS v0.1.0.");
  await expect(page.getByRole("region", { name: "New release" })).toHaveCount(0);
  const asked = page.waitForResponse((r) => r.url().endsWith("/api/v1/panel"));
  await page.goto("/nodes");
  await asked;
  await expect(page.getByTestId("cluster-total")).toBeVisible();
  await expect(page.getByRole("region", { name: "New release" })).toHaveCount(0);
});

// What the panel does when Update is pressed is the Go tests' (it moves its
// own Deployment): here the panel is answered for, as one that restarts on
// the new version, since the test panel is shared and cannot restart.
test("Update restarts the panel on the new release, and the page comes back on it", async ({ page, as }) => {
  // The page looks at the panel again every 15 seconds, and is made to here
  // once while the panel is back but the page has not seen it yet.
  test.setTimeout(60_000);
  await as("owner");
  let pressed: unknown;
  // Restarted, the panel runs v0.2.0 and so has no newer release to tell of.
  let restarted = false;
  await panelAs(page, (json) => {
    if (!pressed) return;
    restarted = true;
    delete json.update;
    json.panel = { ...json.panel, version: "v0.2.0" };
  });
  await page.route("**/api/v1/panel/update", async (route) => {
    pressed = route.request().postDataJSON();
    await route.fulfill({ status: 202, json: { version: "v0.1.0", canUpdate: true, autoUpdate: false, updating: "v0.2.0" } });
  });
  // Once pressed, the panel does not answer for a while, then, once the
  // test lets it, answers as the new version.
  let looks = 0;
  let back = false;
  await page.route("**/api/v1/meta", async (route) => {
    if (!pressed) return route.continue();
    looks++;
    if (looks <= 2 || !back) return route.abort("connectionrefused");
    const json = await (await route.fetch()).json();
    await route.fulfill({ json: { ...json, version: "v0.2.0" } });
  });
  await page.goto("/settings");
  const notice = page.getByRole("region", { name: "New release" });
  await notice.getByRole("button", { name: "Update" }).click();

  const dialog = page.getByRole("dialog");
  await expect(dialog).toContainText("Update to v0.2.0?");
  await expect(dialog).toContainText("Players are disconnected for a moment.");
  // Nothing is asked of the panel until it is confirmed.
  expect(pressed).toBeUndefined();
  await dialog.getByRole("button", { name: "Update" }).click();
  await expect(dialog).toBeHidden();
  expect(pressed).toEqual({ version: "v0.2.0" });

  await expect(notice.getByRole("status")).toHaveText("Updating to v0.2.0…");
  await expect(notice.getByRole("button", { name: "Update" })).toHaveCount(0);
  // The restarted panel says nothing of an update: the page still waits
  // for the new version, and loads it.
  await expect.poll(() => restarted, { timeout: 20_000 }).toBe(true);
  await expect(notice.getByRole("status")).toHaveText("Updating to v0.2.0…");
  back = true;
  const reloaded = page.waitForEvent("load", { timeout: 20_000 });
  await expect(notice.getByRole("status")).toHaveText("Updated to v0.2.0. Loading it…", { timeout: 15_000 });
  expect(looks).toBeGreaterThan(2);
  await reloaded;
});

test("an update that cannot be done says why, and nothing changes", async ({ page, as }) => {
  await as("owner");
  await page.route("**/api/v1/panel/update", (route) =>
    route.fulfill({ status: 409, json: { code: "cannot_update", message: "avSYS cannot update itself here." } }),
  );
  await page.goto("/settings");
  const notice = page.getByRole("region", { name: "New release" });
  await notice.getByRole("button", { name: "Update" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Update" }).click();
  await expect(page.getByText("avSYS cannot update itself here.")).toBeVisible();
  await expect(notice.getByRole("status")).toHaveCount(0);
});

test("an update under way is shown as such when the page is opened", async ({ page, as }) => {
  await as("owner");
  await panelAs(page, (json) => {
    json.panel = { ...json.panel, updating: "v0.2.0" };
  });
  await page.goto("/settings");
  const notice = page.getByRole("region", { name: "New release" });
  await expect(notice.getByRole("status")).toHaveText("Updating to v0.2.0…");
  await expect(notice.getByRole("button", { name: "Update" })).toHaveCount(0);
});

test("an update that takes long says so", async ({ page, as }) => {
  await as("owner");
  await page.clock.install();
  await panelAs(page, (json) => {
    json.panel = { ...json.panel, updating: "v0.2.0" };
  });
  // The panel never comes back.
  await page.route("**/api/v1/meta", (route) => route.abort("connectionrefused"));
  await page.goto("/settings");
  const status = page.getByRole("region", { name: "New release" }).getByRole("status");
  await expect(status).toHaveText("Updating to v0.2.0…");
  await page.clock.runFor(11 * 60_000);
  await expect(status).toHaveText("Updating to v0.2.0… This is taking longer than usual.");
});

test("the administrator chooses whether fixes are installed by themselves", async ({ page, as }) => {
  await as("owner");
  // The setting is the shared test panel's: answered for here, so that tests
  // side by side do not see each other's.
  let on = false;
  const sent: unknown[] = [];
  await panelAs(page, (json) => {
    json.panel = { ...json.panel, autoUpdate: on };
  });
  await page.route("**/api/v1/panel", async (route) => {
    if (route.request().method() !== "PATCH") return route.fallback();
    const body = route.request().postDataJSON() as { autoUpdate: boolean };
    sent.push(body);
    on = body.autoUpdate;
    await route.fulfill({ json: { version: "v0.1.0", canUpdate: true, autoUpdate: on } });
  });
  await page.goto("/settings");
  const updates = page.getByRole("region", { name: "Updates" });
  await expect(updates).toContainText("This is avSYS v0.1.0.");
  await expect(updates).toContainText("Fixes are installed while no one is playing.");
  const toggle = updates.getByRole("switch", { name: "Install fixes by itself" });
  await expect(toggle).toHaveAttribute("aria-checked", "false");
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-checked", "true");
  await expect.poll(() => sent).toEqual([{ autoUpdate: true }]);
  await page.reload();
  await expect(toggle).toHaveAttribute("aria-checked", "true");
  await toggle.click();
  await expect(toggle).toHaveAttribute("aria-checked", "false");
  await expect.poll(() => sent).toEqual([{ autoUpdate: true }, { autoUpdate: false }]);
});

test("the panel's own setting is not changed by these tests", async ({ api }) => {
  // The tests above answer for the setting, so the shared panel's stays off.
  const { panel } = await api("owner").get("/panel");
  expect(panel).toMatchObject({ version: "v0.1.0", canUpdate: true, autoUpdate: false });
  expect(panel.updating).toBeUndefined();
});

test("the Nodes page points to Instance settings for a newer release, and has nothing else of updates", async ({ page, as }) => {
  await as("owner");
  await page.goto("/nodes");
  const note = page.getByRole("region", { name: "New release" });
  await expect(note).toHaveText("avSYS v0.2.0 is out. Instance settings");
  await expect(note.getByRole("button")).toHaveCount(0);
  await expect(page.getByRole("switch", { name: "Install fixes by itself" })).toHaveCount(0);
  await expect(page.getByRole("region", { name: "CurseForge" })).toHaveCount(0);
  await note.getByRole("link", { name: "Instance settings" }).click();
  await expect(page).toHaveURL(/\/settings$/);
  await expect(page.getByRole("region", { name: "New release" }).getByRole("button", { name: "Update" })).toBeVisible();
});

test("while the panel updates, the Nodes page says so", async ({ page, as }) => {
  await as("owner");
  await panelAs(page, (json) => {
    json.panel = { ...json.panel, updating: "v0.2.0" };
  });
  await page.goto("/nodes");
  await expect(page.getByRole("region", { name: "New release" })).toHaveText("Updating to v0.2.0. Instance settings");
});
