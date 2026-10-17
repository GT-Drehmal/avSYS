import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronRight, Download, File, FilePlus, Folder, FolderPlus, MoreHorizontal, Pencil, Trash2, Upload, X } from "lucide-react";
import { useRef, useState } from "react";
import { Link, useParams, useSearchParams } from "react-router";
import { Button, Confirm, cx, Empty, Field, Input, Menu, MenuContent, MenuItem, MenuTrigger, Modal, Spinner } from "../../components/ui";
import { cancelled, UploadProgress, useUpload } from "../../components/Uploading";
import { api, qs, serverPath, type FileEntry } from "../../lib/api";
import { ago, bytes } from "../../lib/format";
import { useAction } from "../../lib/queries";
import { toast } from "../../lib/toast";
import { Editor } from "./Editor";
import { useServerContext } from "./ServerPage";

const join = (dir: string, name: string) => (dir === "/" ? `/${name}` : `${dir}/${name}`);
const encodePath = (p: string) => p.split("/").map(encodeURIComponent).join("/");

// Editing is for configuration, not for worlds and jars.
const maxEditable = 2 * 1024 * 1024;
const binary = /\.(jar|zip|gz|tgz|tar|7z|rar|png|jpe?g|gif|webp|ico|dat|dat_old|mca|mcr|nbt|db|sqlite|lock|ogg|mp3|wav|class|so|bin)$/i;

function FileEditor({ base, path, managed, onClose }: { base: string; path: string; managed: string[]; onClose: () => void }) {
  const name = path.slice(path.lastIndexOf("/") + 1);
  const url = `${base}/content?${qs({ path })}`;
  const file = useQuery({ queryKey: ["file", base, path], queryFn: () => api.get<string>(url), staleTime: 0, gcTime: 0 });
  const [draft, setDraft] = useState<string | null>(null);
  const dirty = draft !== null && draft !== file.data;
  const save = useAction(async () => {
    if (draft === null) return;
    await api.putRaw(url, draft);
    await file.refetch();
    toast.info(`Saved ${name}`);
  });

  return (
    <div className="flex h-[calc(100dvh-330px)] min-h-80 flex-col overflow-hidden rounded-lg border border-line bg-bg">
      <div className="flex h-10 shrink-0 items-center justify-between gap-3 border-b border-line bg-surface pr-2 pl-4">
        <span className="truncate font-mono text-xs text-muted">
          {path}
          {dirty && <span className="ml-2 text-warn">unsaved</span>}
        </span>
        <div className="flex items-center gap-1.5">
          <Button size="sm" variant="primary" disabled={!dirty} loading={save.isPending} onClick={() => save.mutate(undefined)}>
            Save
          </Button>
          <Button size="sm" variant="ghost" icon={<X className="size-4" />} aria-label="Close" onClick={onClose} />
        </div>
      </div>
      {managed.length > 0 && (
        <p className="shrink-0 border-b border-line bg-warn-wash px-4 py-2 text-xs leading-relaxed text-warn">
          <span className="font-mono">{managed.join(", ")}</span> {managed.length === 1 ? "is" : "are"} set under Settings and rewritten from there every time the server starts. Change {managed.length === 1 ? "it" : "them"} there, or clear {managed.length === 1 ? "it" : "them"} there to manage {managed.length === 1 ? "it" : "them"} in this file.
        </p>
      )}
      {file.isPending ? (
        <div className="flex flex-1 items-center justify-center">
          <Spinner />
        </div>
      ) : file.isError ? (
        <p className="p-4 text-[13px] text-danger">{file.error.message}</p>
      ) : (
        <Editor name={name} initial={file.data} onChange={setDraft} onSave={() => save.mutate(undefined)} />
      )}
    </div>
  );
}

function NameDialog({ title, action, initial, onSubmit, onClose }: { title: string; action: string; initial: string; onSubmit: (name: string) => Promise<unknown>; onClose: () => void }) {
  const [name, setName] = useState(initial);
  const [busy, setBusy] = useState(false);
  return (
    <Modal open onOpenChange={onClose} title={title}>
      <form
        className="flex flex-col gap-4"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          try {
            await onSubmit(name.trim());
            onClose();
          } catch (err) {
            toast.error(err instanceof Error ? err.message : "That did not work.");
            setBusy(false);
          }
        }}
      >
        <Field label="Name">{(id) => <Input id={id} autoFocus required value={name} onChange={(e) => setName(e.target.value)} pattern="[^/]+" className="font-mono" />}</Field>
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" loading={busy}>
            {action}
          </Button>
        </div>
      </form>
    </Modal>
  );
}

type Dialog = { kind: "folder" } | { kind: "file" } | { kind: "rename"; entry: FileEntry } | { kind: "delete"; entry: FileEntry };

export default function Files() {
  const { server } = useServerContext();
  const params = useParams();
  const [search, setSearch] = useSearchParams();
  const qc = useQueryClient();
  const base = `${serverPath(server.org, server.name)}/files`;
  const root = `/o/${server.org}/s/${server.name}/files`;
  const dir = "/" + (params["*"] ?? "").replace(/\/+$/, "");
  const open = search.get("file");

  const listKey = ["files", base, dir];
  const list = useQuery({ queryKey: listKey, queryFn: () => api.get<{ entries: FileEntry[] }>(`${base}?${qs({ path: dir })}`), staleTime: 0 });
  const refresh = () => qc.invalidateQueries({ queryKey: listKey });

  const [dialog, setDialog] = useState<Dialog | null>(null);
  const { sending, send, cancel } = useUpload();
  const [dragging, setDragging] = useState(false);
  const picker = useRef<HTMLInputElement>(null);
  const remove = useAction((entry: FileEntry) => api.del(`${base}?${qs({ path: join(dir, entry.name) })}`), [listKey]);

  const upload = async (files: FileList | File[]) => {
    const all = Array.from(files);
    try {
      for (const [i, file] of all.entries()) {
        await send(all.length > 1 ? `${file.name} (${i + 1} of ${all.length})` : file.name, `${base}/content?${qs({ path: join(dir, file.name) })}`, file);
      }
    } catch (err) {
      if (!cancelled(err)) toast.error(err instanceof Error ? err.message : "Upload failed.");
    } finally {
      refresh();
    }
  };

  // The panel owns the server.properties entries that are set in Settings.
  const managed = open === "/server.properties" ? Object.keys(server.spec.properties ?? {}).sort() : [];
  if (open) return <FileEditor base={base} path={open} managed={managed} onClose={() => setSearch({})} />;

  const crumbs = dir === "/" ? [] : dir.slice(1).split("/");
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <nav className="flex min-w-0 flex-wrap items-center gap-1 font-mono text-[13px]">
          <Link to={root} className={cx("rounded-sm px-1 hover:text-accent", crumbs.length ? "text-muted" : "text-fg")}>
            data
          </Link>
          {crumbs.map((part, i) => (
            <span key={i} className="flex items-center gap-1">
              <ChevronRight className="size-3 text-faint" />
              <Link to={`${root}/${encodePath(crumbs.slice(0, i + 1).join("/"))}`} className={cx("rounded-sm px-1 hover:text-accent", i === crumbs.length - 1 ? "text-fg" : "text-muted")}>
                {part}
              </Link>
            </span>
          ))}
        </nav>
        <div className="flex items-center gap-2">
          {sending && <UploadProgress sending={sending} onCancel={cancel} />}
          <Button size="sm" icon={<FilePlus className="size-3.5" />} onClick={() => setDialog({ kind: "file" })}>
            New file
          </Button>
          <Button size="sm" icon={<FolderPlus className="size-3.5" />} onClick={() => setDialog({ kind: "folder" })}>
            New folder
          </Button>
          <Button size="sm" icon={<Upload className="size-3.5" />} disabled={!!sending} onClick={() => picker.current?.click()}>
            Upload
          </Button>
          <input ref={picker} type="file" multiple hidden onChange={(e) => e.target.files?.length && upload(e.target.files).then(() => (e.target.value = ""))} />
        </div>
      </div>

      <div
        onDragOver={(e) => {
          e.preventDefault();
          setDragging(true);
        }}
        onDragLeave={() => setDragging(false)}
        onDrop={(e) => {
          e.preventDefault();
          setDragging(false);
          if (e.dataTransfer.files.length) upload(e.dataTransfer.files);
        }}
        className={cx("overflow-hidden rounded-lg border bg-surface transition-colors", dragging ? "border-accent" : "border-line")}
      >
        {list.isPending ? (
          <div className="flex h-40 items-center justify-center">
            <Spinner />
          </div>
        ) : list.isError ? (
          <div className="p-4">
            <Empty title="Could not open this folder">{list.error.message}</Empty>
          </div>
        ) : list.data.entries.length === 0 ? (
          <p className="px-4 py-10 text-center text-[13px] text-muted">This folder is empty. Drop files here to upload them.</p>
        ) : (
          <ul>
            {list.data.entries.map((entry) => {
              const path = join(dir, entry.name);
              const editable = !entry.dir && entry.size <= maxEditable && !binary.test(entry.name);
              const download = `/api/v1${base}/content?${qs({ path })}&download`;
              const body = (
                <>
                  {entry.dir ? <Folder className="size-4 shrink-0 text-accent" /> : <File className="size-4 shrink-0 text-faint" />}
                  <span className="min-w-0 flex-1 truncate font-mono text-[13px]">{entry.name}</span>
                  <span className="w-20 shrink-0 text-right font-mono text-xs text-faint tabular-nums max-sm:hidden">{entry.dir ? "" : bytes(entry.size)}</span>
                  <span className="w-28 shrink-0 text-right text-xs text-faint max-sm:hidden">{ago(entry.modTime)}</span>
                </>
              );
              const rowClass = "flex h-9 min-w-0 flex-1 items-center gap-3 pl-4";
              return (
                <li key={entry.name} className="flex items-center border-b border-line pr-2 last:border-b-0 hover:bg-raised">
                  {entry.dir ? (
                    <Link to={`${root}${encodePath(path)}`} className={rowClass}>
                      {body}
                    </Link>
                  ) : editable ? (
                    <button className={cx(rowClass, "text-left")} onClick={() => setSearch({ file: path })}>
                      {body}
                    </button>
                  ) : (
                    <a href={download} className={rowClass} title="Download">
                      {body}
                    </a>
                  )}
                  <Menu>
                    <MenuTrigger asChild>
                      <Button size="sm" variant="ghost" icon={<MoreHorizontal className="size-4" />} aria-label={`Actions for ${entry.name}`} className="ml-2" />
                    </MenuTrigger>
                    <MenuContent>
                      {!entry.dir && (
                        <MenuItem icon={<Download />} onSelect={() => window.location.assign(download)}>
                          Download
                        </MenuItem>
                      )}
                      <MenuItem icon={<Pencil />} onSelect={() => setDialog({ kind: "rename", entry })}>
                        Rename
                      </MenuItem>
                      <MenuItem icon={<Trash2 />} danger onSelect={() => setDialog({ kind: "delete", entry })}>
                        Delete
                      </MenuItem>
                    </MenuContent>
                  </Menu>
                </li>
              );
            })}
          </ul>
        )}
      </div>

      {dialog?.kind === "folder" && (
        <NameDialog title="New folder" action="Create" initial="" onClose={() => setDialog(null)} onSubmit={(name) => api.post(`${base}/mkdir?${qs({ path: join(dir, name) })}`).then(refresh)} />
      )}
      {dialog?.kind === "file" && (
        <NameDialog
          title="New file"
          action="Create"
          initial=""
          onClose={() => setDialog(null)}
          onSubmit={async (name) => {
            await api.putRaw(`${base}/content?${qs({ path: join(dir, name) })}`, "");
            await refresh();
            setSearch({ file: join(dir, name) });
          }}
        />
      )}
      {dialog?.kind === "rename" && (
        <NameDialog
          title={`Rename ${dialog.entry.name}`}
          action="Rename"
          initial={dialog.entry.name}
          onClose={() => setDialog(null)}
          onSubmit={(name) => api.post(`${base}/rename?${qs({ path: join(dir, dialog.entry.name), to: join(dir, name) })}`).then(refresh)}
        />
      )}
      <Confirm
        open={dialog?.kind === "delete"}
        onOpenChange={() => setDialog(null)}
        title={dialog?.kind === "delete" ? `Delete ${dialog.entry.name}?` : ""}
        description={dialog?.kind === "delete" && dialog.entry.dir ? "The folder and everything in it will be deleted. This cannot be undone." : "This cannot be undone."}
        action="Delete"
        loading={remove.isPending}
        onConfirm={() => dialog?.kind === "delete" && remove.mutate(dialog.entry, { onSuccess: () => setDialog(null) })}
      />
    </div>
  );
}
