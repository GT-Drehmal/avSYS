import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, Plus } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { AddContentDialog, ContentList, kindsPhrase, type Software } from "../components/Content";
import { Picture } from "../components/Picture";
import { autoOpOptions, exposeOptions, GameplayFields, gameplayKeys, gameplayOf, joinHint, joinOptions, More, type Gameplay, type Join } from "../components/ServerFields";
import { Button, Field, Input, Loading, Notice, PageHeader, Section, Select, Toggle } from "../components/ui";
import { onPreview, PreviewsToggle, previewWarning, VersionOptions } from "../components/VersionOptions";
import { api, ApiError, serverIcon, serverPath, type BackupOverride, type ContentRef, type ExposeType, type Server, type ServerSpec, type SnapshotDetail } from "../lib/api";
import { ago, loaderNames } from "../lib/format";
import { keys, useOrgSnapshots, useServers } from "../lib/queries";
import { type Origin } from "../lib/origin";
import { fromGigabytes, toGigabytes, useGameVersions } from "../lib/software";

const GiB = 1024 ** 3;

/** The first of name, name-2, name-3… that no server has. Names are at most 40 characters. */
function freeName(wanted: string, taken: Set<string>) {
  for (let n = 1; ; n++) {
    const suffix = n === 1 ? "" : `-${n}`;
    const name = wanted.slice(0, 40 - suffix.length).replace(/-+$/, "") + suffix;
    if (!taken.has(name)) return name;
  }
}

/**
 * The page for making a server out of one that exists or a snapshot. It is
 * the new-server page with the same sections in the same order, starting
 * filled with what the original has.
 */
export function NewFromSource({ org, origin }: { org: string; origin: Origin }) {
  const servers = useServers(org);
  // Names that snapshots are under go with them: a new server cannot take one.
  const snapshots = useOrgSnapshots(org);
  const snapshot = useQuery({
    queryKey: ["snapshot", org, origin.kind === "snapshot" ? origin.id : ""],
    queryFn: () => api.get<SnapshotDetail>(`/orgs/${org}/snapshots/${origin.kind === "snapshot" ? origin.id : ""}`),
    enabled: origin.kind === "snapshot",
    retry: false,
  });
  const back = (
    <Link to={`/o/${org}`} className="mb-3 inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
      <ChevronLeft className="size-3.5" />
      Servers
    </Link>
  );
  if (servers.isPending || snapshots.isPending || (origin.kind === "snapshot" && snapshot.isPending)) return <Loading />;
  // Without the list of servers it cannot be said which names are taken, or
  // whether the original still exists: nothing is guessed from its absence.
  if (!servers.data) {
    return (
      <>
        {back}
        <PageHeader title="New server" />
        <Notice tone="danger">{servers.error instanceof ApiError ? servers.error.message : "The organization's servers could not be loaded."}</Notice>
      </>
    );
  }
  const taken = new Set(servers.data.map((s) => s.name));
  // Without the snapshots (backups switched off, say) none are known of: the
  // panel says so if a name turns out to have some.
  const history = new Set((snapshots.data?.snapshots ?? []).map((s) => s.server));
  const live = origin.kind === "server" ? servers.data?.find((s) => s.name === origin.name) : undefined;
  const source: Source | null =
    origin.kind === "server"
      ? live
        ? { name: live.name, spec: live.spec, size: 0, live }
        : null
      : snapshot.data
        ? { name: snapshot.data.server, spec: snapshot.data.spec, size: snapshot.data.size, taken: snapshot.data.time, live: servers.data?.find((s) => s.name === snapshot.data.server) }
        : null;
  if (!source) {
    return (
      <>
        {back}
        <PageHeader title="New server" />
        <Notice tone="danger">
          {origin.kind === "server"
            ? `There is no server named ${origin.name} to copy.`
            : snapshot.error instanceof ApiError
              ? snapshot.error.message
              : "The snapshot could not be read."}
        </Notice>
      </>
    );
  }
  return (
    <>
      {back}
      <Form key={`${origin.kind}:${source.name}`} org={org} origin={origin} source={source} taken={taken} history={history} />
    </>
  );
}

interface Source {
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

/** What can be changed about the new server beyond its name and size. Everything starts as the original has it. */
interface Options extends Gameplay {
  version: string;
  motd: string;
  join: Join;
  autoOp: string;
  sleep: boolean;
  idleMinutes: string;
  schedule: Schedule;
  expose: ExposeType;
}

function optionsOf(spec: ServerSpec): Options {
  const p = spec.properties ?? {};
  const byRole = spec.autoWhitelist && spec.autoWhitelist !== "off" ? spec.autoWhitelist : null;
  return {
    ...gameplayOf(p),
    version: spec.version,
    motd: p["motd"] ?? "",
    join: p["white-list"] !== "true" ? "anyone" : (byRole ?? "list"),
    // Not set is the same as "operator".
    autoOp: spec.autoOp || "operator",
    sleep: !!spec.sleep?.enabled,
    idleMinutes: String(spec.sleep?.idleMinutes || 15),
    schedule: !spec.backups ? "org" : spec.backups.enabled === false ? "off" : "own",
    expose: spec.expose?.type ?? "Router",
  };
}

/**
 * The part of a request that says what differs from the original. Only what
 * was changed is named: the rest is taken from the snapshot, which also has
 * what this form does not show.
 */
function changesOf(now: Options, was: Options): Partial<ServerSpec> {
  const changes: Partial<ServerSpec> = {};
  // A setting put back on "Server default" is sent as null: the panel stops
  // setting it, and the copied server.properties keeps what it has.
  const properties: Record<string, string | null> = {};
  const property = (name: string, value: string) => (properties[name] = value === "" ? null : value);
  if (now.version !== was.version) changes.version = now.version;
  if (now.motd !== was.motd) property("motd", now.motd);
  for (const key of Object.keys(gameplayKeys) as (keyof Gameplay)[]) {
    if (now[key] !== was[key]) property(gameplayKeys[key], now[key]);
  }
  if (now.join !== was.join) {
    if (now.join === "anyone") {
      property("white-list", "false");
    } else {
      property("white-list", "true");
      // Whoever is taken off the whitelist is put out, not only kept out.
      property("enforce-whitelist", "true");
      changes.autoWhitelist = now.join === "list" ? "off" : now.join;
    }
  }
  if (now.autoOp !== was.autoOp) changes.autoOp = now.autoOp as ServerSpec["autoOp"];
  const sleeps = now.sleep && now.expose === "Router";
  if (sleeps !== was.sleep || (sleeps && now.idleMinutes !== was.idleMinutes)) changes.sleep = { enabled: sleeps, idleMinutes: Number(now.idleMinutes) || 15 };
  if (now.schedule !== was.schedule) changes.backups = now.schedule === "off" ? ({ enabled: false, minute: null, keep: null } satisfies BackupOverride) : null;
  if (Object.keys(properties).length > 0) changes.properties = properties as Record<string, string>;
  return changes;
}

function Form({ org, origin, source, taken, history }: { org: string; origin: Origin; source: Source; taken: Set<string>; history: Set<string> }) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { spec } = source;
  const duplicating = origin.kind === "server";
  // A server that still exists is being copied; one that is gone gets its name back.
  const copying = taken.has(source.name);
  // Whole gigabytes that hold the snapshot's files with some room to spare.
  const leastStorage = Math.max(1, Math.ceil((source.size * 1.2) / GiB));
  const custom = !!spec.image || (spec.env?.length ?? 0) > 0;
  // The picture the new server will have: the original's as it is now for a
  // copy of it as it is now, or the one a snapshot holds.
  const picture = origin.kind === "snapshot" ? `/api/v1/orgs/${org}/snapshots/${origin.id}/icon` : source.live ? serverIcon(source.live) : undefined;
  const original = optionsOf(spec);
  const [options, setOptions] = useState(original);
  const option = <K extends keyof Options>(key: K, value: Options[K]) => setOptions((o) => ({ ...o, [key]: value }));
  const versions = useGameVersions(spec.modpack ? null : spec.loader);
  const [previews, setPreviews] = useState(false);
  const routed = options.expose === "Router";
  const changes = changesOf(options, original);
  const [moreNetwork, setMoreNetwork] = useState(false);
  const [moreWorld, setMoreWorld] = useState(false);

  const [displayName, setDisplayName] = useState((spec.displayName || source.name) + (copying ? " copy" : ""));
  // One that is gone is brought back under its own name, and its history goes
  // on. A copy is a new server, and takes a name no server or snapshot has.
  const [name, setName] = useState(copying ? freeName(`${source.name}-copy`, new Set([...taken, ...history])) : source.name);
  const nameHint = taken.has(name)
    ? `There is already a server named ${name}.`
    : history.has(name) && !(name === source.name && !copying)
      ? `Snapshots of an earlier server named ${name} go with the name. Choose another.`
      : "It cannot be changed later.";
  const [memory, setMemory] = useState(toGigabytes(spec.resources.memory) || "4");
  const [storage, setStorage] = useState(String(Math.max(Number(toGigabytes(spec.storage.size)) || 10, leastStorage)));
  const [hostname, setHostname] = useState("");
  const [start, setStart] = useState(true);
  const [keepCustom, setKeepCustom] = useState(custom);
  const [mods, setMods] = useState<ContentRef[]>(spec.mods ?? []);
  const [addingMods, setAddingMods] = useState(false);
  // Only sent when changed: what is left out is taken as the original has it.
  const modsChanged = JSON.stringify(mods) !== JSON.stringify(spec.mods ?? []);
  const fits: Software = { loader: spec.loader, version: options.version, modpack: spec.modpack };
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    const request: Partial<ServerSpec> = {
      displayName,
      state: start ? "Running" : "Stopped",
      resources: { memory: fromGigabytes(memory) },
      storage: { size: fromGigabytes(storage) },
      // Reached the way the original is unless that was changed: a server
      // that is not exposed at all is not given an address because it was
      // copied. A fixed address or port is the original's own and is not
      // taken along.
      expose: options.expose === "None" ? { type: "None" } : { type: options.expose, hostname: hostname.trim() },
      ...(keepCustom ? { image: spec.image, env: spec.env } : {}),
      ...(modsChanged ? { mods } : {}),
      ...changes,
    };
    try {
      const from = origin.kind === "server" ? { fromServer: origin.name } : { fromSnapshot: origin.id };
      const server = await api.post<Server>(`/orgs/${org}/servers`, { name, ...from, spec: request });
      await Promise.all([qc.invalidateQueries({ queryKey: keys.servers(org) }), qc.invalidateQueries({ queryKey: keys.snapshots(org) })]);
      // Where any new server opens. How the copying goes shows there, as on
      // every tab, until its files are in.
      navigate(`/o/${org}/s/${server.name}`);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not create the server.");
      setBusy(false);
    }
  };

  const software = spec.modpack ? `${spec.modpack.title || spec.modpack.project} modpack` : `${loaderNames[spec.loader] ?? spec.loader} ${spec.version}`;
  const worldChanged = (["maxPlayers", "pvp"] as const).filter((k) => options[k] !== original[k]).length;
  const networkChanged = (options.expose !== original.expose ? 1 : 0) + ((options.sleep && routed) !== original.sleep || (options.sleep && routed && options.idleMinutes !== original.idleMinutes) ? 1 : 0);
  return (
    <form onSubmit={submit}>
      <PageHeader title={duplicating ? `Duplicate ${source.name}` : copying ? `New server from a snapshot of ${source.name}` : `Bring back ${source.name}`} />

      <Section title="Starting point" description="Everything below starts as the original has it.">
        <div className="flex items-center gap-3 rounded-md border border-accent bg-accent-wash px-3 py-2.5">
          <Picture name={spec.displayName || source.name} url={picture} size={36} />
          <div className="min-w-0 flex-1">
            <p className="truncate text-[13px] font-medium">
              {duplicating ? `${source.name} as it is now` : `${source.name} as it was ${ago(source.taken)}`}
              {source.taken && <span className="ml-2 font-normal text-muted">{new Date(source.taken).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}</span>}
            </p>
            <p className="truncate text-xs text-muted">
              {software}
              {copying ? `. ${source.name} itself is not touched.` : ". It no longer exists; this brings it back."}
            </p>
          </div>
        </div>
        {!spec.modpack && (
          <Field
            label="Game version"
            hint={onPreview(versions.data, options.version) ? previewWarning : options.version !== original.version ? "A world opened in a newer version does not open in an older one again." : undefined}
          >
            {(id) => (
              <div className="flex flex-wrap items-center gap-x-4 gap-y-1">
                <Select id={id} value={options.version} onChange={(e) => option("version", e.target.value)} className="w-64">
                  {/* Keeps the version it has selectable when the list could not be loaded, or does not have it. */}
                  <VersionOptions versions={versions.data} previews={previews || onPreview(versions.data, options.version)} keep={original.version === "LATEST" ? undefined : { value: original.version, label: original.version }} />
                </Select>
                <PreviewsToggle versions={versions.data} previews={previews} onChange={setPreviews} selected={options.version} />
              </div>
            )}
          </Field>
        )}
      </Section>

      <Section title="Name and picture" description="How it appears in the panel and in a player's server list.">
        <div className="flex items-center gap-3">
          <Picture name={displayName || source.name} url={picture} size={56} />
          <p className="text-xs leading-relaxed text-muted">Its picture comes with its files. It can be changed in the new server's settings.</p>
        </div>
        <Field label="Display name">{(id) => <Input id={id} required value={displayName} onChange={(e) => setDisplayName(e.target.value)} />}</Field>
        <Field label="Short name" hint={nameHint}>
          {(id) => <Input id={id} required value={name} onChange={(e) => setName(e.target.value)} pattern="[a-z]([a-z0-9\-]{0,38}[a-z0-9])?" className="font-mono" spellCheck={false} />}
        </Field>
        <Field label="Message of the day" hint="The line under its name in a player's server list.">
          {(id) => <Input id={id} value={options.motd} onChange={(e) => option("motd", e.target.value)} placeholder="A Minecraft Server" />}
        </Field>
      </Section>

      <Section
        title="Content"
        description={spec.modpack ? "Its modpack comes with it. Anything added on top of it can be changed here." : "Comes with it. What is taken away here is removed before it first starts."}
      >
        {mods.length > 0 ? (
          <ContentList mods={mods} onRemove={(i) => setMods(mods.filter((_, j) => j !== i))} />
        ) : (
          <p className="text-[13px] text-muted">{spec.modpack ? "Nothing on top of its modpack." : `No ${kindsPhrase(fits)}.`}</p>
        )}
        <div className="flex flex-wrap items-center gap-3">
          <Button icon={<Plus className="size-4" />} onClick={() => setAddingMods(true)}>
            Add {kindsPhrase(fits)}
          </Button>
          {modsChanged && (
            <button type="button" className="text-xs text-muted hover:text-fg" onClick={() => setMods(spec.mods ?? [])}>
              Put back as {source.name} has it
            </button>
          )}
        </div>
        {addingMods && (
          <AddContentDialog
            software={fits}
            mods={mods}
            description="Downloaded when the server first starts. Files can be uploaded once it exists."
            onAdd={(ref) => setMods((current) => [...current, ref])}
            onClose={() => setAddingMods(false)}
          />
        )}
      </Section>

      <Section title="World" description="Its world comes with it. Settings left on Server default stay as its server.properties has them.">
        <div className="grid grid-cols-2 gap-4">
          <GameplayFields value={options} onChange={option} only={["gamemode", "difficulty"]} />
        </div>
        <More label="Player limit and PvP" changed={worldChanged} open={moreWorld} onToggle={() => setMoreWorld(!moreWorld)} />
        {moreWorld && (
          <div className="grid grid-cols-2 gap-4">
            <GameplayFields value={options} onChange={option} only={["maxPlayers", "pvp"]} />
          </div>
        )}
      </Section>

      <Section title="Players">
        <div className="grid grid-cols-2 gap-4">
          <Field label="Who can join" hint={joinHint(options.join)}>
            {(id) => (
              <Select id={id} value={options.join} onChange={(e) => option("join", e.target.value as Join)}>
                {joinOptions}
              </Select>
            )}
          </Field>
          <Field label="Operators by role" hint="Members with this role who have linked a Minecraft account are operators in game.">
            {(id) => (
              <Select id={id} value={options.autoOp} onChange={(e) => option("autoOp", e.target.value)}>
                {autoOpOptions}
              </Select>
            )}
          </Field>
        </div>
      </Section>

      <Section title="Resources" description={source.size > 0 ? `Its files come to ${(source.size / GiB).toFixed(1)} GB.` : undefined}>
        <div className="grid grid-cols-2 gap-4">
          <Field label="Memory (GB)">{(id) => <Input id={id} type="number" required min={0.5} step={0.5} value={memory} onChange={(e) => setMemory(e.target.value)} />}</Field>
          <Field label="Storage (GB)">{(id) => <Input id={id} type="number" required min={leastStorage} step={1} value={storage} onChange={(e) => setStorage(e.target.value)} />}</Field>
        </div>
      </Section>

      <Section title="Network">
        <Field label="Hostname" hint="Optional. Its own: the original's address is not taken along.">
          {(id) => <Input id={id} value={hostname} disabled={options.expose === "None"} onChange={(e) => setHostname(e.target.value)} placeholder="play.example.com" className="font-mono" spellCheck={false} />}
        </Field>
        <More label="How it is reached, and sleep" changed={networkChanged} open={moreNetwork} onToggle={() => setMoreNetwork(!moreNetwork)} />
        {moreNetwork && (
          <>
            <Field label="How it is reached">
              {(id) => (
                <Select id={id} value={options.expose} onChange={(e) => option("expose", e.target.value as ExposeType)}>
                  {exposeOptions}
                </Select>
              )}
            </Field>
            <Toggle
              checked={options.sleep && routed}
              disabled={!routed}
              onChange={(v) => option("sleep", v)}
              label="Sleep when empty"
              hint={routed ? "Stops the game while nobody is playing. The first player to join wakes it and is asked to rejoin once it is up." : "Needs the shared address."}
            />
            {options.sleep && routed && (
              <Field label="After minutes with no players">
                {(id) => <Input id={id} type="number" min={1} value={options.idleMinutes} onChange={(e) => option("idleMinutes", e.target.value)} className="max-w-32" />}
              </Field>
            )}
          </>
        )}
      </Section>

      <Section title="Backups">
        <Field label="Scheduled snapshots">
          {(id) => (
            <Select id={id} value={options.schedule} onChange={(e) => option("schedule", e.target.value as Schedule)}>
              <option value="org">Same as the organization</option>
              {original.schedule === "own" && <option value="own">Its own, as {source.name} has</option>}
              <option value="off">Off</option>
            </Select>
          )}
        </Field>
      </Section>

      <Section title={duplicating ? "Duplicate" : "Create"}>
        <Toggle checked={start} onChange={setStart} label="Start it when its files are in place" />
        {custom && (
          <Toggle
            checked={keepCustom}
            onChange={setKeepCustom}
            label="Keep its custom image and environment"
            hint={
              <>
                {spec.image && (
                  <>
                    Image <span className="font-mono">{spec.image}</span>.{" "}
                  </>
                )}
                {(spec.env?.length ?? 0) > 0 && (
                  <>
                    Variables <span className="font-mono">{spec.env!.map((v) => v.name).join(", ")}</span>.
                  </>
                )}
              </>
            }
          />
        )}
        {error && <Notice tone="danger">{error}</Notice>}
        <div className="flex gap-2">
          <Button type="submit" variant="primary" loading={busy}>
            {duplicating ? "Duplicate" : "Create server"}
          </Button>
          <Link to={source.live ? serverPath(org, source.live.name).replace("/orgs/", "/o/").replace("/servers/", "/s/") : `/o/${org}/backups`} className="inline-flex h-8 items-center rounded-md px-3 text-[13px] font-medium text-muted hover:bg-raised hover:text-fg">
            Cancel
          </Link>
        </div>
      </Section>
    </form>
  );
}
