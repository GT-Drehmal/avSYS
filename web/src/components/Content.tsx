import { ExternalLink, FileArchive, Upload, X } from "lucide-react";
import { useState, type ReactNode } from "react";
import { api, qs, type ContentKind, type ContentRef, type Loader, type ModrinthHit, type ModrinthVersion, type PackProject, type ResourcePack, type Server, type ServerSpec } from "../lib/api";
import { ago, bytes, loaderNames } from "../lib/format";
import { useGameVersions } from "../lib/software";
import { toast } from "../lib/toast";
import { ModrinthSearch, ProjectIcon } from "./ModrinthSearch";
import { Button, Chip, cx, Field, Input, Modal, Notice, Spinner } from "./ui";
import { cancelled, UploadProgress, useUpload } from "./Uploading";

/** What the picker offers: content installed on the server, or the one resource pack offered to players. */
export type PickKind = ContentKind | "resourcepack";

export const kindNames: Record<PickKind, string> = { mod: "Mods", plugin: "Plugins", datapack: "Datapacks", resourcepack: "Resource pack" };

/** The parts of a spec that decide what content fits a server. */
export type Software = Pick<ServerSpec, "loader" | "version"> & { modpack?: ServerSpec["modpack"] };

/** What can be installed depends on what the server runs. */
export function kindsFor(software: Software): ContentKind[] {
  if (software.modpack) return ["mod", "datapack"];
  switch (software.loader) {
    case "vanilla":
      return ["datapack"];
    case "paper":
    case "purpur":
    case "folia":
      return ["plugin", "datapack"];
    default:
      return ["mod", "datapack"];
  }
}

/**
 * Search filters that fit a server. A modpack decides the loader and game
 * version itself. latestRelease is what
 * "LATEST" currently means, so a server following the newest release is only
 * offered content that already supports it.
 */
export function filtersFor(software: Software, kind: PickKind, latestRelease?: string) {
  // A datapack or resource pack is the game's own, whatever the loader.
  const own = kind === "datapack" || kind === "resourcepack";
  if (software.modpack) {
    // Known once the panel has pinned the pack; until then nothing is narrowed.
    return { loader: own ? kind : software.modpack.loader, gameVersion: software.modpack.gameVersion };
  }
  return {
    loader: own ? kind : software.loader,
    gameVersion: software.version === "LATEST" ? latestRelease : software.version,
  };
}

/** What a search is narrowed to, in words: "Paper 1.21.4", or just the game version for a datapack. */
function fitsOf(filters: { loader?: string; gameVersion?: string }) {
  const own = filters.loader === "datapack" || filters.loader === "resourcepack";
  const loader = filters.loader && !own ? (loaderNames[filters.loader as Loader] ?? filters.loader) : filters.gameVersion ? "Minecraft" : "";
  return [loader, filters.gameVersion].filter(Boolean).join(" ") || undefined;
}

/** "mods or datapacks", for sentences about what a server accepts. */
export const kindsPhrase = (software: Software) =>
  kindsFor(software)
    .map((k) => kindNames[k].toLowerCase())
    .join(" or ");

interface AddContentProps {
  software: Software;
  /** What is already chosen, so it can be shown as such. */
  mods: ContentRef[];
  onAdd: (ref: ContentRef) => void;
  /** True while a previous addition is being saved. */
  busy?: boolean;
  /** Stores a file from this computer on the server. Omit where there is no server yet. */
  onUpload?: (file: File, kind: ContentKind) => Promise<unknown>;
  description: string;
  /** The resource pack, where it can be chosen here too. */
  pack?: PackPicker;
  /** The kind it opens on. */
  start?: PickKind;
  onClose: () => void;
}

/** Choosing a server's one resource pack. */
export interface PackPicker {
  current?: ResourcePack | null;
  /** Makes it this one, from Modrinth or a URL; settled once it is saved. */
  onPick: (pack: ResourcePack) => Promise<unknown>;
  /** Where one is uploaded, for a server that exists, and what to do with the server after. */
  upload?: { path: string; onDone: (next: Server) => void };
}

/** A dialog for picking mods, plugins, datapacks or the resource pack, from Modrinth, a URL or this computer. */
export function AddContentDialog({ software, mods, onAdd, busy, onUpload, description, pack, start, onClose }: AddContentProps) {
  const kinds: PickKind[] = [...kindsFor(software), ...(pack ? (["resourcepack"] as const) : [])];
  const [kind, setKind] = useState<PickKind>(start && kinds.includes(start) ? start : kinds[0]);
  const [tab, setTab] = useState<"modrinth" | "url" | "upload">("modrinth");
  const [uploading, setUploading] = useState<string | null>(null);
  const [dragging, setDragging] = useState(false);
  // Content installed on the server: the resource pack has a pane of its own.
  const content = kind as ContentKind;
  const extension = kind === "datapack" ? ".zip" : ".jar";

  const upload = async (files: FileList | null) => {
    if (!onUpload || !files) return;
    try {
      for (const file of Array.from(files)) {
        setUploading(file.name);
        await onUpload(file, content);
      }
    } finally {
      setUploading(null);
    }
  };
  const [url, setUrl] = useState("");
  const picked = new Set(mods.filter((m) => m.source === "modrinth").map((m) => m.project));
  // "Latest" is what the server's own software will get, which can be behind the newest there is.
  const latest = useGameVersions(software.modpack ? null : software.loader).data?.latest;

  const filters = filtersFor(software, kind, latest);

  const tabClass = (active: boolean) => cx("h-7 rounded-sm px-2.5 text-xs font-medium", active ? "bg-hover text-fg" : "text-muted hover:text-fg");
  const canUpload = kind === "resourcepack" ? !!pack?.upload : !!onUpload;
  const sourceTab = tab === "upload" && !canUpload ? "modrinth" : tab;
  return (
    <Modal open onOpenChange={onClose} size="lg" title="Add content" description={kind === "resourcepack" ? "Offered to each player's game as they join." : description}>
      <div className="flex flex-col gap-4">
        <div className="flex flex-wrap items-center justify-between gap-2">
          <div className="flex gap-1 rounded-md border border-line bg-bg p-0.5">
            {kinds.map((k) => (
              <button key={k} type="button" className={tabClass(kind === k)} onClick={() => setKind(k)}>
                {kindNames[k]}
              </button>
            ))}
          </div>
          <div className="flex gap-1 rounded-md border border-line bg-bg p-0.5">
            <button type="button" className={tabClass(tab === "modrinth")} onClick={() => setTab("modrinth")}>
              Modrinth
            </button>
            <button type="button" className={tabClass(tab === "url")} onClick={() => setTab("url")}>
              From URL
            </button>
            {canUpload && (
              <button type="button" className={tabClass(tab === "upload")} onClick={() => setTab("upload")}>
                Upload
              </button>
            )}
          </div>
        </div>
        {kind === "resourcepack" && pack ? (
          <PackPane pack={pack} tab={sourceTab} filters={filters} fits={fitsOf(filters)} onClose={onClose} />
        ) : tab === "modrinth" ? (
          <div className="h-[min(60vh,560px)]">
            <ModrinthSearch
              key={kind}
              type={content}
              {...filters}
              fits={fitsOf(filters)}
              picked={picked}
              busy={busy}
              placeholder={`Search ${kindNames[kind].toLowerCase()} on Modrinth`}
              onPick={(hit) => onAdd({ source: "modrinth", kind: content, project: hit.slug, title: hit.title, iconUrl: hit.icon_url ?? undefined })}
            />
          </div>
        ) : tab === "upload" ? (
          <label
            onDragOver={(e) => {
              e.preventDefault();
              setDragging(true);
            }}
            onDragLeave={() => setDragging(false)}
            onDrop={(e) => {
              e.preventDefault();
              setDragging(false);
              upload(e.dataTransfer.files);
            }}
            className={cx(
              "flex h-48 cursor-pointer flex-col items-center justify-center gap-2 rounded-md border border-dashed px-6 text-center transition-colors",
              dragging ? "border-accent bg-accent-wash" : "border-line-strong hover:bg-raised",
            )}
          >
            <input type="file" multiple hidden accept={extension} disabled={!!uploading} onChange={(e) => upload(e.target.files).then(() => (e.target.value = ""))} />
            {uploading ? (
              <>
                <Spinner />
                <p className="text-[13px] text-muted">Uploading {uploading}</p>
              </>
            ) : (
              <>
                <Upload className="size-5 text-muted" />
                <p className="text-[13px] font-medium">Choose {extension} files, or drop them here</p>
                <p className="max-w-sm text-xs leading-relaxed text-muted">
                  For {kindNames[kind].toLowerCase()} you have on this computer. They are stored with the server and installed when it next starts.
                </p>
              </>
            )}
          </label>
        ) : (
          <form
            className="flex flex-col gap-4"
            onSubmit={(e) => {
              e.preventDefault();
              // Dialogs render in a portal, but React still bubbles the event
              // to a form the dialog was opened from.
              e.stopPropagation();
              const name = decodeURIComponent(url.split("/").pop()?.split("?")[0] ?? url);
              onAdd({ source: "url", kind: content, project: url.trim(), title: name });
              setUrl("");
            }}
          >
            <Field label="Download URL" hint="A direct link to the file.">
              {(id) => <Input id={id} type="url" required value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://example.com/some-mod-1.2.3.jar" className="font-mono" />}
            </Field>
            <div className="flex justify-end">
              <Button type="submit" variant="primary" loading={busy}>
                Add
              </Button>
            </div>
          </form>
        )}
      </div>
    </Modal>
  );
}

/** A Modrinth version's file as a server's resource pack. */
export function packOfVersion(v: ModrinthVersion, project: Omit<PackProject, "version" | "versionName">): ResourcePack {
  const file = v.files?.find((f) => f.primary) ?? v.files?.[0];
  if (!file) throw new Error(`${project.title || project.id} ${v.version_number} has no file to offer.`);
  return { url: file.url, sha1: file.hashes.sha1 ?? "", uploaded: null, project: { ...project, version: v.id, versionName: v.version_number } };
}

/** The newest release of a Modrinth project that fits, as a resource pack. */
export async function newestPack(project: Omit<PackProject, "version" | "versionName">, gameVersion?: string): Promise<ResourcePack> {
  const versions = await api.get<ModrinthVersion[]>(`/catalog/modrinth/projects/${encodeURIComponent(project.id)}/versions?${qs({ loader: "resourcepack", gameVersion })}`);
  const v = versions.find((v) => v.version_type === "release") ?? versions[0];
  if (!v) throw new Error(`${project.title || project.id} has no version for Minecraft ${gameVersion}.`);
  return packOfVersion(v, project);
}

const projectOf = (hit: ModrinthHit): Omit<PackProject, "version" | "versionName"> => ({ source: "modrinth", id: hit.slug, title: hit.title, iconUrl: hit.icon_url ?? undefined });

/** What a resource pack is called: its project, its file, or the end of its address. */
export const packTitle = (p: ResourcePack) => p.project?.title || p.project?.id || p.uploaded?.name || decodeURIComponent(p.url.split("/").pop()?.split("?")[0] || p.url);

/** The resource pack's three ways in, in the picker. One replaces the one there was. */
function PackPane({ pack, tab, filters, fits, onClose }: { pack: PackPicker; tab: "modrinth" | "url" | "upload"; filters: { loader?: string; gameVersion?: string }; fits?: string; onClose: () => void }) {
  const [busy, setBusy] = useState(false);
  const [url, setUrl] = useState("");
  const { sending, send, cancel } = useUpload();
  const current = pack.current?.url ? pack.current : null;
  const pick = async (make: () => Promise<ResourcePack>) => {
    setBusy(true);
    try {
      await pack.onPick(await make());
      onClose();
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Something went wrong.");
    } finally {
      setBusy(false);
    }
  };
  const upload = async (file: File | undefined) => {
    if (!file || !pack.upload) return;
    try {
      pack.upload.onDone(await send<Server>(file.name, `${pack.upload.path}?name=${encodeURIComponent(file.name)}`, file));
      toast.info(`${file.name} is the resource pack now`);
      onClose();
    } catch (err) {
      if (!cancelled(err)) toast.error(err instanceof Error ? err.message : "Upload failed.");
    }
  };
  return (
    <div className="flex flex-col gap-3">
      {current && <Notice tone="info">A server offers one resource pack. Choosing one here replaces {packTitle(current)}.</Notice>}
      {tab === "modrinth" ? (
        <div className="h-[min(56vh,520px)]">
          <ModrinthSearch
            type="resourcepack"
            {...filters}
            fits={fits}
            picked={new Set(current?.project ? [current.project.id] : [])}
            busy={busy}
            pickLabels={["Use this pack", "In use"]}
            placeholder="Search resource packs on Modrinth"
            onPick={(hit) => pick(() => newestPack(projectOf(hit), filters.gameVersion))}
          />
        </div>
      ) : tab === "upload" ? (
        <label className={cx("flex h-48 flex-col items-center justify-center gap-2 rounded-md border border-dashed border-line-strong px-6 text-center transition-colors", !sending && "cursor-pointer hover:bg-raised")}>
          <input
            type="file"
            hidden
            accept=".zip,application/zip"
            aria-label="Resource pack file"
            disabled={!!sending}
            onChange={(e) => {
              const file = e.target.files?.[0];
              e.target.value = "";
              void upload(file);
            }}
          />
          {sending ? (
            <UploadProgress sending={sending} onCancel={cancel} />
          ) : (
            <>
              <Upload className="size-5 text-muted" />
              <p className="text-[13px] font-medium">Choose a .zip</p>
              <p className="max-w-sm text-xs leading-relaxed text-muted">With pack.mcmeta at its top. The panel keeps it with the server's files and serves it to players.</p>
            </>
          )}
        </label>
      ) : (
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            e.stopPropagation();
            const address = url.trim();
            pick(async () => {
              // Its hash lets the game keep it between joins; without one it
              // is fetched every time, so a pack the panel cannot reach is
              // still taken.
              const sha1 = await api
                .post<{ sha1: string }>("/catalog/sha1", { url: address })
                .then((r) => r.sha1)
                .catch(() => "");
              return { url: address, sha1, uploaded: null, project: null };
            });
          }}
        >
          <Field label="Download URL" hint="A direct link to the .zip, which every player must be able to reach.">
            {(id) => <Input id={id} type="url" required value={url} onChange={(e) => setUrl(e.target.value)} placeholder="https://example.com/pack.zip" className="font-mono" />}
          </Field>
          <div className="flex justify-end">
            <Button type="submit" variant="primary" loading={busy}>
              Use this pack
            </Button>
          </div>
        </form>
      )}
    </div>
  );
}

/** A server's resource pack, as content: what it is and where it comes from, with room for actions. */
export function PackCard({ pack, children }: { pack: ResourcePack; children?: ReactNode }) {
  const p = pack.project;
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-lg border border-line bg-surface px-4 py-2.5">
      {p ? <ProjectIcon url={p.iconUrl} size={32} /> : <span className="grid size-8 shrink-0 place-items-center rounded-md bg-raised"><FileArchive className="size-4 text-muted" /></span>}
      <div className="min-w-0 flex-1">
        <p className="flex items-center gap-2 truncate text-[13px] font-medium">
          {packTitle(pack)}
          {p && (
            <a href={`https://modrinth.com/resourcepack/${p.id}`} target="_blank" rel="noreferrer" className="text-faint hover:text-accent" aria-label="Open on Modrinth">
              <ExternalLink className="size-3" />
            </a>
          )}
        </p>
        <p className="truncate font-mono text-xs text-faint">
          {pack.uploaded ? `${bytes(pack.uploaded.size)} · uploaded ${ago(pack.uploaded.time)} · served by the panel` : p ? `modrinth:${p.id}` : pack.url}
        </p>
      </div>
      <Chip>resource pack</Chip>
      {children}
    </div>
  );
}

const sourceLine = (m: ContentRef) => (m.source === "url" ? m.project : m.source === "upload" ? "uploaded file" : `${m.source}:${m.project}`);

interface ContentListProps {
  mods: ContentRef[];
  /** The Java the game version ships with. Entries needing more say so. */
  gameJava?: number;
  /** Rendered between an entry's kind and its remove button, e.g. a version picker. */
  extra?: (mod: ContentRef, index: number) => ReactNode;
  onRemove?: (index: number) => void;
}

export function ContentList({ mods, gameJava, extra, onRemove }: ContentListProps) {
  return (
    <ul className="overflow-hidden rounded-lg border border-line bg-surface">
      {mods.map((m, i) => (
        <li key={`${m.source}/${m.project}`} className="flex items-center gap-3 border-b border-line px-4 py-2.5 last:border-b-0">
          <ProjectIcon url={m.iconUrl} size={32} />
          <div className="min-w-0 flex-1">
            <p className="flex items-center gap-2 truncate text-[13px] font-medium">
              {m.title || m.project}
              {m.source === "modrinth" && (
                <a href={`https://modrinth.com/project/${m.project}`} target="_blank" rel="noreferrer" className="text-faint hover:text-accent" aria-label="Open on Modrinth">
                  <ExternalLink className="size-3" />
                </a>
              )}
            </p>
            <p className="truncate font-mono text-xs text-faint">{sourceLine(m)}</p>
          </div>
          {!!gameJava && !!m.java && m.java > gameJava && (
            <Chip className="border-warn/40 text-warn" >needs Java {m.java}</Chip>
          )}
          <Chip>{m.kind ?? "mod"}</Chip>
          {extra?.(m, i)}
          {onRemove && <Button size="sm" variant="ghost" icon={<X className="size-4" />} aria-label={`Remove ${m.title || m.project}`} onClick={() => onRemove(i)} />}
        </li>
      ))}
    </ul>
  );
}
