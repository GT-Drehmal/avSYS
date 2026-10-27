import type { ContentKind, Loader } from "./api";
import { ArchiveError, type Contents, type Entry, gunzip, readArchive } from "./archive";
import { parseProperties } from "./properties";

/** What an archive of a server's folder says about the server. */
export interface Found {
  /** The folder in the archive that is the server's, "" for its top. */
  root: string;
  /** How large the server's files are, unpacked. */
  size: number;
  loader: Loader | null;
  /** The game version, "" when it could not be told. */
  version: string;
  loaderVersion: string;
  /** The world's folder, "" when there is none. */
  world: string;
  properties: Record<string, string>;
  plugins: number;
  mods: number;
  /** The players its ops.json and whitelist.json name. */
  ops: string[];
  whitelist: string[];
  /** The most memory its start scripts gave Java (-Xmx), in MiB; 0 if none say. */
  heap: number;
  /** Its mods, plugins and its world's datapacks, read to be listed as its content. */
  jars: Jar[];
  /** How many of them were too large to read: then none are listed. */
  unread: number;
  /** A resource pack file it has, the world's or in resourcepacks/. */
  pack: Jar | null;
}

export interface Jar {
  name: string;
  kind: ContentKind | "resourcepack";
  sha1: string;
  data: Uint8Array;
  /** A datapack the world did not load: it was beside it, not in it. */
  unused?: boolean;
}

/** As large a file as can be added as content. */
const maxJar = 256 << 20;
// A mod or a plugin, a .jar right in one of those folders, a datapack or a
// resource pack.
const jarIn = /(^|\/)((mods|plugins)\/[^/]+\.jar|(datapacks|resourcepacks)\/[^/]+\.zip|resources\.zip)$/i;

const interesting = new Set(["server.properties", "level.dat", "version_history.json", "ops.json", "whitelist.json"]);
// What started it, which says how much memory it was given.
const starts = /(\.(sh|bat|cmd|command|ps1)|^user_jvm_args\.txt|^start)$/i;
const base = (p: string) => p.slice(p.lastIndexOf("/") + 1);
const dirname = (p: string) => (p.includes("/") ? p.slice(0, p.lastIndexOf("/")) : "");
const depth = (p: string) => p.split("/").length;
// The server's own jar, right in its folder or one down, which says inside
// which game it is and whose.
const serverJar = (path: string, size: number) => /\.jar$/i.test(path) && depth(path) <= 2 && !/^(mods|plugins|libraries|versions)\//i.test(path) && size <= 128 << 20;

/** Reads an archive of a server's folder and makes out what it can. */
export async function inspectServer(file: File): Promise<Found> {
  const contents = await readArchive(file, (path, size) =>
      !path.startsWith("__MACOSX/") &&
      depth(path) <= 5 &&
      ((interesting.has(base(path)) && size < 8 << 20) || (starts.test(base(path)) && size < 64 << 10) || (jarIn.test(path) && size <= maxJar) || serverJar(path, size)),
  );
  return await examine(contents);
}

async function examine({ entries, files }: Contents): Promise<Found> {
  const real = entries.filter((e) => !e.path.startsWith("__MACOSX/"));
  const root = findRoot(real);
  const under = (p: string) => (root === "" ? p : p.startsWith(`${root}/`) ? p.slice(root.length + 1) : null);
  const inside: Entry[] = [];
  for (const e of real) {
    const rel = under(e.path);
    if (rel) inside.push({ ...e, path: rel });
  }
  const at = (rel: string) => files.get(root === "" ? rel : `${root}/${rel}`);
  const text = (rel: string) => {
    const data = at(rel);
    return data ? new TextDecoder().decode(data) : "";
  };
  const properties = parseProperties(text("server.properties"));

  // The world: the folder level-name names, or else the first that holds a
  // level.dat.
  const worlds = inside.filter((e) => !e.dir && depth(e.path) === 2 && base(e.path) === "level.dat").map((e) => dirname(e.path));
  const world = worlds.find((w) => w === properties["level-name"]) ?? worlds.find((w) => w === "world") ?? worlds[0] ?? "";
  let version = "";
  const level = world ? at(`${world}/level.dat`) : undefined;
  if (level) version = await worldVersion(level).catch(() => "");

  let software = softwareOf(inside, text("version_history.json"));
  // Not told by the names of its files: asked of its jar.
  if (!software.version && (software.loader === null || software.loader === "vanilla" || software.loader === "paper" || software.loader === "purpur" || software.loader === "folia")) {
    for (const e of inside) {
      const data = !e.dir && depth(e.path) === 1 && /\.jar$/i.test(e.path) ? at(e.path) : undefined;
      const says = data && (await jarSays(data).catch(() => null));
      if (!says) continue;
      // A Paper found otherwise stays Paper, as one whose jar was renamed.
      software = { loader: software.loader === null || software.loader === "vanilla" ? says.loader : software.loader, version: says.version, loaderVersion: "" };
      break;
    }
  }
  return {
    root,
    size: inside.reduce((sum, e) => sum + e.size, 0),
    loader: software.loader,
    // The world says which version last opened it, which is what it must
    // be opened with now.
    version: version || software.version,
    loaderVersion: software.loaderVersion,
    world,
    properties,
    plugins: inside.filter((e) => !e.dir && depth(e.path) === 2 && e.path.startsWith("plugins/") && e.path.endsWith(".jar")).length,
    mods: inside.filter((e) => !e.dir && depth(e.path) === 2 && e.path.startsWith("mods/") && e.path.endsWith(".jar")).length,
    ops: players(text("ops.json")),
    whitelist: players(text("whitelist.json")),
    // The world the game opens there: the one level-name names, which is
    // made world, or world.
    ...(await jarsOf(inside, at, worlds.includes(properties["level-name"] ?? "") ? properties["level-name"] : "world")),
    heap: Math.max(0, ...inside.filter((e) => !e.dir && depth(e.path) === 1 && starts.test(e.path)).map((e) => maxHeap(text(e.path)))),
  };
}

/**
 * The mods and plugins right in mods/ and plugins/, the datapacks of the
 * world and any beside it, and a resource pack, with their SHA-1.
 */
async function jarsOf(inside: Entry[], at: (rel: string) => Uint8Array | undefined, world: string): Promise<{ jars: Jar[]; unread: number; pack: Jar | null }> {
  const jars: Jar[] = [];
  const packs: Jar[] = [];
  let unread = 0;
  const kindOf = (p: string): Pick<Jar, "kind" | "unused"> | null => {
    if (/^mods\/[^/]+\.jar$/.test(p)) return { kind: "mod" };
    if (/^plugins\/[^/]+\.jar$/.test(p)) return { kind: "plugin" };
    if (p.startsWith(`${world}/datapacks/`) && /^[^/]+\.zip$/.test(p.slice(world.length + 11))) return { kind: "datapack" };
    if (/^datapacks\/[^/]+\.zip$/.test(p)) return { kind: "datapack", unused: true };
    if (p === `${world}/resources.zip` || p === "resources.zip" || /^resourcepacks\/[^/]+\.zip$/.test(p)) return { kind: "resourcepack" };
    return null;
  };
  for (const e of inside) {
    const found = e.dir ? null : kindOf(e.path);
    if (!found) continue;
    const data = at(e.path);
    if (!data) {
      if (found.kind !== "resourcepack") unread++;
      continue;
    }
    const digest = new Uint8Array(await crypto.subtle.digest("SHA-1", data.slice()));
    const sha1 = Array.from(digest, (b) => b.toString(16).padStart(2, "0")).join("");
    (found.kind === "resourcepack" ? packs : jars).push({ name: base(e.path), sha1, data, ...found });
  }
  // A datapack beside the world that is in it too is the same one.
  const inWorld = jars.filter((j) => j.kind === "datapack" && !j.unused);
  const once = jars.filter((j) => !j.unused || !inWorld.some((w) => w.name === j.name || w.sha1 === j.sha1));
  // The world's own pack first.
  return { jars: once, unread, pack: packs.find((p) => p.name === "resources.zip") ?? packs[0] ?? null };
}

/** The largest -Xmx in a start script, in MiB; 0 if it has none. Lines made comments do not count. */
export function maxHeap(script: string): number {
  let most = 0;
  const live = script
    .split("\n")
    .filter((line) => !/^\s*(#|::|rem\b)/i.test(line))
    .join("\n");
  for (const [, n, unit] of live.matchAll(/-Xmx(\d+)([kKmMgG]?)\b/g)) {
    const mib = { k: Number(n) / 1024, m: Number(n), g: Number(n) * 1024, "": Number(n) / 1024 ** 2 }[unit.toLowerCase() as "k" | "m" | "g" | ""];
    most = Math.max(most, mib);
  }
  return Math.round(most);
}

/** The names in one of the game's lists of players; none if it is not one. */
function players(json: string): string[] {
  try {
    const list: unknown = JSON.parse(json);
    if (!Array.isArray(list)) return [];
    const names = list.map((p) => (p && typeof p === "object" && "name" in p ? String(p.name) : "")).filter((n) => /^[A-Za-z0-9_]{2,16}$/.test(n));
    return [...new Set(names)];
  } catch {
    return [];
  }
}

/**
 * The folder the server is in: the one with its server.properties, or the
 * one holding its world, or a single folder everything is in.
 */
function findRoot(entries: Entry[]): string {
  const shallowest = (name: string) =>
    entries
      .filter((e) => !e.dir && base(e.path) === name)
      .map((e) => dirname(e.path))
      .sort((a, b) => depth(a) - depth(b) || a.length - b.length)[0];
  const props = shallowest("server.properties");
  if (props !== undefined) return props;
  const level = shallowest("level.dat");
  if (level !== undefined) return dirname(level);
  const tops = new Set(entries.map((e) => e.path.split("/")[0]));
  if (tops.size === 1) {
    const [only] = tops;
    if (entries.some((e) => e.path.startsWith(`${only}/`))) return only;
  }
  return "";
}

/** The server's software, from its jar's name, its libraries and its own files. */
function softwareOf(entries: Entry[], history: string): { loader: Loader | null; version: string; loaderVersion: string } {
  const paths = entries.map((e) => e.path);
  const tops = new Set(paths.filter((p) => !p.includes("/")).map((p) => p.toLowerCase()));
  const has = (prefix: string) => paths.some((p) => p === prefix || p.startsWith(`${prefix}/`));
  const match = (re: RegExp) => {
    for (const p of paths) {
      const m = re.exec(p);
      if (m) return m;
    }
    return null;
  };
  const jar = (re: RegExp) => {
    for (const t of tops) {
      const m = re.exec(t);
      if (m) return m;
    }
    return null;
  };

  let m = match(/^libraries\/net\/neoforged\/neoforge\/([^/]+)\//);
  if (m) return { loader: "neoforge", version: neoForgeGame(m[1]), loaderVersion: m[1] };
  m = match(/^libraries\/net\/minecraftforge\/forge\/([^/-]+)-([^/]+)\//);
  if (m) return { loader: "forge", version: m[1], loaderVersion: m[2] };
  m = jar(/^fabric-server-mc\.(.+)-loader\.(.+)-launcher\..+\.jar$/);
  if (m) return { loader: "fabric", version: m[1], loaderVersion: m[2] };
  if (has(".fabric") || tops.has("fabric-server-launch.jar") || has("libraries/net/fabricmc")) {
    // The game's own jar, which Fabric's launcher keeps by its version.
    return { loader: "fabric", version: match(/^versions\/([^/]+)\/server-\1\.jar$/)?.[1] ?? "", loaderVersion: match(/^libraries\/net\/fabricmc\/fabric-loader\/([^/]+)\//)?.[1] ?? "" };
  }
  if (tops.has("quilt-server-launch.jar") || has("libraries/org/quiltmc")) {
    return { loader: "quilt", version: "", loaderVersion: match(/^libraries\/org\/quiltmc\/quilt-loader\/([^/]+)\//)?.[1] ?? "" };
  }
  const game = /\(MC: ([^)]+)\)/.exec(history)?.[1] ?? "";
  m = jar(/^folia-(\d[^-]*)-.+\.jar$/);
  if (m || /Folia/i.test(history)) return { loader: "folia", version: m?.[1] ?? game, loaderVersion: "" };
  m = jar(/^purpur-(\d[^-]*)-.+\.jar$/);
  if (m || tops.has("purpur.yml") || /Purpur/i.test(history)) return { loader: "purpur", version: m?.[1] ?? game, loaderVersion: "" };
  m = jar(/^paper-(\d[^-]*)-.+\.jar$/);
  // Spigot's and Bukkit's plugins run on Paper.
  if (m || game || has("config/paper-global.yml") || tops.has("paper.yml") || tops.has("spigot.yml") || tops.has("bukkit.yml") || has("plugins")) {
    return { loader: "paper", version: m?.[1] ?? game, loaderVersion: "" };
  }
  m = jar(/^minecraft_server\.(.+)\.jar$/);
  if (m) return { loader: "vanilla", version: m[1], loaderVersion: "" };
  // Mods with nothing to say which loader they are for.
  if (has("mods")) return { loader: null, version: "", loaderVersion: "" };
  if (tops.has("server.jar")) return { loader: "vanilla", version: "", loaderVersion: "" };
  return { loader: null, version: "", loaderVersion: "" };
}

/**
 * The game and software a server jar is for, as it says inside: Mojang's,
 * Paper's and Purpur's list the game's jar they hold, by version and name,
 * and older ones of Mojang's say which version they are.
 */
async function jarSays(data: Uint8Array): Promise<{ loader: Loader; version: string } | null> {
  const { files } = await readArchive(new File([data as BlobPart], "server.jar"), (p) => p === "META-INF/versions.list" || p === "version.json");
  const list = files.get("META-INF/versions.list");
  if (list) {
    const [, id = "", path = ""] = new TextDecoder().decode(list).split("\n")[0].trim().split("\t");
    const name = base(path);
    if (id) return { loader: name.startsWith("paper-") ? "paper" : name.startsWith("purpur-") ? "purpur" : name.startsWith("folia-") ? "folia" : "vanilla", version: id };
  }
  const version = files.get("version.json");
  if (version) {
    const id = (JSON.parse(new TextDecoder().decode(version)) as { id?: unknown }).id;
    if (typeof id === "string" && id) return { loader: "vanilla", version: id };
  }
  return null;
}

/** The game version a NeoForge version is for: 21.1.77 is for 1.21.1, 21.0.x for 1.21. */
function neoForgeGame(v: string) {
  const [major, minor] = v.split(".");
  if (!major || minor === undefined || Number(major) < 20) return "";
  return minor === "0" ? `1.${major}` : `1.${major}.${minor}`;
}

/** A world in an archive. */
export interface FoundWorld {
  /** The folder in the archive that holds its level.dat, "" for the top. */
  root: string;
  /** Its name in the game. */
  name: string;
  /** The game version that last opened it, "" when it could not be told. */
  version: string;
  /** How large it is unpacked, the Nether and the End beside it included. */
  size: number;
  /** The picture it has in its folder, as a save made in the game does. */
  icon?: Uint8Array;
}

/** Finds the world in an archive, wherever it is in it. */
export async function inspectWorld(file: File): Promise<FoundWorld> {
  const { entries, files } = await readArchive(file, (path, size) => (base(path) === "level.dat" || base(path) === "icon.png") && size < 8 << 20 && !path.startsWith("__MACOSX/"));
  const levels = entries.filter((e) => !e.dir && base(e.path) === "level.dat" && !e.path.startsWith("__MACOSX/"));
  // The shallowest: the Nether's folder in Paper's way has one of its own.
  const level = levels.sort((a, b) => depth(a.path) - depth(b.path) || a.path.length - b.path.length)[0];
  if (!level) throw new ArchiveError("No world in this archive: it has no level.dat.");
  const root = dirname(level.path);
  const data = files.get(level.path);
  const read = data ? await levelInfo(data).catch(() => ({ name: "", version: "" })) : { name: "", version: "" };
  // Paper keeps the Nether and the End beside the world, as world_nether and world_the_end.
  const beside = root ? [`${root}_nether/`, `${root}_the_end/`] : [];
  const size = entries.filter((e) => (root === "" || e.path.startsWith(`${root}/`) || beside.some((b) => e.path.startsWith(b))) && !e.path.startsWith("__MACOSX/")).reduce((sum, e) => sum + e.size, 0);
  const icon = files.get(root ? `${root}/icon.png` : "icon.png");
  return { root, name: read.name || base(root) || file.name.replace(/\.(zip|tar|tgz|tar\.gz)$/i, ""), version: read.version, size, ...(icon ? { icon } : {}) };
}

/** A world's name and the version that last opened it, from its level.dat. */
async function levelInfo(levelDat: Uint8Array) {
  const data = levelDat[0] === 0x1f && levelDat[1] === 0x8b ? await gunzip(levelDat) : levelDat;
  const at = (path: string[]) => {
    const nbt = new Nbt(data);
    if (nbt.u8() !== 10) return "";
    nbt.string();
    return nbt.find(path) ?? "";
  };
  return { name: at(["Data", "LevelName"]), version: at(["Data", "Version", "Name"]) };
}

/** The version that last opened a world, from its level.dat: Data.Version.Name. */
export async function worldVersion(levelDat: Uint8Array): Promise<string> {
  return (await levelInfo(levelDat)).version;
}

/** Just enough of NBT to find one string in a compound, passing over the rest. */
class Nbt {
  private p = 0;
  private view: DataView;
  constructor(private b: Uint8Array) {
    this.view = new DataView(b.buffer, b.byteOffset, b.byteLength);
  }
  u8() {
    return this.b[this.p++];
  }
  private i32() {
    const v = this.view.getInt32(this.p);
    this.p += 4;
    return v;
  }
  string() {
    const len = this.view.getUint16(this.p);
    this.p += 2;
    const s = new TextDecoder().decode(this.b.subarray(this.p, this.p + len));
    this.p += len;
    return s;
  }

  /** Inside a compound, follows names down to a string. */
  find(path: string[]): string | null {
    for (;;) {
      if (this.p >= this.b.length) return null;
      const type = this.u8();
      if (type === 0) return null;
      const name = this.string();
      if (name === path[0]) {
        if (path.length === 1) return type === 8 ? this.string() : null;
        if (type !== 10) return null;
        return this.find(path.slice(1));
      }
      this.skip(type);
    }
  }

  private skip(type: number) {
    switch (type) {
      case 1:
        this.p += 1;
        return;
      case 2:
        this.p += 2;
        return;
      case 3:
      case 5:
        this.p += 4;
        return;
      case 4:
      case 6:
        this.p += 8;
        return;
      // The length is read before it is added: "this.p += this.i32()"
      // would add it to where p was before the length.
      case 7: {
        const n = this.i32();
        this.p += n;
        return;
      }
      case 8:
        this.string();
        return;
      case 9: {
        const inner = this.u8();
        const n = this.i32();
        for (let i = 0; i < n; i++) this.skip(inner);
        return;
      }
      case 10:
        for (;;) {
          const t = this.u8();
          if (t === 0 || this.p >= this.b.length) return;
          this.string();
          this.skip(t);
        }
      case 11: {
        const n = this.i32();
        this.p += 4 * n;
        return;
      }
      case 12: {
        const n = this.i32();
        this.p += 8 * n;
        return;
      }
      default:
        throw new Error("bad NBT");
    }
  }
}
