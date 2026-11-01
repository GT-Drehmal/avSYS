import { expect, test } from "./panel";

test.beforeEach(async ({ as }) => {
  await as("owner");
});

/** Shows survival with these settings and status added, and keeps what is saved. */
async function survivalWith(page: import("@playwright/test").Page, spec: object, status: object = {}) {
  const sent: { spec: { ports?: unknown } }[] = [];
  const change = (s: { name: string; spec: object; status: object }) => (s.name === "survival" ? { ...s, spec: { ...s.spec, ...spec }, status: { ...s.status, ...status } } : s);
  await page.route("**/api/v1/orgs/friends/servers", async (route) => route.fulfill({ json: (await (await route.fetch()).json()).map(change) }));
  await page.route("**/api/v1/orgs/friends/servers/survival", async (route) => {
    if (route.request().method() === "PATCH") {
      sent.push(route.request().postDataJSON());
      return route.fulfill({ status: 503, json: { code: "test", message: "Not sent by the test." } });
    }
    return route.fulfill({ json: change(await (await route.fetch()).json()) });
  });
  return sent;
}

test("an installed mod that needs a port is said, and opening it is one press and a save", async ({ page }) => {
  const sent = await survivalWith(page, { mods: [{ source: "modrinth", kind: "plugin", project: "simple-voice-chat" }] });
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByText("Simple Voice Chat is installed and listens on UDP 24454.")).toBeVisible();
  await page.getByRole("button", { name: "Open it" }).click();
  await expect(page.getByText("Simple Voice Chat is installed")).toHaveCount(0);
  await expect(page.getByLabel("Port name")).toHaveValue("voice-chat");
  await expect(page.getByText(/Each gets its own public port/)).toBeVisible();
  // One of one's own as well.
  await page.getByRole("button", { name: "Add a port" }).click();
  await page.getByLabel("Port name").nth(1).fill("votifier");
  await page.getByLabel("Port number").nth(1).fill("8192");
  await page.getByRole("button", { name: /^Save/ }).click();
  await expect.poll(() => sent.length).toBe(1);
  expect(sent[0].spec.ports).toEqual([
    { name: "voice-chat", port: 24454, protocol: "UDP" },
    { name: "votifier", port: 8192, protocol: "TCP" },
  ]);
});

test("an open port says where it is reached, and a port is taken off", async ({ page }) => {
  const sent = await survivalWith(
    page,
    { ports: [{ name: "geyser", port: 19132, protocol: "UDP" }] },
    { ports: [{ name: "geyser", port: 19132, protocol: "UDP", publicPort: 25601, address: "survival.friends.play.example:25601" }] },
  );
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByText("survival.friends.play.example:25601")).toBeVisible();
  await page.getByRole("button", { name: "Remove geyser" }).click();
  await page.getByRole("button", { name: /^Save/ }).click();
  await expect.poll(() => sent.length).toBe(1);
  expect(sent[0].spec.ports).toEqual([]);
});

test("a port that cannot be opened says why", async ({ page }) => {
  const why = "Not open: all public ports from 25600 to 25609 are in use. It opens once one is free.";
  await survivalWith(page, { ports: [{ name: "geyser", port: 19132, protocol: "UDP" }] }, { ports: [{ name: "geyser", port: 19132, protocol: "UDP", message: why }] });
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByText(why)).toBeVisible();
  await expect(page.getByText("Being opened.")).toHaveCount(0);
});

test("a server not reachable from outside opens no ports", async ({ page }) => {
  await survivalWith(page, { expose: { type: "None" } });
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByText("Not reachable from outside.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Add a port" })).toHaveCount(0);
});

test("with eight ports no preset adds a ninth, and a sleeping server says its ports do not wake it", async ({ page }) => {
  const eight = Array.from({ length: 8 }, (_, i) => ({ name: `p${i}`, port: 1000 + i, protocol: "TCP" }));
  await survivalWith(page, { ports: eight, mods: [{ source: "modrinth", kind: "plugin", project: "geyser" }], sleep: { enabled: true, idleMinutes: 15 } });
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByRole("button", { name: "Open it" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Add a port" })).toBeDisabled();
  await expect(page.getByRole("button", { name: "BlueMap" })).toHaveCount(0);
  await expect(page.getByText(/These ports do not work while the server sleeps/)).toBeVisible();
});

test("a preset is not offered again under a name a port has, as one moved to another number", async ({ page }) => {
  await survivalWith(page, {
    mods: [{ source: "modrinth", kind: "plugin", project: "geyser" }],
    ports: [{ name: "geyser", port: 19133, protocol: "UDP" }],
  });
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByLabel("Port name")).toHaveValue("geyser");
  // Neither said to be needed, nor among the presets: it would be a second port of that name.
  await expect(page.getByText("Geyser is installed")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Geyser", exact: true })).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Simple Voice Chat", exact: true })).toBeVisible();
});

test("a new server is given its extra ports as it is made", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByLabel("Display name").fill("Voice");
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Fabric/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  await page.getByRole("button", { name: /How it is reached, ports and sleep/ }).click();
  await page.getByRole("button", { name: "Add a port" }).click();
  await page.getByLabel("Port name").fill("voice-chat");
  await page.getByLabel("Port number").fill("24454");
  await page.getByLabel("Protocol").selectOption("UDP");
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/voice$`));
  const { spec } = await api("owner").get(`/orgs/${slug}/servers/voice`);
  expect(spec.ports).toEqual([{ name: "voice-chat", port: 24454, protocol: "UDP" }]);
});

test("a copy keeps its original's extra ports, and is given others when they are changed", async ({ page, as }) => {
  await as("owner");
  const made = () => page.waitForRequest((r) => r.method() === "POST" && r.url().endsWith("/orgs/friends/servers"));
  await page.route("**/api/v1/orgs/friends/servers", (route) =>
    route.request().method() === "POST" ? route.fulfill({ status: 503, contentType: "application/json", body: JSON.stringify({ code: "test", message: "Not sent by the test." }) }) : route.continue(),
  );
  await page.goto("/o/friends/new?from=server:survival");
  await page.getByRole("button", { name: /How it is reached, ports and sleep/ }).click();
  let request = made();
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  expect((await request).postDataJSON().spec).not.toHaveProperty("ports");
  await page.getByRole("button", { name: "Add a port" }).click();
  await page.getByLabel("Port name").fill("map");
  await page.getByLabel("Port number").fill("8100");
  request = made();
  await page.getByRole("button", { name: "Duplicate", exact: true }).click();
  expect((await request).postDataJSON().spec.ports).toEqual([{ name: "map", port: 8100, protocol: "TCP" }]);
});

test("a template of settings alone keeps its extra ports, for the servers made from it", async ({ page, as, org, api }) => {
  const slug = await org([]);
  const made = await api("owner").post(`/orgs/${slug}/templates`, {
    name: "Voice",
    description: "",
    spec: { loader: "fabric", version: "1.21.4", resources: { memory: "4Gi" }, storage: { size: "10Gi" }, ports: [{ name: "voice-chat", port: 24454, protocol: "UDP" }] },
  });
  await as("owner");
  await page.goto(`/o/${slug}/templates/${made.id}/edit`);
  await page.getByRole("button", { name: /How it is reached, ports and sleep/ }).click();
  await expect(page.getByLabel("Port name")).toHaveValue("voice-chat");
  await page.getByLabel("Port number").fill("24455");
  await page.getByRole("button", { name: "Save", exact: true }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/templates$`));
  const now = await api("owner").get(`/orgs/${slug}/templates/${made.id}`);
  expect(now.spec.ports).toEqual([{ name: "voice-chat", port: 24455, protocol: "UDP" }]);
});
