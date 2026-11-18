import type { Page } from "@playwright/test";
import { shownColours } from "../src/lib/mctext";
import { levelDat, zip } from "./archives";
import { content, expect, test } from "./panel";

// A server's Settings say what its world is: where it came from, as the
// panel put it in, and what the world says of itself, read from its files.
// One from the library that is still there can be put back.

const png = Buffer.from("iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mP8z8BQDwAEhQGAhKmMIQAAAABJRU5ErkJggg==", "base64");

const libraryWorld = (id: string, name: string, version = "1.21.4") => ({
  id,
  name,
  description: "",
  createdBy: "owner",
  createdAt: new Date(Date.now() - 86_400_000).toISOString(),
  updatedAt: new Date().toISOString(),
  root: "world",
  version,
  size: 12 << 20,
  source: "upload",
  ready: true,
});

/** The library as the panel would have it, each world with a picture. */
async function library(page: Page, org: string, worlds: ReturnType<typeof libraryWorld>[]) {
  await page.route(new RegExp(`/api/v1/orgs/${org}/worlds(/.*)?(\\?.*)?$`), (route) => {
    const path = new URL(route.request().url()).pathname;
    if (path.endsWith("/icon")) return route.fulfill({ status: 200, contentType: "image/png", body: png });
    return route.fulfill({ json: worlds });
  });
}

type Shown = { origin?: Record<string, unknown> | null; facts?: Record<string, unknown> | null; phase?: string };

/**
 * A stopped server whose world came from where shown says, and says of
 * itself what it says: its files cannot be reached where it says nothing.
 * Changed as the test goes on.
 */
async function server(page: Page, org: string, name: string, shown: Shown) {
  const as = (s: { name: string; spec: object; status: object }) =>
    s.name === name ? { ...s, spec: { ...s.spec, worldOrigin: shown.origin ?? null }, status: { ...s.status, phase: shown.phase ?? "Stopped" } } : s;
  await page.route(`**/api/v1/orgs/${org}/servers`, async (route) =>
    route.request().method() === "GET" ? route.fulfill({ json: (await (await route.fetch()).json()).map(as) }) : route.fallback(),
  );
  await page.route(`**/api/v1/orgs/${org}/servers/${name}`, async (route) =>
    route.request().method() === "GET" ? route.fulfill({ json: as(await (await route.fetch()).json()) }) : route.fallback(),
  );
  await page.route(`**/api/v1/orgs/${org}/servers/${name}/world`, (route) =>
    shown.facts ? route.fulfill({ json: shown.facts }) : route.fulfill({ status: 502, json: { code: "workspace", message: "The server's files are not reachable right now." } }),
  );
  // The panel's own word of it, which knows nothing of this, is not heard.
  await page.route("**/api/v1/events?*", (route) => route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body: "event: waiting\ndata: null\n\n" }));
}

const hourAgo = () => new Date(Date.now() - 3_600_000).toISOString();

/** A colour as the browser gives it back: "rgb(…)". */
const rgb = (hex: string) => `rgb(${[1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16)).join(", ")})`;

test.beforeEach(async ({ as }) => {
  await as("owner");
});

test("a server made in the panel says its world is new, and nothing more while its files cannot be reached", async ({ page, org }) => {
  const slug = await org(["fresh"]);
  await page.goto(`/o/${slug}/s/fresh/settings`);
  // As the panel itself recorded it.
  await expect(page.getByTestId("world-now")).toContainText("New world");
  await expect(page.getByTestId("world-now")).toContainText(/Put in/);
  await expect(page.getByRole("button", { name: "Replace the world" })).toBeVisible();
});

// Each kind of origin, as its own test: opening Settings once each keeps
// every test well inside its time, WebKit included.
for (const [origin, words, more] of [
  [{ kind: "library", library: { id: "w1", name: "Island" } }, "Island · from the world library", null],
  [{ kind: "library", library: { id: "gone", name: "Old map" } }, "Old map · from the world library", null],
  [{ kind: "upload", file: "my-save.zip" }, "Uploaded · my-save.zip", null],
  [{ kind: "archive", file: "server.tar.gz" }, "Imported · server.tar.gz", null],
  [{ kind: "new", seed: "1234" }, "New world · seed 1234", null],
  [{ kind: "new" }, "New world", null],
  [{ kind: "copy", server: "survival" }, "Copied from survival", null],
  [{ kind: "snapshot", server: "survival", snapshot: "abcd1234" }, "From a snapshot of survival", null],
  [{ kind: "template", template: { id: "t1", name: "Event base" } }, "From the template Event base", null],
  [{ kind: "library", library: { id: "w1", name: "Island" }, server: "survival" }, "Island · from the world library", "Copied from survival"],
  [{ kind: "new", seed: "7", template: { id: "t1", name: "Event base" } }, "New world · seed 7", "From the template Event base"],
] as const) {
  test(`Settings say where the world came from: ${words}${more ? `, ${more}` : ""}`, async ({ page, org }) => {
    const slug = await org(["event"]);
    await library(page, slug, [libraryWorld("w1", "Island")]);
    const shown: Shown = { origin: { ...origin, time: hourAgo() } };
    await server(page, slug, "event", shown);
    const line = page.getByTestId("world-now");
    await page.goto(`/o/${slug}/s/event/settings`);
    await expect(line.locator("p").first()).toHaveText(words);
    await expect(line).toContainText("Put in 1 hour ago");
    if (more) await expect(line).toContainText(more);
  });
}

test("only a library world that is still there has its picture and a link to it", async ({ page, org }) => {
  const slug = await org(["event"]);
  await library(page, slug, [libraryWorld("w1", "Island")]);
  const shown: Shown = {};
  await server(page, slug, "event", shown);
  const line = page.getByTestId("world-now");
  shown.origin = { kind: "library", library: { id: "w1", name: "Island" }, time: hourAgo() };
  await page.goto(`/o/${slug}/s/event/settings`);
  await expect(line.getByRole("link", { name: "Island" })).toHaveAttribute("href", `/o/${slug}/worlds?q=Island`);
  await expect(line.locator("img")).toBeVisible();
  shown.origin = { kind: "library", library: { id: "gone", name: "Old map" }, time: hourAgo() };
  await page.goto(`/o/${slug}/s/event/settings`);
  await expect(line).toContainText("Old map");
  await expect(line.getByRole("link")).toHaveCount(0);
  await expect(page.getByRole("button", { name: /^Put back/ })).toHaveCount(0);
});

test("what the world says of itself shows beside where it came from, or alone", async ({ page, org }) => {
  const slug = await org(["event"]);
  const shown: Shown = { facts: { level: "world", exists: true, name: "Island", seed: "42", version: "1.21.4", lastPlayed: hourAgo(), size: 12 << 20 } };
  await server(page, slug, "event", shown);
  const line = page.getByTestId("world-now");
  // A server from before origins were kept, or whose world was changed by hand.
  await page.goto(`/o/${slug}/s/event/settings`);
  await expect(line.locator("p").first()).toHaveText("Island");
  await expect(line).toContainText("Seed 42 · Last opened in 1.21.4 · Played 1 hour ago · 12 MB");
  // With where it came from: what that says already is not said twice.
  shown.origin = { kind: "new", seed: "42", time: hourAgo() };
  await page.goto(`/o/${slug}/s/event/settings`);
  await expect(line.locator("p").first()).toHaveText("New world · seed 42");
  await expect(line).toContainText('"Island" · Last opened in 1.21.4 · Played 1 hour ago · 12 MB');
  await expect(line).not.toContainText("Seed 42");
  // A world the game has not made yet.
  shown.facts = { level: "world", exists: false };
  await page.goto(`/o/${slug}/s/event/settings`);
  await expect(line).toContainText("Made when the server starts");
  // Files that cannot be reached: where it came from, and nothing else.
  shown.facts = null;
  await page.goto(`/o/${slug}/s/event/settings`);
  await expect(line.locator("p").first()).toHaveText("New world · seed 42");
  await expect(line).not.toContainText("Last opened");
  // Neither: nothing.
  shown.origin = null;
  await page.goto(`/o/${slug}/s/event/settings`);
  await expect(page.getByRole("button", { name: "Replace the world" })).toBeVisible();
  await expect(line).toHaveCount(0);
});

test("a world's own name is shown as the game colours it, in either theme, its codes not shown", async ({ page, org }, info) => {
  const slug = await org(["event"]);
  // As Drehmal's level.dat has it.
  const shown: Shown = { facts: { level: "world", exists: true, name: "§6§lDrehmal： §6§lAPOTHEOSIS", version: "1.21.4", size: 12 << 20 } };
  await server(page, slug, "event", shown);
  const line = page.getByTestId("world-now");
  await page.goto(`/o/${slug}/s/event/settings`);
  const title = line.locator("p").first();
  await expect(title).toHaveText("Drehmal： APOTHEOSIS");
  await expect(title.locator("[data-mc]")).toHaveAttribute("title", "Drehmal： APOTHEOSIS");
  const gold = title.locator("span[style]");
  await expect(gold).toHaveText("Drehmal： APOTHEOSIS");
  await expect(gold).toHaveCSS("font-weight", "700");
  await expect(gold).toHaveCSS("color", rgb(shownColours.dark["6"]));
  await line.screenshot({ path: info.outputPath("world-dark.png") });
  await page.emulateMedia({ colorScheme: "light" });
  await expect(gold).toHaveCSS("color", rgb(shownColours.light["6"]));
  await line.screenshot({ path: info.outputPath("world-light.png") });
  // With where it came from above it, the name is on the line below.
  shown.origin = { kind: "new", time: hourAgo() };
  await page.goto(`/o/${slug}/s/event/settings`);
  await expect(title).toHaveText("New world");
  await expect(line).toContainText('"Drehmal： APOTHEOSIS" · Last opened in 1.21.4 · 12 MB');
  await expect(line.locator("span[style]")).toHaveCSS("color", rgb(shownColours.light["6"]));
  await expect(line).not.toContainText("§");
});

test("a world from the library is put back as it is there, after asking", async ({ page, org }) => {
  const slug = await org(["event"]);
  await library(page, slug, [libraryWorld("w1", "Island"), libraryWorld("w2", "Arena")]);
  const shown: Shown = { origin: { kind: "library", library: { id: "w1", name: "Island" }, time: hourAgo() } };
  await server(page, slug, "event", shown);
  const sent: Record<string, string>[] = [];
  await page.route(`**/api/v1/orgs/${slug}/servers/event/import?*`, async (route) => {
    sent.push(Object.fromEntries(new URL(route.request().url()).searchParams));
    // As the panel does: the same world, put in now.
    shown.origin = { kind: "library", library: { id: "w1", name: "Island" }, time: new Date().toISOString() };
    await route.fulfill({ status: 202, json: await (await page.request.get(`/api/v1/orgs/${slug}/servers/event`)).json() });
  });
  await page.goto(`/o/${slug}/s/event/settings`);
  await page.getByRole("button", { name: "Put back Island" }).click();
  const dialog = page.getByRole("dialog", { name: "Put back Island?" });
  await expect(dialog).toContainText("A snapshot is taken first.");
  // It is that world, and no other.
  await expect(dialog.getByRole("group", { name: "World", exact: true })).toHaveCount(0);
  await expect(dialog.getByRole("button", { name: "Change" })).toHaveCount(0);
  await expect(dialog.getByTestId("world-summary")).toContainText("1.21.4");
  // Called off, nothing is sent.
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toHaveCount(0);
  expect(sent).toEqual([]);
  await page.getByRole("button", { name: "Put back Island" }).click();
  await dialog.getByRole("button", { name: "Put back", exact: true }).click();
  await expect(dialog).toHaveCount(0);
  expect(sent).toEqual([{ world: "true", replace: "true", library: "w1" }]);
  // Still that world, and it can be put back again.
  await expect(page.getByTestId("world-now").locator("p").first()).toHaveText("Island · from the world library");
  await expect(page.getByTestId("world-now")).toContainText(/Put in (now|\d+ seconds? ago)/);
  await expect(page.getByRole("button", { name: "Put back Island" })).toBeVisible();
  // Replacing the world is as it was: any kind, from the start.
  await page.getByRole("button", { name: "Replace the world" }).click();
  const replace = page.getByRole("dialog", { name: "Replace the world?" });
  await expect(replace.getByRole("group", { name: "World", exact: true })).toBeVisible();
  await expect(replace.getByRole("button", { name: "Upload world" })).toHaveAttribute("aria-pressed", "true");
});

test("putting a world back is held to what replacing it is: stopped first, and a newer world asked about", async ({ page, org }) => {
  const slug = await org(["event"]);
  await library(page, slug, [libraryWorld("w1", "Island", "1.21.5")]);
  const shown: Shown = { origin: { kind: "library", library: { id: "w1", name: "Island" }, time: hourAgo() }, phase: "Running" };
  await server(page, slug, "event", shown);
  let sent: Record<string, string> | null = null;
  await page.route(`**/api/v1/orgs/${slug}/servers/event/import?*`, async (route) => {
    sent = Object.fromEntries(new URL(route.request().url()).searchParams);
    await route.fulfill({ status: 202, json: await (await page.request.get(`/api/v1/orgs/${slug}/servers/event`)).json() });
  });
  await page.goto(`/o/${slug}/s/event/settings`);
  await expect(page.getByRole("button", { name: "Put back Island" })).toBeDisabled();
  await expect(page.getByText("Stop the server to replace or delete the world.")).toBeVisible();
  shown.phase = "Stopped";
  await page.goto(`/o/${slug}/s/event/settings`);
  await page.getByRole("button", { name: "Put back Island" }).click();
  const dialog = page.getByRole("dialog", { name: "Put back Island?" });
  await expect(dialog.getByText("The world was last opened in 1.21.5. 1.21.4 cannot open the world, so the server will not start.")).toBeVisible();
  await dialog.getByRole("button", { name: "Put back", exact: true }).click();
  await page.getByRole("dialog", { name: "Open a 1.21.5 world in 1.21.4?" }).getByRole("button", { name: "Put back anyway" }).click();
  await expect(dialog).toHaveCount(0);
  expect(sent).toEqual({ world: "true", replace: "true", library: "w1", newerWorld: "true" });
});

test("a world uploaded to replace another is sent with its name", async ({ page, org }) => {
  const slug = await org(["event"]);
  await server(page, slug, "event", {});
  await page.route(/\/api\/v1\/orgs\/[^/]+\/servers\/[^/]+\/import\/upload/, (route) => {
    const url = new URL(route.request().url());
    if (route.request().method() === "GET") return route.fulfill({ json: { received: 0 } });
    return route.fulfill({ json: { received: Number(url.searchParams.get("offset")) + Number(url.searchParams.get("length")) } });
  });
  let sent: Record<string, string> | null = null;
  await page.route(`**/api/v1/orgs/${slug}/servers/event/import?*`, async (route) => {
    sent = Object.fromEntries(new URL(route.request().url()).searchParams);
    await route.fulfill({ status: 202, json: await (await page.request.get(`/api/v1/orgs/${slug}/servers/event`)).json() });
  });
  await page.goto(`/o/${slug}/s/event/settings`);
  await page.getByRole("button", { name: "Replace the world" }).click();
  const dialog = page.getByRole("dialog", { name: "Replace the world?" });
  await dialog.getByLabel("World archive").setInputFiles({ name: "My Save.zip", mimeType: "application/zip", buffer: zip({ "Save/level.dat": levelDat("1.21.4", "Save") }) });
  await expect(dialog.getByTestId("world-summary")).toContainText("Save");
  await dialog.getByRole("button", { name: "Replace the world" }).click();
  await expect(dialog).toHaveCount(0);
  expect(sent).toMatchObject({ world: "true", replace: "true", from: "upload", file: "My Save.zip" });
});

test("a template's own world from the library says so in its form", async ({ page, org }) => {
  const slug = await org([]);
  await library(page, slug, [libraryWorld("w1", "Island")]);
  const base = {
    id: "t9", name: "Event base", description: "", createdBy: "owner", createdAt: new Date().toISOString(), updatedAt: new Date().toISOString(), current: null, versions: [],
    settingsOnly: true, spec: { loader: "paper", version: "1.21.4", resources: { memory: "2Gi" }, storage: { size: "5Gi" } },
    world: "own", ownWorld: { root: "world", name: "Island", version: "1.21.4", size: 4096, library: { id: "w1", name: "Island" } },
  };
  await page.route(new RegExp(`/api/v1/orgs/${slug}/templates(/t9)?$`), (route) => route.fulfill({ json: route.request().url().endsWith("/t9") ? base : [base] }));
  for (const at of [`/o/${slug}/templates/t9/edit`, `/o/${slug}/new?from=template:t9`]) {
    await page.goto(at);
    await expect(page.getByTestId("world-summary")).toContainText("from the world library");
    await expect(page.getByTestId("world-summary")).toContainText("kept with the template");
  }
});

const islandFacts = { level: "world", exists: true, name: "Island", version: "1.21.4", size: 12 << 20 };

test("the world is downloaded from Settings as it is, running or stopped, by the browser itself", async ({ page, org }) => {
  const slug = await org(["event"]);
  const shown: Shown = { facts: islandFacts, phase: "Running" };
  await server(page, slug, "event", shown);
  for (const phase of ["Running", "Stopped"]) {
    shown.phase = phase;
    await page.goto(`/o/${slug}/s/event/settings`);
    const link = page.getByRole("link", { name: "Download" });
    await expect(link).toHaveAttribute("href", `/api/v1/orgs/${slug}/servers/event/world/archive`);
    await expect(link).toHaveAttribute("download", "");
    // Replacing and deleting it wait for the server to stop; downloading does not.
    await expect(page.getByRole("button", { name: "Replace the world" })).toBeEnabled({ enabled: phase === "Stopped" });
    await expect(page.getByRole("button", { name: "Delete the world" })).toBeEnabled({ enabled: phase === "Stopped" });
    await expect(page.getByText("Stop the server to replace or delete the world.")).toHaveCount(phase === "Stopped" ? 0 : 1);
  }
  // A download of the browser's own, straight from the panel: not read into
  // the page first, as a world of gigabytes cannot be. (This panel has no
  // files behind it to send; the Go tests check what it sends.)
  const [download] = await Promise.all([page.waitForEvent("download"), page.getByRole("link", { name: "Download" }).click()]);
  expect(new URL(download.url()).pathname).toBe(`/api/v1/orgs/${slug}/servers/event/world/archive`);
  // The page stayed where it was.
  await expect(page.getByRole("link", { name: "Download" })).toBeVisible();
});

test("the world cannot be downloaded while its files cannot be read, before there is one, or while the server starts", async ({ page, org }) => {
  const slug = await org(["event"]);
  const shown: Shown = { facts: null, phase: "Running" };
  await server(page, slug, "event", shown);
  for (const [facts, phase, why] of [
    [null, "Running", "Can't reach the server's files."],
    [{ level: "world", exists: false }, "Stopped", "No world yet."],
    [islandFacts, "Starting", "Wait for the server to start or stop."],
  ] as const) {
    shown.facts = facts;
    shown.phase = phase;
    await page.goto(`/o/${slug}/s/event/settings`);
    await expect(page.getByTestId("world-why")).toContainText(why);
    await expect(page.getByRole("button", { name: "Download" })).toBeDisabled();
    await expect(page.getByRole("link", { name: "Download" })).toHaveCount(0);
  }
});

test("a server's world is downloaded from its Settings, which only its admins see", async ({ page, as }) => {
  await as("admin");
  await page.goto("/o/friends/s/creative/settings");
  // Nothing to read its files from in this panel.
  await expect(page.getByTestId("world-why")).toContainText("Can't reach the server's files.");
  await expect(page.getByRole("button", { name: "Download" })).toBeDisabled();
  await as("operator");
  await page.goto("/o/friends/s/creative");
  await expect(page.getByRole("link", { name: "Files", exact: true })).toBeVisible();
  await expect(content(page).getByRole("link", { name: "Settings", exact: true })).toHaveCount(0);
  await expect(page.getByRole("link", { name: "Download" })).toHaveCount(0);
});

test("deleting the world asks first, says a snapshot is taken, and is only for a stopped server", async ({ page, org }) => {
  const slug = await org(["event"]);
  const shown: Shown = { facts: islandFacts, phase: "Running" };
  await server(page, slug, "event", shown);
  const sent: { query: Record<string, string>; body: string | null }[] = [];
  await page.route(`**/api/v1/orgs/${slug}/servers/event/import?*`, async (route) => {
    sent.push({ query: Object.fromEntries(new URL(route.request().url()).searchParams), body: route.request().postData() });
    await route.fulfill({ status: 202, json: await (await page.request.get(`/api/v1/orgs/${slug}/servers/event`)).json() });
  });
  await page.goto(`/o/${slug}/s/event/settings`);
  await expect(page.getByRole("button", { name: "Delete the world" })).toBeDisabled();
  shown.phase = "Stopped";
  await page.goto(`/o/${slug}/s/event/settings`);
  await page.getByRole("button", { name: "Delete the world" }).click();
  const dialog = page.getByRole("dialog", { name: "Delete the world of event?" });
  await expect(dialog).toContainText("A snapshot is taken first, so it can be put back from Backups. The next start makes a new world.");
  // Called off: nothing is sent.
  await dialog.getByRole("button", { name: "Cancel" }).click();
  await expect(dialog).toHaveCount(0);
  expect(sent).toEqual([]);
  await page.getByRole("button", { name: "Delete the world" }).click();
  await dialog.getByRole("button", { name: "Delete the world" }).click();
  await expect(dialog).toHaveCount(0);
  // A new world in its place, made as the server's settings say.
  expect(sent).toHaveLength(1);
  expect(sent[0].query).toEqual({ world: "new", replace: "true" });
  expect(sent[0].body ?? "").toBe("");
});
