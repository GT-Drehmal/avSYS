import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// Several resource packs joined into one, in order, the top one first.

const faithful = { sha1: "a".repeat(40), size: 4_000_000, project: { source: "modrinth", id: "faithful-32x", title: "Faithful 32x", versionName: "1.21.4" } };
const sounds = { sha1: "b".repeat(40), size: 120_000, name: "Better Sounds.zip", hidden: 3, hiddenBy: faithful.sha1 };
const joined = (stack: object[]) => ({ url: "https://panel.example/packs/c.zip", sha1: "c".repeat(40), uploaded: { size: 4_100_000, time: new Date().toISOString() }, stack });

/**
 * survival with this resource pack. Putting its packs in order, taking them
 * off and adding one on top are answered as the panel would, and listed.
 */
async function survival(page: Page, pack: any) {
  const state = { pack, orders: [] as string[][], added: [] as any[] };
  const withPack = (s: { name: string; spec: object }) => (s.name === "survival" ? { ...s, spec: { ...s.spec, resourcePack: state.pack } } : s);
  const server = async () => withPack(await (await page.request.get("/api/v1/orgs/friends/servers/survival")).json());
  await page.route("**/api/v1/orgs/friends/servers", async (route) => route.fulfill({ json: (await (await route.fetch()).json()).map(withPack) }));
  await page.route("**/api/v1/orgs/friends/servers/survival", async (route) => route.fulfill({ json: withPack(await (await route.fetch()).json()) }));
  const stackNow = () => state.pack?.stack ?? (state.pack ? [{ sha1: state.pack.sha1, size: state.pack.uploaded?.size ?? 0, name: state.pack.uploaded?.name }] : []);
  await page.route("**/api/v1/orgs/friends/servers/survival/resource-packs**", async (route) => {
    const req = route.request();
    if (req.method() === "PUT") {
      const { order } = req.postDataJSON();
      state.orders.push(order);
      const by = new Map(stackNow().map((p: any) => [p.sha1, p]));
      state.pack = order.length ? joined(order.map((sha: string) => by.get(sha))) : null;
    } else {
      const top = req.url().endsWith("/url") ? { sha1: req.postDataJSON().sha1, size: 1000, project: req.postDataJSON().project } : { sha1: "d".repeat(40), size: 2000, name: new URL(req.url()).searchParams.get("name") };
      state.added.push(top);
      state.pack = joined([top, ...stackNow()]);
    }
    return route.fulfill({ json: await server() });
  });
  return state;
}

const packs = (page: Page) => page.getByRole("list", { name: "Packs, the top one first" }).getByRole("listitem");

test.describe("as an owner", () => {
  test.beforeEach(async ({ as }) => {
    await as("owner");
  });

  test("packs are listed top first, saying what a higher one covers, and move up and down", async ({ page }) => {
    const state = await survival(page, joined([faithful, sounds]));
    await page.goto("/o/friends/s/survival/content");
    await expect(page.getByRole("region", { name: "Resource packs" }).getByRole("heading")).toHaveText("Resource packs2");
    await expect(packs(page)).toHaveCount(2);
    await expect(packs(page).nth(0)).toContainText("Faithful 32x");
    await expect(packs(page).nth(1)).toContainText("Better Sounds");
    await expect(packs(page).nth(1)).toContainText("117 KB · 3 files covered by Faithful 32x");
    // The top one cannot go higher, nor the bottom one lower.
    await expect(page.getByRole("button", { name: "Move Faithful 32x up" })).toBeDisabled();
    await expect(page.getByRole("button", { name: "Move Better Sounds down" })).toBeDisabled();
    await page.getByRole("button", { name: "Move Better Sounds up" }).click();
    await expect(packs(page).nth(0)).toContainText("Better Sounds");
    expect(state.orders).toEqual([[sounds.sha1, faithful.sha1]]);
    await page.getByRole("button", { name: "Move Better Sounds down" }).click();
    await expect(packs(page).nth(0)).toContainText("Faithful 32x");
    expect(state.orders).toEqual([
      [sounds.sha1, faithful.sha1],
      [faithful.sha1, sounds.sha1],
    ]);
  });

  test("a pack is dragged into another place", async ({ page }) => {
    const state = await survival(page, joined([faithful, sounds]));
    await page.goto("/o/friends/s/survival/content");
    await packs(page).nth(1).dragTo(packs(page).nth(0));
    await expect.poll(() => state.orders).toEqual([[sounds.sha1, faithful.sha1]]);
    await expect(packs(page).nth(0)).toContainText("Better Sounds");
  });

  test("a pack taken off leaves the others; the last one leaves none", async ({ page }) => {
    const state = await survival(page, joined([faithful, sounds]));
    await page.goto("/o/friends/s/survival/content");
    await page.getByRole("button", { name: "Remove Better Sounds" }).click();
    await expect(packs(page)).toHaveCount(1);
    await page.getByRole("button", { name: "Remove Faithful 32x" }).click();
    await expect(page.getByText(/^None\. A pack added here/)).toBeVisible();
    expect(state.orders).toEqual([[faithful.sha1], []]);
  });

  test("a pack added to a server that has one goes on top of it", async ({ page }) => {
    const state = await survival(page, { url: "https://panel.example/packs/e.zip", sha1: "e".repeat(40), uploaded: { name: "World Pack.zip", size: 5000, time: new Date().toISOString() } });
    await page.goto("/o/friends/s/survival/content");
    await expect(packs(page)).toHaveCount(0);
    await page.getByRole("button", { name: "Add resource pack" }).click();
    const dialog = page.getByRole("dialog");
    // Not a replacement.
    await expect(dialog.getByText(/This replaces/)).toHaveCount(0);
    await dialog.getByRole("button", { name: "Upload", exact: true }).click();
    await dialog.getByLabel("Resource pack file").setInputFiles({ name: "Fancy Glass.zip", mimeType: "application/zip", buffer: Buffer.from("PK") });
    await expect(dialog).toHaveCount(0);
    expect(state.added).toEqual([{ sha1: "d".repeat(40), size: 2000, name: "Fancy Glass.zip" }]);
    await expect(packs(page)).toHaveCount(2);
    await expect(packs(page).nth(0)).toContainText("Fancy Glass");
    await expect(packs(page).nth(1)).toContainText("World Pack");
  });

  test("one from Modrinth is added on top, and is marked added", async ({ page }) => {
    const state = await survival(page, joined([sounds]));
    await page.goto("/o/friends/s/survival/content");
    await page.getByRole("button", { name: "Add resource pack" }).click();
    const dialog = page.getByRole("dialog");
    await dialog.getByRole("button", { name: /^Faithful 32x/ }).click();
    await expect(dialog).toHaveCount(0);
    expect(state.added).toHaveLength(1);
    expect(state.added[0].project.id).toBe("faithful-32x");
    await expect(packs(page).nth(0)).toContainText("Faithful 32x");
    await page.getByRole("button", { name: "Add resource pack" }).click();
    await expect(page.getByRole("dialog").getByRole("button", { name: /^Faithful 32x/ }).locator("svg.lucide-check")).toBeVisible();
  });
});

test("a viewer sees the order, and cannot change it", async ({ page, as }) => {
  await as("viewer");
  await survival(page, joined([faithful, sounds]));
  await page.goto("/o/friends/s/survival/content");
  await expect(packs(page)).toHaveCount(2);
  await expect(page.getByRole("button", { name: /^Move / })).toHaveCount(0);
  await expect(page.getByRole("button", { name: /^Remove / })).toHaveCount(0);
  await expect(packs(page).nth(0)).toHaveAttribute("draggable", "false");
});
