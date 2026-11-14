import { expect, test } from "./panel";

// The test panel runs v0.1.0, installed with the chart, and GitHub (as the
// panel's stand-in for it answers) has v0.2.0 out.

test("the administrator is told of a newer release on the Nodes page, with the command that upgrades", async ({ page, as }) => {
  await as("owner");
  // The panel asks GitHub, never the browser.
  const asked: string[] = [];
  page.on("request", (r) => {
    if (new URL(r.url()).hostname.endsWith("github.com")) asked.push(r.url());
  });
  await page.goto("/nodes");
  const notice = page.getByRole("region", { name: "New release" });
  await expect(notice).toContainText("avSYS v0.2.0 is out.");
  await expect(notice.getByRole("link", { name: "What's new" })).toHaveAttribute("href", "https://github.com/GT-Drehmal/avSYS/releases/tag/v0.2.0");
  await expect(notice).toContainText("To upgrade, run:");
  await expect(notice.getByRole("button", { name: /helm upgrade/ })).toHaveText(
    "helm upgrade avsys oci://ghcr.io/gt-drehmal/charts/avsys -n avsys --reset-then-reuse-values --version 0.2.0",
  );
  await expect(notice.getByRole("link", { name: "install.yaml" })).toHaveCount(0);
  expect(asked).toEqual([]);
});

test("a panel installed from install.yaml is told to bring the new one over", async ({ page, as }) => {
  await as("owner");
  await page.route("**/api/v1/nodes", async (route) => {
    const json = await (await route.fetch()).json();
    json.update = {
      ...json.update,
      command: "kubectl apply -f install.yaml",
      manifest: "https://github.com/GT-Drehmal/avSYS/releases/download/v0.2.0/install.yaml",
    };
    await route.fulfill({ json });
  });
  await page.goto("/nodes");
  const notice = page.getByRole("region", { name: "New release" });
  await expect(notice).toContainText("Copy your changes into the new install.yaml, then run:");
  await expect(notice.getByRole("link", { name: "install.yaml" })).toHaveAttribute("href", "https://github.com/GT-Drehmal/avSYS/releases/download/v0.2.0/install.yaml");
  await expect(notice.getByRole("button", { name: "kubectl apply -f install.yaml" })).toBeVisible();
});

test("where how the panel was installed is not known, the notice has no command", async ({ page, as }) => {
  await as("owner");
  await page.route("**/api/v1/nodes", async (route) => {
    const json = await (await route.fetch()).json();
    json.update = { version: json.update.version, notes: json.update.notes };
    await route.fulfill({ json });
  });
  await page.goto("/nodes");
  const notice = page.getByRole("region", { name: "New release" });
  await expect(notice).toContainText("avSYS v0.2.0 is out.");
  await expect(notice).not.toContainText("run:");
});

test("with no newer release, there is no notice", async ({ page, as }) => {
  await as("owner");
  await page.route("**/api/v1/nodes", async (route) => {
    const json = await (await route.fetch()).json();
    delete json.update;
    await route.fulfill({ json });
  });
  await page.goto("/nodes");
  await expect(page.getByTestId("cluster-total")).toBeVisible();
  await expect(page.getByRole("region", { name: "New release" })).toHaveCount(0);
});
