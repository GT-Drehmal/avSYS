import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate } from "react-router";
import { Button, Confirm, Field, Input, Notice, Section, Select, Textarea, Toggle } from "../../components/ui";
import { api, serverPath, type ExposeType, type Loader, type ServerSpec, type SpecPatch } from "../../lib/api";
import { difficulties, gameModes, parseProperties, worldKeys } from "../../lib/properties";
import { useAction, useUpdateServer } from "../../lib/queries";
import { fromGigabytes, loaders, toGigabytes, useGameVersions } from "../../lib/software";
import { toast } from "../../lib/toast";
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
  whitelist: string;
  packURL: string;
  packSHA1: string;
  packEnforce: boolean;
  packPrompt: string;
  env: string;
  image: string;
}

const lines = (text: string) => text.split("\n").map((l) => l.trim()).filter(Boolean);

function toDraft(spec: ServerSpec): Draft {
  const p = spec.properties ?? {};
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
    sleepEnabled: !!spec.sleep.enabled,
    idleMinutes: String(spec.sleep.idleMinutes),
    motd: p["motd"] ?? "",
    difficulty: p["difficulty"] ?? "",
    gamemode: p["gamemode"] ?? "",
    maxPlayers: p["max-players"] ?? "",
    pvp: p["pvp"] ?? "",
    onlineMode: p["online-mode"] ?? "",
    whitelistOn: p["white-list"] ?? "",
    otherProperties: Object.entries(p)
      .filter(([k]) => !typed.has(k))
      .map(([k, v]) => `${k}=${v}`)
      .join("\n"),
    ops: (spec.ops ?? []).join("\n"),
    whitelist: (spec.whitelist ?? []).join("\n"),
    packURL: spec.resourcePack?.url ?? "",
    packSHA1: spec.resourcePack?.sha1 ?? "",
    packEnforce: !!spec.resourcePack?.enforce,
    packPrompt: spec.resourcePack?.prompt ?? "",
    env: (spec.env ?? []).map((e) => `${e.name}=${e.value ?? ""}`).join("\n"),
    image: spec.image ?? "",
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
    sleep: { enabled: d.sleepEnabled, idleMinutes: Number(d.idleMinutes) || 15 },
    properties: properties as Record<string, string>,
    ops: lines(d.ops),
    whitelist: lines(d.whitelist),
    resourcePack: d.packURL.trim() ? { url: d.packURL.trim(), sha1: d.packSHA1.trim(), enforce: d.packEnforce, prompt: d.packPrompt } : null,
    env: lines(d.env).flatMap((line) => {
      const i = line.indexOf("=");
      return i > 0 ? [{ name: line.slice(0, i).trim(), value: line.slice(i + 1) }] : [];
    }),
    image: d.image.trim(),
  };
}

const triState = (
  <>
    <option value="">Server default</option>
    <option value="true">On</option>
    <option value="false">Off</option>
  </>
);

export function Settings() {
  const { server } = useServerContext();
  const navigate = useNavigate();
  const update = useUpdateServer(server.org, server.name);
  const versions = useGameVersions();
  const specKey = JSON.stringify(server.spec);
  const saved = useMemo(() => toDraft(server.spec), [specKey]); // eslint-disable-line react-hooks/exhaustive-deps
  const [draft, setDraft] = useState(saved);
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

  const hash = useAction(async () => {
    const { sha1 } = await api.post<{ sha1: string }>("/catalog/sha1", { url: draft.packURL });
    set("packSHA1", sha1);
  });
  const remove = useAction(() => api.del(serverPath(server.org, server.name)));

  const save = (e: React.FormEvent) => {
    e.preventDefault();
    update.mutate(toPatch(draft, server.spec), {
      onSuccess: (next) => {
        setDraft(toDraft(next.spec));
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
        <Field label="Display name">{(id) => <Input id={id} {...text("displayName")} placeholder={server.name} />}</Field>
        <Field label="Message of the day">{(id) => <Input id={id} {...text("motd")} placeholder="A Minecraft Server" />}</Field>
      </Section>

      <Section title="Software" description={server.spec.modpack ? "The modpack decides the game version and mod loader." : "Changing the loader or going back a version can break an existing world. Back it up first."}>
        {server.spec.modpack ? (
          <Notice tone="info">Running the {server.spec.modpack.title || server.spec.modpack.project} modpack. Change its version under Mods.</Notice>
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
            <Field label="Game version">
              {(id) => (
                <Select id={id} {...text("version")}>
                  <option value="LATEST">Latest release{versions.data ? ` (${versions.data.latest})` : ""}</option>
                  {/* Keeps a version selectable even if Mojang's list could not be loaded. */}
                  {draft.version !== "LATEST" && !versions.data?.releases.includes(draft.version) && <option value={draft.version}>{draft.version}</option>}
                  {versions.data?.releases.map((v) => (
                    <option key={v} value={v}>
                      {v}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
          </div>
        )}
      </Section>

      <Section title="Gameplay" description="Fields left on Default leave server.properties as it is.">
        <div className="grid grid-cols-2 gap-4">
          <Field label="Difficulty">
            {(id) => (
              <Select id={id} {...text("difficulty")}>
                <option value="">Server default</option>
                {difficulties.map(([v, label]) => (
                  <option key={v} value={v}>
                    {label}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field label="Game mode">
            {(id) => (
              <Select id={id} {...text("gamemode")}>
                <option value="">Server default</option>
                {gameModes.map(([v, label]) => (
                  <option key={v} value={v}>
                    {label}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field label="Player limit">{(id) => <Input id={id} type="number" min={1} {...text("maxPlayers")} placeholder="20" />}</Field>
          <Field label="PvP">
            {(id) => (
              <Select id={id} {...text("pvp")}>
                {triState}
              </Select>
            )}
          </Field>
        </div>
        <Field
          label="Other properties"
          hint={`One key=value per line, for anything else in server.properties.${
            worldSet.length > 0 ? ` ${worldSet.join(", ")} ${worldSet.length === 1 ? "is" : "are"} only read while a world is generated; a change does not alter an existing world.` : ""
          }`}
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
      </Section>

      <Section title="Resource pack" description="The URL must be reachable by players.">
        <Field label="Pack URL">{(id) => <Input id={id} type="url" {...text("packURL")} placeholder="https://example.com/pack.zip" className="font-mono" />}</Field>
        {draft.packURL && (
          <>
            <Field label="SHA-1" hint="Optional.">
              {(id) => (
                <div className="flex gap-2">
                  <Input id={id} {...text("packSHA1")} className="font-mono" spellCheck={false} />
                  <Button loading={hash.isPending} onClick={() => hash.mutate(undefined)}>
                    Compute
                  </Button>
                </div>
              )}
            </Field>
            <Toggle checked={draft.packEnforce} onChange={(v) => set("packEnforce", v)} label="Required" hint="Players who decline the pack are disconnected." />
            <Field label="Prompt">{(id) => <Input id={id} {...text("packPrompt")} placeholder="Shown when the game asks to download the pack" />}</Field>
          </>
        )}
      </Section>

      <Section title="Resources" description="Storage can grow but not shrink.">
        <div className="grid grid-cols-2 gap-4">
          <Field label="Memory (GB)">{(id) => <Input id={id} type="number" required min={0.5} step={0.5} {...text("memory")} />}</Field>
          <Field label="Storage (GB)">{(id) => <Input id={id} type="number" required min={toGigabytes(server.spec.storage.size) || 1} step={1} {...text("storage")} />}</Field>
        </div>
      </Section>

      <Section title="Network">
        <Field label="Exposure">
          {(id) => (
            <Select id={id} value={draft.exposeType} onChange={(e) => set("exposeType", e.target.value as ExposeType)}>
              <option value="Router">Shared address, routed by hostname</option>
              <option value="LoadBalancer">Its own IP address (LoadBalancer)</option>
              <option value="NodePort">A port on every node (NodePort)</option>
              <option value="None">Inside the cluster only</option>
            </Select>
          )}
        </Field>
        {draft.exposeType !== "None" && (
          <Field label="Hostname" hint={usesRouter ? "Point this name's DNS at the avSYS router." : "Optional."}>
            {(id) => <Input id={id} required={usesRouter} {...text("hostname")} placeholder="play.example.com" className="font-mono" spellCheck={false} />}
          </Field>
        )}
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
          hint={usesRouter ? "The first player to join wakes it and is asked to rejoin once it is up." : "Needs the shared address."}
        />
        {draft.sleepEnabled && usesRouter && <Field label="After minutes with no players">{(id) => <Input id={id} type="number" min={1} {...text("idleMinutes")} className="max-w-32" />}</Field>}
      </Section>

      <Section title="Advanced" description="Options of the itzg/minecraft-server image.">
        <Field label="Environment" hint="One NAME=value per line.">
          {(id) => <Textarea id={id} rows={3} {...text("env")} placeholder="USE_AIKAR_FLAGS=true" className="font-mono" spellCheck={false} />}
        </Field>
        <Field label="Image" hint="Leave empty for the default.">
          {(id) => <Input id={id} {...text("image")} placeholder="itzg/minecraft-server:java21" className="font-mono" spellCheck={false} />}
        </Field>
      </Section>

      <Section title="Delete server" description="Removes the server and all of its files.">
        <div>
          <Button variant="danger" onClick={() => setDeleting(true)}>
            Delete {server.name}
          </Button>
        </div>
      </Section>

      {dirty && (
        <div className="fixed inset-x-0 bottom-0 z-30 border-t border-line-strong bg-surface/95 backdrop-blur md:left-56">
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
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete ${server.name}?`}
        description="The world and every file of this server are deleted with it. This cannot be undone."
        action="Delete server"
        typeToConfirm={server.name}
        loading={remove.isPending}
        onConfirm={() => remove.mutate(undefined, { onSuccess: () => navigate(`/o/${server.org}`) })}
      />
    </>
  );
}
