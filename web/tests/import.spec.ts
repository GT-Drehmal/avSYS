import { gzipSync } from "node:zlib";
import { expect, test } from "./panel";

// Archives are made here, byte by byte, as a server's folder would be packed
// on another host: the page reads them itself, before anything is sent.

/** A level.dat: gzipped NBT with Data.Version.Name. */
function levelDat(version: string, name = "world") {
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
    // Arrays before the version, as 1.21.9 and later write the spawn point.
    tag(10, "spawn", Buffer.concat([tag(11, "pos", Buffer.concat([int(3), int(0), int(64), int(0)])), tag(8, "dimension", str("minecraft:overworld")), end])),
    tag(7, "bytes", Buffer.concat([int(2), Buffer.from([1, 2])])),
    tag(12, "longs", Buffer.concat([int(1), Buffer.alloc(8)])),
    tag(8, "LevelName", str(name)),
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
      "MyServer/server.properties": "#Minecraft server properties\nmotd=The old one\ngamemode=1\ndifficulty=hard\nmax-players=40\nlevel-name=Drehmal\nserver-port=25599\nresource-pack=https\\://example.com/pack.zip\nrequire-resource-pack=true\nresource-pack-prompt={\"text\"\\:\"Our pack\"}\n",
      "MyServer/Drehmal/level.dat": levelDat("1.21.4"),
      "MyServer/paper-1.21.4-151.jar": "jar",
      // One Modrinth has, and one it does not.
      "MyServer/plugins/WorldEdit.jar": "worldedit jar",
      "MyServer/plugins/Essentials.jar": "essentials jar",
      "MyServer/start.sh": "#!/bin/sh\n# java -Xmx16G -jar paper.jar\njava -Xms1G -Xmx4G -jar paper.jar nogui\n",
      "MyServer/ops.json": JSON.stringify([{ uuid: "853c80ef-3c37-49fd-aa49-938b674adae6", name: "jeb_", level: 4, bypassesPlayerLimit: false }]),
      "MyServer/whitelist.json": JSON.stringify([{ uuid: "853c80ef-3c37-49fd-aa49-938b674adae6", name: "jeb_" }, { uuid: "069a79f4-44e9-4726-a5be-fca90e38aaf5", name: "Notch" }]),
    }),
  );
  await page.getByLabel("Server archive").setInputFiles({ name: "myserver.tar.gz", mimeType: "application/gzip", buffer: archive });
  await expect(page.getByTestId("archive-summary")).toContainText('world "Drehmal" · 2 plugins · 1 operator · 2 whitelisted');

  // What it ran, and how it was set up.
  await expect(page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.4");
  await expect(page.getByLabel("Display name")).toHaveValue("MyServer");
  await expect(page.getByLabel("Message of the day")).toHaveValue("The old one");
  await expect(page.getByLabel("Game mode")).toHaveValue("creative");
  await expect(page.getByLabel("Difficulty")).toHaveValue("hard");
  // Its plugins are its content: one from Modrinth, to keep up to date
  // here, and one uploaded as it is.
  await expect(page.getByText("WorldEdit", { exact: true })).toBeVisible();
  await expect(page.getByText("Essentials", { exact: true })).toBeVisible();
  await expect(page.getByText(/14 B · uploaded when created/)).toBeVisible();
  // Its own lists, to see and change before it is made.
  await expect(page.getByLabel("Whitelisted players")).toHaveValue("jeb_\nNotch");
  await expect(page.getByLabel("Operators", { exact: true })).toHaveValue("jeb_");
  // It had no whitelist, so it stays open.
  await expect(page.getByLabel("Who can join")).toHaveValue("anyone");
  // As much memory as its start script gave Java, and room beside it.
  await expect(page.getByLabel("Memory (GB)")).toHaveValue("5.5");
  // Its world is there already: how one is made does not apply.
  await page.getByRole("button", { name: /World options and more/ }).click();
  await expect(page.getByLabel("Seed")).toBeDisabled();

  let sent = "";
  await page.route(`**/api/v1/orgs/${slug}/servers/myserver/import?*`, async (route) => {
    sent = route.request().url();
    const server = await api("owner").get(`/orgs/${slug}/servers/myserver`);
    await route.fulfill({ json: server });
  });
  const uploaded: string[] = [];
  await page.route(`**/api/v1/orgs/${slug}/servers/myserver/content?*`, async (route) => {
    const url = new URL(route.request().url());
    uploaded.push(`${url.searchParams.get("kind")} ${url.searchParams.get("name")}`);
    await route.fulfill({ json: await api("owner").get(`/orgs/${slug}/servers/myserver`) });
  });
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await page.getByRole("dialog").getByRole("button", { name: "Create without a whitelist" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/myserver$`));
  await expect(page.getByText("MyServer is set up")).toBeVisible();

  // Unpacked from the folder it is in, into a server made stopped and
  // started once its files were in.
  expect(new URL(sent).searchParams.get("root")).toBe("MyServer");
  // Its jars are installed from the list, not kept besides.
  expect(new URL(sent).searchParams.get("content")).toBe("listed");
  // And its lists of players from its settings, not its old files.
  expect(new URL(sent).searchParams.get("players")).toBe("listed");
  expect(uploaded).toEqual(["plugin Essentials.jar"]);
  const { spec } = await api("owner").get(`/orgs/${slug}/servers/myserver`);
  expect(spec).toMatchObject({ loader: "paper", version: "1.21.4", state: "Running" });
  expect(spec.properties).toMatchObject({ motd: "The old one", gamemode: "creative", difficulty: "hard", "max-players": "40" });
  expect(spec).not.toHaveProperty("autoWhitelist");
  // Its operators and whitelist are listed with it, to change from the panel.
  expect(spec).toMatchObject({ ops: ["jeb_"], whitelist: ["jeb_", "Notch"] });
  expect(spec.resources.memory).toBe("5632Mi");
  expect(spec.mods).toMatchObject([{ source: "modrinth", kind: "plugin", project: "P0000042", version: "V0000042", title: "WorldEdit" }]);
  expect(spec.resourcePack).toMatchObject({ url: "https://example.com/pack.zip", enforce: true, prompt: '{"text":"Our pack"}' });
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
    // Its datapacks are content too: the world's, and one beside it that
    // the world did not load, and is said to be off. One that is in both
    // places is one.
    "world/datapacks/terrain.zip": "datapack",
    "datapacks/terrain.zip": "datapack",
    "datapacks/unused.zip": "unused datapack",
    // A pack file is its resource pack.
    "world/resources.zip": "pack",
  });
  await page.getByLabel("Server archive").setInputFiles({ name: "fabric.zip", mimeType: "application/zip", buffer: archive });
  await expect(page.getByTestId("archive-summary")).toContainText("1 mod");
  await expect(page.getByText("terrain", { exact: true })).toHaveCount(1);
  await expect(page.getByText("unused", { exact: true })).toBeVisible();
  await expect(page.getByText(/15 B · uploaded when created · not on in its world before/)).toBeVisible();
  await expect(page.getByText("resources", { exact: true })).toBeVisible();
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
  const uploaded: string[] = [];
  await page.route(new RegExp(`/api/v1/orgs/${slug}/servers/fabric/(content|resource-pack)\\?`), async (route) => {
    const url = new URL(route.request().url());
    uploaded.push(`${url.searchParams.get("kind") ?? "resource-pack"} ${url.searchParams.get("name")}`);
    await route.fulfill({ json: await api("owner").get(`/orgs/${slug}/servers/fabric`) });
  });
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/fabric$`));
  await expect(page.getByText("fabric is set up")).toBeVisible();
  // The top of the archive: nothing to name.
  expect(root ?? "").toBe("");
  // Modrinth does not have them: they are uploaded as they are.
  expect(uploaded).toEqual(["mod sodium.jar", "datapack terrain.zip", "datapack unused.zip", "resource-pack resources.zip"]);
  const { spec } = await api("owner").get(`/orgs/${slug}/servers/fabric`);
  expect(spec).toMatchObject({ loader: "fabric", version: "1.21.4", loaderVersion: "0.16.5" });
  expect(spec).not.toHaveProperty("autoWhitelist");
  expect(spec.properties).toMatchObject({ "white-list": "true" });
});

test("a server run by Fabric's launcher is known by the game jar it keeps", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("button", { name: "Import", exact: true }).click();
  const archive = zip({
    "srv/server.properties": "motd=Launched\n",
    "srv/fabric-server-launch.jar": "jar",
    "srv/libraries/net/fabricmc/fabric-loader/0.16.5/fabric-loader-0.16.5.jar": "jar",
    "srv/versions/1.21.4/server-1.21.4.jar": "jar",
  });
  await page.getByLabel("Server archive").setInputFiles({ name: "launched.zip", mimeType: "application/zip", buffer: archive });
  await expect(page.getByTestId("archive-summary")).toContainText("no world");
  await expect(page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Fabric/ })).toHaveAttribute("aria-pressed", "true");
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.4");
});

test("an import the panel turns down takes its server away again, and says why", async ({ page, as, org, api }) => {
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
  await page.getByRole("dialog").getByRole("button", { name: "Create without a whitelist" }).click();
  await expect(page.getByRole("region", { name: "Setting up broken" })).toContainText("Could not import broken: Could not unpack the archive: Truncated input file. Nothing was kept.");
  expect(await api("owner").get(`/orgs/${slug}/servers`)).toEqual([]);
  // Its page, where the form went on to, says it is gone.
  await expect(page.getByText(/It may have been deleted/)).toBeVisible();
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

test("a new server starts from a world of one's own", async ({ page, as, org, api }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByLabel("Display name").fill("Island");
  await page.getByRole("button", { name: "Use a world I have" }).click();
  // A single-player save, with the Nether and the End inside it.
  const save = zip({
    "saves/Island Save/level.dat": levelDat("1.21.4", "Island"),
    "saves/Island Save/region/r.0.0.mca": "region",
    "saves/Island Save/DIM-1/region/r.0.0.mca": "nether",
  });
  await page.getByLabel("World archive").setInputFiles({ name: "island.zip", mimeType: "application/zip", buffer: save });
  await expect(page.getByTestId("world-summary")).toContainText("Last opened in 1.21.4");
  await expect(page.getByText("Island", { exact: true })).toBeVisible();
  // Opened in the version it was last opened in, and made already.
  await expect(page.getByLabel("Game version")).toHaveValue("1.21.4");
  await page.getByRole("button", { name: /World options and more/ }).click();
  await expect(page.getByLabel("Seed")).toBeDisabled();

  let sent: URLSearchParams | null = null;
  await page.route(`**/api/v1/orgs/${slug}/servers/island/import?*`, async (route) => {
    sent = new URL(route.request().url()).searchParams;
    await route.fulfill({ json: await api("owner").get(`/orgs/${slug}/servers/island`) });
  });
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/island$`));
  await expect(page.getByText("Island is set up")).toBeVisible();
  expect(sent?.get("root")).toBe("saves/Island Save");
  expect(sent?.get("world")).toBe("true");
  // A world has no lists of players of its own to leave out.
  expect(sent?.get("players")).toBeNull();
  const { spec } = await api("owner").get(`/orgs/${slug}/servers/island`);
  expect(spec).toMatchObject({ loader: "paper", version: "1.21.4", state: "Running" });
  expect(spec.properties).not.toHaveProperty("level-seed");
});

test("a world from a newer game than the one picked is warned of", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Paper/ }).click();
  await page.getByRole("button", { name: "Use a world I have" }).click();
  await page.getByLabel("World archive").setInputFiles({ name: "new.zip", mimeType: "application/zip", buffer: zip({ "level.dat": levelDat("26.3") }) });
  await expect(page.getByLabel("Game version")).toHaveValue("26.3");
  await expect(page.getByText(/newer than/)).toHaveCount(0);
  await page.getByLabel("Game version").selectOption("1.21.4");
  await expect(page.getByText("This world was last opened in 26.3, which is newer than 1.21.4. Pick 26.3 or newer.")).toBeVisible();
  // Removed, it is no longer used, and without one it says so.
  await page.getByRole("button", { name: "Remove the world" }).click();
  await expect(page.getByTestId("world-summary")).toHaveCount(0);
  await expect(page.getByText(/newer than/)).toHaveCount(0);
  await page.getByLabel("Display name").fill("Later");
  await page.getByRole("checkbox", { name: /End User License Agreement/ }).check();
  await page.getByRole("button", { name: "Create server" }).click();
  await expect(page.getByText("Choose a world, or make a new one.")).toBeVisible();
});

test("an archive with no world in it says so", async ({ page, as, org }) => {
  const slug = await org([]);
  await as("owner");
  await page.goto(`/o/${slug}/new`);
  await page.getByRole("group", { name: "Server type" }).getByRole("button", { name: /^Vanilla/ }).click();
  await page.getByRole("button", { name: "Use a world I have" }).click();
  await page.getByLabel("World archive").setInputFiles({ name: "pics.zip", mimeType: "application/zip", buffer: zip({ "a.png": "png" }) });
  await expect(page.getByText("No world in this archive: it has no level.dat.")).toBeVisible();
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
  await page.getByRole("dialog").getByRole("button", { name: "Create without a whitelist" }).click();
  await expect(page).toHaveURL(new RegExp(`/o/${slug}/s/dotted$`));
  await expect(page.getByRole("region", { name: "Setting up dotted" })).toContainText(/Lost touch with the panel while importing dotted/);
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

test("a stopped server's world can be replaced with one of one's own, saying what it is first", async ({ page, as }) => {
  await as("owner");
  // Answered once the archive is in; the panel says later how it went.
  let ended = "";
  await page.route("**/api/v1/events?*", (route) =>
    route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body: ended ? `event: operation.done\ndata: ${ended}\n\n` : "event: waiting\ndata: null\n\n" }),
  );
  // Not while it runs.
  await page.goto("/o/friends/s/survival/settings");
  await expect(page.getByRole("button", { name: "Replace the world" })).toBeDisabled();
  await expect(page.getByText("Stop the server first.")).toBeVisible();

  await page.goto("/o/friends/s/creative/settings");
  await page.getByRole("button", { name: "Replace the world" }).click();
  const dialog = page.getByRole("dialog", { name: "Replace the world?" });
  await expect(dialog).toContainText("A snapshot is taken first.");
  const replace = dialog.getByRole("button", { name: "Replace the world" });
  await expect(replace).toBeDisabled();
  // One with no world in it is turned down before anything is sent.
  await dialog.getByLabel("World archive").setInputFiles({ name: "notes.zip", mimeType: "application/zip", buffer: zip({ "notes.txt": "hello" }) });
  await expect(dialog.getByText("No world in this archive: it has no level.dat.")).toBeVisible();
  await expect(replace).toBeDisabled();

  await dialog.getByLabel("World archive").setInputFiles({ name: "map.zip", mimeType: "application/zip", buffer: zip({ "Map/level.dat": levelDat("1.21.5", "Adventure Map"), "Map/region/r.0.0.mca": "x" }) });
  await expect(dialog.getByTestId("world-summary")).toContainText('World "Adventure Map" · last opened in 1.21.5');
  await expect(dialog.getByText("This server runs 1.21.4, older than the version this world was last opened in. The game cannot open it.")).toBeVisible();
  let sent: URL | null = null;
  await page.route("**/api/v1/orgs/friends/servers/creative/import?*", async (route) => {
    sent = new URL(route.request().url());
    await route.fulfill({ status: 202, json: await (await page.request.get("/api/v1/orgs/friends/servers/creative")).json() });
  });
  await replace.click();
  await expect(dialog).toHaveCount(0);
  await expect(page.getByText(/Replaced the world/)).toHaveCount(0);
  expect(sent).not.toBeNull();
  const params = (sent as unknown as URL).searchParams;
  expect([params.get("root"), params.get("world"), params.get("replace"), params.get("wait")]).toEqual(["Map", "true", "true", null]);
  ended = JSON.stringify({ kind: "import", org: "friends", server: "creative" });
  await expect(page.getByText("Replaced the world of Creative")).toBeVisible({ timeout: 15_000 });
});

test("a world that could not be put in place after it was sent says so when the panel is done", async ({ page, as }) => {
  await as("owner");
  let ended = "";
  await page.route("**/api/v1/events?*", (route) =>
    route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body: ended ? `event: operation.done\ndata: ${ended}\n\n` : "event: waiting\ndata: null\n\n" }),
  );
  await page.goto("/o/friends/s/creative/settings");
  await page.getByRole("button", { name: "Replace the world" }).click();
  const dialog = page.getByRole("dialog", { name: "Replace the world?" });
  await dialog.getByLabel("World archive").setInputFiles({ name: "map.zip", mimeType: "application/zip", buffer: zip({ "level.dat": levelDat("1.21"), "region/r.0.0.mca": "x" }) });
  await expect(dialog.getByTestId("world-summary")).toBeVisible();
  await page.route("**/api/v1/orgs/friends/servers/creative/import?*", async (route) =>
    route.fulfill({ status: 202, json: await (await page.request.get("/api/v1/orgs/friends/servers/creative")).json() }),
  );
  await dialog.getByRole("button", { name: "Replace the world" }).click();
  await expect(dialog).toHaveCount(0);
  // Another tab, or a page that did not send it, says nothing of it.
  const other = await page.context().newPage();
  await other.route("**/api/v1/events?*", (route) =>
    route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body: ended ? `event: operation.done\ndata: ${ended}\n\n` : "event: waiting\ndata: null\n\n" }),
  );
  await other.goto("/o/friends/s/creative");
  ended = JSON.stringify({ kind: "import", org: "friends", server: "creative", error: "the current world could not be saved first, so nothing was changed: the server's files are not reachable" });
  await expect(page.getByText("Replacing the world of Creative failed: the current world could not be saved first, so nothing was changed: the server's files are not reachable")).toBeVisible({ timeout: 15_000 });
  await other.waitForTimeout(4_000);
  await expect(other.getByText(/Replacing the world/)).toHaveCount(0);
});

test("a world the panel would not take in place of a server's says why, and nothing is changed", async ({ page, as }) => {
  await as("owner");
  await page.goto("/o/friends/s/creative/settings");
  await page.getByRole("button", { name: "Replace the world" }).click();
  const dialog = page.getByRole("dialog", { name: "Replace the world?" });
  // An older world is upgraded by the game: nothing to warn of.
  await dialog.getByLabel("World archive").setInputFiles({ name: "map.zip", mimeType: "application/zip", buffer: zip({ "level.dat": levelDat("1.21"), "region/r.0.0.mca": "x" }) });
  await expect(dialog.getByTestId("world-summary")).toContainText("last opened in 1.21 ·");
  await expect(dialog.getByText(/This server runs/)).toHaveCount(0);
  await page.route("**/api/v1/orgs/friends/servers/creative/import?*", (route) =>
    route.fulfill({ status: 502, json: { code: "snapshot_failed", message: "The current world could not be saved first, so nothing was changed." } }),
  );
  await dialog.getByRole("button", { name: "Replace the world" }).click();
  await expect(dialog.getByText("The current world could not be saved first, so nothing was changed.")).toBeVisible();
  await expect(dialog).toBeVisible();
});

test("a replaced world whose end is heard before the panel's answer is still told of, and only then", async ({ page, as }) => {
  await as("owner");
  const done = JSON.stringify({ kind: "import", org: "friends", server: "creative" });
  // The end is said on every stream from the start, as for an import by
  // someone else a moment ago: not told of, as nothing here waits on it.
  await page.route("**/api/v1/events?*", (route) =>
    route.fulfill({ status: 200, headers: { "Content-Type": "text/event-stream" }, body: `event: operation.done\ndata: ${done}\n\n` }),
  );
  await page.goto("/o/friends/s/creative/settings");
  await page.waitForTimeout(4_000);
  await expect(page.getByText(/Replaced the world/)).toHaveCount(0);

  await page.getByRole("button", { name: "Replace the world" }).click();
  const dialog = page.getByRole("dialog", { name: "Replace the world?" });
  await dialog.getByLabel("World archive").setInputFiles({ name: "map.zip", mimeType: "application/zip", buffer: zip({ "level.dat": levelDat("1.21"), "region/r.0.0.mca": "x" }) });
  await expect(dialog.getByTestId("world-summary")).toBeVisible();
  // Answered only after the stream has said it ended.
  let answer = () => {};
  const answered = new Promise<void>((resolve) => {
    answer = resolve;
  });
  await page.route("**/api/v1/orgs/friends/servers/creative/import?*", async (route) => {
    await answered;
    await route.fulfill({ status: 202, json: await (await page.request.get("/api/v1/orgs/friends/servers/creative")).json() });
  });
  await dialog.getByRole("button", { name: "Replace the world" }).click();
  await page.waitForTimeout(4_000);
  await expect(page.getByText(/Replaced the world/)).toHaveCount(0);
  answer();
  await expect(page.getByText("Replaced the world of Creative")).toBeVisible();
  await expect(page.getByText("Replaced the world of Creative")).toHaveCount(1);
});

test("a stopped server held while the panel puts its files in place cannot be started, or its world replaced", async ({ page, as }) => {
  await as("owner");
  const held = (s: { name: string; status: object }) => (s.name === "creative" ? { ...s, status: { ...s.status, phase: "Stopped", message: "Importing files." } } : s);
  await page.route("**/api/v1/orgs/friends/servers", async (route) => route.fulfill({ json: (await (await route.fetch()).json()).map(held) }));
  await page.route("**/api/v1/orgs/friends/servers/creative", async (route) => route.fulfill({ json: held(await (await route.fetch()).json()) }));
  await page.goto("/o/friends/s/creative/settings");
  await expect(page.getByRole("main").getByRole("button", { name: "Start", exact: true })).toBeDisabled();
  await expect(page.getByRole("button", { name: "Replace the world" })).toBeDisabled();
});
