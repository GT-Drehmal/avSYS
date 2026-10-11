import { useQueryClient } from "@tanstack/react-query";
import { ChevronDown, ChevronLeft, ChevronRight, Plus, X } from "lucide-react";
import { useRef, useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { AddContentDialog, ContentList, kindsPhrase, type Software } from "../components/Content";
import { ModrinthSearch, ProjectIcon } from "../components/ModrinthSearch";
import { Button, cx, Field, Input, Notice, PageHeader, Section, Select, Textarea, Toggle } from "../components/ui";
import { api, ApiError, type ContentRef, type Loader, type Modpack, type Server, type ServerSpec } from "../lib/api";
import { difficulties, gameModes, levelTypes, parseProperties } from "../lib/properties";
import { keys } from "../lib/queries";
import { toast } from "../lib/toast";
import { fromGigabytes, loaders, useGameVersions } from "../lib/software";

const slugify = (name: string) =>
  name
    .toLowerCase()
    .replace(/[^a-z0-9]+/g, "-")
    .replace(/^[-0-9]+|-+$/g, "")
    .slice(0, 40);

export function NewServer() {
  const { org = "" } = useParams();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const versions = useGameVersions();

  const [kind, setKind] = useState<"game" | "modpack">("game");
  // Nothing is picked for you: the type decides what the server can run.
  const [loader, setLoader] = useState<Loader | null>(null);
  const [needsLoader, setNeedsLoader] = useState(false);
  const softwareRef = useRef<HTMLDivElement>(null);
  const [version, setVersion] = useState("LATEST");
  const [modpack, setModpack] = useState<Modpack | null>(null);
  const [mods, setMods] = useState<ContentRef[]>([]);
  const [addingMods, setAddingMods] = useState(false);
  const [displayName, setDisplayName] = useState("");
  const [customName, setCustomName] = useState<string | null>(null);
  const [memory, setMemory] = useState("4");
  const [storage, setStorage] = useState("10");
  const [hostname, setHostname] = useState("");
  const [gamemode, setGamemode] = useState("");
  const [difficulty, setDifficulty] = useState("");
  const [moreWorld, setMoreWorld] = useState(false);
  const [levelType, setLevelType] = useState("");
  const [seed, setSeed] = useState("");
  const [structures, setStructures] = useState(true);
  const [hardcore, setHardcore] = useState(false);
  const [maxPlayers, setMaxPlayers] = useState("");
  const [pvp, setPvp] = useState("");
  const [otherProperties, setOtherProperties] = useState("");
  const [eula, setEula] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const name = customName ?? slugify(displayName);
  // What content fits. Until a pack is picked, what it will accept is still
  // that of a modpack, whose own loader is the one that counts.
  const software: Software | null =
    kind === "modpack" ? { loader: "vanilla", version, modpack: modpack ?? { source: "modrinth", project: "" } } : loader ? { loader, version } : null;

  // Picks were filtered to fit one kind of server, so they do not carry over
  // to another.
  const changeSoftware = (change: () => void) => {
    if (mods.length > 0) {
      toast.info(`Cleared ${mods.length} item${mods.length === 1 ? "" : "s"} picked for the previous software`);
      setMods([]);
    }
    change();
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    if (kind === "modpack" && !modpack) return setError("Pick a modpack, or switch to a game version.");
    if (kind === "game" && !loader) {
      setNeedsLoader(true);
      softwareRef.current?.scrollIntoView({ behavior: "smooth", block: "center" });
      return;
    }
    setBusy(true);
    setError(null);
    // Only what was chosen is sent, so everything else keeps the game's or
    // the modpack's own default.
    const properties: Record<string, string> = { ...parseProperties(otherProperties) };
    if (gamemode) properties["gamemode"] = gamemode;
    if (difficulty) properties["difficulty"] = difficulty;
    if (levelType) properties["level-type"] = levelType;
    if (seed.trim()) properties["level-seed"] = seed.trim();
    if (!structures) properties["generate-structures"] = "false";
    if (hardcore) properties["hardcore"] = "true";
    if (maxPlayers) properties["max-players"] = maxPlayers;
    if (pvp) properties["pvp"] = pvp;
    const spec: Partial<ServerSpec> = {
      displayName,
      eula,
      resources: { memory: fromGigabytes(memory) },
      storage: { size: fromGigabytes(storage) },
      expose: { type: "Router", hostname: hostname.trim() },
      mods,
      properties,
      ...(kind === "modpack" ? { modpack } : { loader: loader ?? undefined, version }),
    };
    try {
      const server = await api.post<Server>(`/orgs/${org}/servers`, { name, spec });
      await qc.invalidateQueries({ queryKey: keys.servers(org) });
      navigate(`/o/${org}/s/${server.name}`);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not create the server.");
      setBusy(false);
    }
  };

  const tile = (active: boolean) =>
    cx("flex flex-col gap-0.5 rounded-md border px-3 py-2.5 text-left transition-colors", active ? "border-accent bg-accent-wash" : "border-line-strong bg-raised hover:bg-hover");
  const segment = (active: boolean) => cx("h-7 rounded-sm px-3 text-xs font-medium", active ? "bg-hover text-fg" : "text-muted hover:text-fg");
  const unset = kind === "modpack" ? "Modpack default" : "Default";

  return (
    <form onSubmit={submit}>
      <Link to={`/o/${org}`} className="mb-3 inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
        <ChevronLeft className="size-3.5" />
        Servers
      </Link>
      <PageHeader title="New server" />

      <Section title="Software" description="Start from a plain game version and add mods or plugins later, or install a whole modpack.">
        <div className="flex w-fit gap-1 rounded-md border border-line bg-bg p-0.5">
          <button type="button" className={segment(kind === "game")} onClick={() => kind !== "game" && changeSoftware(() => setKind("game"))}>
            Game version
          </button>
          <button type="button" className={segment(kind === "modpack")} onClick={() => kind !== "modpack" && changeSoftware(() => setKind("modpack"))}>
            Modpack
          </button>
        </div>
        {kind === "game" ? (
          <>
            <div ref={softwareRef} className="flex flex-col gap-2">
              <div role="group" aria-label="Server type" className="grid grid-cols-2 gap-2 sm:grid-cols-4">
                {loaders.map((l) => (
                  <button
                    key={l.id}
                    type="button"
                    aria-pressed={loader === l.id}
                    className={tile(loader === l.id)}
                    onClick={() => {
                      setNeedsLoader(false);
                      if (loader !== l.id) changeSoftware(() => setLoader(l.id));
                    }}
                  >
                    <span className="text-[13px] font-medium">{l.name}</span>
                    <span className="text-xs leading-snug text-muted">{l.blurb}</span>
                  </button>
                ))}
              </div>
              {needsLoader && !loader && (
                <p role="alert" className="text-xs text-danger">
                  Choose a server type.
                </p>
              )}
            </div>
            <Field label="Game version" className="max-w-64">
              {(id) => (
                <Select id={id} value={version} onChange={(e) => setVersion(e.target.value)}>
                  <option value="LATEST">Latest release{versions.data ? ` (${versions.data.latest})` : ""}</option>
                  {versions.data?.releases.map((v) => (
                    <option key={v} value={v}>
                      {v}
                    </option>
                  ))}
                </Select>
              )}
            </Field>
          </>
        ) : modpack ? (
          <div className="flex items-center gap-3 rounded-md border border-accent bg-accent-wash px-3 py-2.5">
            <ProjectIcon url={modpack.iconUrl} />
            <div className="min-w-0 flex-1">
              <p className="truncate text-[13px] font-medium">{modpack.title}</p>
              <p className="truncate font-mono text-xs text-muted">modrinth:{modpack.project}</p>
            </div>
            <Button size="sm" variant="ghost" icon={<X className="size-4" />} aria-label="Choose a different modpack" onClick={() => setModpack(null)} />
          </div>
        ) : (
          <div className="h-96 overflow-hidden rounded-md border border-line bg-surface p-3">
            <ModrinthSearch
              type="modpack"
              placeholder="Search modpacks on Modrinth"
              onPick={(hit) => {
                setModpack({ source: "modrinth", project: hit.slug, title: hit.title, iconUrl: hit.icon_url ?? undefined });
                if (!displayName) setDisplayName(hit.title);
              }}
            />
          </div>
        )}
      </Section>

      <Section
        title="Content"
        description={
          kind === "modpack"
            ? "Optional extras on top of the modpack. You can also add them later from the server's Mods tab."
            : software
              ? `Optional. Add ${kindsPhrase(software)} now, or later from the server's Mods tab.`
              : "Optional. What can be added depends on the server type."
        }
      >
        {mods.length > 0 && <ContentList mods={mods} onRemove={(i) => setMods(mods.filter((_, j) => j !== i))} />}
        <div>
          <Button icon={<Plus className="size-4" />} disabled={!software || (kind === "modpack" && !modpack)} onClick={() => setAddingMods(true)}>
            Add {software ? kindsPhrase(software) : "content"}
          </Button>
        </div>
        {addingMods && software && (
          <AddContentDialog
            software={software}
            mods={mods}
            description="Downloaded when the server first starts, along with anything it requires."
            onAdd={(ref) => setMods((current) => [...current, ref])}
            onClose={() => setAddingMods(false)}
          />
        )}
      </Section>

      <Section title="Name">
        <Field label="Display name">{(id) => <Input id={id} required value={displayName} onChange={(e) => setDisplayName(e.target.value)} placeholder="Survival" />}</Field>
        <Field label="Short name" hint="Used in URLs, SSH targets and Kubernetes. It cannot be changed later.">
          {(id) => <Input id={id} required value={name} onChange={(e) => setCustomName(e.target.value)} pattern="[a-z]([a-z0-9\-]{0,38}[a-z0-9])?" className="font-mono" spellCheck={false} />}
        </Field>
      </Section>

      <Section
        title="World"
        description={
          kind === "modpack"
            ? "The modpack's own settings apply unless you choose otherwise. A pack that ships a ready-made world ignores the world options."
            : "Game mode and difficulty can be changed at any time. The world options are fixed once the world has been generated."
        }
      >
        <div className="grid grid-cols-2 gap-4">
          <Field label="Game mode">
            {(id) => (
              <Select id={id} value={gamemode} onChange={(e) => setGamemode(e.target.value)}>
                <option value="">{kind === "modpack" ? unset : "Survival"}</option>
                {gameModes.filter(([v]) => kind === "modpack" || v !== "survival").map(([v, label]) => (
                  <option key={v} value={v}>
                    {label}
                  </option>
                ))}
              </Select>
            )}
          </Field>
          <Field label="Difficulty">
            {(id) => (
              <Select id={id} value={hardcore ? "hard" : difficulty} disabled={hardcore} onChange={(e) => setDifficulty(e.target.value)}>
                <option value="">{kind === "modpack" ? unset : "Easy"}</option>
                {difficulties.filter(([v]) => kind === "modpack" || v !== "easy").map(([v, label]) => (
                  <option key={v} value={v}>
                    {label}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        </div>
        <button type="button" aria-expanded={moreWorld} onClick={() => setMoreWorld(!moreWorld)} className="flex w-fit items-center gap-1 text-[13px] text-muted hover:text-fg">
          {moreWorld ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
          World options and more
        </button>
        {moreWorld && (
          <>
            <div className="flex flex-col gap-4 rounded-md border border-line p-4">
              <p className="text-xs text-muted">Fixed once the world has been generated.</p>
              <div className="grid grid-cols-2 gap-4">
                <Field label="World type">
                  {(id) => (
                    <Select id={id} value={levelType} onChange={(e) => setLevelType(e.target.value)}>
                      <option value="">{unset}</option>
                      {levelTypes.map(([v, label]) => (
                        <option key={v} value={v}>
                          {label}
                        </option>
                      ))}
                    </Select>
                  )}
                </Field>
                <Field label="Seed">{(id) => <Input id={id} value={seed} onChange={(e) => setSeed(e.target.value)} placeholder="Random" className="font-mono" spellCheck={false} />}</Field>
              </div>
              <Toggle checked={structures} onChange={setStructures} label="Generate structures" hint="Villages, strongholds, temples and the like." />
              <Toggle checked={hardcore} onChange={setHardcore} label="Hardcore" hint="Difficulty is locked to hard, and players who die can only spectate." />
            </div>
            <div className="grid grid-cols-2 gap-4">
              <Field label="Player limit">{(id) => <Input id={id} type="number" min={1} value={maxPlayers} onChange={(e) => setMaxPlayers(e.target.value)} placeholder="20" />}</Field>
              <Field label="PvP">
                {(id) => (
                  <Select id={id} value={pvp} onChange={(e) => setPvp(e.target.value)}>
                    <option value="">{kind === "modpack" ? unset : "On"}</option>
                    {kind === "modpack" && <option value="true">On</option>}
                    <option value="false">Off</option>
                  </Select>
                )}
              </Field>
            </div>
            <Field label="Other properties" hint="One key=value per line, for anything else in server.properties. These and everything outside the box above can be changed later in Settings.">
              {(id) => <Textarea id={id} rows={3} value={otherProperties} onChange={(e) => setOtherProperties(e.target.value)} placeholder={"view-distance=12\nspawn-protection=0"} className="font-mono" spellCheck={false} />}
            </Field>
          </>
        )}
      </Section>

      <Section title="Resources" description={kind === "modpack" ? "Large modpacks usually want 6 GB or more." : "4 GB suits a small server; plugins and view distance push it up. The Java heap is set somewhat below this."}>
        <div className="grid grid-cols-2 gap-4">
          <Field label="Memory (GB)">{(id) => <Input id={id} type="number" required min={0.5} step={0.5} value={memory} onChange={(e) => setMemory(e.target.value)} />}</Field>
          <Field label="Storage (GB)">{(id) => <Input id={id} type="number" required min={1} step={1} value={storage} onChange={(e) => setStorage(e.target.value)} />}</Field>
        </div>
      </Section>

      <Section title="Address" description="Many servers can share one IP address: players are routed by the hostname they connect with.">
        <Field label="Hostname" hint="Point this name's DNS at the avSYS router. You can leave it empty and set it later.">
          {(id) => <Input id={id} value={hostname} onChange={(e) => setHostname(e.target.value)} placeholder="play.example.com" className="font-mono" spellCheck={false} />}
        </Field>
      </Section>

      <Section title="Minecraft EULA">
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
        {error && <Notice tone="danger">{error}</Notice>}
        <div className="flex gap-2">
          <Button type="submit" variant="primary" loading={busy}>
            Create server
          </Button>
          <Link to={`/o/${org}`} className="inline-flex h-8 items-center rounded-md px-3 text-[13px] font-medium text-muted hover:bg-raised hover:text-fg">
            Cancel
          </Link>
        </div>
      </Section>
    </form>
  );
}
