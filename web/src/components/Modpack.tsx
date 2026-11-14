import { Upload } from "lucide-react";
import { useEffect, useState } from "react";
import type { Modpack } from "../lib/api";
import type { PackFile } from "../lib/mrpack";
import { ProjectIcon } from "./ModrinthSearch";
import { cx, SearchBox, Spinner } from "./ui";

/** A modpack's picture: the one in its file, where it was read from one, or its project's. */
export function PackIcon({ modpack, icon, size }: { modpack: Modpack; icon?: Uint8Array | null; size?: number }) {
  const [url, setUrl] = useState<string | undefined>();
  useEffect(() => {
    if (!icon) return setUrl(undefined);
    const made = URL.createObjectURL(new Blob([icon.slice()], { type: "image/png" }));
    setUrl(made);
    return () => URL.revokeObjectURL(made);
  }, [icon]);
  return <ProjectIcon url={url ?? modpack.iconUrl} size={size} kind="modpack" name={modpack.title || modpack.project} />;
}

/** Where a modpack file is chosen or dropped. */
export function PackDrop({ reading, onFile, note = "A Modrinth modpack file." }: { reading: boolean; onFile: (file: File | undefined) => void; note?: string }) {
  const [dragging, setDragging] = useState(false);
  return (
    <label
      onDragOver={(e) => {
        e.preventDefault();
        setDragging(true);
      }}
      onDragLeave={() => setDragging(false)}
      onDrop={(e) => {
        e.preventDefault();
        setDragging(false);
        if (!reading) onFile(e.dataTransfer.files[0]);
      }}
      className={cx(
        "flex cursor-pointer items-center gap-3 rounded-md border border-dashed px-4 py-3 transition-colors",
        dragging ? "border-accent bg-accent-wash" : "border-line-strong hover:bg-raised",
      )}
    >
      <input
        type="file"
        hidden
        accept=".mrpack"
        aria-label="Modpack file"
        disabled={reading}
        onChange={(e) => {
          const file = e.target.files?.[0];
          e.target.value = "";
          onFile(file);
        }}
      />
      {reading ? <Spinner className="size-5" /> : <Upload className="size-5 shrink-0 text-muted" />}
      <span className="flex min-w-0 flex-col">
        <span className="text-[13px] font-medium">Upload a .mrpack, or drop one here</span>
        <span className="text-xs text-muted">{note}</span>
      </span>
    </label>
  );
}

/** What kind of file a pack brings, by the folder it goes in. */
const folderNames: Record<string, string> = { mods: "Mod", resourcepacks: "Resource pack", shaderpacks: "Shader pack", datapacks: "Datapack", plugins: "Plugin" };

/**
 * The files a modpack brings, those for players only after the rest, said
 * to be left off the server. A long list can be searched.
 */
export function PackFiles({ files }: { files: PackFile[] }) {
  const [query, setQuery] = useState("");
  const server = files.filter((f) => f.server).length;
  const left = files.length - server;
  const q = query.trim().toLowerCase();
  const shown = [...files.filter((f) => f.server), ...files.filter((f) => !f.server)].filter((f) => !q || f.path.toLowerCase().includes(q));
  if (files.length === 0) return <p className="text-xs text-muted">The pack lists no files to download.</p>;
  return (
    <section aria-label="In the modpack" className="flex flex-col gap-2">
      <div className="flex items-center justify-between gap-3">
        <p className="text-xs text-muted">
          {server} on the server{left > 0 ? ` · ${left} left out, client only` : ""}
        </p>
        {files.length > 8 && <SearchBox value={query} onChange={setQuery} label="Search the modpack" className="w-48" />}
      </div>
      <ul className="max-h-72 divide-y divide-line overflow-y-auto rounded-md border border-line">
        {shown.map((f) => {
          const folder = f.path.includes("/") ? f.path.slice(0, f.path.indexOf("/")) : "";
          const name = f.name.replace(/\.(jar|zip)$/i, "");
          return (
            <li key={f.path} className={cx("flex items-center gap-2.5 px-3 py-1.5", !f.server && "opacity-60")}>
              <ProjectIcon size={24} kind={folder === "mods" ? "mod" : folder === "resourcepacks" ? "resourcepack" : undefined} name={name} />
              <span className="min-w-0 flex-1 truncate text-[13px]">{name}</span>
              <span className="shrink-0 text-xs text-muted">{f.server ? (folderNames[folder] ?? folder) : "Client only, left out"}</span>
            </li>
          );
        })}
        {shown.length === 0 && <li className="px-3 py-2 text-xs text-muted">Nothing matches.</li>}
      </ul>
    </section>
  );
}
