import { levelDat, zip } from "./archives";
import { expect, test } from "./panel";

// A Fabric server as an import leaves it: on the loader build its archive had.
const pinned = { eula: true, loader: "fabric", version: "1.21.4", loaderVersion: "0.16.5", state: "Stopped", resources: { memory: "2Gi" }, storage: { size: "2Gi" } };

test("a server moved to another game version in its settings drops the loader build it had", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await api("owner").post(`/orgs/${slug}/servers`, { name: "fabric", spec: pinned });
  await as("owner");
  await page.goto(`/o/${slug}/s/fabric/settings`);
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.4");
  await page.getByLabel("Game version").selectOption("1.21.5");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByText("Settings saved")).toBeVisible();
  const { spec } = await api("owner").get(`/orgs/${slug}/servers/fabric`);
  expect(spec).toMatchObject({ loader: "fabric", version: "1.21.5" });
  expect(spec).not.toHaveProperty("loaderVersion");
  await expect(page.getByLabel("Loader build")).toHaveValue("");
  await expect(page.getByLabel("Loader build")).toHaveAttribute("placeholder", "Newest");
});

test("a server's loader build is shown in its settings, cleared with another version or type, and saved as typed", async ({ page, as, org, api }) => {
  const slug = await org(["paper"]);
  await api("owner").post(`/orgs/${slug}/servers`, { name: "fabric", spec: pinned });
  await as("owner");
  await page.goto(`/o/${slug}/s/fabric/settings`);
  const build = page.getByLabel("Loader build");
  await expect(build).toHaveValue("0.16.5");
  await page.getByLabel("Game version").selectOption("1.21.5");
  await expect(build).toHaveValue("");
  // Back on the version it is for, it is back too.
  await page.getByLabel("Game version").selectOption("1.21.4");
  await expect(build).toHaveValue("0.16.5");
  await page.getByLabel("Type").selectOption("forge");
  await expect(build).toHaveValue("");
  // A type with no builds to pick has no field.
  await page.getByLabel("Type").selectOption("paper");
  await expect(build).toHaveCount(0);
  await page.getByLabel("Type").selectOption("fabric");
  await expect(build).toHaveValue("0.16.5");
  await build.fill(" 0.16.10 ");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByText("Settings saved")).toBeVisible();
  expect((await api("owner").get(`/orgs/${slug}/servers/fabric`)).spec).toMatchObject({ loader: "fabric", version: "1.21.4", loaderVersion: "0.16.10" });
  // Emptied, it is the newest again.
  await build.fill("");
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByText("Settings saved").last()).toBeVisible();
  await expect.poll(async () => (await api("owner").get(`/orgs/${slug}/servers/fabric`)).spec.loaderVersion).toBeUndefined();

  await page.goto(`/o/${slug}/s/paper/settings`);
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.4");
  await expect(build).toHaveCount(0);
});

test("the server form shows the loader build of the server it starts from, and clears it with another version or type", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await api("owner").post(`/orgs/${slug}/servers`, { name: "fabric", spec: pinned });
  await as("owner");
  const build = page.getByLabel("Loader build");

  // A copy has the original's.
  await page.goto(`/o/${slug}/new?from=server:fabric`);
  await expect(build).toHaveValue("0.16.5");
  await page.getByLabel("Game version").selectOption("1.21.5");
  await expect(build).toHaveValue("");
  await page.getByLabel("Game version").selectOption("1.21.4");
  await expect(build).toHaveValue("0.16.5");

  // An import has the archive's.
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("button", { name: "Import", exact: true }).click();
  const archive = zip({
    "fabric-server-mc.1.21.4-loader.0.16.5-launcher.1.0.1.jar": "jar",
    "world/level.dat": levelDat("1.21.4"),
  });
  await page.getByLabel("Server archive").setInputFiles({ name: "fabric.zip", mimeType: "application/zip", buffer: archive });
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.4");
  await expect(build).toHaveValue("0.16.5");
  await page.getByLabel("Game version").selectOption("1.21.5");
  await expect(build).toHaveValue("");
  await page.getByLabel("Game version").selectOption("1.21.4");
  await expect(build).toHaveValue("0.16.5");
  const type = page.getByRole("group", { name: "Server type" });
  await type.getByRole("button", { name: /^Forge/ }).click();
  await expect(build).toHaveValue("");
  await type.getByRole("button", { name: /^Paper/ }).click();
  await expect(build).toHaveCount(0);
  // Back on the type it is for, it is back too, as in Settings.
  await type.getByRole("button", { name: /^Fabric/ }).click();
  await expect(build).toHaveValue("0.16.5");

  // So for a copy.
  await page.goto(`/o/${slug}/new?from=server:fabric`);
  await expect(build).toHaveValue("0.16.5");
  await type.getByRole("button", { name: /^Forge/ }).click();
  await expect(build).toHaveValue("");
  await type.getByRole("button", { name: /^Fabric/ }).click();
  await expect(build).toHaveValue("0.16.5");
});

test("a template whose version is no longer listed goes to Latest without its loader build", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await api("owner").post(`/orgs/${slug}/templates`, {
    name: "Pinned",
    description: "",
    spec: { loader: "fabric", version: "1.21.4", loaderVersion: "0.16.5", resources: { memory: "2Gi" }, storage: { size: "2Gi" } },
  });
  // 1.21.4 has left the list of versions Fabric has a build for.
  await page.route("**/api/v1/catalog/minecraft/versions?loader=fabric", async (route) => {
    const list = await (await route.fetch()).json();
    return route.fulfill({ json: { ...list, versions: list.versions.filter((v: { id: string }) => v.id !== "1.21.4") } });
  });
  await as("owner");
  await page.goto(`/o/${slug}/templates`);
  await page.getByRole("listitem", { name: "Pinned" }).getByRole("button", { name: "New server" }).click();
  await expect(page.getByText("Fabric has no build for 1.21.4 yet")).toBeVisible();
  await expect(page.getByLabel("Game version")).toHaveValue("LATEST");
  await expect(page.getByLabel("Loader build")).toHaveValue("");
});

test("a world from another game version clears the loader build typed for the one picked", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Fabric/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  const build = page.getByLabel("Loader build");
  await build.fill("0.16.5");
  await page.getByRole("button", { name: "Upload world" }).click();
  await page.getByLabel("World archive").setInputFiles({ name: "world.zip", mimeType: "application/zip", buffer: zip({ "world/level.dat": levelDat("1.21.5") }) });
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.5");
  await expect(build).toHaveValue("");
});
