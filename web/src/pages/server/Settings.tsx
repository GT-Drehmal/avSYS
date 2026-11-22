import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { BottomBar, Button, Confirm, Field, Input, Notice, Section, Select, Textarea, Toggle } from "../../components/ui";
import { useQuery } from "@tanstack/react-query";
import { api, serverPath, type ExposeType, type Loader, type Member, type Role, type ServerSpec, type SpecPatch } from "../../lib/api";
import { launchOf } from "../../lib/launch";
import { parseProperties, worldKeys } from "../../lib/properties";
import { keys, useAction, useMeta, useUpdateServer } from "../../lib/queries";
import { fromGigabytes, loaders, toGigabytes, useGameVersions } from "../../lib/software";
import { softwareWords } from "../../lib/format";
import { toast } from "../../lib/toast";
import { onPreview, PreviewsToggle, previewWarning, VersionOptions } from "../../components/VersionOptions";
import { PicturePicker } from "../../components/Picture";
import { foundRefs, PackFit } from "../../components/Content";
import { autoOpOptions, exposeOptions, GameplayFields, imageDocs, LaunchFlags, lines, MemberLists, memberRoleOptions, openWarning, type Picks, playersExample, propertiesDocs, triState } from "../../components/ServerFields";
import { ExtraPorts, type PortDraft, portDrafts, portsOf } from "../../components/ExtraPorts";
import { NodeMove } from "../../components/NodeMove";
import { LoaderBuildField } from "../../components/LoaderBuild";
import { ReplaceWorld } from "../../components/ReplaceWorld";
import { AddedInGame } from "../../components/AddedInGame";
import { BedrockNeedsMods, SaveAsList, SavedListsField } from "../../components/PlayerLists";
import { NewerWorldConfirm } from "../../components/NewerWorld";
import { newerThan, newerWorldWarning, runsOn } from "../../lib/worlds";
import { NotesField } from "../../components/Notes";
import { useServerContext } from "./ServerPage";

// server.properties entries with their own control. Anything else is edited
// as plain key=value lines.
const typedProperties = ["motd", "difficulty", "gamemode", "max-players", "pvp", "online-mode", "white-list"] as const;

interface Draft {
  displayName: string;
  /** The panel's notes on it, which are not part of its spec. */
  notes: string;
  version: string;
  loader: Loader;
  /** The loader build it is pinned to; empty for the newest. */
  loaderVersion: string;
  memory: string;
  storage: string;
  exposeType: ExposeType;
  hostname: string;
  loadBalancerIP: string;
  ports: PortDraft[];
  sleepEnabled: boolean;
  idleMinutes: string;
  motd: string;
  difficulty: string;
  gamemode: string;
  maxPlayers: string;
  pvp: string;
  onlineMode: string;
  whitelistOn: string;
  otherProperties: string;
  ops: string;
  autoOp: string;
  autoWhitelist: string;
  whitelist: string;
  opMembers: Picks;
  whitelistMembers: Picks;
  whitelistLists: string[];
  opLists: string[];
  env: string;
  image: string;
  aikar: boolean;
  extraFlags: string;
}

const picksOf = (p: ServerSpec["opMembers"]): Picks => ({ also: p?.also ?? [], except: p?.except ?? [] });
// None picked is sent as null, which takes the field away.
const picksFor = (p: Picks) => (p.also.length || p.except.length ? p : null);

/** Whether anyone who finds its address can join: reachable, with no whitelist. */
const isOpen = (d: Draft) => d.exposeType !== "None" && d.whitelistOn !== "true";

function toDraft(spec: ServerSpec, notes: string): Draft {
  const p = spec.properties ?? {};
  const launch = launchOf(spec);
  const typed = new Set<string>(typedProperties);
  return {
    displayName: spec.displayName ?? "",
    notes,
    version: spec.version,
    loader: spec.loader,
    loaderVersion: spec.loaderVersion ?? "",
    memory: toGigabytes(spec.resources.memory),
    storage: toGigabytes(spec.storage.size),
    exposeType: spec.expose.type,
    hostname: spec.expose.hostname ?? "",
    loadBalancerIP: spec.expose.loadBalancerIP ?? "",
    ports: portDrafts(spec.ports),
    sleepEnabled: !!spec.sleep.enabled,
    idleMinutes: String(spec.sleep.idleMinutes),
    motd: p["motd"] ?? "",
    difficulty: p["difficulty"] ?? "",
    gamemode: p["gamemode"] ?? "",
    maxPlayers: p["max-players"] ?? "",
    pvp: p["pvp"] ?? "",
    onlineMode: p["online-mode"] ?? "",
    // The game takes TRUE as well as true.
    whitelistOn: (p["white-list"] ?? "").toLowerCase(),
    otherProperties: Object.entries(p)
      .filter(([k]) => !typed.has(k))
      .map(([k, v]) => `${k}=${v}`)
      .join("\n"),
    ops: (spec.ops ?? []).join("\n"),
    // Not set is the same as "operator".
    autoOp: spec.autoOp || "operator",
    // Not set is the same as "off".
    autoWhitelist: spec.autoWhitelist || "off",
    whitelist: (spec.whitelist ?? []).join("\n"),
    opMembers: picksOf(spec.opMembers),
    whitelistMembers: picksOf(spec.whitelistMembers),
    whitelistLists: spec.whitelistLists ?? [],
    opLists: spec.opLists ?? [],
    env: (launch.env ?? []).map((e) => `${e.name}=${e.value ?? ""}`).join("\n"),
    image: spec.image ?? "",
    aikar: launch.aikar,
    extraFlags: launch.extraFlags,
  };
}

/**
 * A draft changed while it was saved, with the other properties the panel
 * set as it saved, as enforce-whitelist is with the whitelist: left out, the
 * next save would take them out again. They go first, as the cursor is put
 * at the end of what is being typed.
 */
function withAdded(draft: Draft, sent: Draft, saved: Draft): Draft {
  const known = { ...parseProperties(sent.otherProperties), ...parseProperties(draft.otherProperties) };
  const added = Object.entries(parseProperties(saved.otherProperties))
    .filter(([key]) => !(key in known))
    .map(([key, value]) => `${key}=${value}`);
  return added.length > 0 ? { ...draft, otherProperties: [...added, draft.otherProperties].filter(Boolean).join("\n") } : draft;
}

/**
 * The settings to save. The version is sent only when it was changed: one
 * picked as "Latest release" is the newest there is now, given as latest,
 * which the server then keeps.
 */
function toPatch(d: Draft, current: ServerSpec, latest: string | undefined): SpecPatch {
  const properties: Record<string, string | null> = {};
  // A removed key has to be sent as null, or the merge would keep it.
  for (const key of Object.keys(current.properties ?? {})) properties[key] = null;
  const typed: Record<string, string> = {
    motd: d.motd,
    difficulty: d.difficulty,
    gamemode: d.gamemode,
    "max-players": d.maxPlayers,
    pvp: d.pvp,
    "online-mode": d.onlineMode,
    "white-list": d.whitelistOn,
  };
  for (const [k, v] of Object.entries(typed)) if (v !== "") properties[k] = v;
  Object.assign(properties, parseProperties(d.otherProperties));
  const softwareChanged = d.version !== current.version || d.loader !== current.loader;
  // A build is for one type and version: another of either drops it.
  const build = d.loaderVersion.trim();
  return {
    displayName: d.displayName,
    ...(softwareChanged ? { version: d.version === "LATEST" && latest ? latest : d.version } : {}),
    loader: d.loader,
    ...(softwareChanged || build !== (current.loaderVersion ?? "") ? { loaderVersion: build || null } : {}),
    resources: { ...current.resources, memory: fromGigabytes(d.memory) },
    storage: { ...current.storage, size: fromGigabytes(d.storage) },
    expose: { type: d.exposeType, hostname: d.hostname.trim(), loadBalancerIP: d.loadBalancerIP.trim() },
    ports: portsOf(d.ports),
    sleep: { enabled: d.sleepEnabled, idleMinutes: Number(d.idleMinutes) || 15 },
    properties: properties as Record<string, string>,
    ops: lines(d.ops),
    autoOp: d.autoOp as ServerSpec["autoOp"],
    autoWhitelist: d.autoWhitelist as ServerSpec["autoWhitelist"],
    whitelist: lines(d.whitelist),
    opMembers: picksFor(d.opMembers),
    whitelistMembers: picksFor(d.whitelistMembers),
    whitelistLists: d.whitelistLists.length > 0 ? d.whitelistLists : null,
    opLists: d.opLists.length > 0 ? d.opLists : null,
    env: lines(d.env).flatMap((line) => {
      const i = line.indexOf("=");
      return i > 0 ? [{ name: line.slice(0, i).trim(), value: line.slice(i + 1) }] : [];
    }),
    image: d.image.trim(),
    jvm: { aikarFlags: d.aikar, extraFlags: d.extraFlags },
  };
}

/**
 * A newer game version than the server is on, of those its type has a
 * build for: a release, or for a server on a snapshot or pre-release, the
 * newest of either. None for a modpack, which has the pack's version, or
 * while the type's own list is not known.
 */
function newerVersion(spec: ServerSpec, own: ReturnType<typeof useGameVersions>): string | undefined {
  const list = own.data;
  if (spec.modpack || own.isPlaceholderData || !list || list.loader !== spec.loader) return undefined;
  const offer = onPreview(list, spec.version) ? list.ordered[0] : list.latest;
  const at = list.ordered.indexOf(spec.version);
  return offer && at > 0 && list.ordered.indexOf(offer) < at ? offer : undefined;
}

export function Settings() {
  const { server } = useServerContext();
  const navigate = useNavigate();
  const update = useUpdateServer(server.org, server.name);
  const specKey = JSON.stringify(server.spec);
  // biome-ignore lint/correctness/useExhaustiveDependencies: the spec is a new object on every update; what it says is what counts
  const saved = useMemo(() => toDraft(server.spec, server.notes ?? ""), [specKey, server.notes]);
  const [draft, setDraft] = useState(saved);
  const versions = useGameVersions(draft.loader);
  const [previews, setPreviews] = useState(false);
  // Known to be missing, as opposed to the list not having loaded.
  const noBuild = draft.version !== "LATEST" && !!versions.data && !versions.isPlaceholderData && !versions.data.all.has(draft.version);
  const dirty = JSON.stringify(draft) !== JSON.stringify(saved);
  const [deleting, setDeleting] = useState(false);
  const [savingList, setSavingList] = useState<"whitelist" | "ops" | null>(null);

  // Pick up changes made elsewhere, unless there are local edits to protect.
  const previous = useRef(saved);
  useEffect(() => {
    const before = JSON.stringify(previous.current);
    previous.current = saved;
    setDraft((d) => (JSON.stringify(d) === before ? saved : d));
  }, [saved]);

  const set = <K extends keyof Draft>(key: K, value: Draft[K]) => setDraft((d) => ({ ...d, [key]: value }));
  // The pinned build goes with the type and version it is for, and comes back with them.
  const setSoftware = (change: Partial<Pick<Draft, "loader" | "version">>) =>
    setDraft((d) => {
      const next = { ...d, ...change };
      return { ...next, loaderVersion: next.loader === saved.loader && next.version === saved.version ? saved.loaderVersion : "" };
    });
  const text = (key: keyof Draft) => ({ value: draft[key] as string, onChange: (e: { target: { value: string } }) => set(key, e.target.value as never) });

  const meta = useMeta();
  const members = useQuery({ queryKey: keys.members(server.org), queryFn: () => api.get<Member[]>(`/orgs/${server.org}/members`) });
  // A last snapshot is taken unless this is switched off, so that what is
  // deleted can be brought back.
  const [finalSnapshot, setFinalSnapshot] = useState(true);
  const keeps = !!meta.data?.snapshots && finalSnapshot;
  const remove = useAction(() => api.del(serverPath(server.org, server.name) + (keeps ? "" : "?snapshot=false")));

  // Every version of the game, in the order they came out, to tell which is newer.
  const allVersions = useGameVersions();
  const order = allVersions.data?.ordered ?? [];
  // "Latest" is each kind's own newest, which may be behind the game's.
  const savedVersions = useGameVersions(server.spec.loader);
  const latestOf = (list: typeof versions) => (list.isPlaceholderData ? undefined : list.data?.latest);
  // The version its world was last opened in, as far as is known: the one
  // the game says it runs, or else the one it was set to.
  const reported = server.status.version?.split(" ").at(-1);
  const ranOn = reported && order.includes(reported) ? reported : runsOn(server.spec, latestOf(savedVersions));
  const goingTo = draft.version === "LATEST" ? latestOf(versions) : draft.version;
  const newer = newerVersion(server.spec, savedVersions);
  // A world opened in a newer version cannot be opened in an older one.
  const older = newerThan(order, ranOn, goingTo);

  const [opening, setOpening] = useState(false);
  const [downgrading, setDowngrading] = useState(false);
  const save = (e?: React.FormEvent, sure = false, sureOlder = false) => {
    e?.preventDefault();
    // Made older than its world, which it then cannot open: asked first.
    const goingOlder = older && draft.version !== saved.version;
    if (!sureOlder && goingOlder) return setDowngrading(true);
    setDowngrading(false);
    // Opened to anyone by this change: asked first.
    if (!sure && isOpen(draft) && !isOpen(saved)) return setOpening(true);
    setOpening(false);
    const sent = draft;
    // Notes alone are saved whatever the server is doing.
    const notesOnly = JSON.stringify({ ...draft, notes: "" }) === JSON.stringify({ ...saved, notes: "" });
    const notes = draft.notes !== saved.notes ? { notes: draft.notes } : {};
    const spec = toPatch(draft, server.spec, latestOf(versions));
    update.mutate(notesOnly ? { notes: draft.notes } : { spec, ...notes, ...(goingOlder ? { newerWorld: true as const } : {}) }, {
      onSuccess: (next) => {
        // Changed again while it was saved: that change is kept, to save next.
        const now = toDraft(next.spec, next.notes ?? "");
        setDraft((d) => (d === sent ? now : withAdded(d, sent, now)));
        toast.info("Settings saved");
      },
    });
  };

  const usesRouter = draft.exposeType === "Router";
  const otherKeys = parseProperties(draft.otherProperties);
  const worldSet = worldKeys.filter((k) => k in otherKeys);
  return (
    <>
    <form onSubmit={save}>
      <Section title="General">
        <PicturePicker
          name={server.spec.displayName || server.name}
          path={`${serverPath(server.org, server.name)}/icon`}
          hint="Shown in the server list after a restart."
        />
        <Field label="Display name">{(id) => <Input id={id} {...text("displayName")} placeholder={server.name} />}</Field>
        <Field label="Message of the day">{(id) => <Input id={id} {...text("motd")} placeholder="A Minecraft Server" />}</Field>
        <NotesField value={draft.notes} onChange={(notes) => set("notes", notes)} />
      </Section>

      <Section title="Software" description={server.spec.modpack ? undefined : "Changing the type or going to an older version can break the world. Take a snapshot first."}>
        {server.spec.modpack ? (
          <Notice tone="info">Running the {server.spec.modpack.title || server.spec.modpack.project} modpack. Change the modpack's version under Content.</Notice>
        ) : (
          <div className="grid grid-cols-2 gap-4">
            <Field label="Type">
              {(id) => (
                <Select id={id} value={draft.loader} onChange={(e) => setSoftware({ loader: e.target.value as Loader })}>
                  {loaders.map((l) => (
                    <option key={l.id} value={l.id}>
                      {l.name}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
            <Field
              label="Game version"
              hint={
                noBuild
                  ? `${loaders.find((l) => l.id === draft.loader)?.name} has no build for ${draft.version}, so the server cannot start. The newest is ${versions.data?.latest}.`
                  : older && ranOn && goingTo
                    ? newerWorldWarning(ranOn, goingTo)
                    : onPreview(versions.data, draft.version)
                    ? previewWarning
                    : undefined
              }
            >
              {(id) => (
                <div className="flex flex-col gap-1">
                  <Select id={id} value={draft.version} onChange={(e) => setSoftware({ version: e.target.value })}>
                    {/* Keeps the version it has selectable when the list could not be loaded, or does not have it. */}
                    <VersionOptions
                      versions={versions.data}
                      previews={previews || onPreview(versions.data, draft.version)}
                      keep={draft.version === "LATEST" ? undefined : { value: draft.version, label: noBuild ? `${draft.version} (no build)` : draft.version }}
                    />
                  </Select>
                  <PreviewsToggle versions={versions.data} previews={previews} onChange={setPreviews} selected={draft.version} />
                  {/* Said while the server's own version is picked, and picked by hand: nothing changes by itself. */}
                  {newer && draft.loader === saved.loader && draft.version === saved.version && (
                    <p className="text-xs text-muted">
                      {softwareWords(server.spec.loader, newer)} is out.{" "}
                      <button type="button" onClick={() => setSoftware({ version: newer })} className="text-accent hover:underline">
                        Use {newer}
                      </button>
                    </p>
                  )}
                </div>
              )}
            </Field>
            <LoaderBuildField loader={draft.loader} value={draft.loaderVersion} onChange={(value) => set("loaderVersion", value)} />
          </div>
        )}
        {/* What a change of version does to the pack it offers. */}
        {/* One joined from several is joined again for the version it is changed to. */}
        {!server.spec.modpack && server.spec.resourcePack?.url && !server.spec.resourcePack.stack?.length && draft.version !== server.spec.version && (
          <PackFit pack={server.spec.resourcePack} version={draft.version === "LATEST" ? versions.data?.latest : draft.version} />
        )}
      </Section>

      <Section title="Gameplay">
        <div className="grid grid-cols-2 gap-4">
          <GameplayFields
            value={{ difficulty: draft.difficulty, gamemode: draft.gamemode, maxPlayers: draft.maxPlayers, pvp: draft.pvp }}
            onChange={(key, value) => set(key, value)}
          />
        </div>
        <Field
          label="Other properties"
          docs={propertiesDocs}
          hint={`One key=value per line.${worldSet.length > 0 ? ` ${worldSet.join(", ")} only ${worldSet.length === 1 ? "affects" : "affect"} new worlds.` : ""}`}
        >
          {(id) => <Textarea id={id} rows={3} {...text("otherProperties")} placeholder={"view-distance=12\nspawn-protection=0"} className="font-mono" spellCheck={false} />}
        </Field>
      </Section>

      <Section title="Players">
        <div className="grid grid-cols-2 gap-4">
          <Field label="Whitelist">
            {(id) => (
              <Select id={id} {...text("whitelistOn")}>
                {triState}
              </Select>
            )}
          </Field>
          <Field label="Account check" hint="Off lets unverified accounts join.">
            {(id) => (
              <Select id={id} {...text("onlineMode")}>
                {triState}
              </Select>
            )}
          </Field>
        </div>
        <SavedListsField
          org={server.org}
          title="Whitelist from saved lists"
          hint={draft.whitelistLists.length > 0 && draft.whitelistOn !== "true" ? "Only matters with the whitelist on." : "A change to a list reaches this server too."}
          label="whitelist"
          value={draft.whitelistLists}
          onChange={(ids) => set("whitelistLists", ids)}
        />
        <BedrockNeedsMods org={server.org} server={server.name} mods={[...(server.spec.mods ?? []), ...foundRefs(server.found)]} ids={[...draft.whitelistLists, ...draft.opLists]} />
        <div className="grid grid-cols-2 gap-4">
          {(["whitelist", "ops"] as const).map((list) => (
            <div key={list} className="flex flex-col gap-3">
              <Field label={list === "whitelist" ? "Whitelisted players" : "Operators"}>
                {(id) => <Textarea id={id} rows={4} {...text(list)} placeholder={playersExample} className="font-mono" spellCheck={false} />}
              </Field>
              <AddedInGame server={server} list={list} listed={draft[list]} onKeep={(name) => setDraft((d) => ({ ...d, [list]: [...lines(d[list]), name].join("\n") }))} />
              <Button size="sm" className="self-start" aria-label={list === "whitelist" ? "Save the whitelist as a list" : "Save the operators as a list"} onClick={() => setSavingList(list)}>
                Save as list
              </Button>
            </div>
          ))}
        </div>
        <SavedListsField org={server.org} title="Operators from saved lists" label="operators" value={draft.opLists} onChange={(ids) => set("opLists", ids)} />
        {savingList && <SaveAsList org={server.org} server={server.name} list={savingList} open onOpenChange={(o) => !o && setSavingList(null)} />}
        <div className="grid grid-cols-2 gap-4">
          <Field
            label="Whitelisted by membership"
            hint={
              draft.autoWhitelist !== "off" && draft.whitelistOn !== "true"
                ? "Only matters with the whitelist on."
                : "Members need a linked Minecraft account."
            }
          >
            {(id) => (
              <Select id={id} {...text("autoWhitelist")}>
                <option value="off">Nobody</option>
                {memberRoleOptions}
              </Select>
            )}
          </Field>
          <Field label="Operators by role" hint="Members need a linked Minecraft account.">
            {(id) => (
              <Select id={id} {...text("autoOp")}>
                {autoOpOptions}
              </Select>
            )}
          </Field>
        </div>
        {(members.data?.length ?? 0) > 0 && (
          <div className="flex flex-col gap-1.5">
            <div className="text-[13px] font-medium text-fg">Members</div>
            <p className="text-xs text-muted">Set by role unless you pick otherwise.</p>
            <MemberLists
              members={members.data ?? []}
              whitelistFrom={draft.autoWhitelist === "off" ? "" : (draft.autoWhitelist as Role)}
              opFrom={draft.autoOp === "off" ? "" : ((draft.autoOp || "operator") as Role)}
              whitelist={draft.whitelistMembers}
              ops={draft.opMembers}
              onChange={({ whitelist, ops }) => setDraft((d) => ({ ...d, whitelistMembers: whitelist, opMembers: ops }))}
            />
          </div>
        )}
      </Section>

      <Section title="Resources" description="Storage can grow but not shrink.">
        <div className="grid grid-cols-2 gap-4">
          <Field label="Memory (GB)">{(id) => <Input id={id} type="number" required min={0.5} step={0.5} {...text("memory")} />}</Field>
          <Field label="Storage (GB)">{(id) => <Input id={id} type="number" required min={toGigabytes(server.spec.storage.size) || 1} step={1} {...text("storage")} />}</Field>
        </div>
        <LaunchFlags aikar={draft.aikar} extra={draft.extraFlags} onAikar={(on) => set("aikar", on)} onExtra={(text) => set("extraFlags", text)} />
      </Section>

      <Section title="Node" description="Moving stops the server for a while.">
        <NodeMove server={server} />
      </Section>

      <Section title="Network">
        <Field label="Exposure">
          {(id) => (
            <Select id={id} value={draft.exposeType} onChange={(e) => set("exposeType", e.target.value as ExposeType)}>
              {exposeOptions}
            </Select>
          )}
        </Field>
        {draft.exposeType !== "None" && (
          <Field label="Hostname" hint={usesRouter ? "Point the hostname's DNS at the avSYS router." : "Optional."} missing="Enter a hostname.">
            {(id) => <Input id={id} required={usesRouter} {...text("hostname")} placeholder="play.example.com" className="font-mono" spellCheck={false} />}
          </Field>
        )}
        <Field label="Extra ports" hint="For mods like voice chat, Geyser or a web map.">
          {() => <ExtraPorts ports={draft.ports} onChange={(ports) => set("ports", ports)} mods={[...(server.spec.mods ?? []), ...foundRefs(server.found)]} status={server.status.ports ?? []} expose={draft.exposeType} sleeps={draft.sleepEnabled && draft.exposeType === "Router"} />}
        </Field>
        {draft.exposeType === "LoadBalancer" && (
          <Field label="Requested IP" hint="Optional.">
            {(id) => <Input id={id} {...text("loadBalancerIP")} placeholder="192.168.1.240" className="font-mono" />}
          </Field>
        )}
      </Section>

      <Section title="Sleep" description="Stops the server while nobody is playing.">
        <Toggle
          checked={draft.sleepEnabled && usesRouter}
          disabled={!usesRouter}
          onChange={(v) => set("sleepEnabled", v)}
          label="Sleep when empty"
          hint={usesRouter ? "The first player to join wakes the server, then joins again." : "Needs the shared address."}
        />
        {draft.sleepEnabled && usesRouter && <Field label="After minutes with no players">{(id) => <Input id={id} type="number" min={1} {...text("idleMinutes")} className="max-w-32" />}</Field>}
      </Section>

      <Section title="Advanced" description="Options of the itzg/minecraft-server image.">
        <Field label="Environment" docs={imageDocs} hint="One NAME=value per line.">
          {(id) => <Textarea id={id} rows={3} {...text("env")} placeholder="ENABLE_ROLLING_LOGS=true" className="font-mono" spellCheck={false} />}
        </Field>
        <Field label="Image" hint="Leave empty for the default.">
          {(id) => <Input id={id} {...text("image")} placeholder="itzg/minecraft-server:java21" className="font-mono" spellCheck={false} />}
        </Field>
      </Section>

      <Section title="World" description="Download, replace or delete the world.">
        <ReplaceWorld server={server} />
      </Section>

      <Section title="Delete server" description="Removes the server and all of its files.">
        <div>
          <Button variant="danger" onClick={() => setDeleting(true)}>
            Delete {server.name}
          </Button>
        </div>
      </Section>

      {dirty && (
        <BottomBar>
          <div className="mx-auto flex max-w-5xl items-center justify-between gap-4 px-5 py-3 md:px-10">
            <p className="text-[13px] text-muted">Unsaved changes{server.status.phase === "Running" ? ". Most apply at the next restart." : "."}</p>
            <div className="flex gap-2">
              <Button onClick={() => setDraft(saved)}>Discard</Button>
              <Button type="submit" variant="primary" loading={update.isPending}>
                Save changes
              </Button>
            </div>
          </div>
        </BottomBar>
      )}

    </form>
      <Confirm
        open={opening}
        onOpenChange={setOpening}
        title="Open the server to anyone?"
        description={openWarning}
        action="Save without a whitelist"
        loading={update.isPending}
        onConfirm={() => save(undefined, true, true)}
      />
      <NewerWorldConfirm
        open={downgrading}
        onOpenChange={setDowngrading}
        world={ranOn ?? ""}
        version={goingTo ?? ""}
        more={`To go back, restore a snapshot from before ${ranOn} instead.`}
        action={`Change to ${goingTo} anyway`}
        loading={update.isPending}
        onConfirm={() => save(undefined, false, true)}
      />
      <Confirm
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete ${server.name}?`}
        description={keeps ? "A final snapshot is taken, so you can bring the server back from Backups." : "The server's world and files are deleted. This cannot be undone."}
        action={remove.isPending && keeps ? "Taking a snapshot" : "Delete server"}
        typeToConfirm={server.name}
        loading={remove.isPending}
        onConfirm={() => remove.mutate(undefined, { onSuccess: () => navigate(`/o/${server.org}`) })}
      >
        {meta.data?.snapshots && <Toggle checked={finalSnapshot} onChange={setFinalSnapshot} disabled={remove.isPending} label="Take a final snapshot" />}
      </Confirm>
    </>
  );
}
