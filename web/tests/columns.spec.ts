import type { Locator, Page } from "@playwright/test";
import { expect, test } from "./panel";

// The list of servers is one grid, which its heading and every row share:
// the name takes the room the other columns do not need, a long one goes
// onto a second line rather than being cut short, and an address is only as
// wide as it is, up to a point, past which it is shown whole when pointed at.

const columns = ["server", "software", "players", "address", "actions"] as const;
const long = ["Harpy Express (Oct. 2026) (Copy)", "Murder Mystery Test Server (Copy)"];

const box = async (l: Locator) => {
  const b = await l.boundingBox();
  if (!b) throw new Error("not shown");
  return b;
};
const heading = (page: Page) => page.getByRole("main").locator("div:has(> [data-col=server]):not(a)").first();
const rows = (page: Page) => page.getByRole("main").getByRole("listitem");

/** Whether an element shows all of its text: none of it cut off at the end or below. */
const whole = (l: Locator) => l.evaluate((e) => e.scrollWidth <= e.clientWidth + 1 && e.scrollHeight <= e.clientHeight + 1);
/** How many lines its text takes. */
const lines = (l: Locator) =>
  l.evaluate((e) => {
    const style = getComputedStyle(e);
    const line = parseFloat(style.lineHeight) || parseFloat(style.fontSize) * 1.2;
    return Math.round(e.getBoundingClientRect().height / line);
  });

/** Each column of each row starts and ends where its heading does. */
async function linedUp(page: Page) {
  const head = heading(page);
  const n = await rows(page).count();
  expect(n).toBeGreaterThan(0);
  for (const col of columns) {
    const h = await box(head.locator(`[data-col=${col}]`));
    for (let i = 0; i < n; i++) {
      const c = await box(rows(page).nth(i).locator(`[data-col=${col}]`));
      expect(Math.abs(c.x - h.x), `${col} of row ${i} starts with its heading`).toBeLessThan(1);
      expect(Math.abs(c.width - h.width), `${col} of row ${i} is as wide as its heading`).toBeLessThan(1);
    }
  }
}

for (const width of [1600, 1100]) {
  test.describe(`at ${width} wide`, () => {
    test.beforeEach(async ({ page }) => page.setViewportSize({ width, height: 900 }));

    test("long names are shown whole, and the heading lines up with the rows", async ({ page, as }) => {
      await as("owner");
      await page.goto("/o/club");
      await expect(rows(page)).toHaveCount(5);
      for (const name of long) {
        const shown = page.getByRole("main").getByTitle(name, { exact: true });
        await expect(shown).toHaveText(name);
        expect(await whole(shown), `${name} is not cut short`).toBe(true);
        expect(await lines(shown)).toBeLessThanOrEqual(2);
      }
      await linedUp(page);
      // The address column is as wide as the widest address, and no wider.
      const addresses = rows(page).locator("[data-col=address] > *");
      const widest = Math.max(...(await addresses.evaluateAll((all) => all.map((e) => e.getBoundingClientRect().width))));
      const column = await box(heading(page).locator("[data-col=address]"));
      expect(column.width).toBeLessThanOrEqual(widest + 1);
      for (const address of await rows(page).locator("[data-col=address] button span").all()) expect(await whole(address)).toBe(true);
      // So the one with a short address has its buttons close after the widest.
      const harpy = rows(page).filter({ hasText: long[0] });
      const after = (await box(harpy.locator("[data-col=actions] button").first())).x - (column.x + column.width);
      expect(after).toBeLessThanOrEqual(17);
    });

    test("with no addresses, the name has the room", async ({ page, as, org, api }) => {
      const slug = await org([]);
      const owner = api("owner");
      for (const [name, displayName] of [
        ["harpy", long[0]],
        ["mystery", long[1]],
        ["a", "A"],
      ])
        await owner.post(`/orgs/${slug}/servers`, {
          name,
          spec: { eula: true, displayName, loader: "paper", version: "1.21.4", state: "Stopped", resources: { memory: "2Gi" }, storage: { size: "2Gi" } },
        });
      await as("owner");
      await page.goto(`/o/${slug}`);
      await expect(rows(page)).toHaveCount(3);
      await linedUp(page);
      const list = await box(heading(page));
      const server = await box(heading(page).locator("[data-col=server]"));
      expect(server.width).toBeGreaterThan(list.width * 0.45);
      // "No address" ends a column's gap from the buttons.
      for (const name of long) {
        const row = rows(page).filter({ hasText: name });
        await expect(row.getByTitle(name, { exact: true })).toHaveText(name);
        expect(await lines(row.getByTitle(name, { exact: true }))).toBe(1);
        const none = await box(row.getByText("No address"));
        const actions = await box(row.locator("[data-col=actions] button"));
        expect(actions.x - (none.x + none.width)).toBeLessThanOrEqual(17);
      }
    });

    test("a long address is cut short, and shown whole when pointed at or tabbed to", async ({ page, as, org }) => {
      const slug = await org(["far", "near"]);
      const far = "survival-world-for-the-whole-club.pc.tail1cd093.ts.net";
      await page.route(`**/api/v1/orgs/${slug}/servers`, async (route) => {
        const servers = await (await route.fetch()).json();
        for (const s of servers) s.status.address = s.name === "far" ? far : "lobby.2cml.org";
        await route.fulfill({ json: servers });
      });
      await as("owner");
      await page.goto(`/o/${slug}`);
      await expect(rows(page)).toHaveCount(2);
      await linedUp(page);
      const column = await box(heading(page).locator("[data-col=address]"));
      const rem = await page.evaluate(() => parseFloat(getComputedStyle(document.documentElement).fontSize));
      expect(column.width).toBeLessThanOrEqual(18 * rem + 1);
      const address = page.getByRole("button", { name: far });
      expect(await whole(address.locator("span").first())).toBe(false);
      await address.hover();
      await expect(page.getByRole("tooltip")).toHaveText(far);
      await page.mouse.move(0, 0);
      await expect(page.getByRole("tooltip")).toBeHidden();
      // One that fits has nothing more to show.
      const near = page.getByRole("button", { name: "lobby.2cml.org" });
      await near.hover();
      await expect(near).toHaveAttribute("title", "Copy");
      await page.waitForTimeout(500);
      await expect(page.getByRole("tooltip")).toBeHidden();
      await page.mouse.move(0, 0);
      await expect(page.getByRole("tooltip")).toBeHidden();
      // From the server's link, the next stop is its address.
      await rows(page).filter({ hasText: "far" }).getByRole("link").focus();
      await page.keyboard.press("Tab");
      await expect(address).toBeFocused();
      await expect(page.getByRole("tooltip")).toHaveText(far);
    });
  });
}

test("just wide enough for columns, every row fits, and its buttons are whole", async ({ page, as }) => {
  await page.setViewportSize({ width: 800, height: 900 });
  await as("owner");
  await page.goto("/o/friends");
  await expect(rows(page)).toHaveCount(4);
  await linedUp(page);
  const list = await box(heading(page));
  for (const row of await rows(page).all()) {
    const actions = await box(row.locator("[data-col=actions]"));
    expect(actions.x + actions.width).toBeLessThanOrEqual(list.x + list.width);
    for (const button of await row.locator("[data-col=actions] button").all()) expect(await whole(button)).toBe(true);
  }
});

test("on a phone a row is its name and its buttons", async ({ page, as }) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await as("owner");
  await page.goto("/o/club");
  const harpy = rows(page).filter({ hasText: long[0] });
  await expect(harpy.locator("[data-col=software]")).toBeHidden();
  await expect(harpy.locator("[data-col=address]")).toBeHidden();
  const name = await box(harpy.locator("[data-col=server]"));
  const actions = await box(harpy.locator("[data-col=actions]"));
  expect(actions.x).toBeGreaterThan(name.x + name.width - 1);
  expect(Math.abs(actions.y + actions.height / 2 - (name.y + name.height / 2))).toBeLessThan(2);
});
