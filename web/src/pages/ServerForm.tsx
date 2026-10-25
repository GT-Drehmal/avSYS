import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive as ArchiveIcon, ChevronLeft, Plus, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import { AddContentDialog, ContentList, contentList, kindsFor, kindsPhrase, PackCard, PackFit, type PickKind, type Software } from "../components/Content";
import { ModrinthSearch, ProjectIcon } from "../components/ModrinthSearch";
import { Picture, PictureChange, pictureForRequest } from "../components/Picture";
import { autoOpOptions, exposeOptions, joinHint, joinOptions, LaunchFlags, lines, More, NodeChoice, openWarning, type Join } from "../components/ServerFields";
import { Button, Chip, Confirm, cx, Field, Input, Notice, PageHeader, Section, Select, Textarea, Toggle } from "../components/ui";
import { cancelled } from "../components/Uploading";
import { onPreview, PreviewsToggle, previewWarning, VersionOptions } from "../components/VersionOptions";
import { api, ApiError, qs, serverIcon, serverPath, type BackupOverride, type ContentKind, type ContentRef, type ExposeType, type Loader, type Modpack, type Operation, type ResourcePack, type Server, type ServerSpec } from "../lib/api";
import { ArchiveError } from "../lib/archive";
import { ago, bytes, loaderNames } from "../lib/format";
import { launchOf } from "../lib/launch";
import type { Origin } from "../lib/origin";
import { difficulties, gameModes, levelTypes, parseProperties } from "../lib/properties";
import { keys, useMe } from "../lib/queries";
import { type Found, type FoundWorld, inspectServer, inspectWorld } from "../lib/serverImport";
import { SetupFailed, type SetupWork, startSetup } from "../lib/setups";
import { fromGigabytes, loaders, toGigabytes, useGameVersions } from "../lib/software";
import { toast } from "../lib/toast";

const GiB = 1024 ** 3;

const slugify = (name: string) =>
  name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^[-0-9]+|-+$/g, "")
    .slice(0, 40);

/** The first of name, name-2, name-3… that is not taken. Names are at most 40 characters. */
function freeName(wanted: string, taken: Set<string>) {
  for (let n = 1; ; n++) {
    const suffix = n === 1 ? "" : `-${n}`;
    const name = wanted.slice(0, 40 - suffix.length).replace(/-+$/, "") + suffix;
    if (!taken.has(name)) return name;
  }
}

/** A server or snapshot a new server is made out of. */
export interface Source {
  origin: Origin;
  name: string;
  spec: ServerSpec;
  /** How large its files are, where that is known. */
  size: number;
  /** When the snapshot was taken, for one made from a snapshot. */
  taken?: string;
  /** The server itself, if it still exists. */
  live?: Server;
}

/** An archive of an existing server's folder, and what it says about the server. */
interface Archive {
  file: File;
  found: Found;
}

/** The most an archive sent from here may be. */

/** A setting in server.properties as the form has it: some are numbers in older files. */
const named = (value: string | undefined, names: readonly (readonly [string, string])[]) => {
  if (value === undefined) return "";
  const n = Number(value);
  return Number.isInteger(n) && value.trim() !== "" ? (names[n]?.[0] ?? "") : value;
};

const count = (n: number, thing: string) => `${n} ${thing}${n === 1 ? "" : "s"}`;

/** A value in server.properties as written there, where ":" and "=" are escaped. */
const unescaped = (value: string | undefined) => (value ?? "").replace(/\\(.)/g, "$1").trim();

/** The resource pack a server.properties names, as the form has one. */
function packOf(p: Record<string, string>): ResourcePack | null {
  const url = unescaped(p["resource-pack"]);
  if (!/^https?:\/\//.test(url)) return null;
  const sha1 = unescaped(p["resource-pack-sha1"]);
  const prompt = unescaped(p["resource-pack-prompt"]);
  return { url, ...(/^[0-9a-f]{40}$/i.test(sha1) ? { sha1 } : {}), enforce: p["require-resource-pack"] === "true", ...(prompt ? { prompt } : {}) };
}

/** Whether an archive's mods, plugins and datapacks are listed as its content, rather than kept only as files. */
const listed = (f: Found) => f.jars.length > 0 && f.unread === 0;

type Upload = { file: File; kind: ContentKind };

/** What an archive has as content: its mods, plugins and datapacks, and a resource pack file. */
interface ArchiveContent {
  mods: ContentRef[];
  uploads: Upload[];
  /** Those of them the world did not load before, by project or file name. */
  unused: string[];
  pack: ResourcePack | null;
  packFile: File | null;
}

/**
 * An archive's content as the form lists it: what Modrinth has, as its
 * projects, to be kept up to date from here; the rest, to upload. Not
 * reaching Modrinth makes them all uploads.
 */
async function contentOf(f: Found): Promise<ArchiveContent> {
  const jars = listed(f) ? f.jars : [];
  const all = f.pack ? [...jars, f.pack] : jars;
  const out: ArchiveContent = { mods: [], uploads: [], unused: [], pack: null, packFile: null };
  if (all.length === 0) return out;
  const hits = await api
    .post<{ hash: string; project: string; version: string; versionName: string; url: string }[]>("/catalog/modrinth/files", { hashes: all.map((j) => j.sha1) })
    .catch(() => []);
  const ids = [...new Set(hits.map((h) => h.project))];
  const projects = new Map<string, { title: string; icon_url: string | null }>();
  for (let i = 0; i < ids.length; i += 50) {
    const some = await api.get<{ id: string; title: string; icon_url: string | null }[]>(`/catalog/modrinth/projects?${qs({ ids: ids.slice(i, i + 50).join(",") })}`).catch(() => []);
    for (const p of some) projects.set(p.id, p);
  }
  const seen = new Set<string>();
  for (const jar of jars) {
    if (jar.kind === "resourcepack") continue;
    const hit = hits.find((h) => h.hash === jar.sha1);
    if (hit && !seen.has(hit.project)) {
      seen.add(hit.project);
      const p = projects.get(hit.project);
      out.mods.push({ source: "modrinth", kind: jar.kind, project: hit.project, version: hit.version, versionName: hit.versionName, title: p?.title ?? jar.name, ...(p?.icon_url ? { iconUrl: p.icon_url } : {}) });
      if (jar.unused) out.unused.push(hit.project);
    } else if (!hit) {
      out.uploads.push({ file: new File([jar.data.slice()], jar.name, { type: jar.kind === "datapack" ? "application/zip" : "application/java-archive" }), kind: jar.kind });
      if (jar.unused) out.unused.push(jar.name);
    }
  }
  if (f.pack) {
    const hit = hits.find((h) => h.hash === f.pack?.sha1);
    const p = hit && projects.get(hit.project);
    if (hit?.url) {
      out.pack = { url: hit.url, sha1: hit.hash, project: { source: "modrinth", id: hit.project, version: hit.version, versionName: hit.versionName, ...(p ? { title: p.title } : {}), ...(p?.icon_url ? { iconUrl: p.icon_url } : {}) } };
    } else {
      out.packFile = new File([f.pack.data.slice()], f.pack.name, { type: "application/zip" });
      out.pack = { url: "", sha1: "", uploaded: { name: f.pack.name, size: f.pack.data.length, time: new Date().toISOString() }, project: null };
    }
  }
  return out;
}

/** When it is snapshotted: as its organization, as the server it is a copy of, or not. */
type Schedule = "org" | "own" | "off";

/** Everything the form holds. A new server starts from defaults; one made from another, from what that one has. */
interface Values {
  kind: "game" | "modpack" | "import";
  loader: Loader | null;
  version: string;
  /** The loader's own version, as an imported server had it; empty for the newest. */
  loaderVersion: string;
  /** The server to import, for kind "import". */
  archive: Archive | null;
  /** A world of one's own for a new server, instead of a new one. */
  myWorld: { file: File; found: FoundWorld } | null;
  modpack: Modpack | null;
  displayName: string;
  /** null while it follows the display name. */
  customName: string | null;
  motd: string;
  picture: PictureChange;
  mods: ContentRef[];
  /** The resource pack offered to players, with whether it is required and its prompt. */
  pack: ResourcePack | null;
  /** Files from this computer for a server not made yet, uploaded once it is, before it first starts. */
  uploads: Upload[];
  /** Content from an imported archive that its world did not load before. */
  unused: string[];
  /** The resource pack's file, likewise; pack then stands for it, with no address yet. */
  packFile: File | null;
  gamemode: string;
  difficulty: string;
  levelType: string;
  seed: string;
  structures: boolean;
  hardcore: boolean;
  maxPlayers: string;
  pvp: string;
  otherProperties: string;
  join: Join;
  autoOp: string;
  /** Players named by hand, one a line: on the whitelist, and operators. */
  whitelist: string;
  ops: string;
  memory: string;
  storage: string;
  /** The node to put it on; empty for the panel to choose. */
  node: string;
  aikar: boolean;
  extraFlags: string;
  hostname: string;
  exposeType: ExposeType;
  sleep: boolean;
  idleMinutes: string;
  schedule: Schedule;
}

/** The server.properties entries the form has a control of its own for. The rest are "other properties". */
const shown = new Set(["motd", "gamemode", "difficulty", "level-type", "level-seed", "generate-structures", "hardcore", "max-players", "pvp", "white-list", "enforce-whitelist"]);

const fresh: Values = {
  kind: "game",
  // Nothing is picked for you: the type decides what the server can run.
  loader: null,
  version: "LATEST",
  loaderVersion: "",
  archive: null,
  myWorld: null,
  modpack: null,
  displayName: "",
  customName: null,
  motd: "",
  picture: { kind: "none" },
  mods: [],
  pack: null,
  uploads: [],
  unused: [],
  packFile: null,
  gamemode: "",
  difficulty: "",
  levelType: "",
  seed: "",
  structures: true,
  hardcore: false,
  maxPlayers: "",
  pvp: "",
  otherProperties: "",
  // Who may join. A new server is its organization's own unless said otherwise.
  join: "viewer",
  autoOp: "operator",
  whitelist: "",
  ops: "",
  memory: "4",
  storage: "10",
  node: "",
  // Recommended for every server, and on unless switched off.
  aikar: true,
  extraFlags: "",
  hostname: "",
  exposeType: "Router",
  sleep: false,
  idleMinutes: "15",
  schedule: "org",
};

/** The form filled in with what a server or snapshot has. */
function valuesOf(source: Source, name: string, copying: boolean, leastStorage: number): Values {
  const { spec } = source;
  const p = spec.properties ?? {};
  const byRole = spec.autoWhitelist && spec.autoWhitelist !== "off" ? spec.autoWhitelist : null;
  return {
    kind: spec.modpack ? "modpack" : "game",
    loader: spec.loader,
    version: spec.version,
    loaderVersion: "",
    archive: null,
    myWorld: null,
    modpack: spec.modpack ?? null,
    displayName: (spec.displayName || source.name) + (copying ? " copy" : ""),
    customName: name,
    motd: p["motd"] ?? "",
    picture: { kind: "keep" },
    mods: spec.mods ?? [],
    pack: spec.resourcePack?.url ? spec.resourcePack : null,
    uploads: [],
    unused: [],
    packFile: null,
    gamemode: p["gamemode"] ?? "",
    difficulty: p["difficulty"] ?? "",
    levelType: p["level-type"] ?? "",
    seed: p["level-seed"] ?? "",
    structures: p["generate-structures"] !== "false",
    hardcore: p["hardcore"] === "true",
    maxPlayers: p["max-players"] ?? "",
    pvp: p["pvp"] ?? "",
    otherProperties: Object.entries(p)
      .filter(([k]) => !shown.has(k))
      .map(([k, v]) => `${k}=${v}`)
      .join("\n"),
    join: p["white-list"]?.toLowerCase() !== "true" ? "anyone" : (byRole ?? "list"),
    // Not set is the same as "operator".
    autoOp: spec.autoOp || "operator",
    whitelist: (spec.whitelist ?? []).join("\n"),
    ops: (spec.ops ?? []).join("\n"),
    memory: toGigabytes(spec.resources.memory) || "4",
    storage: String(Math.max(Number(toGigabytes(spec.storage.size)) || 10, leastStorage)),
    // The panel chooses for a copy too, unless asked: the original's node
    // may have no room for both.
    node: "",
    aikar: launchOf(spec).aikar,
    // Not taken from a snapshot, which the server could have written: they
    // run code. A copy of a server that is there takes its own.
    extraFlags: source.origin.kind === "server" ? launchOf(spec).extraFlags : "",
    // A server's address is its own, and not taken along.
    hostname: "",
    exposeType: spec.expose?.type ?? "Router",
    sleep: !!spec.sleep?.enabled,
    idleMinutes: String(spec.sleep?.idleMinutes || 15),
    schedule: !spec.backups ? "org" : spec.backups.enabled === false ? "off" : "own",
  };
}

/** The server.properties the form's values come to. An empty value is one the panel does not set. */
function propertiesOf(v: Values): Record<string, string> {
  const properties: Record<string, string> = { ...parseProperties(v.otherProperties) };
  const set = (key: string, value: string) => {
    if (value !== "") properties[key] = value;
  };
  set("motd", v.motd.trim());
  set("gamemode", v.gamemode);
  set("difficulty", v.difficulty);
  set("level-type", v.levelType);
  set("level-seed", v.seed.trim());
  if (!v.structures) properties["generate-structures"] = "false";
  if (v.hardcore) properties["hardcore"] = "true";
  set("max-players", v.maxPlayers);
  set("pvp", v.pvp);
  if (v.join === "anyone") {
    properties["white-list"] = "false";
  } else {
    properties["white-list"] = "true";
    // Whoever is taken off the whitelist is put out, not only kept out.
    properties["enforce-whitelist"] = "true";
  }
  return properties;
}

/**
 * The form for making a server, every way one is made: from nothing, or out
 * of a server or a snapshot, which fill it in to start with. Whatever it
 * starts with can be changed before the server is made.
 */
export function ServerForm({ org, source, taken, history }: { org: string; source?: Source; taken: Set<string>; history: Set<string> }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const me = useMe().data;
  // A hostname under another organization's domain is refused; its own are said here.
  const domains = useQuery({ queryKey: ["domains", org], queryFn: () => api.get<{ domains: string[] }>(`/orgs/${org}/domains`) });
  const spec = source?.spec;
  const duplicating = source?.origin.kind === "server";
  // A server that still exists is being copied; one that is gone gets its name back.
  const copying = !!source && taken.has(source.name);
  // Whole gigabytes that hold the snapshot's files with some room to spare.
  const leastStorage = source ? Math.max(1, Math.ceil((source.size * 1.2) / GiB)) : 1;
  // One that is gone is brought back under its own name, and its history goes
  // on. A copy is a new server, and takes a name no server or snapshot has.
  const startingName = source ? (copying ? freeName(`${source.name}-copy`, new Set([...taken, ...history])) : source.name) : "";
  const [start] = useState(() => (source ? valuesOf(source, startingName, copying, leastStorage) : fresh));
  const [v, setValues] = useState(start);
  const importing = v.kind === "import";
  const found = importing ? (v.archive?.found ?? null) : null;
  // A world of one's own goes with a new server of a game version.
  const [usingMine, setUsingMine] = useState(false);
  const mine = !source && v.kind === "game" && usingMine ? v.myWorld : null;
  // What is sent to be unpacked into the server once it is made.
  const toImport = found && v.archive ? { file: v.archive.file, root: found.root, world: false, listed: listed(found) } : mine ? { file: mine.file, root: mine.found.root, world: true, listed: false } : null;
  const toUpload = v.uploads.length > 0 || !!v.packFile || !!toImport;
  const set = <K extends keyof Values>(key: K, value: Values[K]) => setValues((was) => ({ ...was, [key]: value }));

  const [needsLoader, setNeedsLoader] = useState(false);
  const softwareRef = useRef<HTMLDivElement>(null);
  const versions = useGameVersions(v.kind !== "modpack" ? v.loader : null);
  const [previews, setPreviews] = useState(false);
  // A version picked for one kind of server may have no build for the next.
  useEffect(() => {
    // An imported server keeps the version it ran, listed or not.
    if (source || importing || v.version === "LATEST" || !versions.data || versions.isPlaceholderData || versions.data.all.has(v.version)) return;
    toast.info(`${loaders.find((l) => l.id === v.loader)?.name ?? "This software"} has no build for ${v.version} yet`);
    setValues((was) => ({ ...was, version: "LATEST" }));
  }, [source, importing, v.version, versions.data, versions.isPlaceholderData, v.loader]);
  const [addingMods, setAddingMods] = useState<PickKind | null>(null);
  const [moreWorld, setMoreWorld] = useState(false);
  const [moreNetwork, setMoreNetwork] = useState(false);
  const [moreLaunch, setMoreLaunch] = useState(false);
  const [eula, setEula] = useState(false);
  const [startNow, setStartNow] = useState(true);
  const custom = !!spec?.image || (spec?.env?.length ?? 0) > 0;
  const [keepCustom, setKeepCustom] = useState(custom);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [reading, setReading] = useState(false);
  const archiveInput = useRef<HTMLInputElement>(null);
  // The archive read last: one read earlier that ends later is not used.
  const reads = useRef(0);

  const name = v.customName ?? slugify(v.displayName);
  const bringingBack = !!source && !copying && name === source.name;
  const nameHint = taken.has(name)
    ? `There is already a server named ${name}.`
    : history.has(name) && !bringingBack
      ? `A deleted server used this name. Choose another${source ? "" : ", or bring it back from Backups"}.`
      : "It cannot be changed later.";
  const routed = v.exposeType === "Router";
  // Reachable with no whitelist: anyone who finds it can join. Said while
  // it is picked, and asked about when this form opens it: not for a copy
  // of a server that is open already, but for one brought back from a
  // snapshot, which is reachable again only now.
  const isOpen = (x: Pick<Values, "join" | "exposeType">) => x.join === "anyone" && x.exposeType !== "None";
  const open = isOpen(v);
  const ask = open && !(source?.origin.kind === "server" && isOpen(start));
  const [askOpen, setAskOpen] = useState(false);
  // What content fits. Until a pack is picked, what it will accept is still
  // that of a modpack, whose own loader is the one that counts.
  const software: Software | null =
    v.kind === "modpack" ? { loader: "vanilla", version: v.version, modpack: v.modpack ?? { source: "modrinth", project: "" } } : v.loader ? { loader: v.loader, version: v.version } : null;
  const modsChanged = JSON.stringify(v.mods) !== JSON.stringify(start.mods);
  const packChanged = JSON.stringify(v.pack) !== JSON.stringify(start.pack);
  // "Latest release" is whichever version that is now: picked for a copy of
  // a server already on it, nothing changes.
  const effective = (version: string) => (version === "LATEST" ? (versions.data?.latest ?? version) : version);
  const versionChanged = found ? !!found.version && effective(v.version) !== found.version : effective(v.version) !== effective(start.version);
  // The picture made from it will have: the original's as it is now for a
  // copy of it as it is now, or the one a snapshot holds.
  const brought = source ? (source.origin.kind === "snapshot" ? `/api/v1/orgs/${org}/snapshots/${source.origin.id}/icon` : source.live ? serverIcon(source.live) : undefined) : undefined;
  // "Server default" for a copy leaves its server.properties as it is; for a
  // new server it is the game's or the modpack's own default.
  const unset = source || importing ? "Server default" : v.kind === "modpack" ? "Modpack default" : undefined;
  // A world that is there already was made with its own seed and type.
  const worldMade = !!source || !!found?.world || !!mine;
  // An import unpacks next to the archive, so both need room at once.
  const least = toImport ? Math.max(1, Math.ceil(((found?.size ?? mine?.found.size ?? 0) + toImport.file.size) / GiB)) : leastStorage;
  // Every version, in the order they came out, to tell which is newer.
  const allVersions = useGameVersions();
  const newer = (a: string, b: string) => {
    const order = allVersions.data?.ordered;
    const i = order?.indexOf(a) ?? -1;
    const j = order?.indexOf(b) ?? -1;
    return i >= 0 && j >= 0 && i < j;
  };
  const worldTooNew = !!mine?.found.version && newer(mine.found.version, effective(v.version));
  const [readingWorld, setReadingWorld] = useState(false);
  const worldInput = useRef<HTMLInputElement>(null);

  // Picks were filtered to fit one kind of server, so they do not carry over
  // to another.
  const changeSoftware = (change: Partial<Values>) => {
    // Files to upload were picked for it too: a mod is not a plugin.
    const cleared = v.mods.length + v.uploads.length;
    if (cleared > 0) toast.info(`Removed ${cleared} item${cleared === 1 ? "" : "s"} that no longer fit`);
    setValues((was) => ({ ...was, ...change, mods: [], uploads: [], loaderVersion: "" }));
  };

  /** Reads an archive of a world, to start the server with. */
  const pickWorld = async (file: File) => {
    setError(null);
    setReadingWorld(true);
    const read = ++reads.current;
    try {
      const w = await inspectWorld(file);
      if (read !== reads.current) return;
      setValues((was) => ({
        ...was,
        myWorld: { file, found: w },
        // Opened in the version it was last opened in, where there is one.
        version: w.version && versions.data?.all.has(w.version) ? w.version : was.version,
        storage: String(Math.max(Number(was.storage) || 10, Math.ceil((w.size * 1.5 + file.size) / GiB))),
      }));
    } catch (err) {
      if (read === reads.current) setError(err instanceof ArchiveError ? err.message : "Could not read this archive.");
    } finally {
      if (read === reads.current) setReadingWorld(false);
    }
  };

  /** Reads an archive of a server and fills the form in with what it says. */
  const pickArchive = async (file: File) => {
    setError(null);
    setReading(true);
    const read = ++reads.current;
    try {
      const f = await inspectServer(file);
      const content = await contentOf(f);
      if (read !== reads.current) return;
      const p = f.properties;
      setValues((was) => ({
        ...was,
        kind: "import",
        archive: { file, found: f },
        loader: f.loader,
        version: f.version || "LATEST",
        loaderVersion: f.loaderVersion,
        mods: content.mods,
        uploads: content.uploads,
        unused: content.unused,
        displayName: was.displayName || (f.root.split("/").pop() || file.name.replace(/\.(zip|tar|tgz|tar\.gz)$/i, "")),
        motd: p.motd ?? was.motd,
        gamemode: named(p.gamemode, gameModes),
        difficulty: named(p.difficulty, difficulties),
        hardcore: p.hardcore === "true",
        maxPlayers: p["max-players"] ?? "",
        pvp: p.pvp ?? "",
        join: p["white-list"]?.toLowerCase() === "true" ? "list" : "anyone",
        // Its own lists, to see and change here.
        whitelist: f.whitelist.join("\n"),
        ops: f.ops.join("\n"),
        // As much memory as it had, with the room beside Java the panel keeps.
        memory: f.heap > 0 ? String(Math.ceil(((f.heap + 768) / 0.9 / 1024) * 2) / 2) : was.memory,
        // The one server.properties gives players, or else a pack file it has.
        ...(packOf(p) ? { pack: packOf(p), packFile: null } : { pack: content.pack && { ...content.pack, enforce: p["require-resource-pack"] === "true" }, packFile: content.packFile }),
        storage: String(Math.max(10, Math.ceil((f.size * 1.5 + file.size) / GiB))),
      }));
      setNeedsLoader(false);
    } catch (err) {
      if (read === reads.current) setError(err instanceof ArchiveError ? err.message : "Could not read this archive.");
    } finally {
      if (read === reads.current) setReading(false);
    }
  };

  /** A new server's request: everything chosen, and nothing else. */
  const newRequest = (): Partial<ServerSpec> => {
    const properties = propertiesOf(v);
    // Not on the whitelist is the game's own default.
    if (properties["white-list"] === "false") delete properties["white-list"];
    return {
      displayName: v.displayName,
      eula,
      resources: { memory: fromGigabytes(v.memory) },
      storage: { size: fromGigabytes(v.storage) },
      expose: v.exposeType === "None" ? { type: "None" } : { type: v.exposeType, hostname: v.hostname.trim() },
      mods: v.mods,
      // One to upload is the server's once it is made and the file is in.
      ...(v.pack && !v.packFile ? { resourcePack: v.pack } : {}),
      properties,
      // Members from this role up who have linked a Minecraft account are let in.
      ...(v.join !== "anyone" && v.join !== "list" ? { autoWhitelist: v.join } : {}),
      ...(v.autoOp !== "operator" ? { autoOp: v.autoOp as ServerSpec["autoOp"] } : {}),
      ...(lines(v.whitelist).length > 0 ? { whitelist: lines(v.whitelist) } : {}),
      ...(lines(v.ops).length > 0 ? { ops: lines(v.ops) } : {}),
      ...(v.sleep && routed ? { sleep: { enabled: true, idleMinutes: Number(v.idleMinutes) || 15 } } : {}),
      ...(v.schedule === "off" ? { backups: { enabled: false } } : {}),
      jvm: { aikarFlags: v.aikar, extraFlags: v.extraFlags },
      // The game looks for its picture and its files when it starts, so a
      // server given any is made stopped and started once they are there.
      ...(v.picture.kind === "file" || toUpload ? { state: "Stopped" as const } : {}),
      ...(v.kind === "modpack" ? { modpack: v.modpack ?? undefined } : { loader: v.loader ?? undefined, version: v.version, ...(v.loaderVersion ? { loaderVersion: v.loaderVersion } : {}) }),
    };
  };

  /**
   * The request for a server made from another: what it is given to run on,
   * and of the rest only what was changed. What is left out is taken from
   * the original, which also has what this form does not show.
   */
  const copyRequest = (): Partial<ServerSpec> => {
    const changes: Partial<ServerSpec> = {};
    if (versionChanged) changes.version = v.version;
    // A setting put back on "Server default", or taken out of the other
    // properties, is sent as null: the panel stops setting it, and the copied
    // server.properties keeps what it has.
    const now = propertiesOf(v);
    const was = propertiesOf(start);
    const properties: Record<string, string | null> = {};
    for (const key of new Set([...Object.keys(now), ...Object.keys(was)])) {
      if (now[key] !== was[key]) properties[key] = now[key] ?? null;
    }
    if (Object.keys(properties).length > 0) changes.properties = properties as Record<string, string>;
    if (v.join !== start.join && v.join !== "anyone") changes.autoWhitelist = v.join === "list" ? "off" : v.join;
    if (v.autoOp !== start.autoOp) changes.autoOp = v.autoOp as ServerSpec["autoOp"];
    if (v.whitelist !== start.whitelist) changes.whitelist = lines(v.whitelist);
    if (v.ops !== start.ops) changes.ops = lines(v.ops);
    const sleeps = v.sleep && routed;
    if (sleeps !== start.sleep || (sleeps && v.idleMinutes !== start.idleMinutes)) changes.sleep = { enabled: sleeps, idleMinutes: Number(v.idleMinutes) || 15 };
    if (v.schedule !== start.schedule) changes.backups = v.schedule === "off" ? ({ enabled: false, minute: null, keep: null } satisfies BackupOverride) : null;
    if (modsChanged) changes.mods = v.mods;
    // Left out, the original's comes with it, an uploaded one in its files.
    // That one only made required keeps its file: the panel knows it by
    // its address, and a request does not say a pack was uploaded.
    if (packChanged) changes.resourcePack = v.pack && { ...v.pack, uploaded: null };
    // One to upload is the copy's once its files are in: the original's goes.
    if (v.packFile) changes.resourcePack = null;
    const jvm: NonNullable<ServerSpec["jvm"]> = {};
    if (v.aikar !== start.aikar) jvm.aikarFlags = v.aikar;
    if (v.extraFlags !== start.extraFlags) jvm.extraFlags = v.extraFlags;
    // An original with them only in its environment has neither field: the
    // one not changed is sent too, or it would be lost as the other is set.
    const legacy = spec?.jvm?.aikarFlags == null && !spec?.jvm?.extraFlags;
    if (Object.keys(jvm).length > 0) changes.jvm = legacy ? { aikarFlags: v.aikar, extraFlags: v.extraFlags } : jvm;
    return {
      displayName: v.displayName,
      // Files to upload go in before it first starts: it is started after.
      state: startNow && !toUpload ? "Running" : "Stopped",
      resources: { memory: fromGigabytes(v.memory) },
      storage: { size: fromGigabytes(v.storage) },
      // Reached the way the original is unless that was changed: a server
      // that is not exposed at all is not given an address because it was
      // copied. A fixed address or port is the original's own and is not
      // taken along.
      expose: v.exposeType === "None" ? { type: "None" } : { type: v.exposeType, hostname: v.hostname.trim() },
      ...(keepCustom ? { image: spec?.image, env: spec?.env } : {}),
      ...changes,
    };
  };

  /**
   * Sends the archive to a server just made, to be unpacked into it. One
   * the panel turned down is no use without its files: it is deleted again.
   * A lost connection is not known to have failed, as the panel goes on
   * with what it was sent: that one is kept, to be looked at.
   */
  const importArchive = async (w: SetupWork, server: string, title: string) => {
    if (!toImport) return;
    const path = serverPath(org, server);
    try {
      // In parts, so that a connection lost for a moment does not lose what
      // was sent, and an archive of any size goes.
      await w.sendParts(`Sending ${toImport.file.name}`, path, toImport.file, (upload) =>
        api.put<Server>(
          `${path}/import?${qs({ root: toImport.root, ...(toImport.world ? { world: "true" } : {}), ...(toImport.listed ? { content: "listed" } : {}), ...(toImport.world ? {} : { players: "listed" }), from: "upload", upload, size: toImport.file.size, wait: "true" })}`,
          {},
        ),
      );
    } catch (err) {
      if (err instanceof ApiError && (cancelled(err) || (err.status >= 400 && err.code !== "error"))) {
        const why = cancelled(err) ? "the upload was stopped." : err.message;
        const removed = await removeServer(server);
        await qc.invalidateQueries({ queryKey: keys.servers(org) });
        if (!removed) throw new SetupFailed(`Could not import ${title}: ${why} It could not be removed: delete it in its Settings.`, `/o/${org}/s/${server}/settings`);
        throw new SetupFailed(`Could not import ${title}: ${why} Nothing was kept.`);
      }
      throw new SetupFailed(`Lost touch with the panel while importing ${title}. Check its files before starting it.`, `/o/${org}/s/${server}/files`);
    }
  };

  /**
   * Deletes a server made only to be imported into. An import cut short may
   * hold it for a moment more, until the panel has let go of it.
   */
  const removeServer = async (server: string) => {
    for (let tries = 0; tries < 30; tries++) {
      try {
        await api.del(`${serverPath(org, server)}?snapshot=false`);
        return true;
      } catch (err) {
        if (err instanceof ApiError && err.status === 404) return true;
        if (!(err instanceof ApiError) || err.status !== 409) return false;
      }
      await new Promise((r) => setTimeout(r, 1000));
    }
    return false;
  };

  /**
   * Uploads the files chosen for a server just made, one at a time. One that
   * did not go leaves the rest unsent and the server stopped, to be given
   * them on its Content page.
   */
  const uploadAll = async (w: SetupWork, server: string) => {
    const path = serverPath(org, server);
    const all = v.uploads.length + (v.packFile ? 1 : 0);
    let n = 0;
    const label = (name: string) => (all > 1 ? `${name} (${++n} of ${all})` : name);
    try {
      for (const { file, kind } of v.uploads) {
        await w.send<Server>(label(file.name), `${path}/content?${qs({ kind, name: file.name, wait: "true" })}`, file);
      }
      if (v.packFile) {
        await w.send<Server>(label(v.packFile.name), `${path}/resource-pack?${qs({ name: v.packFile.name, wait: "true" })}`, v.packFile);
        // Whether it is required and its prompt, which an upload leaves as they were.
        if (v.pack?.enforce || v.pack?.prompt) await api.patch<Server>(path, { spec: { resourcePack: { enforce: !!v.pack.enforce, prompt: v.pack.prompt ?? "" } } });
      }
    } catch (err) {
      const why = cancelled(err) ? "the upload was stopped." : err instanceof Error ? err.message : "an upload failed.";
      throw new SetupFailed(`Not started: ${why} Add the rest on its Content page, then start it.`, `/o/${org}/s/${server}/content`);
    } finally {
      await qc.invalidateQueries({ queryKey: keys.servers(org) });
    }
  };

  /** Starts a server just made, once its files are in. */
  const startIt = async (w: SetupWork, server: string) => {
    w.say("Starting");
    try {
      await api.post(`${serverPath(org, server)}/start`);
    } catch (err) {
      throw new SetupFailed(`Created but could not start${err instanceof Error ? `: ${err.message}` : "."}`, `/o/${org}/s/${server}`);
    }
  };

  /**
   * Waits for a copy's own files to be copied in, which what is uploaded
   * joins, and checks that they all were.
   */
  const copied = async (w: SetupWork, server: string) => {
    w.say("Copying files", true);
    const path = serverPath(org, server);
    const since = Date.now();
    let lost = 0;
    const stopped = () => new SetupFailed("Not started: the upload was stopped. Add the files on its Content page, then start it.", `/o/${org}/s/${server}/content`);
    for (;;) {
      if (w.signal.aborted) throw stopped();
      const op = await api.get<Operation | null>(`${path}/operation`).then(
        (running) => running ?? null,
        (err) => {
          // Gone, as when it was deleted meanwhile: there is nothing to give
          // files to. Just made, the panel may not have caught up with it.
          if (err instanceof ApiError && err.status === 404 && Date.now() - since > 15_000) throw new SetupFailed("It is gone: nothing was uploaded.");
          return "unknown" as const;
        },
      );
      lost = op === "unknown" ? lost + 1 : 0;
      // Asked again for a minute, then given up on.
      if (lost > 60) throw new SetupFailed("Lost touch with the panel while its files were copied. Nothing was uploaded.", `/o/${org}/s/${server}`);
      if (op === null) break;
      await new Promise((r) => setTimeout(r, 1000));
    }
    // Ended is not done: a copy that failed leaves the server marked. The
    // mark may take a moment to be seen gone.
    for (let tries = 0; ; tries++) {
      const made = await api.get<Server>(path).catch(() => null);
      if (made && !made.unfinished) return;
      if (tries >= 5) throw new SetupFailed(made?.unfinished ?? "Could not tell whether its files were copied. Nothing was uploaded.", `/o/${org}/s/${server}`);
      await new Promise((r) => setTimeout(r, 1000));
    }
  };

  /** Hands what is left of making it to the panel's corner, and goes to it. */
  const handOff = (server: string, work: (w: SetupWork) => Promise<void>) => {
    const title = v.displayName || server;
    startSetup(org, server, title, work, (ok) => {
      void qc.invalidateQueries({ queryKey: keys.servers(org) });
      if (ok) toast.info(`${title} is set up`);
    });
    navigate(`/o/${org}/s/${server}`);
  };

  const submit = async (e?: React.FormEvent, sure = false) => {
    e?.preventDefault();
    if (!source && v.kind === "modpack" && !v.modpack) return setError("Pick a modpack.");
    if (!source && importing && !v.archive) return setError("Choose an archive.");
    if (!source && v.kind === "game" && usingMine && !v.myWorld) return setError("Choose a world, or make a new one.");
    if (!source && v.kind !== "modpack" && !v.loader) {
      setNeedsLoader(true);
      softwareRef.current?.scrollIntoView({ behavior: "smooth", block: "center" });
      return;
    }
    if (ask && !sure) return setAskOpen(true);
    setAskOpen(false);
    setBusy(true);
    setError(null);
    const title = v.displayName || name;
    try {
      if (source) {
        const from = source.origin.kind === "server" ? { fromServer: source.origin.name } : { fromSnapshot: source.origin.id };
        // Left out, it keeps the one its files bring.
        const pictured = v.picture.kind === "file" ? { picture: await pictureForRequest(v.picture.file) } : v.picture.kind === "none" ? { picture: null } : {};
        // "auto" lets the panel choose for a copy of a server kept to some
        // nodes, which would otherwise be kept to them too.
        const placed = v.node === "auto" ? { spec: { ...copyRequest(), nodeSelector: {} } } : { ...(v.node ? { node: v.node } : {}), spec: copyRequest() };
        // Answered at once: the panel takes the snapshot of a server copied
        // as it is now, and then puts the files in, which its page shows.
        const server = await api.post<Server>(`/orgs/${org}/servers`, { name, ...from, ...pictured, ...placed });
        await Promise.all([qc.invalidateQueries({ queryKey: keys.servers(org) }), qc.invalidateQueries({ queryKey: keys.snapshots(org) })]);
        // How the copying goes shows on the server's page until its files
        // are in. Files to upload join them after.
        if (!toUpload) return navigate(`/o/${org}/s/${server.name}`);
        return handOff(server.name, async (w) => {
          await copied(w, server.name);
          await uploadAll(w, server.name);
          if (startNow) await startIt(w, server.name);
        });
      }
      const server = await api.post<Server>(`/orgs/${org}/servers`, { name, ...(v.node ? { node: v.node } : {}), spec: newRequest() });
      await qc.invalidateQueries({ queryKey: keys.servers(org) });
      const picture = v.picture.kind === "file" ? v.picture.file : null;
      if (!toUpload && !picture) return navigate(`/o/${org}/s/${server.name}`);
      return handOff(server.name, async (w) => {
        // Its files go in first: made stopped, it is started once they are.
        if (toUpload) {
          await importArchive(w, server.name, title);
          try {
            await uploadAll(w, server.name);
          } catch (err) {
            if (picture) await api.putRaw(`${serverPath(org, server.name)}/icon?wait=true`, picture).catch(() => undefined);
            throw err;
          }
        }
        if (picture) {
          // Its picture goes onto its volume, which may not be there yet:
          // the panel waits for it, puts the picture in place and starts
          // the server.
          try {
            await w.send(`Its picture`, `${serverPath(org, server.name)}/icon?wait=true&start=true`, picture);
            qc.setQueryData(["picture-version"], Date.now());
          } catch (err) {
            const why = cancelled(err) ? "the upload was stopped." : err instanceof ApiError ? err.message : "it did not go.";
            throw new SetupFailed(`Not started: could not set its picture: ${why} Set it in Settings, then start it.`, `/o/${org}/s/${server.name}/settings`);
          }
          return;
        }
        await startIt(w, server.name);
      });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not create the server.");
      setBusy(false);
    }
  };

  const tile = (active: boolean) =>
    cx("flex flex-col gap-0.5 rounded-md border px-3 py-2.5 text-left transition-colors", active ? "border-accent bg-accent-wash" : "border-line-strong bg-raised hover:bg-hover");
  const segment = (active: boolean) => cx("h-7 rounded-sm px-3 text-xs font-medium", active ? "bg-hover text-fg" : "text-muted hover:text-fg");
  // The starting point says what the original runs, whatever is picked below.
  const softwareName = start.modpack ? `${start.modpack.title || start.modpack.project} modpack` : `${loaderNames[start.loader ?? "vanilla"] ?? start.loader} ${start.version}`;
  const versionField = (
    <Field
      label="Game version"
      hint={
        onPreview(versions.data, v.version)
          ? previewWarning
          : (source || found) && versionChanged
            ? "A world opened in a newer version cannot open in an older one."
            : (found?.version || mine?.found.version) && effective(v.version) === (found?.version || mine?.found.version)
              ? "The version its world was last opened in."
              : undefined
      }
    >
      {(id) => (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
          <Select
            id={id}
            value={v.version}
            onChange={(e) => {
              const version = e.target.value;
              setValues((was) => ({ ...was, version, loaderVersion: found && version === found.version && was.loader === found.loader ? found.loaderVersion : "" }));
            }}
            className="w-64"
          >
            {/* Keeps the version it has selectable when the list could not be loaded, or does not have it. */}
            <VersionOptions
              versions={versions.data}
              previews={previews || onPreview(versions.data, v.version)}
              keep={found?.version ? { value: found.version, label: found.version } : source && start.version !== "LATEST" ? { value: start.version, label: start.version } : undefined}
            />
          </Select>
          <PreviewsToggle versions={versions.data} previews={previews} onChange={setPreviews} selected={v.version} />
        </div>
      )}
    </Field>
  );
  const option = (list: readonly (readonly [string, string])[], hide?: string) =>
    list
      .filter(([value]) => value !== hide)
      .map(([value, label]) => (
        <option key={value} value={value}>
          {label}
        </option>
      ));
  const worldChanged = source ? (["maxPlayers", "pvp", "otherProperties"] as const).filter((k) => v[k] !== start[k]).length : 0;
  const networkChanged = (v.exposeType !== start.exposeType ? 1 : 0) + ((v.sleep && routed) !== start.sleep || (v.sleep && routed && v.idleMinutes !== start.idleMinutes) ? 1 : 0);

  return (
    <>
    <form onSubmit={submit}>
      <Link to={`/o/${org}`} className="mb-3 inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
        <ChevronLeft className="size-3.5" />
        Servers
      </Link>
      <PageHeader title={!source ? "New server" : duplicating ? `Duplicate ${source.name}` : copying ? `New server from a snapshot of ${source.name}` : `Bring back ${source.name}`} />

      {source ? (
        <Section title="Starting point">
          <div className="flex items-center gap-3 rounded-md border border-accent bg-accent-wash px-3 py-2.5">
            <Picture name={spec?.displayName || source.name} url={brought} size={36} />
            <div className="min-w-0 flex-1">
              <p className="flex items-center gap-2 truncate text-[13px] font-medium">
                {spec?.displayName || source.name}
                {!source.live && <Chip>deleted</Chip>}
              </p>
              <p className="truncate text-xs text-muted">
                {[source.taken && `Snapshot from ${ago(source.taken)} · ${new Date(source.taken).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}`, softwareName]
                  .filter(Boolean)
                  .join(" · ")}
              </p>
            </div>
          </div>
          {v.kind === "game" && versionField}
        </Section>
      ) : (
        <Section title="Software">
          <div className="flex w-fit gap-1 rounded-md border border-line bg-bg p-0.5">
            <button
              type="button"
              className={segment(v.kind === "game")}
              onClick={() => {
                if (v.kind === "game") return;
                reads.current++;
                setReading(false);
                changeSoftware({ kind: "game" });
              }}
            >
              Game version
            </button>
            <button
              type="button"
              className={segment(v.kind === "modpack")}
              onClick={() => {
                if (v.kind === "modpack") return;
                reads.current++;
                setReading(false);
                changeSoftware({ kind: "modpack" });
              }}
            >
              Modpack
            </button>
            <button type="button" className={segment(importing)} onClick={() => !importing && changeSoftware({ kind: "import" })}>
              Import
            </button>
          </div>
          {importing && (
            <>
              <input
                ref={archiveInput}
                type="file"
                accept=".zip,.tar,.tgz,.gz"
                className="hidden"
                aria-label="Server archive"
                onChange={(e) => {
                  const file = e.target.files?.[0];
                  e.target.value = "";
                  if (file) void pickArchive(file);
                }}
              />
              {v.archive ? (
                <div className="flex items-center gap-3 rounded-md border border-accent bg-accent-wash px-3 py-2.5">
                  <ArchiveIcon className="size-5 shrink-0 text-muted" />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-[13px] font-medium">{v.archive.file.name}</p>
                    <p className="truncate text-xs text-muted" data-testid="archive-summary">
                      {[
                        `${bytes(v.archive.found.size)} unpacked`,
                        v.archive.found.world ? `world "${v.archive.found.world}"` : "no world",
                        v.archive.found.plugins > 0 && count(v.archive.found.plugins, "plugin"),
                        v.archive.found.mods > 0 && count(v.archive.found.mods, "mod"),
                        v.archive.found.ops.length > 0 && count(v.archive.found.ops.length, "operator"),
                        v.archive.found.whitelist.length > 0 && `${v.archive.found.whitelist.length} whitelisted`,
                      ]
                        .filter(Boolean)
                        .join(" · ")}
                    </p>
                  </div>
                  <Button size="sm" disabled={reading} onClick={() => archiveInput.current?.click()}>
                    Change
                  </Button>
                  <Button size="sm" variant="ghost" disabled={reading} icon={<X className="size-4" />} aria-label="Remove the archive" onClick={() => setValues((was) => ({ ...was, archive: null, mods: [], uploads: [], unused: [], pack: null, packFile: null }))} />
                </div>
              ) : (
                <div className="flex flex-col items-start gap-2 rounded-md border border-dashed border-line-strong px-4 py-4">
                  <Button icon={<ArchiveIcon className="size-4" />} loading={reading} onClick={() => archiveInput.current?.click()}>
                    Choose an archive
                  </Button>
                  <p className="text-xs leading-relaxed text-muted">A .zip or .tar.gz of the server's folder.</p>
                </div>
              )}
              {found && !found.version && <p className="text-xs text-muted">Could not tell the game version. Pick the one it ran.</p>}
            </>
          )}
          {v.kind === "game" || found ? (
            <>
              <div ref={softwareRef} className="flex flex-col gap-2">
                <div role="group" aria-label="Server type" className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                  {loaders.map((l) => (
                    <button
                      key={l.id}
                      type="button"
                      aria-pressed={v.loader === l.id}
                      className={tile(v.loader === l.id)}
                      onClick={() => {
                        setNeedsLoader(false);
                        if (v.loader !== l.id) changeSoftware({ loader: l.id });
                      }}
                    >
                      <span className="text-[13px] font-medium">{l.name}</span>
                      <span className="text-xs leading-snug text-muted">{l.blurb}</span>
                    </button>
                  ))}
                </div>
                {needsLoader && !v.loader && (
                  <p role="alert" className="text-xs text-danger">
                    Choose a server type.
                  </p>
                )}
              </div>
              {versionField}
            </>
          ) : importing ? null : v.modpack ? (
            <div className="flex items-center gap-3 rounded-md border border-accent bg-accent-wash px-3 py-2.5">
              <ProjectIcon url={v.modpack.iconUrl} kind="modpack" name={v.modpack.title} />
              <div className="min-w-0 flex-1">
                <p className="truncate text-[13px] font-medium">{v.modpack.title}</p>
                <p className="truncate font-mono text-xs text-muted">modrinth:{v.modpack.project}</p>
              </div>
              <Button size="sm" variant="ghost" icon={<X className="size-4" />} aria-label="Choose a different modpack" onClick={() => set("modpack", null)} />
            </div>
          ) : (
            <div className="h-[30rem] overflow-hidden rounded-md border border-line bg-surface p-3">
              <ModrinthSearch
                type="modpack"
                placeholder="Search modpacks on Modrinth"
                onPick={(hit) =>
                  setValues((was) => ({
                    ...was,
                    modpack: { source: "modrinth", project: hit.slug, title: hit.title, iconUrl: hit.icon_url ?? undefined },
                    displayName: was.displayName || hit.title,
                  }))
                }
              />
            </div>
          )}
        </Section>
      )}

      <Section title="Name and picture">
        <PictureChange name={v.displayName || source?.name || ""} current={brought} value={v.picture} onChange={(change) => set("picture", change)} />
        <Field label="Display name">{(id) => <Input id={id} required value={v.displayName} onChange={(e) => set("displayName", e.target.value)} />}</Field>
        <Field label="Short name" hint={nameHint}>
          {(id) => <Input id={id} required value={name} onChange={(e) => set("customName", e.target.value)} pattern="[a-z]([a-z0-9\-]{0,38}[a-z0-9])?" className="font-mono" spellCheck={false} />}
        </Field>
        <Field label="Message of the day" hint="Shown under the name in the server list.">
          {(id) => <Input id={id} value={v.motd} onChange={(e) => set("motd", e.target.value)} placeholder="A Minecraft Server" />}
        </Field>
      </Section>

      <Section
        title="Content"
        description={
          found
            ? `What it has comes with it. ${contentList(software)}. Optional.`
            : !source
              ? `${contentList(software)}. Optional.`
              : v.modpack
                ? `${contentList(software)} on top of the modpack.`
                : `${contentList(software)}. Copied from the original.`
        }
      >
        {v.mods.length + v.uploads.length > 0 ? (
          <ContentList
            // Files to upload are listed with the rest, by their names.
            mods={[...v.mods, ...v.uploads.map((u): ContentRef => ({ source: "upload", kind: u.kind, project: u.file.name, title: u.file.name }))]}
            note={(_, i) => {
              const upload = i >= v.mods.length ? v.uploads[i - v.mods.length] : null;
              const off = v.unused.includes(upload ? upload.file.name : v.mods[i].project) && "not on in its world before";
              return [upload && `${bytes(upload.file.size)} · uploaded when created`, off].filter(Boolean).join(" · ") || undefined;
            }}
            onRemove={(i) =>
              i < v.mods.length ? set("mods", v.mods.filter((_, j) => j !== i)) : set("uploads", v.uploads.filter((_, j) => j !== i - v.mods.length))
            }
          />
        ) : (
          source && software && <p className="text-[13px] text-muted">{v.modpack ? "Nothing on top of its modpack." : `No ${kindsPhrase(software)}.`}</p>
        )}
        <div className="flex flex-wrap items-center gap-3">
          {/* One picker for all of it, the resource pack too. */}
          <Button icon={<Plus className="size-4" />} disabled={!software || (v.kind === "modpack" && !v.modpack)} onClick={() => software && setAddingMods(kindsFor(software)[0])}>
            Add content
          </Button>
          {source && (modsChanged || packChanged || toUpload) && (
            <button type="button" className="text-xs text-muted hover:text-fg" onClick={() => setValues((was) => ({ ...was, mods: start.mods, pack: start.pack, uploads: [], packFile: null }))}>
              Undo changes
            </button>
          )}
        </div>
        {v.pack && (
          <div className="flex flex-col gap-3">
            <PackCard pack={v.pack}>
              <Button size="sm" disabled={!software} onClick={() => setAddingMods("resourcepack")}>
                Replace
              </Button>
              <Button size="sm" variant="ghost" icon={<X className="size-4" />} aria-label="Remove the resource pack" onClick={() => setValues((was) => ({ ...was, pack: null, packFile: null }))} />
            </PackCard>
            <PackFit pack={v.pack} version={v.kind === "modpack" ? v.modpack?.gameVersion : effective(v.version)} />
            <Toggle checked={!!v.pack.enforce} onChange={(enforce) => setValues((was) => ({ ...was, pack: was.pack && { ...was.pack, enforce } }))} label="Pack required" hint="Players who decline the pack are disconnected." />
            <Field label="Pack prompt" hint="Shown when the game asks to download the pack.">
              {(id) => <Input id={id} value={v.pack?.prompt ?? ""} onChange={(e) => setValues((was) => ({ ...was, pack: was.pack && { ...was.pack, prompt: e.target.value } }))} placeholder="A pack made for this world" />}
            </Field>
          </div>
        )}
        {addingMods && software && (
          <AddContentDialog
            software={software}
            mods={v.mods}
            description="Downloaded when the server first starts."
            onAdd={(ref) => setValues((was) => ({ ...was, mods: [...was.mods, ref] }))}
            // Kept here until the server is made, then uploaded before it first starts.
            onUpload={async (file, kind) => {
              setValues((was) => ({ ...was, uploads: [...was.uploads.filter((u) => u.file.name !== file.name), { file, kind }] }));
              toast.info(`${file.name} will be uploaded when the server is created`);
            }}
            uploadNote="Uploaded after the server is created."
            start={addingMods}
            pack={{
              current: v.pack,
              onPick: async (pack) => setValues((was) => ({ ...was, packFile: null, pack: { ...pack, enforce: !!was.pack?.enforce, prompt: was.pack?.prompt ?? "" } })),
              keep: (file) =>
                    setValues((was) => ({
                      ...was,
                      packFile: file,
                      pack: { url: "", sha1: "", uploaded: { name: file.name, size: file.size, time: new Date().toISOString() }, project: null, enforce: !!was.pack?.enforce, prompt: was.pack?.prompt ?? "" },
                    })),
            }}
            onClose={() => setAddingMods(null)}
          />
        )}
      </Section>

      <Section
        title="World"
        description={
          source
            ? "Server default keeps the original's setting."
            : v.kind === "modpack"
              ? "Modpacks with their own world ignore these."
              : undefined
        }
      >
        {!source && v.kind === "game" && (
          <>
            <div className="flex w-fit gap-1 rounded-md border border-line bg-bg p-0.5">
              <button
                type="button"
                className={segment(!usingMine)}
                onClick={() => {
                  // A world still being read is not used.
                  reads.current++;
                  setReadingWorld(false);
                  setUsingMine(false);
                  set("myWorld", null);
                }}
              >
                New world
              </button>
              <button type="button" className={segment(usingMine)} onClick={() => setUsingMine(true)}>
                Use a world I have
              </button>
            </div>
            {usingMine && (
              <>
                <input
                  ref={worldInput}
                  type="file"
                  accept=".zip,.tar,.tgz,.gz"
                  className="hidden"
                  aria-label="World archive"
                  onChange={(e) => {
                    const file = e.target.files?.[0];
                    e.target.value = "";
                    if (file) void pickWorld(file);
                  }}
                />
                {v.myWorld ? (
                  <div className="flex items-center gap-3 rounded-md border border-accent bg-accent-wash px-3 py-2.5">
                    <ArchiveIcon className="size-5 shrink-0 text-muted" />
                    <div className="min-w-0 flex-1">
                      <p className="truncate text-[13px] font-medium">{v.myWorld.found.name}</p>
                      <p className="truncate text-xs text-muted" data-testid="world-summary">
                        {[v.myWorld.found.version && `Last opened in ${v.myWorld.found.version}`, bytes(v.myWorld.found.size), v.myWorld.file.name].filter(Boolean).join(" · ")}
                      </p>
                    </div>
                    <Button size="sm" disabled={readingWorld} onClick={() => worldInput.current?.click()}>
                      Change
                    </Button>
                    <Button size="sm" variant="ghost" disabled={readingWorld} icon={<X className="size-4" />} aria-label="Remove the world" onClick={() => set("myWorld", null)} />
                  </div>
                ) : (
                  <div className="flex flex-col items-start gap-2 rounded-md border border-dashed border-line-strong px-4 py-4">
                    <Button icon={<ArchiveIcon className="size-4" />} loading={readingWorld} onClick={() => worldInput.current?.click()}>
                      Choose a world
                    </Button>
                    <p className="text-xs leading-relaxed text-muted">A .zip or .tar.gz with the world's folder, the one with level.dat. A single-player save works.</p>
                  </div>
                )}
                {worldTooNew && (
                  <Notice tone="warn">
                    This world was last opened in {mine?.found.version}, which is newer than {effective(v.version)}. Pick {mine?.found.version} or newer.
                  </Notice>
                )}
              </>
            )}
          </>
        )}
        <div className="grid grid-cols-2 gap-4">
          <Field label="Game mode">
            {(id) => (
              <Select id={id} value={v.gamemode} onChange={(e) => set("gamemode", e.target.value)}>
                <option value="">{unset ?? "Survival"}</option>
                {option(gameModes, unset ? undefined : "survival")}
              </Select>
            )}
          </Field>
          <Field label="Difficulty">
            {(id) => (
              <Select id={id} value={v.hardcore ? "hard" : v.difficulty} disabled={v.hardcore} onChange={(e) => set("difficulty", e.target.value)}>
                <option value="">{unset ?? "Easy"}</option>
                {option(difficulties, unset ? undefined : "easy")}
              </Select>
            )}
          </Field>
        </div>
        <More label={source ? "Player limit, PvP and more" : "World options and more"} changed={worldChanged} open={moreWorld} onToggle={() => setMoreWorld(!moreWorld)} />
        {moreWorld && (
          <>
            <fieldset disabled={worldMade} className="flex flex-col gap-4 rounded-md border border-line p-4 disabled:opacity-60">
              <p className="text-xs text-muted">{worldMade ? "Set when its world was created." : "Cannot be changed after the world is created."}</p>
              <div className="grid grid-cols-2 gap-4">
                <Field label="World type">
                  {(id) => (
                    <Select id={id} value={v.levelType} onChange={(e) => set("levelType", e.target.value)}>
                      <option value="">{unset ?? "Default"}</option>
                      {option(levelTypes)}
                    </Select>
                  )}
                </Field>
                <Field label="Seed">{(id) => <Input id={id} value={v.seed} onChange={(e) => set("seed", e.target.value)} placeholder="Random" className="font-mono" spellCheck={false} />}</Field>
              </div>
              <Toggle checked={v.structures} disabled={worldMade} onChange={(on) => set("structures", on)} label="Generate structures" />
              <Toggle checked={v.hardcore} disabled={worldMade} onChange={(on) => set("hardcore", on)} label="Hardcore" hint="Hard difficulty. Players who die become spectators." />
            </fieldset>
            <div className="grid grid-cols-2 gap-4">
              <Field label="Player limit">{(id) => <Input id={id} type="number" min={1} value={v.maxPlayers} onChange={(e) => set("maxPlayers", e.target.value)} placeholder="20" />}</Field>
              <Field label="PvP">
                {(id) => (
                  <Select id={id} value={v.pvp} onChange={(e) => set("pvp", e.target.value)}>
                    <option value="">{unset ?? "On"}</option>
                    {unset && <option value="true">On</option>}
                    <option value="false">Off</option>
                  </Select>
                )}
              </Field>
            </div>
            <Field label="Other properties" hint="One key=value per line.">
              {(id) => (
                <Textarea id={id} rows={3} value={v.otherProperties} onChange={(e) => set("otherProperties", e.target.value)} placeholder={"view-distance=12\nspawn-protection=0"} className="font-mono" spellCheck={false} />
              )}
            </Field>
          </>
        )}
      </Section>

      <Section title="Players">
        <div className="grid grid-cols-2 gap-4">
          <Field label="Who can join" hint={joinHint(v.join)}>
            {(id) => (
              <Select id={id} value={v.join} onChange={(e) => set("join", e.target.value as Join)}>
                {joinOptions}
              </Select>
            )}
          </Field>
          <Field label="Operators by role" hint="Members need a linked Minecraft account.">
            {(id) => (
              <Select id={id} value={v.autoOp} onChange={(e) => set("autoOp", e.target.value)}>
                {autoOpOptions}
              </Select>
            )}
          </Field>
          <Field label="Whitelisted players" hint={v.join === "anyone" ? "Only used with a whitelist." : "One name per line."}>
            {(id) => <Textarea id={id} rows={3} value={v.whitelist} onChange={(e) => set("whitelist", e.target.value)} className="font-mono" spellCheck={false} />}
          </Field>
          <Field label="Operators" hint="One name per line.">
            {(id) => <Textarea id={id} rows={3} value={v.ops} onChange={(e) => set("ops", e.target.value)} className="font-mono" spellCheck={false} />}
          </Field>
        </div>
        {open && <Notice>{openWarning}</Notice>}
        {v.join !== "anyone" && v.join !== "list" && me && !me.minecraft && (
          <p className="text-xs leading-relaxed text-muted">
            To join it yourself,{" "}
            <Link to="/account" className="text-accent hover:underline">
              link your Minecraft account
            </Link>
            .
          </p>
        )}
      </Section>

      <Section
        title="Resources"
        description={
          found
            ? `Its files use ${(found.size / GiB).toFixed(1)} GB.`
            : source
              ? source.size > 0
                ? `Its files use ${(source.size / GiB).toFixed(1)} GB.`
                : undefined
              : v.kind === "modpack"
                ? "Large modpacks need 6 GB or more."
                : "4 GB suits a small server."
        }
      >
        <div className="grid grid-cols-2 gap-4">
          <Field label="Memory (GB)">{(id) => <Input id={id} type="number" required min={0.5} step={0.5} value={v.memory} onChange={(e) => set("memory", e.target.value)} />}</Field>
          <Field label="Storage (GB)">{(id) => <Input id={id} type="number" required min={least} step={1} value={v.storage} onChange={(e) => set("storage", e.target.value)} />}</Field>
        </div>
        <NodeChoice memory={v.memory} value={v.node} kept={source?.origin.kind === "server" ? source.live?.spec.nodeSelector : undefined} onChange={(node) => set("node", node)} />
        <More label="Launch flags" changed={(v.aikar !== start.aikar ? 1 : 0) + (v.extraFlags !== start.extraFlags ? 1 : 0)} open={moreLaunch} onToggle={() => setMoreLaunch(!moreLaunch)} />
        {moreLaunch && <LaunchFlags aikar={v.aikar} extra={v.extraFlags} onAikar={(on) => set("aikar", on)} onExtra={(text) => set("extraFlags", text)} fromSnapshot={source?.origin.kind === "snapshot"} />}
      </Section>

      <Section title="Network">
        <Field
          label="Hostname"
          hint={[
            source ? "Optional. Not copied from the original." : "Optional. Point its DNS at the avSYS router.",
            (domains.data?.domains.length ?? 0) > 0 ? `Your domains: ${domains.data?.domains.join(", ")}.` : "",
          ]
            .filter(Boolean)
            .join(" ")}
        >
          {(id) => <Input id={id} value={v.hostname} disabled={v.exposeType === "None"} onChange={(e) => set("hostname", e.target.value)} placeholder="play.example.com" className="font-mono" spellCheck={false} />}
        </Field>
        <More label="How it is reached, and sleep" changed={networkChanged} open={moreNetwork} onToggle={() => setMoreNetwork(!moreNetwork)} />
        {moreNetwork && (
          <>
            <Field label="How it is reached">
              {(id) => (
                <Select id={id} value={v.exposeType} onChange={(e) => set("exposeType", e.target.value as ExposeType)}>
                  {exposeOptions}
                </Select>
              )}
            </Field>
            <Toggle
              checked={v.sleep && routed}
              disabled={!routed}
              onChange={(on) => set("sleep", on)}
              label="Sleep when empty"
              hint={routed ? "Stops when nobody is playing. The first player to join wakes it, then joins again." : "Needs the shared address."}
            />
            {v.sleep && routed && (
              <Field label="After minutes with no players">
                {(id) => <Input id={id} type="number" min={1} value={v.idleMinutes} onChange={(e) => set("idleMinutes", e.target.value)} className="max-w-32" />}
              </Field>
            )}
          </>
        )}
      </Section>

      <Section title="Backups">
        <Field label="Scheduled snapshots" hint={source ? undefined : v.schedule === "org" ? "Taken daily." : "Only taken by hand."}>
          {(id) => (
            <Select id={id} value={v.schedule} onChange={(e) => set("schedule", e.target.value as Schedule)}>
              <option value="org">Same as the organization</option>
              {start.schedule === "own" && <option value="own">Its own, as {source?.name} has</option>}
              <option value="off">Off</option>
            </Select>
          )}
        </Field>
      </Section>

      <Section title={!source ? "Minecraft EULA" : duplicating ? "Duplicate" : "Create"}>
        {!source ? (
          <label className="flex items-start gap-2.5 text-[13px] leading-relaxed">
            <input type="checkbox" required checked={eula} onChange={(e) => setEula(e.target.checked)} className="mt-1 size-3.5 accent-(--color-accent)" />
            <span>
              I agree to the{" "}
              <a href="https://aka.ms/MinecraftEULA" target="_blank" rel="noreferrer" className="text-accent hover:underline">
                Minecraft End User License Agreement
              </a>
              .
            </span>
          </label>
        ) : (
          <>
            <Toggle checked={startNow} onChange={setStartNow} label="Start when ready" />
            {custom && (
              <Toggle
                checked={keepCustom}
                onChange={setKeepCustom}
                label="Keep its custom image and environment"
                hint={
                  <>
                    {spec?.image && (
                      <>
                        Image <span className="font-mono">{spec.image}</span>.{" "}
                      </>
                    )}
                    {(spec?.env?.length ?? 0) > 0 && (
                      <>
                        Variables <span className="font-mono">{spec?.env?.map((e) => e.name).join(", ")}</span>.
                      </>
                    )}
                  </>
                }
              />
            )}
          </>
        )}
        {error && <Notice tone="danger">{error}</Notice>}
        <div className="flex gap-2">
          <Button type="submit" variant="primary" loading={busy}>
            {!source ? "Create server" : duplicating ? "Duplicate" : "Create server"}
          </Button>
          <Link
            to={source?.live ? `/o/${org}/s/${source.live.name}` : source ? `/o/${org}/backups` : `/o/${org}`}
            className="inline-flex h-8 items-center rounded-md px-3 text-[13px] font-medium text-muted hover:bg-raised hover:text-fg"
          >
            Cancel
          </Link>
        </div>
      </Section>
    </form>
      <Confirm
        open={askOpen}
        onOpenChange={setAskOpen}
        title="Open it to anyone?"
        description={openWarning}
        action="Create without a whitelist"
        onConfirm={() => void submit(undefined, true)}
      />
    </>
  );
}
