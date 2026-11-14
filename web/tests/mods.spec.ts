import type { Page } from "@playwright/test";
import { content, expect, test } from "./panel";

// Mods that listen on ports of their own have them opened as they are
// added, and closed as they are taken off.

test.beforeEach(async ({ as }) => {
  await as("owner");
});

const geyserPorts = [
  { name: "geyser", port: 19132, protocol: "UDP", for: "geyser" },
  { name: "geyser-tcp", port: 19132, protocol: "TCP", for: "geyser" },
];

/** Has Modrinth's search find Geyser first, whatever is searched for. */
async function findsGeyser(page: Page) {
  await page.route("**/api/v1/catalog/modrinth/search?*", async (route) => {
    const response = await route.fetch();
    const body = await response.json();
    body.hits[0] = { ...body.hits[0], project_id: "wKkoqHrH", slug: "geyser", title: "Geyser", project_type: "plugin", categories: ["paper"] };
    await route.fulfill({ response, json: body });
  });
}

async function addGeyser(page: Page) {
  await page.getByRole("button", { name: "Add plugins" }).click();
  await page.getByRole("dialog").getByRole("button", { name: /^Geyser by/ }).click();
  await page.keyboard.press("Escape");
}

test("Geyser added in the new-server form opens its ports there, and taken off closes them", async ({ page, org, api }) => {
  const slug = await org([]);
  await findsGeyser(page);
  await page.goto(`/o/${slug}/new`);
  await page.getByLabel("Display name").fill("Bedrock");
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Game version").selectOption("1.21.4");
  await page.getByRole("button", { name: /Connection, ports and sleep/ }).click();
  await expect(page.getByLabel("Port name")).toHaveCount(0);

  await addGeyser(page);
  const plugins = page.getByRole("region", { name: "Plugins" });
  await expect(plugins.getByText("Ports open: UDP and TCP 19132")).toBeVisible();
  await expect(page.getByLabel("Port name")).toHaveCount(2);
  await expect(page.getByLabel("Port name").first()).toHaveValue("geyser");
  await expect(page.getByLabel("Port name").last()).toHaveValue("geyser-tcp");
  await expect(page.getByText("For Geyser.")).toHaveCount(2);
  // Nothing more to offer for it.
  await expect(page.getByText("Geyser is installed")).toHaveCount(0);

  await page.getByRole("button", { name: "Remove Geyser", exact: true }).click();
  await expect(page.getByLabel("Port name")).toHaveCount(0);
  await expect(page.getByText("For Geyser.")).toHaveCount(0);

  // Added again, and one of its ports closed by hand: that one stays closed.
  await addGeyser(page);
  await page.getByRole("button", { name: "Remove geyser-tcp" }).click();
  await expect(plugins.getByText("Port open: UDP 19132")).toBeVisible();
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/bedrock$`));
  const { spec } = await api("owner").get(`/orgs/${slug}/servers/bedrock`);
  expect(spec.ports).toEqual([geyserPorts[0]]);
});

test("Geyser added on the Content page opens its ports in the same save, and taken off closes them", async ({ page, org, api }) => {
  const slug = await org(["bedrock"]);
  const server = `/orgs/${slug}/servers/bedrock`;
  // One of the server's own, which stays.
  await api("owner").patch(server, { spec: { ports: [{ name: "votifier", port: 8192, protocol: "TCP" }] } });
  await findsGeyser(page);
  await page.goto(`/o/${slug}/s/bedrock/content`);
  await addGeyser(page);
  await expect.poll(async () => (await api("owner").get(server)).spec.ports).toEqual([{ name: "votifier", port: 8192, protocol: "TCP" }, ...geyserPorts]);
  const plugins = page.getByRole("region", { name: "Plugins" });
  await expect(plugins.getByText("Ports open: UDP and TCP 19132")).toBeVisible();

  await page.goto(`/o/${slug}/s/bedrock/settings`);
  await expect(page.getByLabel("Port name")).toHaveCount(3);
  await expect(page.getByText("For Geyser.")).toHaveCount(2);

  await page.goto(`/o/${slug}/s/bedrock/content`);
  await plugins.getByRole("button", { name: "Remove Geyser", exact: true }).click();
  await expect.poll(async () => (await api("owner").get(server)).spec.ports).toEqual([{ name: "votifier", port: 8192, protocol: "TCP" }]);
  await expect(plugins.getByText("No plugins.")).toBeVisible();
});

test("a web map set up is linked from the server's page, and Bedrock players are told where to join", async ({ page }) => {
  const ports = [...geyserPorts, { name: "bluemap", port: 8100, protocol: "TCP", for: "bluemap" }];
  const reached = [
    { name: "geyser", port: 19132, protocol: "UDP", publicPort: 25601, address: "survival.friends.play.example:25601" },
    { name: "geyser-tcp", port: 19132, protocol: "TCP", publicPort: 25601, address: "survival.friends.play.example:25601" },
    { name: "bluemap", port: 8100, protocol: "TCP", publicPort: 25602, address: "survival.friends.play.example:25602" },
  ];
  const change = (s: { name: string; spec: object; status: object }) => (s.name === "survival" ? { ...s, spec: { ...s.spec, ports }, status: { ...s.status, ports: reached } } : s);
  await page.route("**/api/v1/orgs/friends/servers", async (route) => route.fulfill({ json: (await (await route.fetch()).json()).map(change) }));
  await page.route("**/api/v1/orgs/friends/servers/survival", async (route) => route.fulfill({ json: change(await (await route.fetch()).json()) }));
  await page.goto("/o/friends/s/survival");
  await expect(content(page).getByRole("link", { name: "Open BlueMap" })).toHaveAttribute("href", "http://survival.friends.play.example:25602/");
  await content(page).getByRole("button", { name: "Connect" }).click();
  const dialog = page.getByRole("dialog");
  await expect(dialog.getByText("Bedrock address")).toBeVisible();
  await expect(dialog.getByText("survival.friends.play.example:25601")).toBeVisible();
});

test("a server with no map or Geyser opened shows neither", async ({ page }) => {
  await page.goto("/o/friends/s/survival");
  await expect(content(page).getByText("Survival").first()).toBeVisible();
  await expect(content(page).getByRole("link", { name: /^Open / })).toHaveCount(0);
  await content(page).getByRole("button", { name: "Connect" }).click();
  await expect(page.getByRole("dialog").getByText("Server address")).toBeVisible();
  await expect(page.getByRole("dialog").getByText("Bedrock address")).toHaveCount(0);
});
