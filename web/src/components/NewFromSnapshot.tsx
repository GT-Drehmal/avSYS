import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronDown, ChevronRight } from "lucide-react";
import { useState } from "react";
import { useNavigate } from "react-router";
import { api, ApiError, type BackupOverride, type ExposeType, type Server, type ServerSpec, type Snapshot, type SnapshotDetail } from "../lib/api";
import { ago, describeSoftware, loaderNames } from "../lib/format";
import { keys, useServers } from "../lib/queries";
import { fromGigabytes, toGigabytes, useGameVersions } from "../lib/software";
import { autoOpOptions, exposeOptions, GameplayFields, gameplayKeys, gameplayOf, memberRoleOptions, type Gameplay } from "./ServerFields";
import { Button, Field, Input, Loading, Modal, Notice, Select, Toggle } from "./ui";
import { onPreview, PreviewsToggle, previewWarning, VersionOptions } from "./VersionOptions";

const GiB = 1024 ** 3;

/** The first of name, name-2, name-3… that no server has. Names are at most 40 characters. */
function freeName(wanted: string, taken: Set<string>) {
  for (let n = 1; ; n++) {
    const suffix = n === 1 ? "" : `-${n}`;
    const name = wanted.slice(0, 40 - suffix.length).replace(/-+$/, "") + suffix;
    if (!taken.has(name)) return name;
  }
}

interface Props {
  org: string;
  /** The snapshot to make a server from, or null while the dialog is closed. */
  snapshot: Snapshot | null;
  onClose: () => void;
}

/**
 * Makes a new server out of a snapshot: a deleted server brought back, or a
 * copy of one that is still there.
 */
export function NewFromSnapshot({ org, snapshot, onClose }: Props) {
  const detail = useQuery({
    queryKey: ["snapshot", org, snapshot?.id],
    queryFn: () => api.get<SnapshotDetail>(`/orgs/${org}/snapshots/${snapshot!.id}`),
    enabled: !!snapshot,
    retry: false,
  });
  const servers = useServers(org);
  const software = snapshot ? [loaderNames[snapshot.loader as keyof typeof loaderNames] ?? snapshot.loader, snapshot.version].filter(Boolean).join(" ") : "";
  return (
    <Modal
      open={!!snapshot}
      onOpenChange={(open) => !open && onClose()}
      title="New server from this snapshot"
      description={snapshot ? [`${snapshot.server} as it was ${ago(snapshot.time)}`, software].filter(Boolean).join(" · ") : ""}
    >
      {detail.isPending || servers.isPending ? (
        <Loading />
      ) : detail.data && servers.data ? (
        <Form
          key={detail.data.id}
          org={org}
          source={{ name: detail.data.server, spec: detail.data.spec, size: detail.data.size }}
          taken={new Set(servers.data.map((s) => s.name))}
          from={{ fromSnapshot: detail.data.id }}
          onClose={onClose}
        />
      ) : (
        <Notice tone="danger">{detail.error instanceof ApiError ? detail.error.message : "The snapshot could not be read."}</Notice>
      )}
    </Modal>
  );
}

/**
 * Makes a copy of a server as it is now. A snapshot of it is taken first,
 * unless its newest one already has everything.
 */
export function DuplicateServer({ server, open, onClose }: { server: Server; open: boolean; onClose: () => void }) {
  const servers = useServers(server.org);
  return (
    <Modal open={open} onOpenChange={(o) => !o && onClose()} title={`Duplicate ${server.name}`} description={describeSoftware(server)}>
      {!servers.data ? (
        <Loading />
      ) : (
        <Form
          org={server.org}
          // How large its files are is not known here; its storage is at least that.
          source={{ name: server.name, spec: server.spec, size: 0 }}
          taken={new Set(servers.data.map((s) => s.name))}
          from={{ fromServer: server.name }}
          onClose={onClose}
        />
      )}
    </Modal>
  );
}

/** Who may join: anyone, the organization's members from a role up, or only who is on the whitelist. */
type Join = "anyone" | "viewer" | "operator" | "admin" | "owner" | "list";

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

interface FormProps {
  org: string;
  /** The server the new one is made of: its name, its settings, and how large its files are. */
  source: { name: string; spec: ServerSpec; size: number };
  taken: Set<string>;
  /** What it is made from: a snapshot, or the server as it is now. */
  from: { fromSnapshot: string } | { fromServer: string };
  onClose: () => void;
}

function Form({ org, source, taken, from, onClose }: FormProps) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { spec } = source;
  const detail = { server: source.name, size: source.size };
  const duplicating = "fromServer" in from;
  // A server that still exists is being copied; one that is gone gets its name back.
  const copying = taken.has(detail.server);
  // Whole gigabytes that hold the snapshot's files with some room to spare.
  const leastStorage = Math.max(1, Math.ceil((detail.size * 1.2) / GiB));
  const custom = !!spec.image || (spec.env?.length ?? 0) > 0;
  const original = optionsOf(spec);
  const [options, setOptions] = useState(original);
  const option = <K extends keyof Options>(key: K, value: Options[K]) => setOptions((o) => ({ ...o, [key]: value }));
  const [more, setMore] = useState(false);
  const versions = useGameVersions(spec.modpack ? null : spec.loader);
  const [previews, setPreviews] = useState(false);
  // Whether it gets a hostname of its own, which is how the router finds it.
  const routed = options.expose === "Router";
  const changes = changesOf(options, original);
  // How many things differ from the original, counting each game setting as one.
  const { properties: changedProperties = {}, ...changedSettings } = changes;
  const changed = Object.keys(changedSettings).length + Object.keys(changedProperties).length + (options.expose !== original.expose ? 1 : 0);

  const [displayName, setDisplayName] = useState((spec.displayName || detail.server) + (copying ? " copy" : ""));
  const [name, setName] = useState(freeName(copying ? `${detail.server}-copy` : detail.server, taken));
  const [memory, setMemory] = useState(toGigabytes(spec.resources.memory) || "4");
  const [storage, setStorage] = useState(String(Math.max(Number(toGigabytes(spec.storage.size)) || 10, leastStorage)));
  const [hostname, setHostname] = useState("");
  const [start, setStart] = useState(true);
  const [keepCustom, setKeepCustom] = useState(custom);
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
      ...changes,
    };
    try {
      const server = await api.post<Server>(`/orgs/${org}/servers`, { name, ...from, spec: request });
      await Promise.all([qc.invalidateQueries({ queryKey: keys.servers(org) }), qc.invalidateQueries({ queryKey: keys.snapshots(org) })]);
      onClose();
      // Its Backups tab is where the copying is shown.
      navigate(`/o/${org}/s/${server.name}/backups`);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not create the server.");
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      <p className="text-[13px] leading-relaxed text-muted">
        {duplicating
          ? `A copy of ${detail.server} as it is now. ${detail.server} itself is not touched.`
          : copying
            ? `A copy of ${detail.server} as it was then. ${detail.server} itself is not touched.`
            : `Brings ${detail.server} back as it was then.`}
      </p>
      <div className="grid grid-cols-2 gap-4">
        <Field label="Display name">{(id) => <Input id={id} required value={displayName} onChange={(e) => setDisplayName(e.target.value)} />}</Field>
        <Field label="Short name">
          {(id) => <Input id={id} required value={name} onChange={(e) => setName(e.target.value)} pattern="[a-z]([a-z0-9\-]{0,38}[a-z0-9])?" className="font-mono" spellCheck={false} />}
        </Field>
        <Field label="Memory (GB)">{(id) => <Input id={id} type="number" required min={0.5} step={0.5} value={memory} onChange={(e) => setMemory(e.target.value)} />}</Field>
        <Field label="Storage (GB)">{(id) => <Input id={id} type="number" required min={leastStorage} step={1} value={storage} onChange={(e) => setStorage(e.target.value)} />}</Field>
      </div>
      {options.expose !== "None" && (
        <Field label="Hostname" hint="Optional.">
          {(id) => <Input id={id} value={hostname} onChange={(e) => setHostname(e.target.value)} placeholder="play.example.com" className="font-mono" spellCheck={false} />}
        </Field>
      )}
      <Toggle checked={start} onChange={setStart} label="Start it when its files are in place" />
      <button type="button" aria-expanded={more} onClick={() => setMore(!more)} className="flex w-fit items-center gap-1 text-[13px] text-muted hover:text-fg">
        {more ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
        More options
        {changed > 0 && <span className="text-accent">· {changed} changed</span>}
      </button>
      {more && (
        <div className="flex flex-col gap-4 rounded-md border border-line p-4">
          <p className="text-xs text-muted">As {detail.server} has them. Anything else can be changed in the new server's settings.</p>
          {!spec.modpack && (
            <Field label="Game version" hint={onPreview(versions.data, options.version) ? previewWarning : options.version !== original.version ? "A world opened in a newer version does not open in an older one again." : undefined}>
              {(id) => (
                <div className="flex flex-col gap-1">
                  <Select id={id} value={options.version} onChange={(e) => option("version", e.target.value)}>
                    {/* Keeps the version it has selectable when the list could not be loaded, or does not have it. */}
                    <VersionOptions versions={versions.data} previews={previews || onPreview(versions.data, options.version)} keep={original.version === "LATEST" ? undefined : { value: original.version, label: original.version }} />
                  </Select>
                  <PreviewsToggle versions={versions.data} previews={previews} onChange={setPreviews} selected={options.version} />
                </div>
              )}
            </Field>
          )}
          <Field label="Message of the day">{(id) => <Input id={id} value={options.motd} onChange={(e) => option("motd", e.target.value)} placeholder="A Minecraft Server" />}</Field>
          <div className="grid grid-cols-2 gap-4">
            <GameplayFields value={options} onChange={option} />
          </div>
          <p className="-mt-2 text-xs text-muted">Server default leaves server.properties as {detail.server} has it.</p>
          <div className="grid grid-cols-2 gap-4">
            <Field label="Who can join" hint={options.join === "anyone" ? "No whitelist." : options.join === "list" ? "The players on its whitelist." : "Those who have linked a Minecraft account, and the players on its whitelist."}>
              {(id) => (
                <Select id={id} value={options.join} onChange={(e) => option("join", e.target.value as Join)}>
                  <option value="anyone">Anyone with the address</option>
                  {memberRoleOptions}
                  <option value="list">Only its whitelist</option>
                </Select>
              )}
            </Field>
            <Field label="Operators by role">
              {(id) => (
                <Select id={id} value={options.autoOp} onChange={(e) => option("autoOp", e.target.value)}>
                  {autoOpOptions}
                </Select>
              )}
            </Field>
          </div>
          <div className="flex flex-col gap-4">
            <Field label="How it is reached">
              {(id) => (
                <Select id={id} value={options.expose} onChange={(e) => option("expose", e.target.value as ExposeType)}>
                  {exposeOptions}
                </Select>
              )}
            </Field>
            <Field label="Scheduled snapshots">
              {(id) => (
                <Select id={id} value={options.schedule} onChange={(e) => option("schedule", e.target.value as Schedule)}>
                  <option value="org">Same as the organization</option>
                  {original.schedule === "own" && <option value="own">Its own, as {detail.server} has</option>}
                  <option value="off">Off</option>
                </Select>
              )}
            </Field>
          </div>
          <Toggle
            checked={options.sleep && routed}
            disabled={!routed}
            onChange={(v) => option("sleep", v)}
            label="Sleep when empty"
            hint={routed ? undefined : "Needs the shared address."}
          />
          {options.sleep && routed && (
            <Field label="After minutes with no players">
              {(id) => <Input id={id} type="number" min={1} value={options.idleMinutes} onChange={(e) => option("idleMinutes", e.target.value)} className="max-w-32" />}
            </Field>
          )}
        </div>
      )}
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
      <div className="flex justify-end gap-2">
        <Button onClick={onClose}>Cancel</Button>
        <Button type="submit" variant="primary" loading={busy}>
          {duplicating ? "Duplicate" : "Create server"}
        </Button>
      </div>
    </form>
  );
}
