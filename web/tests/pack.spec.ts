import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// The resource pack is content: chosen in the same picker as the rest, from
// Modrinth, an upload or a URL, and shown with it. A server has one.

const sha = "ab".repeat(20);
const uploaded = { url: `http://panel.test/packs/${sha}.zip`, sha1: sha, uploaded: { name: "faithful.zip", size: 3 * 1024 * 1024, time: new Date().toISOString() } };
const picked = { url: "https://files.invalid/faithful-1.21.4-a.zip", sha1: "2".repeat(40), project: { source: "modrinth", id: "faithful-32x", title: "Faithful 32x", version: "r0000002", versionName: "1.21.4-a" } };

test.beforeEach(async ({ as }) => {
  await as("owner");
});

/** Opens the content picker on its resource pack, as Add and then that kind. */
async function choosePack(page: Page) {
  await page.getByRole("button", { name: "Add", exact: true }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Resource pack", exact: true }).click();
}

/** A JSON merge patch, as the panel applies one. */
function merge(into: any, patch: any): any {
  if (patch === null || typeof patch !== "object" || Array.isArray(patch)) return patch;
  const out = { ...(into && typeof into === "object" ? into : {}) };
  for (const [k, v] of Object.entries(patch)) {
    if (v === null) delete out[k];
    else out[k] = merge(out[k], v);
  }
  return out;
}

/**
 * survival with this resource pack. Changes sent to it are kept, as the
 * panel would, and listed; an upload or removal is answered as one too.
 */
async function survival(page: Page, pack: object | null) {
  const state = { pack: pack as any, sent: [] as any[], asked: [] as string[] };
  const withPack = (s: { name: string; spec: object }) => (s.name === "survival" ? { ...s, spec: { ...s.spec, resourcePack: state.pack } } : s);
  const server = async () => withPack(await (await page.request.get("/api/v1/orgs/friends/servers/survival")).json());
  await page.route("**/api/v1/orgs/friends/servers", async (route) => route.fulfill({ json: (await (await route.fetch()).json()).map(withPack) }));
  await page.route("**/api/v1/orgs/friends/servers/survival", async (route) => {
    if (route.request().method() === "PATCH") {
      const { spec } = route.request().postDataJSON();
      state.sent.push(spec);
      if ("resourcePack" in spec) state.pack = spec.resourcePack === null ? null : merge(state.pack, spec.resourcePack);
      return route.fulfill({ json: await server() });
    }
    return route.fulfill({ json: withPack(await (await route.fetch()).json()) });
  });
  await page.route("**/api/v1/orgs/friends/servers/survival/resource-pack*", async (route) => {
    state.asked.push(`${route.request().method()} ${new URL(route.request().url()).search}`);
    state.pack = route.request().method() === "PUT" ? uploaded : null;
    return route.fulfill({ json: await server() });
  });
  return state;
}

test("one picked from Modrinth is its newest release that fits, and another version can be chosen", async ({ page }) => {
  const state = await survival(page, null);
  await page.goto("/o/friends/s/survival/content");
  await expect(page.getByText(/^None\. One added from Modrinth/)).toBeVisible();
  await choosePack(page);
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByPlaceholder("Search resource packs on Modrinth")).toBeVisible();
  await expect(dialog.getByText("1 result for Minecraft 1.21.4")).toBeVisible();
  await dialog.getByRole("button", { name: /^Faithful 32x/ }).click();
  await expect(dialog).toHaveCount(0);
  // The newest release, not the beta above it, with its own file and hash.
  expect(state.sent).toEqual([{ resourcePack: { ...picked, uploaded: null, enforce: false, prompt: "" } }]);
  await expect(page.getByText("Faithful 32x", { exact: true })).toBeVisible();
  await expect(page.getByText("modrinth:faithful-32x")).toBeVisible();
  await expect(page.locator("section").filter({ hasText: "Resource pack" }).getByRole("link", { name: "Open on Modrinth" })).toHaveAttribute("href", "https://modrinth.com/resourcepack/faithful-32x");

  await page.getByRole("button", { name: "1.21.4-a" }).click();
  await page.getByRole("dialog").getByRole("button", { name: /^1\.21\.4-b/ }).click();
  await expect.poll(() => state.sent.length).toBe(2);
  expect(state.sent[1].resourcePack).toMatchObject({ url: "https://files.invalid/faithful-1.21.4-b.zip", sha1: "3".repeat(40), project: { id: "faithful-32x", version: "r0000003", versionName: "1.21.4-b" } });
  await expect(page.getByRole("button", { name: "1.21.4-b" })).toBeVisible();
});

test("one at a URL replaces the one there was, with its hash and the same settings", async ({ page }) => {
  const state = await survival(page, { ...picked, enforce: true, prompt: "For the map" });
  await page.route("**/api/v1/catalog/sha1", (route) => route.fulfill({ json: { sha1: "ef".repeat(20) } }));
  await page.goto("/o/friends/s/survival/content");
  await page.getByRole("button", { name: "Replace" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("A server offers one resource pack. Choosing one here replaces Faithful 32x.")).toBeVisible();
  // The one it has is shown as such.
  await expect(dialog.getByRole("button", { name: /^Faithful 32x/ })).toBeDisabled();
  await dialog.getByRole("button", { name: "From URL" }).click();
  await dialog.getByLabel("Download URL").fill("https://packs.example.org/world.zip");
  await dialog.getByRole("button", { name: "Use this pack" }).click();
  await expect(dialog).toHaveCount(0);
  expect(state.sent).toEqual([{ resourcePack: { url: "https://packs.example.org/world.zip", sha1: "ef".repeat(20), uploaded: null, project: null, enforce: true, prompt: "For the map" } }]);
  await expect(page.getByText("world.zip", { exact: true })).toBeVisible();
  await expect(page.getByText("https://packs.example.org/world.zip")).toBeVisible();
});

test("one at a URL is given its SHA-1, or taken without one when the panel cannot fetch it, saying so", async ({ page }) => {
  const state = await survival(page, null);
  let hashed = 0;
  await page.route("**/api/v1/catalog/sha1", (route) => {
    hashed++;
    return route.fulfill({ status: 502, json: { code: "fetch", message: "The panel could not reach that address." } });
  });
  await page.goto("/o/friends/s/survival/content");
  await choosePack(page);
  let dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "From URL" }).click();
  await dialog.getByLabel("Download URL").fill("https://players-only.example.org/pack.zip");
  await dialog.getByLabel("SHA-1").fill("CD".repeat(20));
  await dialog.getByRole("button", { name: "Use this pack" }).click();
  await expect(dialog).toHaveCount(0);
  // Given, it is used as it is, not worked out.
  expect(hashed).toBe(0);
  expect(state.sent[0].resourcePack).toMatchObject({ url: "https://players-only.example.org/pack.zip", sha1: "CD".repeat(20) });

  await page.getByRole("button", { name: "Replace" }).click();
  dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "From URL" }).click();
  await dialog.getByLabel("Download URL").fill("https://players-only.example.org/other.zip");
  await dialog.getByRole("button", { name: "Use this pack" }).click();
  await expect(page.getByText(/Its SHA-1 could not be worked out \(The panel could not reach that address\.\)/)).toBeVisible();
  expect(hashed).toBe(1);
  expect(state.sent[1].resourcePack).toMatchObject({ url: "https://players-only.example.org/other.zip", sha1: "" });
});

test("while one choice is being made, no other can be made on another tab", async ({ page }) => {
  const state = await survival(page, null);
  let answer: (() => void) | undefined;
  await page.route("**/api/v1/catalog/sha1", async (route) => {
    await new Promise<void>((resolve) => (answer = resolve));
    return route.fulfill({ json: { sha1: "ef".repeat(20) } });
  });
  await page.goto("/o/friends/s/survival/content");
  await choosePack(page);
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "From URL" }).click();
  await dialog.getByLabel("Download URL").fill("https://packs.example.org/world.zip");
  await dialog.getByRole("button", { name: "Use this pack" }).click();
  await expect.poll(() => answer).toBeDefined();
  await dialog.getByRole("button", { name: "Upload", exact: true }).click();
  await expect(dialog.getByLabel("Resource pack file")).toBeDisabled();
  answer?.();
  await expect(dialog).toHaveCount(0);
  expect(state.sent).toHaveLength(1);
  expect(state.asked).toEqual([]);
});

test("a pack's address that does not decode is shown as it is", async ({ page }) => {
  await survival(page, { url: "https://packs.example.org/bad%E0%A4%A.zip" });
  await page.goto("/o/friends/s/survival/content");
  await expect(page.getByText("bad%E0%A4%A.zip", { exact: true })).toBeVisible();
});

test("while a change to it is being saved, it is not replaced or removed", async ({ page }) => {
  const state = await survival(page, { url: "https://packs.example.org/world.zip", sha1: "" });
  let answer: (() => void) | undefined;
  await page.route("**/api/v1/orgs/friends/servers/survival", async (route) => {
    if (route.request().method() === "PATCH") await new Promise<void>((resolve) => (answer = resolve));
    return route.fallback();
  });
  await page.goto("/o/friends/s/survival/content");
  await page.getByLabel("Prompt").fill("For the map");
  await page.getByLabel("Prompt").press("Enter");
  await expect(page.getByRole("button", { name: "Remove the resource pack" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Replace" })).toBeDisabled();
  await expect.poll(() => answer).toBeDefined();
  answer?.();
  await expect(page.getByRole("button", { name: "Remove the resource pack" })).toBeEnabled();
  expect(state.sent).toEqual([{ resourcePack: { prompt: "For the map" } }]);

  // Nor is its prompt changed while whether it is required is saved: the two
  // would be saved at once, and one lost.
  answer = undefined;
  await page.getByRole("switch", { name: "Required" }).click();
  await expect(page.getByLabel("Prompt")).toBeDisabled();
  await expect.poll(() => answer).toBeDefined();
  answer?.();
  await expect(page.getByLabel("Prompt")).toBeEnabled();
  expect(state.sent[1]).toEqual({ resourcePack: { enforce: true } });
});

test("while its newest version is looked up, it is not replaced or removed", async ({ page }) => {
  const state = await survival(page, picked);
  let answer: (() => void) | undefined;
  let asked = 0;
  await page.route("**/api/v1/catalog/modrinth/projects/faithful-32x/versions*", async (route) => {
    // The dialog's own list first; the look for the newest after it is held.
    if (++asked > 1) await new Promise<void>((resolve) => (answer = resolve));
    return route.fallback();
  });
  await page.goto("/o/friends/s/survival/content");
  await page.getByRole("button", { name: "1.21.4-a" }).click();
  await expect(page.getByRole("dialog").getByRole("button", { name: /^1\.21\.4-b/ })).toBeVisible();
  await page.getByRole("dialog").getByRole("button", { name: /^Update to the best fit/ }).click();
  await expect(page.getByRole("dialog")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Replace" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Remove the resource pack" })).toBeDisabled();
  await expect(page.getByLabel("Prompt")).toBeDisabled();
  await expect.poll(() => answer).toBeDefined();
  answer?.();
  await expect.poll(() => state.sent.length).toBe(1);
  await expect(page.getByRole("button", { name: "Replace" })).toBeEnabled();
});

test("for a new server on the latest version, a pack is picked once it is known which version that is", async ({ page, org }) => {
  const slug = await org([]);
  let answer: (() => void) | undefined;
  await page.route("**/api/v1/catalog/minecraft/versions?loader=fabric", async (route) => {
    await new Promise<void>((resolve) => (answer = resolve));
    return route.fallback();
  });
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Fabric/ }).click();
  // In the one picker for all content.
  await page.getByRole("button", { name: "Add content" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Resource pack", exact: true }).click();
  await expect(dialog.getByRole("button", { name: /^Faithful 32x/ })).toBeDisabled();
  await expect.poll(() => answer).toBeDefined();
  answer?.();
  await expect(dialog.getByRole("button", { name: /^Faithful 32x/ })).toBeEnabled();
});

test("one uploaded in the picker is kept and served by the panel, and removing it takes its file", async ({ page }) => {
  const state = await survival(page, null);
  await page.goto("/o/friends/s/survival/content");
  await choosePack(page);
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Upload", exact: true }).click();
  await dialog.getByLabel("Resource pack file").setInputFiles({ name: "faithful.zip", mimeType: "application/zip", buffer: Buffer.from("PK") });
  await expect(dialog).toHaveCount(0);
  expect(state.asked).toEqual(["PUT ?name=faithful.zip"]);
  await expect(page.getByText("faithful.zip", { exact: true })).toBeVisible();
  await expect(page.getByText(/3\.0 MB · uploaded .* · served by the panel/)).toBeVisible();

  await page.getByRole("button", { name: "Remove the resource pack" }).click();
  await expect.poll(() => state.asked).toEqual(["PUT ?name=faithful.zip", "DELETE "]);
  await expect(page.getByText(/^None\. One added from Modrinth/)).toBeVisible();
});

test("an upload says how far it is and can be stopped, and says why the panel refused one", async ({ page }) => {
  let refuse = false;
  const asked: string[] = [];
  await page.route("**/api/v1/orgs/friends/servers/survival/resource-pack*", async (route) => {
    asked.push(route.request().method());
    if (refuse) return route.fulfill({ status: 400, json: { code: "bad_request", message: "There is no pack.mcmeta at the top of that zip, so it is not a resource pack." } });
    // Never answered: it is still on its way when it is stopped.
    await new Promise(() => {});
  });
  await page.goto("/o/friends/s/survival/content");
  await choosePack(page);
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Upload", exact: true }).click();
  const pack = { name: "big.zip", mimeType: "application/zip", buffer: Buffer.alloc(2 * 1024 * 1024) };
  await dialog.getByLabel("Resource pack file").setInputFiles(pack);
  const status = dialog.getByRole("status").filter({ hasText: /^\d+% · .* of 2\.0 MB\s*big\.zip/ });
  await expect(status).toBeVisible();
  await expect(status.getByRole("progressbar", { name: "Upload progress" })).toBeVisible();
  await status.getByRole("button", { name: "Cancel" }).click();
  await expect(status).toHaveCount(0);
  // Stopping it is not a failure to tell of, and the picker stays open.
  await expect(page.getByText("Upload cancelled.")).toHaveCount(0);
  await expect(dialog.getByText("Choose a .zip")).toBeVisible();

  refuse = true;
  await dialog.getByLabel("Resource pack file").setInputFiles(pack);
  await expect(page.getByText("There is no pack.mcmeta at the top of that zip, so it is not a resource pack.")).toBeVisible();
  await expect(status).toHaveCount(0);
  expect(asked).toEqual(["PUT", "PUT"]);
});

test("whether it is required and its prompt are saved from the content page", async ({ page }) => {
  const state = await survival(page, { url: "https://packs.example.org/world.zip", sha1: "" });
  await page.goto("/o/friends/s/survival/content");
  await page.getByRole("switch", { name: "Required" }).click();
  await expect.poll(() => state.sent).toEqual([{ resourcePack: { enforce: true } }]);
  await expect(page.getByRole("switch", { name: "Required" })).toBeChecked();
  await page.getByLabel("Prompt").fill("For the map");
  await page.getByLabel("Prompt").press("Enter");
  await expect.poll(() => state.sent.length).toBe(2);
  expect(state.sent[1]).toEqual({ resourcePack: { prompt: "For the map" } });
});

test("someone who cannot change the server sees its pack and nothing to change it with", async ({ page, as }) => {
  await as("operator");
  await survival(page, picked);
  await page.goto("/o/friends/s/survival/content");
  await expect(page.getByText("Faithful 32x", { exact: true })).toBeVisible();
  await expect(page.getByRole("button", { name: "Replace" })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Remove the resource pack" })).toHaveCount(0);
  await expect(page.getByRole("switch", { name: "Required" })).toBeDisabled();
});

test("settings no longer have the pack, and saving them says nothing of it", async ({ page }) => {
  const state = await survival(page, uploaded);
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByRole("heading", { name: "Resource pack" })).toHaveCount(0);
  await page.getByLabel("Display name").fill("Survival, renamed");
  await page.getByRole("button", { name: /^Save/ }).click();
  await expect.poll(() => state.sent.length).toBe(1);
  expect(state.sent[0].displayName).toBe("Survival, renamed");
  expect(state.sent[0]).not.toHaveProperty("resourcePack");
});

test("a new server can be given a pack from Modrinth when it is made", async ({ page, org, api }) => {
  const slug = await org([]);
  await page.goto(`/o/${slug}/new`);
  await page.getByLabel("Display name").fill("Packed");
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  // In the one picker for all content.
  await page.getByRole("button", { name: "Add content" }).click();
  const dialog = page.getByRole("dialog");
  await dialog.getByRole("button", { name: "Resource pack", exact: true }).click();
  // Nowhere to put a file until the server exists.
  await expect(dialog.getByRole("button", { name: "Upload", exact: true })).toHaveCount(0);
  await dialog.getByRole("button", { name: /^Faithful 32x/ }).click();
  await expect(dialog).toHaveCount(0);
  await page.getByRole("switch", { name: "Pack required" }).click();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/packed$`));
  const { spec } = await api("owner").get(`/orgs/${slug}/servers/packed`);
  expect(spec.resourcePack).toEqual({ ...picked, enforce: true });
});

test("a copy keeps the original's uploaded pack, made required, and can be given another or none", async ({ page }) => {
  await survival(page, uploaded);
  const sent: any[] = [];
  await page.route("**/api/v1/orgs/friends/servers", async (route) => {
    if (route.request().method() !== "POST") return route.fallback();
    sent.push(route.request().postDataJSON());
    return route.fulfill({ status: 503, json: { code: "test", message: "Not sent by the test." } });
  });
  await page.goto("/o/friends/new?from=server:survival");
  await expect(page.getByText("faithful.zip", { exact: true })).toBeVisible();
  // Left as it is, it is not sent: the copy's files bring it.
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  await expect.poll(() => sent.length).toBe(1);
  expect(sent[0].spec).not.toHaveProperty("resourcePack");

  // Made required: sent, but not as an upload, which the panel knows by its address.
  await page.getByRole("switch", { name: "Pack required" }).click();
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  await expect.poll(() => sent.length).toBe(2);
  expect(sent[1].spec.resourcePack).toEqual({ url: uploaded.url, sha1: sha, uploaded: null, enforce: true });

  await page.getByRole("button", { name: "Remove the resource pack" }).click();
  await expect(page.getByText("faithful.zip", { exact: true })).toHaveCount(0);
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  await expect.poll(() => sent.length).toBe(3);
  expect(sent[2].spec.resourcePack).toBeNull();
});

test("files put in the file explorer are uploaded one by one, saying which and how far", async ({ page }) => {
  const put: string[] = [];
  let answer: (() => void) | undefined;
  await page.route("**/api/v1/orgs/friends/servers/survival/files?*", (route) => route.fulfill({ json: { entries: [] } }));
  await page.route("**/api/v1/orgs/friends/servers/survival/files/content?*", async (route) => {
    put.push(`${route.request().method()} ${new URL(route.request().url()).searchParams.get("path")}`);
    if (put.length === 1) await new Promise<void>((resolve) => (answer = resolve));
    return route.fulfill({ status: 204 });
  });
  await page.goto("/o/friends/s/survival/files");
  await page.locator('input[type="file"]').setInputFiles([
    { name: "world.zip", mimeType: "application/zip", buffer: Buffer.alloc(1024 * 1024) },
    { name: "notes.txt", mimeType: "text/plain", buffer: Buffer.from("hi") },
  ]);
  await expect(page.getByRole("status").filter({ hasText: "world.zip (1 of 2)" })).toBeVisible();
  // One upload at a time: another is not started while this one is under way.
  await expect(page.getByRole("button", { name: "Upload", exact: true })).toBeDisabled();
  await page.locator('input[type="file"]').setInputFiles({ name: "other.txt", mimeType: "text/plain", buffer: Buffer.from("x") });
  await expect(page.getByText("Wait for the upload under way to finish, or cancel it.")).toBeVisible();
  await expect(page.getByRole("status").filter({ hasText: "world.zip (1 of 2)" })).toBeVisible();
  await expect.poll(() => answer).toBeDefined();
  answer?.();
  await expect.poll(() => put).toEqual(["PUT /world.zip", "PUT /notes.txt"]);
  await expect(page.getByRole("status").filter({ hasText: /of 2/ })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Upload", exact: true })).toBeEnabled();
});
