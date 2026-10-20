import { gzipSync } from "node:zlib";
import { expect, test } from "./panel";

// Archives are made here, byte by byte, as a server's folder would be packed
// on another host: the page reads them itself, before anything is sent.

/** A level.dat: gzipped NBT with Data.Version.Name. */
function levelDat(version: string) {
  const str = (s: string) => {
    const b = Buffer.from(s);
    const len = Buffer.alloc(2);
    len.writeUInt16BE(b.length);
    return Buffer.concat([len, b]);
  };
  const tag = (type: number, name: string, payload: Buffer) => Buffer.concat([Buffer.from([type]), str(name), payload]);
  const int = (n: number) => {
    const b = Buffer.alloc(4);
    b.writeInt32BE(n);
    return b;
  };
  const end = Buffer.from([0]);
  const data = Buffer.concat([
    tag(1, "hardcore", Buffer.from([0])),
    tag(9, "ServerBrands", Buffer.concat([Buffer.from([8]), int(1), str("Paper")])),
    tag(3, "DataVersion", int(3839)),
    tag(10, "Version", Buffer.concat([tag(3, "Id", int(3839)), tag(8, "Name", str(version)), end])),
    end,
  ]);
  return gzipSync(Buffer.concat([tag(10, "", Buffer.concat([tag(10, "Data", data), end]))]));
}

type Files = Record<string, string | Buffer>;

function tar(files: Files) {
  const blocks: Buffer[] = [];
  for (const [name, content] of Object.entries(files)) {
    const data = Buffer.from(content);
    const h = Buffer.alloc(512);
    h.write(name, 0, 100);
    h.write("0000644\0", 100);
    h.write("0001750\0", 108);
    h.write("0001750\0", 116);
    h.write(`${data.length.toString(8).padStart(11, "0")}\0`, 124);
    h.write("00000000000\0", 136);
    h.write("        ", 148);
    h.write("0", 156);
    h.write("ustar\0", 257);
    h.write("00", 263);
    let sum = 0;
    for (const b of h) sum += b;
    h.write(`${sum.toString(8).padStart(6, "0")}\0 `, 148);
    blocks.push(h, data, Buffer.alloc((512 - (data.length % 512)) % 512));
  }
  blocks.push(Buffer.alloc(1024));
  return Buffer.concat(blocks);
}

function zip(files: Files) {
  const locals: Buffer[] = [];
  const central: Buffer[] = [];
  let offset = 0;
  for (const [name, content] of Object.entries(files)) {
    const data = Buffer.from(content);
    const n = Buffer.from(name);
    const local = Buffer.alloc(30);
    local.writeUInt32LE(0x04034b50, 0);
    local.writeUInt16LE(20, 4);
    local.writeUInt32LE(data.length, 18);
    local.writeUInt32LE(data.length, 22);
    local.writeUInt16LE(n.length, 26);
    const c = Buffer.alloc(46);
    c.writeUInt32LE(0x02014b50, 0);
    c.writeUInt16LE(20, 4);
    c.writeUInt16LE(20, 6);
    c.writeUInt32LE(data.length, 20);
    c.writeUInt32LE(data.length, 24);
    c.writeUInt16LE(n.length, 28);
    c.writeUInt32LE(offset, 42);
    locals.push(local, n, data);
    central.push(c, n);
    offset += 30 + n.length + data.length;
  }
  const dir = Buffer.concat(central);
  const end = Buffer.alloc(22);
  end.writeUInt32LE(0x06054b50, 0);
  end.writeUInt16LE(Object.keys(files).length, 8);
  end.writeUInt16LE(Object.keys(files).length, 10);
  end.writeUInt32LE(dir.length, 12);
  end.writeUInt32LE(offset, 16);
  return Buffer.concat([...locals, dir, end]);
}

test("an existing server is imported from a .tar.gz, its settings read from it", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("button", { name: "Import", exact: true }).click();

  const archive = gzipSync(
    tar({
      "MyServer/server.properties": "#Minecraft server properties\nmotd=The old one\ngamemode=1\ndifficulty=hard\nmax-players=40\nlevel-name=Drehmal\nserver-port=25599\n",
      "MyServer/Drehmal/level.dat": levelDat("1.21.4"),
      "MyServer/paper-1.21.4-151.jar": "jar",
      "MyServer/plugins/WorldEdit.jar": "jar",
      "MyServer/plugins/Essentials.jar": "jar",
      "MyServer/start.sh": "java -jar paper.jar",
    }),
  );
  await page.getByLabel("Server archive").setInputFiles({ name: "myserver.tar.gz", mimeType: "application/gzip", buffer: archive });
  await expect(page.getByTestId("archive-summary")).toContainText('world "Drehmal" · 2 plugins');

  // What it ran, and how it was set up.
  await expect(page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.4");
  await expect(page.getByLabel("Display name")).toHaveValue("MyServer");
  await expect(page.getByLabel("Message of the day")).toHaveValue("The old one");
  await expect(page.getByLabel("Game mode")).toHaveValue("creative");
  await expect(page.getByLabel("Difficulty")).toHaveValue("hard");
  // It had no whitelist, so it stays open.
  await expect(page.getByLabel("Who can join")).toHaveValue("anyone");
  // Its world is there already: how one is made does not apply.
  await page.getByRole("button", { name: /World options and more/ }).click();
  await expect(page.getByLabel("Seed")).toBeDisabled();

  let sent = "";
  await page.route(`**/api/v1/orgs/${slug}/servers/myserver/import?*`, async (route) => {
    sent = route.request().url();
    const server = await api("owner").get(`/orgs/${slug}/servers/myserver`);
    await route.fulfill({ json: server });
  });
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/myserver$`));

  // Unpacked from the folder it is in, into a server made stopped and
  // started once its files were in.
  expect(new URL(sent).searchParams.get("root")).toBe("MyServer");
  const { spec } = await api("owner").get(`/orgs/${slug}/servers/myserver`);
  expect(spec).toMatchObject({ loader: "paper", version: "1.21.4", state: "Running" });
  expect(spec.properties).toMatchObject({ motd: "The old one", gamemode: "creative", difficulty: "hard", "max-players": "40" });
  expect(spec).not.toHaveProperty("autoWhitelist");
});

test("a Fabric server in a .zip keeps its loader's version", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("button", { name: "Import", exact: true }).click();
  const archive = zip({
    "server.properties": "white-list=true\n",
    "fabric-server-mc.1.21.4-loader.0.16.5-launcher.1.0.1.jar": "jar",
    "mods/sodium.jar": "jar",
    "world/level.dat": levelDat("1.21.4"),
  });
  await page.getByLabel("Server archive").setInputFiles({ name: "fabric.zip", mimeType: "application/zip", buffer: archive });
  await expect(page.getByTestId("archive-summary")).toContainText("1 mods");
  await expect(page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Fabric/ })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.4");
  // Named after the file, as it is not in a folder.
  await expect(page.getByLabel("Display name")).toHaveValue("fabric");
  // It had a whitelist: only that one lets players in, as before.
  await expect(page.getByLabel("Who can join")).toHaveValue("list");

  let root: string | null = "unset";
  await page.route(`**/api/v1/orgs/${slug}/servers/fabric/import?*`, async (route) => {
    root = new URL(route.request().url()).searchParams.get("root");
    await route.fulfill({ json: await api("owner").get(`/orgs/${slug}/servers/fabric`) });
  });
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/fabric$`));
  // The top of the archive: nothing to name.
  expect(root ?? "").toBe("");
  const { spec } = await api("owner").get(`/orgs/${slug}/servers/fabric`);
  expect(spec).toMatchObject({ loader: "fabric", version: "1.21.4", loaderVersion: "0.16.5" });
  expect(spec).not.toHaveProperty("autoWhitelist");
  expect(spec.properties).toMatchObject({ "white-list": "true" });
});

test("an import that fails takes its server away again, and says why", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("button", { name: "Import", exact: true }).click();
  await page.getByLabel("Server archive").setInputFiles({ name: "broken.zip", mimeType: "application/zip", buffer: zip({ "server.properties": "", "world/level.dat": levelDat("1.21.4"), "server.jar": "jar" }) });
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.4");
  await page.route(`**/api/v1/orgs/${slug}/servers/broken/import?*`, (route) =>
    route.fulfill({ status: 400, json: { code: "invalid", message: "Could not unpack the archive: Truncated input file." } }),
  );
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page.getByText("Could not import it. Could not unpack the archive: Truncated input file.")).toBeVisible();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/new$`));
  expect(await api("owner").get(`/orgs/${slug}/servers`)).toEqual([]);
});

test("what is not an archive is turned down before anything is sent", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("button", { name: "Import", exact: true }).click();
  await page.getByLabel("Server archive").setInputFiles({ name: "notes.zip", mimeType: "application/zip", buffer: Buffer.from("just some text, long enough to have a header's worth of bytes in it") });
  await expect(page.getByText("Use a .zip or .tar.gz file.")).toBeVisible();
  // Without one, it cannot be made.
  await page.getByLabel("Display name").fill("Nothing");
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page.getByText("Choose an archive.")).toBeVisible();
});

test("an archive picked can be taken away again", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("button", { name: "Import", exact: true }).click();
  await page.getByLabel("Server archive").setInputFiles({ name: "mine.zip", mimeType: "application/zip", buffer: zip({ "server.properties": "", "world/level.dat": levelDat("1.21.4"), "server.jar": "jar" }) });
  await expect(page.getByTestId("archive-summary")).toBeVisible();
  await page.getByRole("button", { name: "Remove the archive" }).click();
  await expect(page.getByTestId("archive-summary")).toHaveCount(0);
  await expect(page.getByRole("button", { name: "Choose an archive" })).toBeVisible();
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page.getByText("Choose an archive.")).toBeVisible();
});

test("a connection lost during an import keeps the server, which may have it", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("button", { name: "Import", exact: true }).click();
  // Packed with ./ in front of every name, as some tools do.
  await page.getByLabel("Server archive").setInputFiles({ name: "dotted.zip", mimeType: "application/zip", buffer: zip({ "./server.properties": "", "./world/level.dat": levelDat("1.21.4"), "./server.jar": "jar" }) });
  await expect(page.getByTestId("archive-summary")).toContainText('world "world"');
  let root: string | null = "unset";
  await page.route(`**/api/v1/orgs/${slug}/servers/dotted/import?*`, (route) => {
    root = new URL(route.request().url()).searchParams.get("root");
    return route.abort("connectionreset");
  });
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/dotted$`));
  await expect(page.getByText(/Lost touch with the panel while importing dotted/)).toBeVisible();
  expect(root ?? "").toBe("");
  const { spec } = await api("owner").get(`/orgs/${slug}/servers/dotted`);
  // Not started: whether its files are in is not known here.
  expect(spec.state).toBe("Stopped");
});

test("a .tar cut short is turned down", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("button", { name: "Import", exact: true }).click();
  const whole = tar({ "server.properties": "motd=x\n", "world/level.dat": levelDat("1.21.4") });
  await page.getByLabel("Server archive").setInputFiles({ name: "cut.tar", mimeType: "application/x-tar", buffer: whole.subarray(0, 700) });
  await expect(page.getByText("This archive is cut short.")).toBeVisible();
});
