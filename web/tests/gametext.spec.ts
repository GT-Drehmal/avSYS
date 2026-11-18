import { shownColours } from "../src/lib/mctext";
import { expect, test } from "./panel";

// What the game writes with its § codes is shown in its colours, without
// the codes: here, its log. The world's own name is in worldorigin.spec.ts.

const rgb = (hex: string) => `rgb(${[1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16)).join(", ")})`;

test("a log's lines are shown in the game's colours, the level's tone kept for the rest", async ({ page, as }) => {
  await as("viewer");
  const base = "**/api/v1/orgs/friends/servers/survival/logs";
  await page.route(base, (route) =>
    route.fulfill({
      json: {
        logs: [
          { name: "logs/latest.log", kind: "current", size: 2048, modTime: new Date().toISOString() },
          { name: "logs/2026-10-14-1.log.gz", kind: "log", size: 1800, modTime: "2026-10-14T01:00:00Z" },
        ],
      },
    }),
  );
  const all = [
    "[01:00:00] [Server thread/INFO]: [Server] §aWelcome §lhome§r, all",
    "[01:00:01] [Server thread/WARN]: §cLow on memory",
    "§6A line of its own",
    "\u001b[33;1m[01:00:02] [Server thread/INFO]: Plain\u001b[m",
  ];
  await page.route(`${base}/file*`, (route) =>
    route.fulfill({ json: { name: "logs/2026-10-14-1.log.gz", total: all.length, truncated: false, firstError: 0, lines: all.map((text, i) => ({ n: i + 1, text })) } }),
  );
  await page.goto("/o/friends/s/survival");
  await page.getByLabel("Log", { exact: true }).selectOption("logs/2026-10-14-1.log.gz");
  const line = (n: number) => page.locator(`[data-n="${n}"]`);
  await expect(line(1)).toContainText("[Server] Welcome home, all");
  await expect(page.getByText("§")).toHaveCount(0);
  await expect(line(1).getByText("Welcome", { exact: true })).toHaveCSS("color", rgb(shownColours.dark.a));
  const home = line(1).getByText("home", { exact: true });
  await expect(home).toHaveCSS("color", rgb(shownColours.dark.a));
  await expect(home).toHaveCSS("font-weight", "700");
  await expect(line(2).getByText("Low on memory")).toHaveCSS("color", rgb(shownColours.dark.c));
  await expect(line(3)).toHaveText("3A line of its own");
  await expect(line(3).getByText("A line of its own")).toHaveCSS("color", rgb(shownColours.dark["6"]));
  // The terminal's own codes are still taken out.
  await expect(line(4)).toContainText("[01:00:02] [Server thread/INFO]: Plain");
  await expect(line(4)).not.toContainText("[33;1m");
});
