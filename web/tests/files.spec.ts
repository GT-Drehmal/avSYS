import type { Page } from "@playwright/test";
import { expect, test } from "./panel";

// The file editor saves only over the file it opened.

test.beforeEach(async ({ as }) => {
  await as("owner");
});

/**
 * survival's files, as the panel would serve them: server.properties with a
 * tag for what it holds, and a few folders. Saves are kept, with the tag
 * each was sent with; one sent with another tag than the file's is refused,
 * as the panel does. Folders are listed as they are asked for.
 */
async function files(page: Page, more: Record<string, string[]> = {}) {
  const state = { text: "motd=hello\n", gone: false, saves: [] as { text: string; tag: string | null }[], listed: [] as string[], made: [] as string[] };
  const tagOf = (text: string) => `"${text.length}-${[...text].reduce((h, c) => (h * 31 + c.charCodeAt(0)) >>> 0, 7)}"`;
  // Folders end in "/".
  const tree: Record<string, string[]> = {
    "/": ["config/", "plugins/", "world/", "eula.txt", "server.jar", "server.properties"],
    "/config": [],
    "/plugins": ["LuckPerms/", "EssentialsX.jar"],
    "/plugins/LuckPerms": ["config.yml"],
    "/world": ["level.dat"],
    ...more,
  };
  const texts: Record<string, string> = { "/eula.txt": "eula=true\n", "/plugins/LuckPerms/config.yml": "storage-method: h2\n" };
  await page.route("**/api/v1/orgs/friends/servers/survival/files?*", (route) => {
    const dir = new URL(route.request().url()).searchParams.get("path") ?? "/";
    state.listed.push(dir);
    const names = tree[dir];
    if (!names) return route.fulfill({ status: 404, json: { code: "not_found", message: "No such folder." } });
    const entries = names
      .filter((n) => !(dir === "/" && n === "server.properties" && state.gone))
      .map((n) => ({ name: n.replace(/\/$/, ""), dir: n.endsWith("/"), size: n.endsWith(".jar") ? 9_000_000 : 20, modTime: new Date().toISOString() }));
    return route.fulfill({ json: { path: dir, entries } });
  });
  await page.route("**/api/v1/orgs/friends/servers/survival/files/content?*", async (route) => {
    const req = route.request();
    const path = new URL(req.url()).searchParams.get("path") ?? "";
    if (path !== "/server.properties") {
      if (req.method() === "PUT") {
        state.made.push(path);
        return route.fulfill({ status: 204, headers: { ETag: tagOf("") } });
      }
      const text = texts[path] ?? "";
      return route.fulfill({ body: text, headers: { "Content-Type": "application/octet-stream", ETag: tagOf(text) } });
    }
    if (req.method() === "GET") {
      if (state.gone) return route.fulfill({ status: 404, json: { code: "not_found", message: "No such file." } });
      return route.fulfill({ body: state.text, headers: { "Content-Type": "application/octet-stream", ETag: tagOf(state.text) } });
    }
    const tag = (await req.headerValue("If-Match")) ?? null;
    const text = req.postData() ?? "";
    state.saves.push({ text, tag });
    if (tag && state.gone) return route.fulfill({ status: 412, json: { code: "removed", message: "This file was removed since you opened it." } });
    if (tag && tag !== tagOf(state.text)) return route.fulfill({ status: 412, json: { code: "changed", message: "This file was changed since you opened it." } });
    state.text = text;
    state.gone = false;
    return route.fulfill({ status: 204, headers: { ETag: tagOf(text) } });
  });
  return { state, tagOf };
}

async function edit(page: Page, text: string) {
  await page.locator(".cm-content").click();
  await page.keyboard.press("ControlOrMeta+End");
  await page.keyboard.type(text);
}

const open = (page: Page) => page.goto("/o/friends/s/survival/files?file=%2Fserver.properties");

test("a save is sent with the tag of the file opened, and the next with that of the one saved", async ({ page }) => {
  const { state, tagOf } = await files(page);
  await open(page);
  await expect(page.locator(".cm-content")).toContainText("motd=hello");
  await edit(page, "pvp=false");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByText("unsaved")).toHaveCount(0);
  await edit(page, "\ndifficulty=hard");
  await page.keyboard.press("ControlOrMeta+s");
  await expect.poll(() => state.saves.length).toBe(2);
  expect(state.saves[0].tag).toBe(tagOf("motd=hello\n"));
  expect(state.saves[1].tag).toBe(tagOf("motd=hello\npvp=false"));
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("a file changed since it was opened is not saved over; reloaded, it shows the change", async ({ page }) => {
  const { state } = await files(page);
  await open(page);
  await expect(page.locator(".cm-content")).toContainText("motd=hello");
  // Someone else saves it meanwhile.
  state.text = "motd=from a friend\n";
  await edit(page, "pvp=false");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("This file was changed since you opened it.");
  expect(state.text).toBe("motd=from a friend\n");
  await expect(page.getByText("unsaved")).toBeVisible();
  await page.getByRole("button", { name: "Reload" }).click();
  await expect(page.locator(".cm-content")).toHaveText("motd=from a friend");
  await expect(page.getByText("unsaved")).toHaveCount(0);
  await expect(page.getByRole("alert")).toHaveCount(0);
});

test("saved anyway, it is written over the change", async ({ page }) => {
  const { state } = await files(page);
  await open(page);
  await expect(page.locator(".cm-content")).toContainText("motd=hello");
  state.text = "motd=from a friend\n";
  await edit(page, "pvp=false");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await page.getByRole("button", { name: "Save anyway" }).click();
  await expect(page.getByRole("alert")).toHaveCount(0);
  await expect(page.getByText("unsaved")).toHaveCount(0);
  expect(state.saves.at(-1)).toEqual({ text: "motd=hello\npvp=false", tag: null });
  expect(state.text).toBe("motd=hello\npvp=false");
});

test("a file removed since it was opened is said so, and can be saved again", async ({ page }) => {
  const { state } = await files(page);
  await open(page);
  await expect(page.locator(".cm-content")).toContainText("motd=hello");
  state.gone = true;
  await edit(page, "pvp=false");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("This file was removed since you opened it.");
  await expect(page.getByRole("button", { name: "Reload" })).toHaveCount(0);
  await page.getByRole("button", { name: "Save anyway" }).click();
  await expect(page.getByRole("alert")).toHaveCount(0);
  expect(state.gone).toBe(false);
});

test("a file read again behind the editor, as on reconnecting, is not what a save is checked against", async ({ page }) => {
  const { state, tagOf } = await files(page);
  await open(page);
  await expect(page.locator(".cm-content")).toContainText("motd=hello");
  state.text = "motd=from a friend\n";
  await page.context().setOffline(true);
  await page.context().setOffline(false);
  await page.evaluate(() => window.dispatchEvent(new Event("focus")));
  await edit(page, "pvp=false");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("alert")).toContainText("This file was changed since you opened it.");
  expect(state.saves[0].tag).toBe(tagOf("motd=hello\n"));
  expect(state.text).toBe("motd=from a friend\n");
});

test("a reload that fails keeps what was typed", async ({ page }) => {
  const { state } = await files(page);
  await open(page);
  await expect(page.locator(".cm-content")).toContainText("motd=hello");
  state.text = "motd=from a friend\n";
  await edit(page, "pvp=false");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page.getByRole("alert")).toBeVisible();
  // Then removed too, before it is reloaded.
  state.gone = true;
  await page.getByRole("button", { name: "Reload" }).click();
  await expect(page.getByText("No such file.")).toBeVisible();
  await expect(page.locator(".cm-content")).toContainText("pvp=false");
  await expect(page.getByText("unsaved")).toBeVisible();
  await page.getByRole("button", { name: "Save anyway" }).click();
  await expect(page.getByText("unsaved")).toHaveCount(0);
  expect(state.text).toBe("motd=hello\npvp=false");
});

const tree = (page: Page) => page.getByRole("tree", { name: "Files" });
const item = (page: Page, name: string) => tree(page).getByRole("treeitem", { name, exact: true });

test("folders open in place, and a file opens beside them", async ({ page }) => {
  await files(page);
  await page.goto("/o/friends/s/survival/files");
  await expect(page.getByText("Choose a file to edit.")).toBeVisible();
  await item(page, "plugins").click();
  await expect(item(page, "plugins")).toHaveAttribute("aria-expanded", "true");
  await expect(item(page, "LuckPerms")).toHaveAttribute("aria-level", "2");
  await item(page, "LuckPerms").click();
  await item(page, "config.yml").click();
  // The file beside the tree, which is as it was.
  await expect(page.locator(".cm-content")).toContainText("storage-method: h2");
  await expect(page).toHaveURL(/file=%2Fplugins%2FLuckPerms%2Fconfig\.yml/);
  await expect(item(page, "config.yml")).toHaveAttribute("aria-selected", "true");
  await expect(item(page, "EssentialsX.jar")).toBeVisible();
  // Another, and the folder closed again.
  await item(page, "eula.txt").click();
  await expect(page.locator(".cm-content")).toContainText("eula=true");
  await item(page, "plugins").click();
  await expect(item(page, "LuckPerms")).toHaveCount(0);
});

test("a file linked to opens with its folders open, and folders stay open for the next visit", async ({ page }) => {
  await files(page);
  await page.goto("/o/friends/s/survival/files?file=%2Fplugins%2FLuckPerms%2Fconfig.yml");
  await expect(item(page, "config.yml")).toHaveAttribute("aria-selected", "true");
  await expect(page.locator(".cm-content")).toContainText("storage-method: h2");
  await item(page, "world").click();
  await page.goto("/o/friends/s/survival/files");
  await expect(item(page, "level.dat")).toBeVisible();
  await expect(item(page, "config.yml")).toBeVisible();
  // An old link to a folder opens it.
  await page.evaluate(() => localStorage.clear());
  await page.goto("/o/friends/s/survival/files/plugins");
  await expect(item(page, "LuckPerms")).toBeVisible();
  await expect(page.getByTitle("New files and uploads go here")).toHaveText("data/plugins");
});

test("leaving a file with changes not saved is asked first", async ({ page }) => {
  await files(page);
  await open(page);
  await expect(page.locator(".cm-content")).toContainText("motd=hello");
  await edit(page, "pvp=false");
  await item(page, "eula.txt").click();
  const ask = page.getByRole("dialog", { name: "Discard your changes to server.properties?" });
  await ask.getByRole("button", { name: "Cancel" }).click();
  await expect(page.locator(".cm-content")).toContainText("pvp=false");
  await item(page, "eula.txt").click();
  await ask.getByRole("button", { name: "Discard" }).click();
  await expect(page.locator(".cm-content")).toContainText("eula=true");
  // Saved, it is left without asking.
  await edit(page, "\n# ok");
  await page.keyboard.press("ControlOrMeta+s");
  await expect(page.getByText("unsaved")).toHaveCount(0);
  await item(page, "server.properties").click();
  await expect(page.locator(".cm-content")).toContainText("motd=hello");
});

test("the tree is moved about with the keyboard", async ({ page }) => {
  await files(page);
  await page.goto("/o/friends/s/survival/files");
  await item(page, "config").focus();
  await page.keyboard.press("ArrowDown");
  await expect(item(page, "plugins")).toBeFocused();
  // What is in plugins is held back, to press again while it is read.
  let answer = () => {};
  const held = new Promise<void>((r) => {
    answer = r;
  });
  await page.route("**/api/v1/orgs/friends/servers/survival/files?path=%2Fplugins", async (route) => {
    await held;
    return route.fallback();
  });
  await page.keyboard.press("ArrowRight");
  await expect(item(page, "plugins")).toHaveAttribute("aria-expanded", "true");
  // It waits for what is in it, not going past it to the next folder.
  await page.keyboard.press("ArrowRight");
  await expect(item(page, "plugins")).toBeFocused();
  answer();
  await expect(item(page, "LuckPerms")).toBeVisible();
  await page.keyboard.press("ArrowRight");
  await expect(item(page, "LuckPerms")).toBeFocused();
  await page.keyboard.press("ArrowLeft");
  await expect(item(page, "plugins")).toBeFocused();
  await page.keyboard.press("ArrowLeft");
  await expect(item(page, "LuckPerms")).toHaveCount(0);
  await page.keyboard.press("End");
  await expect(item(page, "server.properties")).toBeFocused();
  await page.keyboard.press("Enter");
  await expect(page.locator(".cm-content")).toContainText("motd=hello");
});

test("a file that cannot be edited offers to be downloaded", async ({ page }) => {
  await files(page);
  await page.goto("/o/friends/s/survival/files");
  await item(page, "server.jar").click();
  await expect(page.getByText("Files over 2 MB can't be edited here.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Download" })).toBeVisible();
  await expect(page.locator(".cm-content")).toHaveCount(0);
});

test("new files and uploads go in the folder chosen", async ({ page }) => {
  const { state } = await files(page);
  await page.goto("/o/friends/s/survival/files");
  await item(page, "plugins").click();
  await page.getByRole("button", { name: "New file" }).click();
  await page.getByRole("dialog").getByLabel("Name").fill("notes.txt");
  await page.getByRole("dialog").getByRole("button", { name: "Create" }).click();
  await expect.poll(() => state.made).toEqual(["/plugins/notes.txt"]);
  await expect(page).toHaveURL(/file=%2Fplugins%2Fnotes\.txt/);
  // A file chosen: its folder.
  await item(page, "EssentialsX.jar").click();
  await expect(page.getByTitle("New files and uploads go here")).toHaveText("data/plugins");
  await page.getByLabel("Files to upload").setInputFiles({ name: "extra.yml", mimeType: "text/yaml", buffer: Buffer.from("a: 1") });
  await expect.poll(() => state.made).toEqual(["/plugins/notes.txt", "/plugins/extra.yml"]);
});

test("a big folder shows its first files, and more when asked", async ({ page }) => {
  await files(page, { "/world": Array.from({ length: 350 }, (_, i) => `r.${i}.mca`) });
  await page.goto("/o/friends/s/survival/files");
  await item(page, "world").click();
  await expect(item(page, "r.299.mca")).toBeVisible();
  await expect(item(page, "r.300.mca")).toHaveCount(0);
  await tree(page).getByRole("treeitem", { name: "Show 50 more of 50" }).click();
  await expect(item(page, "r.349.mca")).toBeVisible();
});

test("on a phone the file takes the screen, and closing it goes back to the folders as they were", async ({ page }) => {
  await files(page);
  await page.setViewportSize({ width: 390, height: 800 });
  await page.goto("/o/friends/s/survival/files");
  await item(page, "plugins").click();
  await item(page, "LuckPerms").click();
  await item(page, "config.yml").click();
  await expect(page.locator(".cm-content")).toContainText("storage-method: h2");
  await expect(tree(page)).toBeHidden();
  await page.getByRole("button", { name: "Close" }).click();
  await expect(item(page, "config.yml")).toBeVisible();
  await expect(page.locator(".cm-content")).toHaveCount(0);
  await expect(page.getByText(/can't be edited here/)).toHaveCount(0);
});

test("a folder deleted takes the file open in it with it", async ({ page }) => {
  await files(page);
  await page.route("**/api/v1/orgs/friends/servers/survival/files?path=%2Fplugins", (route) => (route.request().method() === "DELETE" ? route.fulfill({ status: 204 }) : route.fallback()));
  await page.goto("/o/friends/s/survival/files?file=%2Fplugins%2FLuckPerms%2Fconfig.yml");
  await expect(page.locator(".cm-content")).toContainText("storage-method: h2");
  await item(page, "plugins").hover();
  await page.getByRole("button", { name: "Actions for plugins" }).click();
  await page.getByRole("menuitem", { name: "Delete" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Delete" }).click();
  await expect(page.locator(".cm-content")).toHaveCount(0);
  await expect(page).not.toHaveURL(/file=/);
});

test("the files open in VS Code, all of them", async ({ page }) => {
  await files(page);
  await page.goto("/o/friends/s/survival/files");
  const link = page.getByRole("link", { name: "Open in VS Code" });
  await expect(link).toHaveAttribute("href", /^vscode:\/\/vscode-remote\/ssh-remote\+[0-9a-f]+\/data$/);
  const host = JSON.parse(Buffer.from((await link.getAttribute("href"))!.match(/ssh-remote\+([0-9a-f]+)/)![1], "hex").toString());
  expect(host.user).toBe("survival.friends");
});

test("a file renamed while open keeps what was typed", async ({ page }) => {
  await files(page);
  const renamed: string[] = [];
  await page.route("**/api/v1/orgs/friends/servers/survival/files/rename?*", (route) => {
    renamed.push(new URL(route.request().url()).searchParams.get("to") ?? "");
    return route.fulfill({ status: 204 });
  });
  await page.goto("/o/friends/s/survival/files?file=%2Fplugins%2FLuckPerms%2Fconfig.yml");
  await expect(page.locator(".cm-content")).toContainText("storage-method: h2");
  await edit(page, "\nnot saved yet");
  // The folder it is in.
  await item(page, "LuckPerms").hover();
  await page.getByRole("button", { name: "Actions for LuckPerms" }).click();
  await page.getByRole("menuitem", { name: "Rename" }).click();
  await page.getByRole("dialog").getByLabel("Name").fill("Perms");
  await page.getByRole("dialog").getByRole("button", { name: "Rename" }).click();
  await expect(page).toHaveURL(/file=%2Fplugins%2FPerms%2Fconfig\.yml/);
  expect(renamed).toEqual(["/plugins/Perms"]);
  await expect(page.locator(".cm-content")).toContainText("not saved yet");
  await expect(page.getByText("unsaved")).toBeVisible();
  // Nothing in the menu reached the row: the folder is still open, and nothing was asked.
  await expect(page.getByRole("dialog")).toHaveCount(0);
});

test("a row's menu does not open or close the row", async ({ page }) => {
  await files(page);
  await page.goto("/o/friends/s/survival/files");
  await item(page, "plugins").click();
  await page.getByRole("button", { name: "Actions for plugins" }).click();
  await page.getByRole("menuitem", { name: "Rename" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
  await expect(item(page, "plugins")).toHaveAttribute("aria-expanded", "true");
  // Nor opens a file whose menu it is.
  await page.getByRole("button", { name: "Actions for eula.txt" }).click();
  await page.getByRole("menuitem", { name: "Rename" }).press("Enter");
  await page.getByRole("dialog").getByRole("button", { name: "Cancel" }).click();
  await expect(page.locator(".cm-content")).toHaveCount(0);
});

test("the top folder can be chosen again for new files", async ({ page }) => {
  const { state } = await files(page);
  await page.goto("/o/friends/s/survival/files");
  await item(page, "plugins").click();
  await expect(page.getByTitle("New files and uploads go here")).toHaveText("data/plugins");
  await page.getByRole("button", { name: "Choose the top folder" }).click();
  await expect(page.getByTitle("New files and uploads go here")).toHaveText("data");
  await page.getByRole("button", { name: "New file" }).click();
  await page.getByRole("dialog").getByLabel("Name").fill("notes.txt");
  await page.getByRole("dialog").getByRole("button", { name: "Create" }).click();
  await expect.poll(() => state.made).toEqual(["/notes.txt"]);
});

test("on a phone a file that cannot be edited shows, with a way back", async ({ page }) => {
  await files(page);
  await page.setViewportSize({ width: 390, height: 800 });
  await page.goto("/o/friends/s/survival/files");
  await item(page, "server.jar").click();
  await expect(page.getByRole("button", { name: "Download" })).toBeVisible();
  await expect(tree(page)).toBeHidden();
  await page.getByRole("button", { name: "Back to the files" }).click();
  await expect(item(page, "server.jar")).toBeVisible();
});

test("an archive is opened where it is, into the folder it makes", async ({ page }) => {
  const root = ["plugins/", "world.zip", "server.properties"];
  const { state } = await files(page, { "/": root, "/world": ["level.dat"] });
  const asked: string[] = [];
  await page.route("**/api/v1/orgs/friends/servers/survival/files/extract?*", async (route) => {
    asked.push(new URL(route.request().url()).searchParams.get("path") ?? "");
    root.splice(1, 0, "world/");
    return route.fulfill({ json: { path: "/world" } });
  });
  await page.goto("/o/friends/s/survival/files");
  await item(page, "world.zip").click();
  await page.getByRole("button", { name: "Actions for world.zip" }).click();
  await page.getByRole("menuitem", { name: "Extract here" }).click();
  await expect(page.getByText("Extracted world.zip to world")).toBeVisible();
  expect(asked).toEqual(["/world.zip"]);
  expect(state.listed.filter((d) => d === "/").length).toBeGreaterThan(1);
  // The folder it made is chosen, and open.
  await expect(page.getByTitle("New files and uploads go here")).toHaveText("data/world");
  await expect(item(page, "level.dat")).toBeVisible();
  // Only archives offer it.
  await page.getByRole("button", { name: "Actions for server.properties" }).click();
  await expect(page.getByRole("menuitem", { name: "Download" })).toBeVisible();
  await expect(page.getByRole("menuitem", { name: "Extract here" })).toHaveCount(0);
});

test("a folder is downloaded as a zip", async ({ page }) => {
  await files(page);
  await page.route("**/api/v1/orgs/friends/servers/survival/files/archive?*", (route) =>
    route.fulfill({ body: "PK", headers: { "Content-Type": "application/zip", "Content-Disposition": "attachment; filename=plugins.zip" } }),
  );
  await page.goto("/o/friends/s/survival/files");
  await page.getByRole("button", { name: "Actions for plugins" }).click();
  // What is asked for: WebKit does not tell of a download answered here.
  const asked = page.waitForRequest("**/files/archive?*");
  await page.getByRole("menuitem", { name: "Download as .zip" }).click();
  expect(new URL((await asked).url()).searchParams.get("path")).toBe("/plugins");
  // A file is downloaded as it is.
  await page.getByRole("button", { name: "Actions for eula.txt" }).click();
  await expect(page.getByRole("menuitem", { name: "Download as .zip" })).toHaveCount(0);
});

test("a file or folder dragged onto a folder is moved into it, and never into itself", async ({ page }) => {
  await files(page);
  const moves: string[] = [];
  await page.route("**/api/v1/orgs/friends/servers/survival/files/rename?*", (route) => {
    const q = new URL(route.request().url()).searchParams;
    moves.push(`${q.get("path")} -> ${q.get("to")}`);
    return route.fulfill({ status: 204 });
  });
  await page.goto("/o/friends/s/survival/files");
  await item(page, "eula.txt").dragTo(item(page, "plugins"));
  await expect.poll(() => moves).toEqual(["/eula.txt -> /plugins/eula.txt"]);
  // The folder it went into is opened.
  await expect(item(page, "LuckPerms")).toBeVisible();
  // Onto a file: into its folder.
  await item(page, "server.properties").dragTo(item(page, "EssentialsX.jar"));
  await expect.poll(() => moves).toEqual(["/eula.txt -> /plugins/eula.txt", "/server.properties -> /plugins/server.properties"]);
  // A folder into one in it, or where it already is: nothing.
  await item(page, "plugins").dragTo(item(page, "LuckPerms"));
  await item(page, "LuckPerms").dragTo(item(page, "EssentialsX.jar"));
  await page.waitForTimeout(300);
  expect(moves).toHaveLength(2);
});

test("a file is moved to a folder named, without dragging", async ({ page }) => {
  await files(page);
  const moves: string[] = [];
  await page.route("**/api/v1/orgs/friends/servers/survival/files/rename?*", (route) => {
    const q = new URL(route.request().url()).searchParams;
    moves.push(`${q.get("path")} -> ${q.get("to")}`);
    return route.fulfill({ status: 204 });
  });
  await page.goto("/o/friends/s/survival/files");
  await page.getByRole("button", { name: "Actions for eula.txt" }).click();
  await page.getByRole("menuitem", { name: "Move to…" }).click();
  const dialog = page.getByRole("dialog", { name: "Move eula.txt" });
  await expect(dialog.getByLabel("To the folder")).toHaveValue("/");
  await dialog.getByLabel("To the folder").fill("/world/");
  await dialog.getByRole("button", { name: "Move" }).click();
  await expect(dialog).toHaveCount(0);
  expect(moves).toEqual(["/eula.txt -> /world/eula.txt"]);
});
