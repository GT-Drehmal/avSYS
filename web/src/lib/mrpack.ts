/**
 * Reads a Modrinth modpack file (.mrpack) in the browser: a zip with
 * modrinth.index.json, which says what the pack is, what it is built for and
 * the files it brings, each with where it runs. Its own files are in
 * overrides/ (and server-overrides/), a picture among them maybe.
 */
import type { Loader } from "./api";
import { ArchiveError, readArchive } from "./archive";

/** A file the pack lists. One for players only is left off the server. */
export interface PackFile {
  path: string;
  name: string;
  server: boolean;
  /** The Modrinth project and version it is from, where known, and the project's slug, name and picture. */
  project?: string;
  version?: string;
  slug?: string;
  title?: string;
  iconUrl?: string;
}

export interface FoundPack {
  name: string;
  versionId: string;
  gameVersion: string;
  loader: Loader;
  /** The pack's picture, if it has one. */
  icon: Uint8Array | null;
  files: PackFile[];
}

/** The loaders a pack can say it needs, by its name for each. */
const loaders: Record<string, Loader> = { "fabric-loader": "fabric", "quilt-loader": "quilt", forge: "forge", neoforge: "neoforge" };

/** Where a pack's picture may be, the one the server shows first. */
const icons = ["server-overrides/server-icon.png", "overrides/server-icon.png", "overrides/icon.png", "icon.png"];

const notPack = "This is not a Modrinth modpack (.mrpack).";

interface Index {
  game?: unknown;
  name?: unknown;
  versionId?: unknown;
  dependencies?: Record<string, unknown>;
  files?: { path?: unknown; env?: { server?: unknown }; downloads?: unknown }[];
}

/** A file on Modrinth's CDN; the project it is of is its first part, and the version, where it says, the next. */
const modrinthFile = /^https:\/\/cdn\.modrinth\.com\/data\/([A-Za-z0-9]{8})\/(?:versions\/([A-Za-z0-9]{8})\/)?/;

const text = (v: unknown) => (typeof v === "string" ? v.trim() : "");

/** Reads a pack, or says plainly why it is not one the server can install. */
export async function inspectPack(file: File): Promise<FoundPack> {
  const head = new Uint8Array(await file.slice(0, 2).arrayBuffer());
  if (head[0] !== 0x50 || head[1] !== 0x4b) throw new ArchiveError(notPack);
  let contents: Awaited<ReturnType<typeof readArchive>>;
  try {
    contents = await readArchive(file, (path, size) => (path === "modrinth.index.json" && size < 32 << 20) || (icons.includes(path) && size < 4 << 20));
  } catch {
    throw new ArchiveError(notPack);
  }
  const raw = contents.files.get("modrinth.index.json");
  if (!raw) throw new ArchiveError(notPack);
  let index: Index;
  try {
    index = JSON.parse(new TextDecoder().decode(raw));
  } catch {
    throw new ArchiveError("The modpack's modrinth.index.json cannot be read.");
  }
  if (index.game !== "minecraft") throw new ArchiveError("This modpack is not for Minecraft.");
  const deps = index.dependencies ?? {};
  const gameVersion = text(deps.minecraft);
  if (!gameVersion) throw new ArchiveError("The modpack does not say which Minecraft version it is for.");
  const loader = Object.entries(loaders).find(([dep]) => text(deps[dep]))?.[1];
  if (!loader) throw new ArchiveError("The modpack needs Fabric, Quilt, Forge or NeoForge.");
  const files = (index.files ?? [])
    .map((f) => {
      const path = text(f.path).replaceAll("\\", "/");
      const downloads = Array.isArray(f.downloads) ? f.downloads : [];
      const from = downloads.map((d) => (typeof d === "string" ? modrinthFile.exec(d) : null)).find((m) => m);
      return { path, name: path.slice(path.lastIndexOf("/") + 1), server: f.env?.server !== "unsupported", ...(from ? { project: from[1], ...(from[2] ? { version: from[2] } : {}) } : {}) };
    })
    .filter((f) => f.name);
  const icon = icons.map((p) => contents.files.get(p)).find((d) => d && d.length > 0) ?? null;
  return { name: text(index.name), versionId: text(index.versionId), gameVersion, loader, icon, files };
}

/** What a pack is called: its own name, or its file's. */
export const packName = (found: FoundPack, file: File) => found.name || file.name.replace(/\.mrpack$/i, "");
