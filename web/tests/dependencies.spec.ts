import { expect, test } from "./panel";

// What Modrinth content needs is put on the list with it, and goes with it.

test("a mod added brings what it needs, which stays while needed and goes with it", async ({ page, as, org, api }) => {
  const slug = await org([]);
  const server = `/orgs/${slug}/servers/harpy`;
  await api("owner").post(`/orgs/${slug}/servers`, {
    name: "harpy",
    spec: { eula: true, loader: "fabric", version: "1.21.4", state: "Stopped", resources: { memory: "2Gi" }, storage: { size: "2Gi" } },
  });
  await as("owner");
  // Modrinth's search finds Kinswathe first, whatever is searched for.
  await page.route("**/api/v1/catalog/modrinth/search?*", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.hits[0] = { ...body.hits[0], project_id: "PKINS001", slug: "kinswathe", title: "Kinswathe", project_type: "mod", categories: ["fabric"] };
    await route.fulfill({ response, json: body });
  });
  await page.goto(`/o/${slug}/s/harpy/content`);
  await page.getByRole("button", { name: "Add mods" }).click();
  await page.getByRole("dialog").getByRole("button", { name: /^Kinswathe by/ }).click();
  await page.keyboard.press("Escape");

  const mods = page.getByRole("region", { name: "Mods" });
  const wathe = mods.getByRole("listitem").filter({ has: page.getByText("Wathe", { exact: true }) });
  await expect(wathe.getByText("Needed by Kinswathe")).toBeVisible();
  await expect(mods.getByRole("button", { name: "Remove Wathe", exact: true })).toHaveCount(0);
  const { spec } = await api("owner").get(server);
  expect(spec.mods.map((m: { project: string; version: string; dependency?: boolean; neededBy?: string[] }) => [m.project, m.version, !!m.dependency, m.neededBy ?? []])).toEqual([
    ["kinswathe", "vKINS001", false, []],
    ["PWATHE01", "vWATHE01", true, ["kinswathe"]],
  ]);

  await mods.getByRole("button", { name: "Remove Kinswathe", exact: true }).click();
  await expect(mods.getByText("No mods.")).toBeVisible();
  await expect.poll(async () => (await api("owner").get(server)).spec.mods ?? []).toEqual([]);
});
