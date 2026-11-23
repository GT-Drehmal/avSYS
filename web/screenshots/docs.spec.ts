import type { Page } from "@playwright/test";
import { readdirSync, readFileSync } from "node:fs";
import { join } from "node:path";
import { expect, test } from "../tests/panel";

// Pictures of the panel for the documentation, written to docs/images.
// The test panel's seeded organization "friends" is what they show.

const shot = async (page: Page, name: string) => {
  // Nothing still moving: no caret, no spinner half-way round.
  await page.waitForLoadState("networkidle").catch(() => {});
  // Every player's head on the page drawn, not still on its way.
  await expect
    .poll(() => page.locator('img[src*="/players/"]').evaluateAll((imgs) => imgs.every((i) => (i as HTMLImageElement).complete && (i as HTMLImageElement).naturalWidth > 0)))
    .toBe(true);
  await page.screenshot({ path: `../docs/images/${name}.png`, animations: "disabled", caret: "hide" });
};

// What the test panel's stand-in for Modrinth answers is written for tests;
// these are written for people.
const packs: Record<string, [author: string, description: string]> = {
  "Adventure Pack": ["Wanderers", "Dungeons, new places and quests to take on with friends."],
  "Tech Pack": ["Gearworks", "Machines, power and factories, from the first gear up."],
  "Cozy Pack": ["Hearth", "Farming, cooking and building, at an easy pace."],
};
const plugins: [title: string, author: string, description: string, downloads: number][] = [
  ["LuckPerms", "Luck", "A permissions plugin for Minecraft servers.", 3_800_000],
  ["EssentialsX", "EssentialsX", "Homes, warps, kits and the commands every server needs.", 3_100_000],
  ["WorldEdit", "EngineHub", "An in-game map editor for building and fixing large areas.", 2_900_000],
  ["CoreProtect", "Intelli", "Logs every block change, and rolls back griefing.", 2_400_000],
  ["Chunky", "pop4959", "Pre-generates chunks, quickly and efficiently.", 2_100_000],
  ["spark", "lucko", "A performance profiler for servers and clients.", 1_900_000],
  ["Vault", "MilkBowl", "The bridge between economy and permissions plugins.", 1_700_000],
  ["BlueMap", "Blue", "A 3D map of your world, in the browser.", 1_200_000],
];

// The test panel cannot reach Mojang for players' skins, so their heads are
// faces of our own (screenshots/heads, 8 pixels square as the panel makes
// them): each player one of their own, in the order first asked for.
const heads = join("screenshots", "heads");
const faceFiles = readdirSync(heads).sort();
const faces = new Map<string, Buffer>();
const faceOf = (uuid: string) => {
  if (!faces.has(uuid)) faces.set(uuid, readFileSync(join(heads, faceFiles[faces.size % faceFiles.length])));
  return faces.get(uuid) as Buffer;
};

/** Waits for this many players' heads to be drawn. */
const expectFaces = (page: Page, count: number) =>
  expect
    .poll(() => page.locator('main img[src*="/players/"]').evaluateAll((imgs) => imgs.filter((i) => (i as HTMLImageElement).naturalWidth > 0).length))
    .toBe(count);

test.beforeEach(async ({ as, page }) => {
  await as("owner");
  await page.route("**/api/v1/players/*/head?*", (route) =>
    route.fulfill({ body: faceOf(new URL(route.request().url()).pathname.split("/").at(-2) ?? ""), contentType: "image/png" }),
  );
  await page.route("**/api/v1/catalog/modrinth/search?*", async (route) => {
    const res = await (await route.fetch()).json();
    if (/plugin/.test(decodeURIComponent(new URL(route.request().url()).search))) {
      const like = res.hits[0];
      res.hits = plugins.map(([title, author, description, downloads], i) => ({
        ...like,
        project_id: `docs${i}`,
        slug: title.toLowerCase(),
        title,
        author,
        description,
        downloads,
        project_type: "plugin",
      }));
      res.total_hits = plugins.length;
    }
    for (const hit of res.hits) {
      const pack = packs[hit.title];
      if (pack) [hit.author, hit.description] = pack;
      hit.downloads = Math.abs(hit.downloads);
      hit.follows = Math.abs(hit.follows ?? 0);
    }
    await route.fulfill({ json: res });
  });
});

test("servers", async ({ page }) => {
  await page.goto("/o/friends");
  await expect(page.getByRole("listitem").filter({ hasText: "Lobby" })).toContainText("Asleep");
  await shot(page, "servers");
});

test("new server", async ({ page }) => {
  await page.goto("/o/friends/new");
  await expect(page.getByRole("heading", { name: "New server" })).toBeVisible();
  await shot(page, "new-server");
});

test("modpack", async ({ page }) => {
  await page.goto("/o/friends/new");
  await page.getByRole("button", { name: "Modpack" }).first().click();
  await expect(page.getByText("Adventure Pack").first()).toBeVisible();
  await shot(page, "modpack");
});

// Nothing runs on the test panel, so the console and the files are what a
// running Paper server would show.
const log = [
  "[18:02:11 INFO]: Starting minecraft server version 1.21.4",
  "[18:02:11 INFO]: Loading properties",
  "[18:02:11 INFO]: This server is running Paper version 1.21.4-232 (MC: 1.21.4)",
  "[18:02:12 INFO]: Using 4 threads for Netty based IO",
  "[18:02:14 INFO]: [LuckPerms] Loading server plugin LuckPerms v5.5.0",
  "[18:02:15 INFO]: Preparing level \"world\"",
  "[18:02:17 INFO]: Preparing spawn area: 100%",
  "[18:02:18 INFO]: Done (6.912s)! For help, type \"help\"",
  "[18:03:41 INFO]: Steve joined the game",
  "[18:05:12 INFO]: Alex joined the game",
  "[18:05:30 INFO]: <Alex> anyone up for the nether?",
  "[18:05:44 INFO]: <Steve> sure, meet at spawn",
];

const properties = `# Minecraft server properties
allow-flight=false
difficulty=normal
enable-command-block=false
gamemode=survival
hardcore=false
max-players=20
motd=Friends' survival world
online-mode=true
pvp=true
simulation-distance=10
spawn-protection=0
view-distance=12
white-list=true
`;

test("a server's page", async ({ page }) => {
  await page.route("**/api/v1/orgs/friends/servers/survival/console", (route) =>
    route.fulfill({
      headers: { "Content-Type": "text/event-stream" },
      body: `event: reset\ndata: null\n\nevent: lines\ndata: ${JSON.stringify(log)}\n\n`,
    }),
  );
  await page.goto("/o/friends/s/survival");
  await expect(page.getByText("meet at spawn")).toBeVisible();
  await shot(page, "server");
});

test("completing a command", async ({ page }) => {
  await page.route("**/api/v1/orgs/friends/servers/survival/console", (route) =>
    route.fulfill({
      headers: { "Content-Type": "text/event-stream" },
      body: `event: reset\ndata: null\n\nevent: lines\ndata: ${JSON.stringify(log)}\n\n`,
    }),
  );
  await page.goto("/o/friends/s/survival");
  await expect(page.getByText("meet at spawn")).toBeVisible();
  const line = page.getByRole("combobox", { name: "Server command" });
  // The panel makes the game's commands the first time they are asked for.
  await expect(async () => {
    await line.fill("");
    await line.pressSequentially("execute as @a at @s run ti");
    await expect(page.getByRole("option", { name: "time" })).toBeVisible({ timeout: 1000 });
  }).toPass({ timeout: 15_000 });
  await line.press("ArrowDown");
  await expect(page.getByRole("option", { name: "time", selected: true })).toBeVisible();
  await shot(page, "console-completion");
});

test("files", async ({ page }) => {
  const tree: Record<string, string[]> = {
    "/": ["config/", "logs/", "plugins/", "world/", "banned-players.json", "eula.txt", "ops.json", "server.properties", "whitelist.json"],
    "/plugins": ["LuckPerms/", "LuckPerms-Bukkit-5.5.0.jar"],
  };
  await page.route("**/api/v1/orgs/friends/servers/survival/files?*", (route) => {
    const dir = new URL(route.request().url()).searchParams.get("path") ?? "/";
    const entries = (tree[dir] ?? []).map((n) => ({
      name: n.replace(/\/$/, ""),
      dir: n.endsWith("/"),
      size: n.endsWith(".jar") ? 1_400_000 : 640,
      modTime: new Date().toISOString(),
    }));
    return route.fulfill({ json: { path: dir, entries } });
  });
  await page.route("**/api/v1/orgs/friends/servers/survival/files/content?*", (route) =>
    route.fulfill({ body: properties, headers: { "Content-Type": "application/octet-stream", ETag: '"1"' } }),
  );
  await page.goto("/o/friends/s/survival/files?file=/server.properties");
  await expect(page.getByText("white-list=true")).toBeVisible();
  await shot(page, "files");
});

test("content", async ({ page }) => {
  await page.goto("/o/friends/s/survival/content");
  await page.getByRole("button", { name: "Add" }).first().click();
  await expect(page.getByRole("dialog").getByText("EssentialsX", { exact: true })).toBeVisible();
  await shot(page, "content-add");
});

test("content found in a server's folders", async ({ page }) => {
  // Drehmal Bingo, moved in with its mods as files (cmd/avsys-uitest/loose.go).
  await page.goto("/o/club/s/bingo/content");
  await expect(page.getByRole("region", { name: "Mods", exact: true }).getByText("Yet Another Bingo", { exact: true })).toBeVisible();
  await expect
    .poll(() => page.locator('main img[src*="/content/icon"]').evaluateAll((imgs) => imgs.every((i) => (i as HTMLImageElement).naturalWidth > 0) && imgs.length > 0))
    .toBe(true);
  await shot(page, "content-found");
});

test("players", async ({ page, api }) => {
  const list = await api("owner").post<{ id: string; players: { uuid: string; name: string }[] }>("/orgs/friends/player-lists", {
    name: "Regulars",
    names: ["Notch", "jeb_", "Alex", "Dinnerbone", "Grumm"],
    bedrock: ["Tim203", "Redned235"],
  });
  // Faces in the order the list shows them.
  for (const p of [...list.players].sort((a, b) => a.name.localeCompare(b.name))) faceOf(p.uuid);
  await page.goto(`/o/friends/players/${list.id}`);
  await expect(page.getByRole("listitem", { name: "Tim203" })).toBeVisible();
  await expectFaces(page, list.players.length);
  await shot(page, "players");
});

test("backups", async ({ page }) => {
  await page.goto("/o/friends/backups");
  await expect(page.getByRole("heading", { name: "Backups" })).toBeVisible();
  await shot(page, "backups");
});

test("recently deleted", async ({ page }) => {
  // A snapshot deleted this morning, kept in Recently deleted: answered
  // here, as deleting one in the test panel would change it for the tests.
  const hour = 60 * 60 * 1000;
  const deletedAt = new Date(Date.now() - 5 * hour);
  await page.route("**/api/v1/orgs/friends/bin", (route) =>
    route.fulfill({
      json: {
        available: true,
        days: 7,
        canEmpty: true,
        snapshots: [
          {
            id: "7f3e9a1c".repeat(8),
            time: new Date(Date.now() - 30 * hour).toISOString(),
            server: "creative",
            trigger: "manual",
            by: "admin",
            version: "1.21.4",
            loader: "fabric",
            size: 350 << 20,
            added: 6 << 20,
            files: 625,
            deletedAt: deletedAt.toISOString(),
            deletedBy: "admin",
            until: new Date(deletedAt.getTime() + 7 * 24 * hour).toISOString(),
          },
        ],
      },
    }),
  );
  await page.goto("/o/friends/backups");
  await expect(page.getByRole("heading", { name: "Backups" })).toBeVisible();
  const bin = page.getByRole("region", { name: "Recently deleted" });
  await bin.getByRole("button", { name: /Recently deleted/ }).click();
  await expect(bin.getByRole("listitem")).toHaveCount(1);
  await page.waitForLoadState("networkidle").catch(() => {});
  // The part of the page it is, not the page around it.
  await bin.screenshot({ path: "../docs/images/recently-deleted.png", animations: "disabled", caret: "hide" });
});

test("sign-in", async ({ page, context }) => {
  // Signed out, on a panel that offers every kind of sign-in.
  await context.setExtraHTTPHeaders({});
  await page.route("**/api/v1/meta", async (route) => {
    const meta = await (await route.fetch()).json();
    meta.providers = [
      { id: "github", name: "GitHub" },
      { id: "discord", name: "Discord" },
      { id: "google", name: "Google" },
    ];
    await route.fulfill({ json: meta });
  });
  await page.goto("/login");
  await expect(page.getByText("Continue with GitHub")).toBeVisible();
  await shot(page, "sign-in");
});
