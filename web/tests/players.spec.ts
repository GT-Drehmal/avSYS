import type { Page } from "@playwright/test";
import { content, expect, test } from "./panel";

// Saved lists of players: made on the Players page, pasted into many at
// once, and picked for a server's whitelist and operators wherever a server
// is made or set up. The panel looks each name up with Mojang, answered here
// from tests/upstream: Herobrine has no account.

type List = { id: string; name: string; players: { name: string; uuid: string }[]; servers: { name: string }[] };

/** The players a list shows, by name. */
const players = (page: Page) =>
  expect.poll(() =>
    content(page)
      .getByRole("list", { name: "Players" })
      .getByRole("listitem")
      .evaluateAll((items) => items.map((i) => i.getAttribute("aria-label"))),
  );

test("a list is made with many players pasted at once, searched, and changed", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}`);
  await page.getByRole("link", { name: "Players" }).click();
  await expect(page.getByText("No lists yet")).toBeVisible();

  await page.getByRole("button", { name: "New list" }).click();
  const dialog = page.getByRole("dialog", { name: "New list" });
  await dialog.getByLabel("Name").fill("Club members");
  await dialog.getByLabel("Players").fill("alex, Notch\njeb_  Herobrine\nnotch");
  await dialog.getByRole("button", { name: "Make list" }).click();

  // Opened, with each player as their account has them, once, and the name nobody has said.
  await expect(page.getByRole("heading", { name: "Club members" })).toBeVisible();
  await players(page).toEqual(["Alex", "jeb_", "Notch"]);
  await expect(page.getByText("No Minecraft account: Herobrine")).toBeVisible();
  await expect(page.getByText("3 players · Not used by any server")).toBeVisible();

  await page.getByRole("searchbox", { name: "Search players" }).fill("no");
  await players(page).toEqual(["Notch"]);
  await page.getByRole("searchbox", { name: "Search players" }).fill("");

  await page.getByRole("button", { name: "Remove jeb_" }).click();
  await players(page).toEqual(["Alex", "Notch"]);
  await page.getByLabel("Add players").fill("Dinnerbone\nGrumm");
  await page.getByRole("button", { name: "Add", exact: true }).click();
  await players(page).toEqual(["Alex", "Dinnerbone", "Grumm", "Notch"]);
  await expect(page.getByLabel("Add players")).toHaveValue("");
  await expect(page.getByText(/No Minecraft account/)).toHaveCount(0);

  const [list] = await api("owner").get<List[]>(`/orgs/${slug}/player-lists`);
  expect(list.players.map((p) => p.name)).toEqual(["Alex", "Dinnerbone", "Grumm", "Notch"]);

  // Listed, and found by a player on it.
  await page.getByRole("link", { name: "Players", exact: true }).first().click();
  const row = page.getByRole("listitem", { name: "Club members" });
  await expect(row).toContainText("4 players · Not used by any server");
  await page.getByRole("searchbox", { name: "Search lists and players" }).fill("dinner");
  await expect(row).toBeVisible();
  await page.getByRole("searchbox", { name: "Search lists and players" }).fill("herobrine");
  await expect(page.getByText("No lists match.")).toBeVisible();
});

test("everyone sees saved lists; only admins change them", async ({ page, as, api }) => {
  const name = `Viewers can see ${Date.now()}-${Math.random().toString(36).slice(2, 6)}`;
  const list = await api("owner").post<List>("/orgs/friends/player-lists", { name, names: ["Alex"] });
  await as("viewer");
  await page.goto("/o/friends/players");
  await expect(page.getByRole("button", { name: "New list" })).toHaveCount(0);
  await page.getByRole("listitem", { name }).click();
  await expect(page).toHaveURL(new RegExp(`/players/${list.id}$`));
  await players(page).toEqual(["Alex"]);
  await expect(page.getByLabel("Add players")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Remove Alex" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: `Actions for ${name}` })).toHaveCount(0);
});

test("a list is renamed, and deleting it says how many servers use it and takes it off them", async ({ page, as, org, api }) => {
  const slug = await org(["one", "two"]);
  const owner = api("owner");
  const list = await owner.post<List>(`/orgs/${slug}/player-lists`, { name: "Bingo", names: ["Alex"] });
  await owner.patch(`/orgs/${slug}/servers/one`, { spec: { whitelistLists: [list.id] } });
  await owner.patch(`/orgs/${slug}/servers/two`, { spec: { opLists: [list.id] } });
  await as("owner");
  await page.goto(`/o/${slug}/players/${list.id}`);
  await expect(page.getByText("1 player · Used by 2 servers")).toBeVisible();
  await expect(content(page)).toContainText("On one (whitelist), two (operators)");

  await page.getByRole("button", { name: "Actions for Bingo" }).click();
  await page.getByRole("menuitem", { name: "Rename" }).click();
  await page.getByRole("dialog", { name: "Rename list" }).getByLabel("Name").fill("Bingo night");
  await page.getByRole("dialog", { name: "Rename list" }).getByRole("button", { name: "Rename" }).click();
  await expect(page.getByRole("heading", { name: "Bingo night" })).toBeVisible();

  await page.getByRole("button", { name: "Actions for Bingo night" }).click();
  await page.getByRole("menuitem", { name: "Delete" }).click();
  const confirm = page.getByRole("dialog", { name: "Delete Bingo night?" });
  await expect(confirm).toContainText("Used by 2 servers.");
  await confirm.getByRole("button", { name: "Delete list" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/players$`));
  await expect(page.getByText("No lists yet")).toBeVisible();
  for (const server of ["one", "two"]) {
    const { spec } = await owner.get(`/orgs/${slug}/servers/${server}`);
    expect(spec.whitelistLists ?? []).toEqual([]);
    expect(spec.opLists ?? []).toEqual([]);
  }
});

test("two saved lists are picked for the whitelist of a new server", async ({ page, as, org, api }) => {
  const slug = await org([]);
  const owner = api("owner");
  const club = await owner.post<List>(`/orgs/${slug}/player-lists`, { name: "Club", names: ["Alex", "Notch"] });
  const event = await owner.post<List>(`/orgs/${slug}/player-lists`, { name: "Event", names: ["Notch", "jeb_"] });
  await owner.post(`/orgs/${slug}/player-lists`, { name: "Staff", names: ["Dinnerbone"] });
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByLabel("Display name").fill("Bingo");
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");

  const whitelist = page.getByRole("list", { name: "Saved lists for whitelist" });
  await expect(whitelist.getByRole("listitem")).toHaveText([/Club\s*2 players/, /Event\s*2 players/, /Staff\s*1 player/]);
  await whitelist.getByRole("checkbox", { name: "Club on the whitelist" }).check();
  await whitelist.getByRole("checkbox", { name: "Event on the whitelist" }).check();
  // Notch is on both, and counted once.
  await expect(page.getByText("2 lists chosen · 3 players")).toBeVisible();
  await page.getByRole("list", { name: "Saved lists for operators" }).getByRole("checkbox", { name: "Staff on the operators" }).check();
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/bingo$`));

  const { spec } = await owner.get(`/orgs/${slug}/servers/bingo`);
  expect([...spec.whitelistLists].sort()).toEqual([club.id, event.id].sort());
  expect(spec.opLists).toHaveLength(1);
  const lists = await owner.get<List[]>(`/orgs/${slug}/player-lists`);
  expect(lists.find((l) => l.id === club.id)?.servers.map((s) => s.name)).toEqual(["bingo"]);
});

test("a template's saved lists fill in a server made from it, and more can be picked", async ({ page, as, org, api }) => {
  const slug = await org([]);
  const owner = api("owner");
  const club = await owner.post<List>(`/orgs/${slug}/player-lists`, { name: "Club", names: ["Alex"] });
  const event = await owner.post<List>(`/orgs/${slug}/player-lists`, { name: "Event", names: ["jeb_"] });
  const template = await owner.post<{ id: string }>(`/orgs/${slug}/templates`, {
    name: "Event night",
    spec: { loader: "paper", version: "1.21.4", resources: { memory: "2Gi" }, storage: { size: "2Gi" }, properties: { "white-list": "true" }, whitelistLists: [club.id] },
  });
  await as("owner");
  await page.goto(`/o/${slug}/new?from=template:${template.id}`);
  const whitelist = page.getByRole("list", { name: "Saved lists for whitelist" });
  await expect(whitelist.getByRole("checkbox", { name: "Club on the whitelist" })).toBeChecked();
  await expect(whitelist.getByRole("checkbox", { name: "Event on the whitelist" })).not.toBeChecked();
  await whitelist.getByRole("checkbox", { name: "Event on the whitelist" }).check();
  await page.getByLabel("Display name").fill("Night one");
  await page.getByLabel("Short name").fill("night-one");
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/night-one$`));
  const { spec } = await owner.get(`/orgs/${slug}/servers/night-one`);
  expect(spec.whitelistLists).toEqual([club.id, event.id]);

  // And a template made in the form keeps the lists picked.
  await page.goto(`/o/${slug}/templates/new?from=template:${template.id}`);
  await expect(page.getByRole("list", { name: "Saved lists for whitelist" }).getByRole("checkbox", { name: "Club on the whitelist" })).toBeChecked();
});

test("a server's Settings pick saved lists, and save its whitelist as a list", async ({ page, as, org, api }) => {
  const slug = await org(["one"]);
  const owner = api("owner");
  const club = await owner.post<List>(`/orgs/${slug}/player-lists`, { name: "Club", names: ["Alex"] });
  await owner.patch(`/orgs/${slug}/servers/one`, { spec: { whitelist: ["Notch"] } });
  await as("owner");
  await page.goto(`/o/${slug}/s/one/settings`);
  await page.getByRole("list", { name: "Saved lists for whitelist" }).getByRole("checkbox", { name: "Club on the whitelist" }).check();
  await page.getByRole("list", { name: "Saved lists for operators" }).getByRole("checkbox", { name: "Club on the operators" }).check();
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByText("Unsaved changes")).toHaveCount(0);
  const { spec } = await owner.get(`/orgs/${slug}/servers/one`);
  expect(spec).toMatchObject({ whitelistLists: [club.id], opLists: [club.id], whitelist: ["Notch"] });

  // Taken off again: none is no field at all.
  await page.getByRole("list", { name: "Saved lists for operators" }).getByRole("checkbox", { name: "Club on the operators" }).uncheck();
  await page.getByRole("button", { name: "Save changes" }).click();
  await expect(page.getByText("Unsaved changes")).toHaveCount(0);
  expect((await owner.get(`/orgs/${slug}/servers/one`)).spec.opLists).toBeUndefined();

  // Who is on its whitelist now, as a list of its own: the names it has,
  // and the list's.
  await page.getByRole("button", { name: "Save the whitelist as a list" }).click();
  const dialog = page.getByRole("dialog", { name: "Save the whitelist as a list" });
  await dialog.getByLabel("List name").fill("From one");
  await dialog.getByRole("button", { name: "Save list" }).click();
  await expect(page.getByRole("heading", { name: "From one" })).toBeVisible();
  await players(page).toEqual(["Alex", "Notch"]);
});

test("with no saved lists, the form says where to make one", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByText("No saved lists yet.").first().getByRole("link", { name: "Make one under Players" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/players$`));
});

// Bedrock players are added by Xbox gamertag, looked up with GeyserMC
// (tests/upstream: Tim203 and Redned235 are known to it, nobody else).

test("Bedrock players are added by gamertag and shown as Bedrock", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/players`);
  await page.getByRole("button", { name: "New list" }).click();
  const dialog = page.getByRole("dialog", { name: "New list" });
  await dialog.getByLabel("Name").fill("Event");
  await dialog.getByRole("group", { name: "Edition" }).getByRole("button", { name: "Bedrock" }).click();
  await expect(dialog.getByText("Xbox gamertags, one per line or separated by commas.")).toBeVisible();
  // A gamertag can have a space: only lines and commas part them.
  await dialog.getByLabel("Players").fill("tim203\nNobody Here");
  await dialog.getByRole("button", { name: "Make list" }).click();

  await expect(page.getByRole("heading", { name: "Event" })).toBeVisible();
  await players(page).toEqual(["Tim203"]);
  await expect(page.getByRole("listitem", { name: "Tim203" }).getByText("Bedrock", { exact: true })).toBeVisible();
  await expect(page.getByText("No Bedrock player found: Nobody Here.")).toBeVisible();

  // Java and Bedrock players on one list, each as they are.
  const edition = page.getByRole("group", { name: "Edition" });
  await expect(edition.getByRole("button", { name: "Java" })).toHaveAttribute("aria-pressed", "true");
  await page.getByLabel("Add players").fill("alex");
  await page.getByRole("button", { name: "Add", exact: true }).click();
  await players(page).toEqual(["Alex", "Tim203"]);
  await expect(page.getByRole("listitem", { name: "Alex" }).getByText("Bedrock", { exact: true })).toHaveCount(0);
  await edition.getByRole("button", { name: "Bedrock" }).click();
  await page.getByLabel("Add players").fill("Redned235, Nobody Else");
  await page.getByRole("button", { name: "Add", exact: true }).click();
  await players(page).toEqual(["Alex", "Redned235", "Tim203"]);
  await expect(page.getByLabel("Add players")).toHaveValue("Nobody Else");
  await expect(page.getByText("No Bedrock player found: Nobody Else.")).toBeVisible();

  const [list] = await api("owner").get<List[]>(`/orgs/${slug}/player-lists`);
  expect(list.players).toEqual([
    { name: "Alex", uuid: expect.any(String), kind: "java" },
    { name: "Redned235", uuid: "0000000000000000000901f08ec7c850", kind: "bedrock", xuid: "2535407489632336" },
    { name: "Tim203", uuid: "0000000000000000000901f64f65c7c3", kind: "bedrock", xuid: "2535432196048835" },
  ]);
});

test("a server without Geyser and Floodgate says its Bedrock players can't join", async ({ page, as, org, api }) => {
  const slug = await org(["one"]);
  const owner = api("owner");
  await owner.post<List>(`/orgs/${slug}/player-lists`, { name: "Club", names: ["Alex"] });
  await owner.post<List>(`/orgs/${slug}/player-lists`, { name: "Event", bedrock: ["Tim203"] });
  await as("owner");
  await page.goto(`/o/${slug}/s/one/settings`);
  const warning = page.getByText("Bedrock players can't join this server until Geyser and Floodgate are added.");
  const whitelist = page.getByRole("list", { name: "Saved lists for whitelist" });
  await whitelist.getByRole("checkbox", { name: "Club on the whitelist" }).check();
  await expect(whitelist.getByRole("checkbox", { name: "Club on the whitelist" })).toBeChecked();
  await expect(warning).toHaveCount(0);
  await whitelist.getByRole("checkbox", { name: "Event on the whitelist" }).check();
  await expect(warning).toBeVisible();
  await expect(page.getByRole("link", { name: "Go to Content" })).toHaveAttribute("href", `/o/${slug}/s/one/content`);

  // With Geyser and Floodgate on it, nothing is said.
  await page.route(`**/api/v1/orgs/${slug}/servers`, async (route) => {
    const servers = (await (await route.fetch()).json()) as { name: string; spec: object }[];
    const mods = [
      { source: "modrinth", kind: "plugin", project: "geyser", title: "Geyser" },
      { source: "url", kind: "plugin", project: "https://download.geysermc.org/v2/projects/floodgate/versions/latest/builds/latest/downloads/spigot", title: "spigot" },
    ];
    return route.fulfill({ json: servers.map((s) => (s.name === "one" ? { ...s, spec: { ...s.spec, mods } } : s)) });
  });
  await page.reload();
  const ops = page.getByRole("list", { name: "Saved lists for operators" });
  await ops.getByRole("checkbox", { name: "Event on the operators" }).check();
  await expect(ops.getByRole("checkbox", { name: "Event on the operators" })).toBeChecked();
  await expect(warning).toHaveCount(0);
});
