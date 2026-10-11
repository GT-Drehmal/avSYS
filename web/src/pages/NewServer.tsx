import { useQueryClient } from "@tanstack/react-query";
import { ChevronLeft, Plus, X } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { AddContentDialog, ContentList, kindsPhrase, type Software } from "../components/Content";
import { ModrinthSearch, ProjectIcon } from "../components/ModrinthSearch";
import { Button, cx, Field, Input, Notice, PageHeader, Section, Select } from "../components/ui";
import { api, ApiError, type ContentRef, type Loader, type Modpack, type Server, type ServerSpec } from "../lib/api";
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
  const [loader, setLoader] = useState<Loader>("paper");
  const [version, setVersion] = useState("LATEST");
  const [modpack, setModpack] = useState<Modpack | null>(null);
  const [mods, setMods] = useState<ContentRef[]>([]);
  const [addingMods, setAddingMods] = useState(false);
  const [displayName, setDisplayName] = useState("");
  const [customName, setCustomName] = useState<string | null>(null);
  const [memory, setMemory] = useState("4");
  const [storage, setStorage] = useState("10");
  const [hostname, setHostname] = useState("");
  const [eula, setEula] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const name = customName ?? slugify(displayName);
  // Until a pack is picked, what it will accept is still that of a modpack.
  const software: Software = kind === "modpack" ? { loader, version, modpack: modpack ?? { source: "modrinth", project: "" } } : { loader, version };

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
    setBusy(true);
    setError(null);
    const spec: Partial<ServerSpec> = {
      displayName,
      eula,
      resources: { memory: fromGigabytes(memory) },
      storage: { size: fromGigabytes(storage) },
      expose: { type: "Router", hostname: hostname.trim() },
      mods,
      ...(kind === "modpack" ? { modpack } : { loader, version }),
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
            <div className="grid grid-cols-2 gap-2 sm:grid-cols-4">
              {loaders.map((l) => (
                <button key={l.id} type="button" aria-pressed={loader === l.id} className={tile(loader === l.id)} onClick={() => loader !== l.id && changeSoftware(() => setLoader(l.id))}>
                  <span className="text-[13px] font-medium">{l.name}</span>
                  <span className="text-xs leading-snug text-muted">{l.blurb}</span>
                </button>
              ))}
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
            : `Optional. Add ${kindsPhrase(software)} now, or later from the server's Mods tab.`
        }
      >
        {mods.length > 0 && <ContentList mods={mods} onRemove={(i) => setMods(mods.filter((_, j) => j !== i))} />}
        <div>
          <Button icon={<Plus className="size-4" />} disabled={kind === "modpack" && !modpack} onClick={() => setAddingMods(true)}>
            Add {kindsPhrase(software)}
          </Button>
        </div>
        {addingMods && (
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
