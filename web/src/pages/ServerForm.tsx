import { useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, Plus, X } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { Link, useNavigate } from "react-router";
import { AddContentDialog, ContentList, kindsPhrase, type Software } from "../components/Content";
import { ModrinthSearch, ProjectIcon } from "../components/ModrinthSearch";
import { Picture, PictureChange, pictureForRequest } from "../components/Picture";
import { autoOpOptions, exposeOptions, joinHint, joinOptions, LaunchFlags, More, type Join } from "../components/ServerFields";
import { Button, Chip, cx, Field, Input, Notice, PageHeader, Section, Select, Textarea, Toggle } from "../components/ui";
import { onPreview, PreviewsToggle, previewWarning, VersionOptions } from "../components/VersionOptions";
import { api, ApiError, serverIcon, serverPath, type BackupOverride, type ContentRef, type ExposeType, type Loader, type Modpack, type Server, type ServerSpec } from "../lib/api";
import { ago, loaderNames } from "../lib/format";
import { launchOf } from "../lib/launch";
import type { Origin } from "../lib/origin";
import { difficulties, gameModes, levelTypes, parseProperties } from "../lib/properties";
import { keys, useMe } from "../lib/queries";
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

/** When it is snapshotted: as its organization, as the server it is a copy of, or not. */
type Schedule = "org" | "own" | "off";

/** Everything the form holds. A new server starts from defaults; one made from another, from what that one has. */
interface Values {
  kind: "game" | "modpack";
  loader: Loader | null;
  version: string;
  modpack: Modpack | null;
  displayName: string;
  /** null while it follows the display name. */
  customName: string | null;
  motd: string;
  picture: PictureChange;
  mods: ContentRef[];
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
  memory: string;
  storage: string;
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
  modpack: null,
  displayName: "",
  customName: null,
  motd: "",
  picture: { kind: "none" },
  mods: [],
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
  memory: "4",
  storage: "10",
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
    modpack: spec.modpack ?? null,
    displayName: (spec.displayName || source.name) + (copying ? " copy" : ""),
    customName: name,
    motd: p["motd"] ?? "",
    picture: { kind: "keep" },
    mods: spec.mods ?? [],
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
    join: p["white-list"] !== "true" ? "anyone" : (byRole ?? "list"),
    // Not set is the same as "operator".
    autoOp: spec.autoOp || "operator",
    memory: toGigabytes(spec.resources.memory) || "4",
    storage: String(Math.max(Number(toGigabytes(spec.storage.size)) || 10, leastStorage)),
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
  const set = <K extends keyof Values>(key: K, value: Values[K]) => setValues((was) => ({ ...was, [key]: value }));

  const [needsLoader, setNeedsLoader] = useState(false);
  const softwareRef = useRef<HTMLDivElement>(null);
  const versions = useGameVersions(v.kind === "game" ? v.loader : null);
  const [previews, setPreviews] = useState(false);
  // A version picked for one kind of server may have no build for the next.
  useEffect(() => {
    if (source || v.version === "LATEST" || !versions.data || versions.isPlaceholderData || versions.data.all.has(v.version)) return;
    toast.info(`${loaders.find((l) => l.id === v.loader)?.name ?? "This software"} has no build for ${v.version} yet`);
    setValues((was) => ({ ...was, version: "LATEST" }));
  }, [source, v.version, versions.data, versions.isPlaceholderData, v.loader]);
  const [addingMods, setAddingMods] = useState(false);
  const [moreWorld, setMoreWorld] = useState(false);
  const [moreNetwork, setMoreNetwork] = useState(false);
  const [moreLaunch, setMoreLaunch] = useState(false);
  const [eula, setEula] = useState(false);
  const [startNow, setStartNow] = useState(true);
  const custom = !!spec?.image || (spec?.env?.length ?? 0) > 0;
  const [keepCustom, setKeepCustom] = useState(custom);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const name = v.customName ?? slugify(v.displayName);
  const bringingBack = !!source && !copying && name === source.name;
  const nameHint = taken.has(name)
    ? `There is already a server named ${name}.`
    : history.has(name) && !bringingBack
      ? `Snapshots of an earlier server named ${name} go with the name. Choose another${source ? "" : ", or bring that server back from the Backups page"}.`
      : "It cannot be changed later.";
  const routed = v.exposeType === "Router";
  // What content fits. Until a pack is picked, what it will accept is still
  // that of a modpack, whose own loader is the one that counts.
  const software: Software | null =
    v.kind === "modpack" ? { loader: "vanilla", version: v.version, modpack: v.modpack ?? { source: "modrinth", project: "" } } : v.loader ? { loader: v.loader, version: v.version } : null;
  const modsChanged = JSON.stringify(v.mods) !== JSON.stringify(start.mods);
  // "Latest release" is whichever version that is now: picked for a copy of
  // a server already on it, nothing changes.
  const effective = (version: string) => (version === "LATEST" ? (versions.data?.latest ?? version) : version);
  const versionChanged = effective(v.version) !== effective(start.version);
  // The picture made from it will have: the original's as it is now for a
  // copy of it as it is now, or the one a snapshot holds.
  const brought = source ? (source.origin.kind === "snapshot" ? `/api/v1/orgs/${org}/snapshots/${source.origin.id}/icon` : source.live ? serverIcon(source.live) : undefined) : undefined;
  // "Server default" for a copy leaves its server.properties as it is; for a
  // new server it is the game's or the modpack's own default.
  const unset = source ? "Server default" : v.kind === "modpack" ? "Modpack default" : undefined;

  // Picks were filtered to fit one kind of server, so they do not carry over
  // to another.
  const changeSoftware = (change: Partial<Values>) => {
    if (v.mods.length > 0) toast.info(`Cleared ${v.mods.length} item${v.mods.length === 1 ? "" : "s"} picked for the previous software`);
    setValues((was) => ({ ...was, ...change, mods: [] }));
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
      properties,
      // Members from this role up who have linked a Minecraft account are let in.
      ...(v.join !== "anyone" && v.join !== "list" ? { autoWhitelist: v.join } : {}),
      ...(v.autoOp !== "operator" ? { autoOp: v.autoOp as ServerSpec["autoOp"] } : {}),
      ...(v.sleep && routed ? { sleep: { enabled: true, idleMinutes: Number(v.idleMinutes) || 15 } } : {}),
      ...(v.schedule === "off" ? { backups: { enabled: false } } : {}),
      jvm: { aikarFlags: v.aikar, extraFlags: v.extraFlags },
      // The game looks for its picture when it starts, so a server given one
      // is made stopped and started by the panel once the picture is there.
      ...(v.picture.kind === "file" ? { state: "Stopped" as const } : {}),
      ...(v.kind === "modpack" ? { modpack: v.modpack ?? undefined } : { loader: v.loader ?? undefined, version: v.version }),
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
    const sleeps = v.sleep && routed;
    if (sleeps !== start.sleep || (sleeps && v.idleMinutes !== start.idleMinutes)) changes.sleep = { enabled: sleeps, idleMinutes: Number(v.idleMinutes) || 15 };
    if (v.schedule !== start.schedule) changes.backups = v.schedule === "off" ? ({ enabled: false, minute: null, keep: null } satisfies BackupOverride) : null;
    if (modsChanged) changes.mods = v.mods;
    const jvm: NonNullable<ServerSpec["jvm"]> = {};
    if (v.aikar !== start.aikar) jvm.aikarFlags = v.aikar;
    if (v.extraFlags !== start.extraFlags) jvm.extraFlags = v.extraFlags;
    if (Object.keys(jvm).length > 0) changes.jvm = jvm;
    return {
      displayName: v.displayName,
      state: startNow ? "Running" : "Stopped",
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

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!source && v.kind === "modpack" && !v.modpack) return setError("Pick a modpack, or switch to a game version.");
    if (!source && v.kind === "game" && !v.loader) {
      setNeedsLoader(true);
      softwareRef.current?.scrollIntoView({ behavior: "smooth", block: "center" });
      return;
    }
    setBusy(true);
    setError(null);
    try {
      if (source) {
        const from = source.origin.kind === "server" ? { fromServer: source.origin.name } : { fromSnapshot: source.origin.id };
        // Left out, it keeps the one its files bring.
        const pictured = v.picture.kind === "file" ? { picture: await pictureForRequest(v.picture.file) } : v.picture.kind === "none" ? { picture: null } : {};
        const server = await api.post<Server>(`/orgs/${org}/servers`, { name, ...from, ...pictured, spec: copyRequest() });
        await Promise.all([qc.invalidateQueries({ queryKey: keys.servers(org) }), qc.invalidateQueries({ queryKey: keys.snapshots(org) })]);
        // How the copying goes shows on the server's page until its files are in.
        navigate(`/o/${org}/s/${server.name}`);
        return;
      }
      const server = await api.post<Server>(`/orgs/${org}/servers`, { name, spec: newRequest() });
      await qc.invalidateQueries({ queryKey: keys.servers(org) });
      // Its picture goes onto its volume, which is not there yet. The panel
      // waits for that, puts the picture in place and then starts the
      // server, whether or not this page is still open. The page moves on
      // and says how it went.
      if (v.picture.kind === "file") {
        api
          .putRaw(`${serverPath(org, server.name)}/icon?wait=true&start=true`, v.picture.file)
          .then(() => {
            qc.setQueryData(["picture-version"], Date.now());
            toast.info(`${v.displayName || server.name} has its picture`);
          })
          .catch((err) => toast.error(err instanceof ApiError ? `The picture was not set: ${err.message}` : "The picture was not set. Set it in the server's settings."))
          .finally(() => qc.invalidateQueries({ queryKey: keys.servers(org) }));
      }
      navigate(`/o/${org}/s/${server.name}`);
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
      hint={onPreview(versions.data, v.version) ? previewWarning : source && versionChanged ? "A world opened in a newer version does not open in an older one again." : undefined}
    >
      {(id) => (
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
          <Select id={id} value={v.version} onChange={(e) => set("version", e.target.value)} className="w-64">
            {/* Keeps the version it has selectable when the list could not be loaded, or does not have it. */}
            <VersionOptions
              versions={versions.data}
              previews={previews || onPreview(versions.data, v.version)}
              keep={source && start.version !== "LATEST" ? { value: start.version, label: start.version } : undefined}
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
    <form onSubmit={submit}>
      <Link to={`/o/${org}`} className="mb-3 inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
        <ChevronLeft className="size-3.5" />
        Servers
      </Link>
      <PageHeader title={!source ? "New server" : duplicating ? `Duplicate ${source.name}` : copying ? `New server from a snapshot of ${source.name}` : `Bring back ${source.name}`} />

      {source ? (
        <Section title="Starting point" description="Everything below starts as the original has it.">
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
            <button type="button" className={segment(v.kind === "game")} onClick={() => v.kind !== "game" && changeSoftware({ kind: "game" })}>
              Game version
            </button>
            <button type="button" className={segment(v.kind === "modpack")} onClick={() => v.kind !== "modpack" && changeSoftware({ kind: "modpack" })}>
              Modpack
            </button>
          </div>
          {v.kind === "game" ? (
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
          ) : v.modpack ? (
            <div className="flex items-center gap-3 rounded-md border border-accent bg-accent-wash px-3 py-2.5">
              <ProjectIcon url={v.modpack.iconUrl} />
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

      <Section title="Name and picture" description="How it appears in the panel and in a player's server list.">
        <PictureChange name={v.displayName || source?.name || ""} current={brought} value={v.picture} onChange={(change) => set("picture", change)} />
        <Field label="Display name">{(id) => <Input id={id} required value={v.displayName} onChange={(e) => set("displayName", e.target.value)} />}</Field>
        <Field label="Short name" hint={nameHint}>
          {(id) => <Input id={id} required value={name} onChange={(e) => set("customName", e.target.value)} pattern="[a-z]([a-z0-9\-]{0,38}[a-z0-9])?" className="font-mono" spellCheck={false} />}
        </Field>
        <Field label="Message of the day" hint={source ? "The line under its name in a player's server list." : "Optional. The line under its name in a player's server list."}>
          {(id) => <Input id={id} value={v.motd} onChange={(e) => set("motd", e.target.value)} placeholder="A Minecraft Server" />}
        </Field>
      </Section>

      <Section
        title="Content"
        description={
          !source
            ? software
              ? "Optional. Can also be added later."
              : "Optional."
            : v.modpack
              ? "Its modpack comes with it. Anything added on top of it can be changed here."
              : "Comes with it. What is taken away here is removed before it first starts."
        }
      >
        {v.mods.length > 0 ? (
          <ContentList mods={v.mods} onRemove={(i) => set("mods", v.mods.filter((_, j) => j !== i))} />
        ) : (
          source && software && <p className="text-[13px] text-muted">{v.modpack ? "Nothing on top of its modpack." : `No ${kindsPhrase(software)}.`}</p>
        )}
        <div className="flex flex-wrap items-center gap-3">
          <Button icon={<Plus className="size-4" />} disabled={!software || (v.kind === "modpack" && !v.modpack)} onClick={() => setAddingMods(true)}>
            Add {software ? kindsPhrase(software) : "content"}
          </Button>
          {source && modsChanged && (
            <button type="button" className="text-xs text-muted hover:text-fg" onClick={() => set("mods", start.mods)}>
              Put back as {source.name} has it
            </button>
          )}
        </div>
        {addingMods && software && (
          <AddContentDialog
            software={software}
            mods={v.mods}
            description={source ? "Downloaded when the server first starts. Files can be uploaded once it exists." : "Downloaded when the server first starts."}
            onAdd={(ref) => setValues((was) => ({ ...was, mods: [...was.mods, ref] }))}
            onClose={() => setAddingMods(false)}
          />
        )}
      </Section>

      <Section
        title="World"
        description={
          source
            ? "Its world comes with it. Settings left on Server default stay as its server.properties has them."
            : v.kind === "modpack"
              ? "A pack that ships a ready-made world ignores the world options."
              : undefined
        }
      >
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
            <fieldset disabled={!!source} className="flex flex-col gap-4 rounded-md border border-line p-4 disabled:opacity-60">
              <p className="text-xs text-muted">{source ? "Its world has been generated already: these are as it was made." : "Fixed once the world has been generated."}</p>
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
              <Toggle checked={v.structures} disabled={!!source} onChange={(on) => set("structures", on)} label="Generate structures" />
              <Toggle checked={v.hardcore} disabled={!!source} onChange={(on) => set("hardcore", on)} label="Hardcore" hint="Difficulty is locked to hard, and players who die can only spectate." />
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
          <Field label="Operators by role" hint="Members with this role who have linked a Minecraft account are operators in game.">
            {(id) => (
              <Select id={id} value={v.autoOp} onChange={(e) => set("autoOp", e.target.value)}>
                {autoOpOptions}
              </Select>
            )}
          </Field>
        </div>
        {v.join !== "anyone" && v.join !== "list" && me && !me.minecraft && (
          <p className="text-xs leading-relaxed text-muted">
            You have not linked a Minecraft account yet, so you could not join it yourself:{" "}
            <Link to="/account" className="text-accent hover:underline">
              link it under Account
            </Link>
            .
          </p>
        )}
      </Section>

      <Section
        title="Resources"
        description={source ? (source.size > 0 ? `Its files come to ${(source.size / GiB).toFixed(1)} GB.` : undefined) : v.kind === "modpack" ? "Large modpacks usually want 6 GB or more." : "4 GB suits a small server."}
      >
        <div className="grid grid-cols-2 gap-4">
          <Field label="Memory (GB)">{(id) => <Input id={id} type="number" required min={0.5} step={0.5} value={v.memory} onChange={(e) => set("memory", e.target.value)} />}</Field>
          <Field label="Storage (GB)">{(id) => <Input id={id} type="number" required min={leastStorage} step={1} value={v.storage} onChange={(e) => set("storage", e.target.value)} />}</Field>
        </div>
        <More label="Launch flags" changed={(v.aikar !== start.aikar ? 1 : 0) + (v.extraFlags !== start.extraFlags ? 1 : 0)} open={moreLaunch} onToggle={() => setMoreLaunch(!moreLaunch)} />
        {moreLaunch && <LaunchFlags aikar={v.aikar} extra={v.extraFlags} onAikar={(on) => set("aikar", on)} onExtra={(text) => set("extraFlags", text)} fromSnapshot={source?.origin.kind === "snapshot"} />}
      </Section>

      <Section title="Network">
        <Field label="Hostname" hint={source ? "Optional. Its own: the original's address is not taken along." : "Optional. Point this name's DNS at the avSYS router."}>
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
              hint={routed ? "Stops the game while nobody is playing. The first player to join wakes it and is asked to rejoin once it is up." : "Needs the shared address."}
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
        <Field label="Scheduled snapshots" hint={source ? undefined : v.schedule === "org" ? "Taken daily with the rest of the organization's servers." : "Only snapshotted by hand."}>
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
              , which applies to running a server.
            </span>
          </label>
        ) : (
          <>
            <Toggle checked={startNow} onChange={setStartNow} label="Start it when its files are in place" />
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
  );
}
