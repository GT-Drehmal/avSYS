import { ExternalLink, Upload, X } from "lucide-react";
import { useState, type ReactNode } from "react";
import type { ContentKind, ContentRef, ServerSpec } from "../lib/api";
import { useGameVersions } from "../lib/software";
import { ModrinthSearch, ProjectIcon } from "./ModrinthSearch";
import { Button, Chip, cx, Field, Input, Modal, Spinner } from "./ui";

export const kindNames: Record<ContentKind, string> = { mod: "Mods", plugin: "Plugins", datapack: "Datapacks" };

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
export function filtersFor(software: Software, kind: ContentKind, latestRelease?: string) {
  if (software.modpack) {
    // Known once the panel has pinned the pack; until then nothing is narrowed.
    return { loader: kind === "datapack" ? "datapack" : software.modpack.loader, gameVersion: software.modpack.gameVersion };
  }
  return {
    loader: kind === "datapack" ? "datapack" : software.loader,
    gameVersion: software.version === "LATEST" ? latestRelease : software.version,
  };
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
  onClose: () => void;
}

/** A dialog for picking mods, plugins or datapacks from Modrinth or a URL. */
export function AddContentDialog({ software, mods, onAdd, busy, onUpload, description, onClose }: AddContentProps) {
  const kinds = kindsFor(software);
  const [kind, setKind] = useState<ContentKind>(kinds[0]);
  const [tab, setTab] = useState<"modrinth" | "url" | "upload">("modrinth");
  const [uploading, setUploading] = useState<string | null>(null);
  const [dragging, setDragging] = useState(false);
  const extension = kind === "datapack" ? ".zip" : ".jar";

  const upload = async (files: FileList | null) => {
    if (!onUpload || !files) return;
    try {
      for (const file of Array.from(files)) {
        setUploading(file.name);
        await onUpload(file, kind);
      }
    } finally {
      setUploading(null);
    }
  };
  const [url, setUrl] = useState("");
  const picked = new Set(mods.filter((m) => m.source === "modrinth").map((m) => m.project));
  const latest = useGameVersions().data?.latest;

  const tabClass = (active: boolean) => cx("h-7 rounded-sm px-2.5 text-xs font-medium", active ? "bg-hover text-fg" : "text-muted hover:text-fg");
  return (
    <Modal open onOpenChange={onClose} size="lg" title="Add content" description={description}>
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
            {onUpload && (
              <button type="button" className={tabClass(tab === "upload")} onClick={() => setTab("upload")}>
                Upload
              </button>
            )}
          </div>
        </div>
        {tab === "modrinth" ? (
          <div className="h-[min(55vh,460px)]">
            <ModrinthSearch
              key={kind}
              type={kind}
              {...filtersFor(software, kind, latest)}
              picked={picked}
              busy={busy}
              placeholder={`Search ${kindNames[kind].toLowerCase()} on Modrinth`}
              onPick={(hit) => onAdd({ source: "modrinth", kind, project: hit.slug, title: hit.title, iconUrl: hit.icon_url ?? undefined })}
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
              onAdd({ source: "url", kind, project: url.trim(), title: name });
              setUrl("");
            }}
          >
            <Field label="Download URL" hint="A direct link to the file, for content that is not on Modrinth.">
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
