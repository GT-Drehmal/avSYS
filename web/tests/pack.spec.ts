import { expect, test } from "./panel";

const sha = "ab".repeat(20);

test.beforeEach(async ({ as }) => {
  await as("owner");
});

test("a resource pack is uploaded from a server's settings, then replaced or removed", async ({ page }) => {
  let uploaded = false;
  const asked: string[] = [];
  const withPack = (server: { spec: object }) =>
    uploaded
      ? { ...server, spec: { ...server.spec, resourcePack: { url: `http://panel.test/packs/${sha}.zip`, sha1: sha, uploaded: { name: "faithful.zip", size: 3 * 1024 * 1024, time: new Date().toISOString() } } } }
      : server;
  await page.route("**/api/v1/orgs/friends/servers/survival", async (route) => {
    if (route.request().method() !== "GET") return route.continue();
    return route.fulfill({ json: withPack(await (await route.fetch()).json()) });
  });
  await page.route("**/api/v1/orgs/friends/servers/survival/resource-pack*", async (route) => {
    asked.push(`${route.request().method()} ${new URL(route.request().url()).search}`);
    uploaded = route.request().method() === "PUT";
    const server = await (await page.request.get("/api/v1/orgs/friends/servers/survival")).json();
    return route.fulfill({ json: withPack(server) });
  });
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByRole("button", { name: "Upload a pack" })).toBeVisible();
  await page.getByLabel("Resource pack file").setInputFiles({ name: "faithful.zip", mimeType: "application/zip", buffer: Buffer.from("PK") });
  await expect.poll(() => asked).toEqual(["PUT ?name=faithful.zip"]);
  await expect(page.getByText("faithful.zip", { exact: true })).toBeVisible();
  await expect(page.getByText(/3\.0 MB · uploaded .* · served by the panel/)).toBeVisible();
  // Its address is the panel's, not one to type.
  await expect(page.getByLabel("Or its URL")).toHaveCount(0);
  await expect(page.getByRole("switch", { name: "Required" })).toBeVisible();

  await page.getByRole("button", { name: "Remove" }).click();
  await expect.poll(() => asked).toEqual(["PUT ?name=faithful.zip", "DELETE "]);
  await expect(page.getByRole("button", { name: "Upload a pack" })).toBeVisible();
  await expect(page.getByLabel("Or its URL")).toBeVisible();
});

test("saving other settings keeps an uploaded pack as it is", async ({ page }) => {
  const sent: { spec: { resourcePack?: unknown } }[] = [];
  const pack = { url: `http://panel.test/packs/${sha}.zip`, sha1: sha, uploaded: { name: "p.zip", size: 1024, time: new Date().toISOString() } };
  await page.route("**/api/v1/orgs/friends/servers/survival", async (route) => {
    if (route.request().method() === "PATCH") {
      sent.push(route.request().postDataJSON());
      return route.fulfill({ status: 503, json: { code: "test", message: "Not sent by the test." } });
    }
    const server = await (await route.fetch()).json();
    return route.fulfill({ json: { ...server, spec: { ...server.spec, resourcePack: pack } } });
  });
  // The page reads it from the organization's list.
  await page.route("**/api/v1/orgs/friends/servers", async (route) => {
    const list = await (await route.fetch()).json();
    return route.fulfill({ json: list.map((s: { name: string; spec: object }) => (s.name === "survival" ? { ...s, spec: { ...s.spec, resourcePack: pack } } : s)) });
  });
  await page.goto("/o/friends/s/survival/settings");
  await page.getByRole("switch", { name: "Required" }).click();
  await page.getByRole("button", { name: /^Save/ }).click();
  await expect.poll(() => sent.length).toBe(1);
  // Its address and hash as they were, required now; nothing said of the upload, which the panel keeps.
  expect(sent[0].spec.resourcePack).toEqual({ url: pack.url, sha1: sha, enforce: true, prompt: "" });
});

test("a pack uploaded with other changes unsaved is kept when they are saved", async ({ page }) => {
  let uploaded = false;
  const sent: { spec: { resourcePack?: unknown; displayName?: string } }[] = [];
  const pack = { url: `http://panel.test/packs/${sha}.zip`, sha1: sha, uploaded: { name: "new.zip", size: 1024, time: new Date().toISOString() } };
  const withPack = (s: { name: string; spec: object }) => (uploaded && s.name === "survival" ? { ...s, spec: { ...s.spec, resourcePack: pack } } : s);
  await page.route("**/api/v1/orgs/friends/servers", async (route) => route.fulfill({ json: (await (await route.fetch()).json()).map(withPack) }));
  await page.route("**/api/v1/orgs/friends/servers/survival", async (route) => {
    if (route.request().method() === "PATCH") {
      sent.push(route.request().postDataJSON());
      return route.fulfill({ status: 503, json: { code: "test", message: "Not sent by the test." } });
    }
    return route.fulfill({ json: withPack(await (await route.fetch()).json()) });
  });
  await page.route("**/api/v1/orgs/friends/servers/survival/resource-pack*", async (route) => {
    uploaded = true;
    const server = await (await page.request.get("/api/v1/orgs/friends/servers/survival")).json();
    return route.fulfill({ json: withPack(server) });
  });
  await page.goto("/o/friends/s/survival/settings");
  await page.getByLabel("Display name").fill("Survival, renamed");
  await page.getByLabel("Resource pack file").setInputFiles({ name: "new.zip", mimeType: "application/zip", buffer: Buffer.from("PK") });
  await expect(page.getByText("new.zip", { exact: true })).toBeVisible();
  // The rename is still there to save, and saving it keeps the new pack.
  await expect(page.getByLabel("Display name")).toHaveValue("Survival, renamed");
  await page.getByRole("button", { name: /^Save/ }).click();
  await expect.poll(() => sent.length).toBe(1);
  expect(sent[0].spec.displayName).toBe("Survival, renamed");
  expect(sent[0].spec.resourcePack).toEqual({ url: pack.url, sha1: sha, enforce: false, prompt: "" });
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
  await page.goto("/o/friends/s/survival/settings");
  const pack = { name: "big.zip", mimeType: "application/zip", buffer: Buffer.alloc(2 * 1024 * 1024) };
  await page.getByLabel("Resource pack file").setInputFiles(pack);
  const status = page.getByRole("status").filter({ hasText: /^\d+% · .* of 2\.0 MB\s*big\.zip/ });
  await expect(status).toBeVisible();
  await expect(status.getByRole("progressbar", { name: "Upload progress" })).toBeVisible();
  await expect(page.getByRole("button", { name: "Upload a pack" })).toBeDisabled();
  await status.getByRole("button", { name: "Cancel" }).click();
  await expect(status).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Upload a pack" })).toBeEnabled();
  // Stopping it is not a failure to tell of.
  await expect(page.getByText("Upload cancelled.")).toHaveCount(0);

  refuse = true;
  await page.getByLabel("Resource pack file").setInputFiles(pack);
  await expect(page.getByText("There is no pack.mcmeta at the top of that zip, so it is not a resource pack.")).toBeVisible();
  await expect(status).toHaveCount(0);
  expect(asked).toEqual(["PUT", "PUT"]);
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
