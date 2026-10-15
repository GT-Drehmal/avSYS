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
  await expect(page.getByText(/Each gets a public port of its own/)).toBeVisible();
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

test("a server not reachable from outside opens no ports", async ({ page }) => {
  await survivalWith(page, { expose: { type: "None" } });
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByText("Not reachable from outside, so no port is opened.")).toBeVisible();
  await expect(page.getByRole("button", { name: "Add a port" })).toHaveCount(0);
});
