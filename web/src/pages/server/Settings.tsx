import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { Button, Confirm, Field, Input, Notice, Section, Select, Textarea, Toggle } from "../../components/ui";
import { useQuery } from "@tanstack/react-query";
import { api, serverPath, type ExposeType, type Loader, type Member, type Role, type ServerSpec, type SpecPatch } from "../../lib/api";
import { launchOf } from "../../lib/launch";
import { parseProperties, worldKeys } from "../../lib/properties";
import { keys, useAction, useMeta, useUpdateServer } from "../../lib/queries";
import { fromGigabytes, loaders, toGigabytes, useGameVersions } from "../../lib/software";
import { toast } from "../../lib/toast";
import { onPreview, PreviewsToggle, previewWarning, VersionOptions } from "../../components/VersionOptions";
import { PicturePicker } from "../../components/Picture";
import { PackFit } from "../../components/Content";
import { autoOpOptions, exposeOptions, GameplayFields, LaunchFlags, lines, MemberLists, memberRoleOptions, openWarning, type Picks, triState } from "../../components/ServerFields";
import { ExtraPorts, type PortDraft } from "../../components/ExtraPorts";
import { NodeMove } from "../../components/NodeMove";
import { ReplaceWorld } from "../../components/ReplaceWorld";
import { useServerContext } from "./ServerPage";

// server.properties entries with their own control. Anything else is edited
// as plain key=value lines.
const typedProperties = ["motd", "difficulty", "gamemode", "max-players", "pvp", "online-mode", "white-list"] as const;

interface Draft {
  displayName: string;
  version: string;
  loader: Loader;
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

function toDraft(spec: ServerSpec): Draft {
  const p = spec.properties ?? {};
  const launch = launchOf(spec);
  const typed = new Set<string>(typedProperties);
  return {
    displayName: spec.displayName ?? "",
    version: spec.version,
    loader: spec.loader,
    memory: toGigabytes(spec.resources.memory),
    storage: toGigabytes(spec.storage.size),
    exposeType: spec.expose.type,
    hostname: spec.expose.hostname ?? "",
    loadBalancerIP: spec.expose.loadBalancerIP ?? "",
    ports: (spec.ports ?? []).map((p) => ({ name: p.name, port: String(p.port), protocol: p.protocol ?? "TCP" })),
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
    env: (launch.env ?? []).map((e) => `${e.name}=${e.value ?? ""}`).join("\n"),
    image: spec.image ?? "",
    aikar: launch.aikar,
    extraFlags: launch.extraFlags,
  };
}

function toPatch(d: Draft, current: ServerSpec): SpecPatch {
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
  return {
    displayName: d.displayName,
    version: d.version,
    loader: d.loader,
    resources: { ...current.resources, memory: fromGigabytes(d.memory) },
    storage: { ...current.storage, size: fromGigabytes(d.storage) },
    expose: { type: d.exposeType, hostname: d.hostname.trim(), loadBalancerIP: d.loadBalancerIP.trim() },
    ports: d.ports.filter((p) => p.name.trim() || p.port.trim()).map((p) => ({ name: p.name.trim(), port: Number(p.port), protocol: p.protocol })),
    sleep: { enabled: d.sleepEnabled, idleMinutes: Number(d.idleMinutes) || 15 },
    properties: properties as Record<string, string>,
    ops: lines(d.ops),
    autoOp: d.autoOp as ServerSpec["autoOp"],
    autoWhitelist: d.autoWhitelist as ServerSpec["autoWhitelist"],
    whitelist: lines(d.whitelist),
    opMembers: picksFor(d.opMembers),
    whitelistMembers: picksFor(d.whitelistMembers),
    env: lines(d.env).flatMap((line) => {
      const i = line.indexOf("=");
      return i > 0 ? [{ name: line.slice(0, i).trim(), value: line.slice(i + 1) }] : [];
    }),
    image: d.image.trim(),
    jvm: { aikarFlags: d.aikar, extraFlags: d.extraFlags },
  };
}

export function Settings() {
  const { server } = useServerContext();
  const navigate = useNavigate();
  const update = useUpdateServer(server.org, server.name);
  const specKey = JSON.stringify(server.spec);
  // biome-ignore lint/correctness/useExhaustiveDependencies: the spec is a new object on every update; what it says is what counts
  const saved = useMemo(() => toDraft(server.spec), [specKey]);
  const [draft, setDraft] = useState(saved);
  const versions = useGameVersions(draft.loader);
  const [previews, setPreviews] = useState(false);
  // Known to be missing, as opposed to the list not having loaded.
  const noBuild = draft.version !== "LATEST" && !!versions.data && !versions.isPlaceholderData && !versions.data.all.has(draft.version);
  const dirty = JSON.stringify(draft) !== JSON.stringify(saved);
  const [deleting, setDeleting] = useState(false);

  // Pick up changes made elsewhere, unless there are local edits to protect.
  const previous = useRef(saved);
  useEffect(() => {
    const before = JSON.stringify(previous.current);
    previous.current = saved;
    setDraft((d) => (JSON.stringify(d) === before ? saved : d));
  }, [saved]);

  const set = <K extends keyof Draft>(key: K, value: Draft[K]) => setDraft((d) => ({ ...d, [key]: value }));
  const text = (key: keyof Draft) => ({ value: draft[key] as string, onChange: (e: { target: { value: string } }) => set(key, e.target.value as never) });

  const meta = useMeta();
  const members = useQuery({ queryKey: keys.members(server.org), queryFn: () => api.get<Member[]>(`/orgs/${server.org}/members`) });
  // A last snapshot is taken unless this is switched off, so that what is
  // deleted can be brought back.
  const [finalSnapshot, setFinalSnapshot] = useState(true);
  const keeps = !!meta.data?.snapshots && finalSnapshot;
  const remove = useAction(() => api.del(serverPath(server.org, server.name) + (keeps ? "" : "?snapshot=false")));

  const [opening, setOpening] = useState(false);
  const save = (e?: React.FormEvent, sure = false) => {
    e?.preventDefault();
    // Opened to anyone by this change: asked first.
    if (!sure && isOpen(draft) && !isOpen(saved)) return setOpening(true);
    setOpening(false);
    const sent = draft;
    update.mutate(toPatch(draft, server.spec), {
      onSuccess: (next) => {
        // Changed again while it was saved: that change is kept, to save next.
        setDraft((d) => (d === sent ? toDraft(next.spec) : d));
        toast.info("Settings saved");
      },
    });
  };

  const usesRouter = draft.exposeType === "Router";
  const otherKeys = parseProperties(draft.otherProperties);
  const worldSet = worldKeys.filter((k) => k in otherKeys);
  return (
    <>
    <form onSubmit={save} className="pb-20">
      <Section title="General">
        <PicturePicker
          name={server.spec.displayName || server.name}
          path={`${serverPath(server.org, server.name)}/icon`}
          hint="Shown in the server list after a restart."
        />
        <Field label="Display name">{(id) => <Input id={id} {...text("displayName")} placeholder={server.name} />}</Field>
        <Field label="Message of the day">{(id) => <Input id={id} {...text("motd")} placeholder="A Minecraft Server" />}</Field>
      </Section>

      <Section title="Software" description={server.spec.modpack ? undefined : "Changing the type or going to an older version can break the world. Take a snapshot first."}>
        {server.spec.modpack ? (
          <Notice tone="info">Running the {server.spec.modpack.title || server.spec.modpack.project} modpack. Change its version under Content.</Notice>
        ) : (
          <div className="grid grid-cols-2 gap-4">
            <Field label="Type">
              {(id) => (
                <Select id={id} value={draft.loader} onChange={(e) => set("loader", e.target.value as Loader)}>
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
                  ? `${loaders.find((l) => l.id === draft.loader)?.name} has no build for ${draft.version}, so it cannot start. The newest is ${versions.data?.latest}.`
                  : onPreview(versions.data, draft.version)
                    ? previewWarning
                    : undefined
              }
            >
              {(id) => (
                <div className="flex flex-col gap-1">
                  <Select id={id} {...text("version")}>
                    {/* Keeps the version it has selectable when the list could not be loaded, or does not have it. */}
                    <VersionOptions
                      versions={versions.data}
                      previews={previews || onPreview(versions.data, draft.version)}
                      keep={draft.version === "LATEST" ? undefined : { value: draft.version, label: noBuild ? `${draft.version} (no build)` : draft.version }}
                    />
                  </Select>
                  <PreviewsToggle versions={versions.data} previews={previews} onChange={setPreviews} selected={draft.version} />
                </div>
              )}
            </Field>
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
          hint={`One key=value per line.${worldSet.length > 0 ? ` ${worldSet.join(", ")} only ${worldSet.length === 1 ? "affects" : "affect"} new worlds.` : ""}`}
        >
          {(id) => <Textarea id={id} rows={3} {...text("otherProperties")} placeholder={"view-distance=12\nspawn-protection=0"} className="font-mono" spellCheck={false} />}
        </Field>
      </Section>

      <Section title="Players" description="Usernames or UUIDs, one per line.">
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
        <div className="grid grid-cols-2 gap-4">
          <Field label="Whitelisted players">{(id) => <Textarea id={id} rows={4} {...text("whitelist")} className="font-mono" spellCheck={false} />}</Field>
          <Field label="Operators">{(id) => <Textarea id={id} rows={4} {...text("ops")} className="font-mono" spellCheck={false} />}</Field>
        </div>
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

      <Section title="Node" description="Moving it stops the server for a while.">
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
          <Field label="Hostname" hint={usesRouter ? "Point its DNS at the avSYS router." : "Optional."}>
            {(id) => <Input id={id} required={usesRouter} {...text("hostname")} placeholder="play.example.com" className="font-mono" spellCheck={false} />}
          </Field>
        )}
        <Field label="Extra ports" hint="For mods like voice chat, Geyser or a web map.">
          {() => <ExtraPorts ports={draft.ports} onChange={(ports) => set("ports", ports)} mods={server.spec.mods ?? []} status={server.status.ports ?? []} expose={draft.exposeType} sleeps={draft.sleepEnabled && draft.exposeType === "Router"} />}
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
          hint={usesRouter ? "The first player to join wakes it, then joins again." : "Needs the shared address."}
        />
        {draft.sleepEnabled && usesRouter && <Field label="After minutes with no players">{(id) => <Input id={id} type="number" min={1} {...text("idleMinutes")} className="max-w-32" />}</Field>}
      </Section>

      <Section title="Advanced" description="Options of the itzg/minecraft-server image.">
        <Field label="Environment" hint="One NAME=value per line.">
          {(id) => <Textarea id={id} rows={3} {...text("env")} placeholder="ENABLE_ROLLING_LOGS=true" className="font-mono" spellCheck={false} />}
        </Field>
        <Field label="Image" hint="Leave empty for the default.">
          {(id) => <Input id={id} {...text("image")} placeholder="itzg/minecraft-server:java21" className="font-mono" spellCheck={false} />}
        </Field>
      </Section>

      <Section title="World" description="Replace it with one you have, as a .zip or .tar.gz.">
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
        <div data-bottom-bar className="fixed inset-x-0 bottom-0 z-30 border-t border-line-strong bg-surface/95 backdrop-blur md:left-56">
          <div className="mx-auto flex max-w-5xl items-center justify-between gap-4 px-5 py-3 md:px-10">
            <p className="text-[13px] text-muted">Unsaved changes{server.status.phase === "Running" ? ". Most apply at the next restart." : "."}</p>
            <div className="flex gap-2">
              <Button onClick={() => setDraft(saved)}>Discard</Button>
              <Button type="submit" variant="primary" loading={update.isPending}>
                Save changes
              </Button>
            </div>
          </div>
        </div>
      )}

    </form>
      <Confirm
        open={opening}
        onOpenChange={setOpening}
        title="Open it to anyone?"
        description={openWarning}
        action="Save without a whitelist"
        loading={update.isPending}
        onConfirm={() => save(undefined, true)}
      />
      <Confirm
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete ${server.name}?`}
        description={keeps ? "A final snapshot is taken, so you can bring it back from Backups." : "Its world and files are deleted. This cannot be undone."}
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
