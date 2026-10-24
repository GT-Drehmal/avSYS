import { useQueries, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import { ArrowLeft, ChevronRight, Code2, Download, File, FilePlus, Folder, FolderInput, FolderOpen, FolderPlus, MoreHorizontal, PackageOpen, Pencil, Search, Trash2, Upload, X } from "lucide-react";
import { useCallback, useEffect, useRef, useState } from "react";
import { useParams, useSearchParams } from "react-router";
import { Faces } from "../../components/Faces";
import { Button, Confirm, cx, Empty, Field, Input, Menu, MenuContent, MenuItem, MenuTrigger, Modal, Spinner } from "../../components/ui";
import { cancelled, UploadProgress, useUpload } from "../../components/Uploading";
import { api, ApiError, qs, readFile, saveFile, serverPath, type EditedFile, type FileEntry } from "../../lib/api";
import { ago, bytes } from "../../lib/format";
import { useAction, useMe, useMeta } from "../../lib/queries";
import { vscodeURL } from "../../lib/ssh";
import { others, usePresence } from "../../lib/presence";
import { toast } from "../../lib/toast";
import { Editor } from "./Editor";
import { useServerContext } from "./ServerPage";

const join = (dir: string, name: string) => (dir === "/" ? `/${name}` : `${dir}/${name}`);
const parentOf = (path: string) => path.slice(0, path.lastIndexOf("/")) || "/";
const nameOf = (path: string) => path.slice(path.lastIndexOf("/") + 1);
/** The folders a path is in, outermost first, not counting the top one. */
const foldersAbove = (path: string) => {
  const parts = path.split("/").filter(Boolean);
  return parts.slice(0, -1).map((_, i) => `/${parts.slice(0, i + 1).join("/")}`);
};
const within = (path: string, dir: string) => path === dir || path.startsWith(`${dir}/`);

// Editing is for configuration, not for worlds and jars.
const maxEditable = 2 * 1024 * 1024;
const binary = /\.(jar|zip|gz|tgz|tar|7z|rar|png|jpe?g|gif|webp|ico|dat|dat_old|mca|mcr|nbt|db|sqlite|lock|ogg|mp3|wav|class|so|bin)$/i;

/** What an editor has: the file as it opened it, and what was typed since. */
type EditorState = { basis: EditedFile | null; draft: string | null };

function FileEditor({
  base,
  path,
  managed,
  carry,
  onClose,
  onDirty,
  onState,
}: {
  base: string;
  path: string;
  managed: string[];
  /** What it had at another path, before the file was renamed or moved. */
  carry?: EditorState;
  onClose: () => void;
  onDirty: (dirty: boolean) => void;
  onState: (state: EditorState) => void;
}) {
  const name = path.slice(path.lastIndexOf("/") + 1);
  const url = `${base}/content?${qs({ path })}`;
  const { org, server } = useParams();
  const me = useMe().data?.user.id;
  // Each once, if in two tabs.
  const alsoOpen = [...new Map(others(usePresence(org ?? ""), me, server ?? "", path).map((p) => [p.user.id, p])).values()];
  // Read once, and again only on Reload: a draft is saved over what it was
  // made from, not over what a read behind it found.
  const file = useQuery({ queryKey: ["file", base, path], queryFn: () => readFile(url), staleTime: Infinity, gcTime: 0, refetchOnWindowFocus: false, refetchOnReconnect: false });
  // The file as the editor has it: as opened, reloaded or last saved. A
  // save is written only over a file that is still this.
  const [basis, setBasis] = useState<EditedFile | null>(carry?.basis ?? null);
  const opened = basis ?? file.data;
  useEffect(() => {
    if (file.data && !basis) setBasis(file.data);
  }, [file.data, basis]);
  const [draft, setDraft] = useState<string | null>(carry?.draft ?? null);
  // Reloaded, the editor starts again from the file as it is now.
  const [loads, setLoads] = useState(0);
  // Why a save was refused: the file changed, or was removed, since.
  const [refused, setRefused] = useState<ApiError | null>(null);
  const dirty = draft !== null && draft !== opened?.text;
  useEffect(() => {
    onDirty(dirty);
  }, [dirty, onDirty]);
  useEffect(() => {
    onState({ basis, draft });
  }, [basis, draft, onState]);
  const save = useAction(async (anyway: boolean) => {
    if (draft === null || !opened) return;
    if (new Blob([draft]).size > maxEditable) throw new ApiError(413, "too_large", "Files over 2 MB can't be saved here. Upload the file instead.");
    try {
      const tag = await saveFile(url, draft, anyway ? null : opened.tag);
      setBasis({ text: draft, tag });
    } catch (e) {
      if (e instanceof ApiError && (e.code === "changed" || e.code === "removed")) {
        setRefused(e);
        return;
      }
      throw e;
    }
    setRefused(null);
    toast.info(`Saved ${name}`);
  });
  // One that cannot be read now leaves the draft as it is.
  const reload = useAction(async () => {
    const now = await readFile(url);
    setBasis(now);
    setDraft(null);
    setRefused(null);
    setLoads((n) => n + 1);
  });

  return (
    <div className="flex h-full min-h-0 flex-col overflow-hidden bg-bg">
      <div className="flex h-10 shrink-0 items-center justify-between gap-3 border-b border-line bg-surface pr-2 pl-4">
        <span className="flex min-w-0 items-center gap-3">
          <span className="truncate font-mono text-xs text-muted">
            {path}
            {dirty && <span className="ml-2 text-warn">unsaved</span>}
          </span>
          {alsoOpen.length > 0 && (
            <span className="flex shrink-0 items-center gap-1.5 text-xs text-muted max-sm:hidden">
              <Faces people={alsoOpen} label="Also has it open" size={18} ring="ring-surface" />
              {alsoOpen.length === 1 ? `${alsoOpen[0].user.name} also has it open` : "Others also have it open"}
            </span>
          )}
        </span>
        <div className="flex items-center gap-1.5">
          <Button size="sm" variant="primary" disabled={!dirty} loading={save.isPending} onClick={() => save.mutate(false)}>
            Save
          </Button>
          {/* On a phone, back to the files; beside them, closed. */}
          <Button
            size="sm"
            variant="ghost"
            icon={
              <>
                <ArrowLeft className="size-4 md:hidden" />
                <X className="size-4 max-md:hidden" />
              </>
            }
            aria-label="Close"
            onClick={onClose}
          />
        </div>
      </div>
      {refused && (
        <div role="alert" className="flex shrink-0 flex-wrap items-center justify-between gap-2 border-b border-line bg-warn-wash px-4 py-2 text-xs text-warn">
          <span>{refused.message}</span>
          <div className="flex items-center gap-1.5">
            {refused.code === "changed" && (
              <Button size="sm" loading={reload.isPending} onClick={() => reload.mutate(undefined)}>
                Reload
              </Button>
            )}
            <Button size="sm" loading={save.isPending} onClick={() => save.mutate(true)}>
              Save anyway
            </Button>
          </div>
        </div>
      )}
      {managed.length > 0 && (
        <p className="shrink-0 border-b border-line bg-warn-wash px-4 py-2 text-xs leading-relaxed text-warn">
          <span className="font-mono">{managed.join(", ")}</span> {managed.length === 1 ? "is" : "are"} set under Settings and rewritten from there every time the server starts. Change {managed.length === 1 ? "it" : "them"} there, or clear {managed.length === 1 ? "it" : "them"} there to manage {managed.length === 1 ? "it" : "them"} in this file.
        </p>
      )}
      {!opened && file.isPending ? (
        <div className="flex flex-1 items-center justify-center">
          <Spinner />
        </div>
      ) : !opened ? (
        <p className="p-4 text-[13px] text-danger">{file.error?.message}</p>
      ) : (
        <Editor key={loads} name={name} initial={loads === 0 && draft !== null ? draft : opened.text} onChange={setDraft} onSave={() => save.mutate(false)} />
      )}
    </div>
  );
}

function NameDialog({
  title,
  action,
  initial,
  label = "Name",
  pattern = "[^/]+",
  onSubmit,
  onClose,
}: {
  title: string;
  action: string;
  initial: string;
  label?: string;
  pattern?: string;
  onSubmit: (name: string) => Promise<unknown>;
  onClose: () => void;
}) {
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
        <Field label={label}>{(id) => <Input id={id} autoFocus required value={name} onChange={(e) => setName(e.target.value)} pattern={pattern} className="font-mono" />}</Field>
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

type Dialog =
  | { kind: "folder"; in: string }
  | { kind: "file"; in: string }
  | { kind: "rename"; path: string; dir: boolean }
  | { kind: "move"; path: string; dir: boolean }
  | { kind: "delete"; path: string; dir: boolean };

/** One line of the tree: a file or folder, or what a folder says while it has none to show. */
type Row =
  | { kind: "entry"; path: string; entry: FileEntry; depth: number }
  | { kind: "note"; dir: string; depth: number; text: string; error?: boolean }
  | { kind: "more"; dir: string; depth: number; left: number };

// A folder shows this many at first, and as many more each time it is asked.
const pageSize = 300;

function stored<T>(key: string, fallback: T): T {
  try {
    const v = localStorage.getItem(key);
    return v ? (JSON.parse(v) as T) : fallback;
  } catch {
    return fallback;
  }
}

function store(key: string, value: unknown) {
  try {
    localStorage.setItem(key, JSON.stringify(value));
  } catch {
    // Not kept: the tree opens as it is.
  }
}

// What the panel can open in place, as the workspace's bsdtar reads.
const archive = /\.(zip|tar|tar\.gz|tgz|tar\.xz|txz|tar\.bz2|tbz2|7z|rar)$/i;

const isEditable = (entry: FileEntry) => !entry.dir && entry.size <= maxEditable && !binary.test(entry.name);

type Found = FileEntry & { path: string };

/** What a search found: a file's name, and the folder it is in. */
function FoundList({ found, onPick }: { found: UseQueryResult<{ results: Found[]; more: boolean }>; onPick: (f: Found) => void }) {
  if (found.isPending) {
    return (
      <div className="flex justify-center p-4">
        <Spinner />
      </div>
    );
  }
  if (found.isError) return <p className="p-3 text-xs text-danger">{found.error.message}</p>;
  const { results, more } = found.data;
  if (results.length === 0) return <p className="p-3 text-xs text-muted">Nothing named like that.</p>;
  return (
    <>
      <ul aria-label="Found" className="min-w-max">
        {results.map((f) => (
          <li key={f.path}>
            <button type="button" onClick={() => onPick(f)} className="flex h-7 w-full items-center gap-1.5 px-2 text-left outline-none hover:bg-raised focus-visible:bg-raised">
              {f.dir ? <Folder className="size-4 shrink-0 text-accent" /> : <File className="size-4 shrink-0 text-muted" />}
              <span className="font-mono text-[13px]">{nameOf(f.path)}</span>
              <span className="truncate font-mono text-xs text-faint">{parentOf(f.path) === "/" ? "" : parentOf(f.path).slice(1)}</span>
            </button>
          </li>
        ))}
      </ul>
      {more && <p className="px-2 py-2 text-xs text-faint">Showing the first {results.length}. Type more of the name to find fewer.</p>}
    </>
  );
}

/**
 * A server's files, as a tree of folders opened in place, with the file
 * being edited beside it. On a phone the file being edited takes the screen.
 */
export default function Files() {
  const { server } = useServerContext();
  const params = useParams();
  const [search, setSearch] = useSearchParams();
  const qc = useQueryClient();
  const meta = useMeta().data;
  const base = `${serverPath(server.org, server.name)}/files`;
  const open = search.get("file");
  // A folder linked to, as files/plugins: opened, and where new things go.
  const linked = `/${(params["*"] ?? "").replace(/\/+$/, "")}`;
  const memory = `avsys.files.${server.org}/${server.name}`;

  // Which folders are open, kept for the next visit.
  const [expanded, setExpanded] = useState<Set<string>>(() => {
    const at = new Set(stored<string[]>(`${memory}.open`, []));
    for (const dir of [...foldersAbove(linked), ...(linked === "/" ? [] : [linked]), ...(open ? foldersAbove(open) : [])]) at.add(dir);
    return at;
  });
  useEffect(() => store(`${memory}.open`, [...expanded]), [expanded, memory]);
  const [width, setWidth] = useState(() => stored(`avsys.files.width`, 280));
  useEffect(() => store("avsys.files.width", width), [width]);

  // What was pressed last: new files and uploads go in it, or in its folder.
  const [selected, setSelected] = useState<{ path: string; dir: boolean }>(() => (open ? { path: open, dir: false } : { path: linked, dir: true }));
  const here = selected.dir ? selected.path : parentOf(selected.path);
  const [focused, setFocused] = useState<string>(selected.path);
  const [shown, setShown] = useState<Record<string, number>>({});

  // Finding a file by name, anywhere in the server's files: asked as typing
  // stops.
  const [finding, setFinding] = useState(false);
  const [query, setQuery] = useState("");
  const [asked, setAsked] = useState("");
  useEffect(() => {
    const t = setTimeout(() => setAsked(query.trim()), 250);
    return () => clearTimeout(t);
  }, [query]);
  const found = useQuery({
    queryKey: ["file-search", base, asked],
    queryFn: () => api.get<{ results: (FileEntry & { path: string })[]; more: boolean }>(`${base}/search?${qs({ q: asked })}`),
    enabled: finding && asked !== "",
    staleTime: 10_000,
  });
  const stopFinding = () => {
    setFinding(false);
    setQuery("");
    setAsked("");
  };

  // Each open folder whose folders are open too, read as it is opened.
  const visible = ["/", ...[...expanded].filter((d) => d !== "/" && foldersAbove(d).every((a) => expanded.has(a))).sort()];
  const lists = useQueries({
    queries: visible.map((dir) => ({
      queryKey: ["files", base, dir],
      queryFn: () => api.get<{ entries: FileEntry[] }>(`${base}?${qs({ path: dir })}`),
      staleTime: 0,
    })),
  });
  const byDir = new Map(visible.map((d, i) => [d, lists[i]]));
  const rows: Row[] = [];
  const walk = (dir: string, depth: number) => {
    const list = byDir.get(dir);
    if (!list || list.isPending) {
      rows.push({ kind: "note", dir, depth, text: "Loading…" });
      return;
    }
    if (list.isError) {
      rows.push({ kind: "note", dir, depth, text: list.error.message, error: true });
      return;
    }
    const entries = list.data.entries;
    if (entries.length === 0) rows.push({ kind: "note", dir, depth, text: dir === "/" ? "No files yet. Drop files here to upload them." : "Empty" });
    const limit = shown[dir] ?? pageSize;
    for (const entry of entries.slice(0, limit)) {
      const path = join(dir, entry.name);
      rows.push({ kind: "entry", path, entry, depth });
      if (entry.dir && expanded.has(path)) walk(path, depth + 1);
    }
    if (entries.length > limit) rows.push({ kind: "more", dir, depth, left: entries.length - limit });
  };
  walk("/", 0);
  const refresh = (dir: string) => qc.invalidateQueries({ queryKey: ["files", base, dir] });

  // The file being edited, and whether it has changes not saved: leaving it
  // then is asked first.
  const [dirty, setDirty] = useState(false);
  const [leaving, setLeaving] = useState<(() => void) | null>(null);
  const guard = (go: () => void) => (dirty ? setLeaving(() => go) : go());
  // What the editor has, so that when the file is renamed, or a folder it is
  // in, the editor at its new path starts with it: what was typed is not lost.
  const editorState = useRef<EditorState>({ basis: null, draft: null });
  const onState = useCallback((state: EditorState) => {
    editorState.current = state;
  }, []);
  const [carried, setCarried] = useState<(EditorState & { path: string }) | null>(null);
  // Only for the file it was carried to: another opened after it starts anew.
  // Looked at as the file open changes, not as it is carried, which is
  // before the file open is the one it was carried to.
  // biome-ignore lint/correctness/useExhaustiveDependencies: only when the file open changes
  useEffect(() => {
    if (carried && carried.path !== open) setCarried(null);
  }, [open]);
  const openFile = (path: string | null) => {
    setDirty(false);
    setSearch(path ? { file: path } : {}, { replace: !!open && !!path });
  };

  const toggle = (dir: string, to?: boolean) =>
    setExpanded((was) => {
      const next = new Set(was);
      if (to ?? !next.has(dir)) next.add(dir);
      else next.delete(dir);
      return next;
    });
  const press = (row: Extract<Row, { kind: "entry" }>) => {
    setSelected({ path: row.path, dir: row.entry.dir });
    setFocused(row.path);
    if (row.entry.dir) toggle(row.path);
    else if (row.path !== open) guard(() => openFile(isEditable(row.entry) ? row.path : null));
  };

  const [dialog, setDialog] = useState<Dialog | null>(null);
  // The row whose menu is open, by its ⋯ or a right click.
  const [menuFor, setMenuFor] = useState<string | null>(null);
  const { sending, send, cancel } = useUpload();
  const [dropOn, setDropOn] = useState<string | null>(null);
  const picker = useRef<HTMLInputElement>(null);
  // After a file or folder is renamed or moved: what was open in it, chosen
  // or being edited is so at its new place, and what was typed is kept.
  const afterMove = (from: string, to: string, dir: boolean) => {
    const moved = (p: string) => (within(p, from) ? to + p.slice(from.length) : p);
    if (dir) setExpanded((was) => new Set([...was].map(moved)));
    setSelected((s) => ({ ...s, path: moved(s.path) }));
    setFocused((f) => moved(f));
    if (open && within(open, from)) {
      setCarried({ path: moved(open), ...editorState.current });
      setSearch({ file: moved(open) }, { replace: true });
    }
  };
  const moveTo = async (from: string, into: string, dir: boolean) => {
    const to = join(into, nameOf(from));
    if (to === from) return;
    await api.post(`${base}/rename?${qs({ path: from, to })}`);
    await Promise.all([refresh(parentOf(from)), refresh(into)]);
    if (into !== "/") toggle(into, true);
    afterMove(from, to, dir);
  };
  const move = useAction(({ from, into, dir }: { from: string; into: string; dir: boolean }) => moveTo(from, into, dir));
  // What is being dragged within the tree, to be moved: never into itself,
  // nor where it is.
  const [moving, setMoving] = useState<{ path: string; dir: boolean } | null>(null);
  const canDropInto = (dir: string) => !moving || (!within(dir, moving.path) && parentOf(moving.path) !== dir);

  const remove = useAction(async ({ path, dir }: { path: string; dir: boolean }) => {
    await api.del(`${base}?${qs({ path })}`);
    await refresh(parentOf(path));
    if (dir) setExpanded((was) => new Set([...was].filter((d) => !within(d, path))));
    if (open && within(open, path)) openFile(null);
    if (within(selected.path, path)) setSelected({ path: parentOf(path), dir: true });
  });

  // An archive opened where it is, into the folder it makes: that is opened
  // and chosen.
  const extract = useAction(async (path: string) => {
    const made = await api.post<{ path: string }>(`${base}/extract?${qs({ path })}`);
    await refresh(parentOf(path));
    if (parentOf(made.path) !== "/") toggle(parentOf(made.path), true);
    toggle(made.path, true);
    setSelected({ path: made.path, dir: true });
    setFocused(made.path);
    toast.info(`Extracted ${nameOf(path)} to ${nameOf(made.path)}`);
  });

  const upload = async (files: FileList | File[], dir: string) => {
    const all = Array.from(files);
    if (dir !== "/") toggle(dir, true);
    try {
      for (const [i, file] of all.entries()) {
        await send(all.length > 1 ? `${file.name} (${i + 1} of ${all.length})` : file.name, `${base}/content?${qs({ path: join(dir, file.name) })}`, file);
      }
    } catch (err) {
      if (!cancelled(err)) toast.error(err instanceof Error ? err.message : "Upload failed.");
    } finally {
      refresh(dir);
    }
  };

  // Moving about the tree from the keyboard, as in a file manager.
  const tree = useRef<HTMLUListElement>(null);
  const focusable = rows.filter((r) => r.kind !== "note");
  const keyOf = (r: Row) => (r.kind === "entry" ? r.path : `more:${r.dir}`);
  const focusRow = (key: string) => {
    setFocused(key);
    tree.current?.querySelector<HTMLElement>(`[data-key="${CSS.escape(key)}"]`)?.focus();
  };
  const onKeyDown = (e: React.KeyboardEvent) => {
    const at = focusable.findIndex((r) => keyOf(r) === focused);
    const row = focusable[at];
    const go = (i: number) => focusable[i] && focusRow(keyOf(focusable[i]));
    if (e.key === "ArrowDown") go(at + 1);
    else if (e.key === "ArrowUp") go(Math.max(at - 1, 0));
    else if (e.key === "Home") go(0);
    else if (e.key === "End") go(focusable.length - 1);
    else if (e.key === "ArrowRight" && row?.kind === "entry" && row.entry.dir) {
      // Into it once what is in it is known, not past it while it is read.
      const next = rows[rows.indexOf(row) + 1];
      if (!expanded.has(row.path)) toggle(row.path, true);
      else if (!(next?.kind === "note" && next.dir === row.path)) go(at + 1);
    } else if (e.key === "ArrowLeft" && row?.kind === "entry") {
      if (row.entry.dir && expanded.has(row.path)) toggle(row.path, false);
      else if (parentOf(row.path) !== "/") focusRow(parentOf(row.path));
    } else if ((e.key === "Delete" || e.key === "Backspace") && row?.kind === "entry") {
      setDialog({ kind: "delete", path: row.path, dir: row.entry.dir });
    } else if (e.key === "F2" && row?.kind === "entry") {
      setDialog({ kind: "rename", path: row.path, dir: row.entry.dir });
    } else if ((e.key === "Enter" || e.key === " ") && row) {
      if (row.kind === "entry") press(row);
      else setShown((s) => ({ ...s, [row.dir]: (s[row.dir] ?? pageSize) + pageSize }));
    } else return;
    e.preventDefault();
  };

  // The divider between the tree and the file, dragged or moved with the arrow keys.
  const resizing = useRef<{ x: number; width: number } | null>(null);
  const clampWidth = (w: number) => Math.min(Math.max(w, 180), 560);

  const root = byDir.get("/");
  // A file chosen that cannot be edited: said so, with a way to download it.
  const openEntry = open || selected.dir ? null : rows.find((r): r is Extract<Row, { kind: "entry" }> => r.kind === "entry" && r.path === selected.path && !isEditable(r.entry));
  const managed = open === "/server.properties" ? Object.keys(server.spec.properties ?? {}).sort() : [];
  const onDirty = useCallback((d: boolean) => setDirty(d), []);

  return (
    <div className="flex h-[calc(100dvh-330px)] min-h-80 overflow-hidden rounded-lg border border-line bg-surface">
      <div className={cx("flex min-w-0 shrink-0 flex-col max-md:w-full! md:w-(--tree)", (open || openEntry) && "max-md:hidden")} style={{ "--tree": `${width}px` } as React.CSSProperties}>
        <div className="flex h-10 shrink-0 items-center gap-1 border-b border-line pr-1.5 pl-3">
          {/* Where new files and uploads go; "data" chooses the top folder. */}
          <span className="min-w-0 flex-1 truncate font-mono text-xs text-muted" title="New files and uploads go here">
            <button
              type="button"
              className={cx("rounded-sm hover:text-accent", here === "/" && "text-fg")}
              aria-label="Choose the top folder"
              onClick={() => setSelected({ path: "/", dir: true })}
            >
              data
            </button>
            {here !== "/" && here}
          </span>
          <Button size="sm" variant="ghost" icon={<FilePlus className="size-4" />} aria-label="New file" title="New file" onClick={() => setDialog({ kind: "file", in: here })} />
          <Button size="sm" variant="ghost" icon={<FolderPlus className="size-4" />} aria-label="New folder" title="New folder" onClick={() => setDialog({ kind: "folder", in: here })} />
          <Button size="sm" variant="ghost" icon={<Upload className="size-4" />} aria-label="Upload" title="Upload" disabled={!!sending} onClick={() => picker.current?.click()} />
          <Button size="sm" variant="ghost" icon={<Search className="size-4" />} aria-label="Find a file" title="Find a file" aria-pressed={finding} onClick={() => (finding ? stopFinding() : setFinding(true))} />
          {/* All of them in VS Code, over SSH with a key from the account. */}
          {meta && (
            <a
              href={vscodeURL(server, meta)}
              aria-label="Open in VS Code"
              title="Open in VS Code"
              className="inline-flex size-7 items-center justify-center rounded-md text-muted hover:bg-raised hover:text-fg"
            >
              <Code2 className="size-4" />
            </a>
          )}
          <input
            ref={picker}
            type="file"
            multiple
            hidden
            aria-label="Files to upload"
            onChange={(e) => e.target.files?.length && upload(e.target.files, here).then(() => (e.target.value = ""))}
          />
        </div>
        {finding && (
          <div className="shrink-0 border-b border-line p-1.5">
            <Input
              autoFocus
              aria-label="Find a file by name"
              placeholder="Find a file by name"
              value={query}
              onChange={(e) => setQuery(e.target.value)}
              onKeyDown={(e) => e.key === "Escape" && stopFinding()}
              className="h-8 font-mono text-xs"
            />
          </div>
        )}
        {sending && (
          <div className="shrink-0 border-b border-line px-3 py-2">
            <UploadProgress sending={sending} onCancel={cancel} />
          </div>
        )}
        <div
          className={cx("min-h-0 flex-1 overflow-auto py-1", dropOn === "/" && "bg-accent-wash")}
          onDragOver={(e) => {
            if (!canDropInto("/")) return;
            e.preventDefault();
            setDropOn((d) => d ?? "/");
          }}
          onDragLeave={(e) => {
            if (!e.currentTarget.contains(e.relatedTarget as Node)) setDropOn(null);
          }}
          onDrop={(e) => {
            e.preventDefault();
            const into = dropOn ?? "/";
            setDropOn(null);
            // From the tree, moved; from the computer, uploaded.
            if (moving) {
              setMoving(null);
              if (canDropInto(into)) move.mutate({ from: moving.path, into, dir: moving.dir });
            } else if (e.dataTransfer.files.length) upload(e.dataTransfer.files, into);
          }}
        >
          {finding && asked !== "" ? (
            <FoundList
              found={found}
              onPick={(f) => {
                stopFinding();
                setExpanded((was) => new Set([...was, ...foldersAbove(f.path), ...(f.dir ? [f.path] : [])]));
                setSelected({ path: f.path, dir: f.dir });
                setFocused(f.path);
                if (!f.dir && f.path !== open) guard(() => openFile(isEditable(f) ? f.path : null));
              }}
            />
          ) : root?.isError ? (
            <div className="p-4">
              <Empty title="Could not open the files">{root.error.message}</Empty>
            </div>
          ) : (
            <ul ref={tree} role="tree" aria-label="Files" onKeyDown={onKeyDown} className="min-w-max">
              {rows.map((row) => {
                const indent = { paddingLeft: 8 + row.depth * 14 };
                if (row.kind === "note") {
                  return (
                    <li key={`note:${row.dir}`} role="none" style={{ paddingLeft: 30 + row.depth * 14 }} className={cx("flex h-7 items-center pr-3 text-xs", row.error ? "text-danger" : "text-faint")}>
                      {row.text === "Loading…" ? <Spinner className="size-3.5" /> : row.text}
                    </li>
                  );
                }
                if (row.kind === "more") {
                  return (
                    <li
                      key={`more:${row.dir}`}
                      role="treeitem"
                      aria-level={row.depth + 1}
                      aria-selected={false}
                      data-key={`more:${row.dir}`}
                      tabIndex={focused === `more:${row.dir}` ? 0 : -1}
                      style={{ paddingLeft: 30 + row.depth * 14 }}
                      className="flex h-7 cursor-pointer items-center pr-3 text-xs text-accent hover:underline"
                      onClick={() => setShown((s) => ({ ...s, [row.dir]: (s[row.dir] ?? pageSize) + pageSize }))}
                    >
                      Show {Math.min(row.left, pageSize)} more of {row.left}
                    </li>
                  );
                }
                const { entry, path } = row;
                const isOpen = entry.dir && expanded.has(path);
                const current = path === (open ?? selected.path);
                return (
                  <li
                    key={path}
                    role="treeitem"
                    aria-level={row.depth + 1}
                    aria-expanded={entry.dir ? isOpen : undefined}
                    aria-selected={current}
                    aria-label={entry.name}
                    data-key={path}
                    tabIndex={focused === path || (!focusable.some((r) => keyOf(r) === focused) && row === focusable[0]) ? 0 : -1}
                    onFocus={() => setFocused(path)}
                    onClick={() => press(row)}
                    // Its menu, as a right click opens in a file manager.
                    onContextMenu={(e) => {
                      e.preventDefault();
                      setSelected({ path, dir: entry.dir });
                      setFocused(path);
                      setMenuFor(path);
                    }}
                    draggable
                    onDragStart={(e) => {
                      e.dataTransfer.setData("application/x-avsys-path", path);
                      e.dataTransfer.effectAllowed = "move";
                      setMoving({ path, dir: entry.dir });
                    }}
                    onDragEnd={() => {
                      setMoving(null);
                      setDropOn(null);
                    }}
                    // Into a folder, or the folder of a file.
                    onDragOver={(e) => {
                      const into = entry.dir ? path : parentOf(path);
                      e.stopPropagation();
                      if (!canDropInto(into)) return;
                      e.preventDefault();
                      setDropOn(into);
                    }}
                    onDragLeave={() => setDropOn((d) => (d === path ? null : d))}
                    style={indent}
                    title={entry.dir ? undefined : `${bytes(entry.size)} · changed ${ago(entry.modTime)}`}
                    className={cx(
                      "group flex h-7 cursor-pointer items-center gap-1.5 pr-1 outline-none select-none hover:bg-raised focus-visible:ring-1 focus-visible:ring-accent focus-visible:ring-inset",
                      current && "bg-accent-wash hover:bg-accent-wash",
                      dropOn === path && "bg-accent-wash ring-1 ring-accent ring-inset",
                    )}
                  >
                    <ChevronRight className={cx("size-3.5 shrink-0 text-faint transition-transform", isOpen && "rotate-90", !entry.dir && "invisible")} />
                    {entry.dir ? (
                      isOpen ? <FolderOpen className="size-4 shrink-0 text-accent" /> : <Folder className="size-4 shrink-0 text-accent" />
                    ) : (
                      <File className={cx("size-4 shrink-0", isEditable(entry) ? "text-muted" : "text-faint")} />
                    )}
                    <span className={cx("min-w-0 flex-1 truncate pr-2 font-mono text-[13px]", !entry.dir && !isEditable(entry) && "text-muted")}>{entry.name}</span>
                    {extract.isPending && extract.variables === path && (
                      <span role="status" aria-label={`Extracting ${entry.name}`}>
                        <Spinner className="size-3.5" />
                      </span>
                    )}
                    {/* Nothing done in its menu, which is drawn elsewhere, reaches the row. */}
                    <span className="contents" onClick={(e) => e.stopPropagation()} onKeyDown={(e) => e.stopPropagation()}>
                    <Menu open={menuFor === path} onOpenChange={(o) => setMenuFor(o ? path : null)}>
                      <MenuTrigger asChild>
                        <Button
                          size="sm"
                          variant="ghost"
                          icon={<MoreHorizontal className="size-4" />}
                          aria-label={`Actions for ${entry.name}`}
                          tabIndex={-1}
                          className={cx("h-6 group-hover:opacity-100 group-focus-visible:opacity-100 data-[state=open]:opacity-100 pointer-coarse:opacity-100", !current && "opacity-0")}
                          onClick={(e) => e.stopPropagation()}
                        />
                      </MenuTrigger>
                      <MenuContent>
                        {entry.dir ? (
                          <MenuItem icon={<Download />} onSelect={() => window.location.assign(`/api/v1${base}/archive?${qs({ path })}`)}>
                            Download as .zip
                          </MenuItem>
                        ) : (
                          <MenuItem icon={<Download />} onSelect={() => window.location.assign(`/api/v1${base}/content?${qs({ path })}&download`)}>
                            Download
                          </MenuItem>
                        )}
                        {!entry.dir && archive.test(entry.name) && (
                          <MenuItem icon={<PackageOpen />} disabled={extract.isPending} onSelect={() => extract.mutate(path)}>
                            Extract here
                          </MenuItem>
                        )}
                        <MenuItem icon={<Pencil />} onSelect={() => setDialog({ kind: "rename", path, dir: entry.dir })}>
                          Rename
                        </MenuItem>
                        <MenuItem icon={<FolderInput />} onSelect={() => setDialog({ kind: "move", path, dir: entry.dir })}>
                          Move to…
                        </MenuItem>
                        <MenuItem icon={<Trash2 />} danger onSelect={() => setDialog({ kind: "delete", path, dir: entry.dir })}>
                          Delete
                        </MenuItem>
                      </MenuContent>
                    </Menu>
                    </span>
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      </div>

      <div
        role="separator"
        aria-orientation="vertical"
        aria-label="Width of the file list"
        aria-valuenow={width}
        aria-valuemin={180}
        aria-valuemax={560}
        tabIndex={0}
        className="w-1 shrink-0 cursor-col-resize touch-none border-l border-line outline-none hover:bg-accent/40 focus-visible:bg-accent/40 max-md:hidden"
        onPointerDown={(e) => {
          e.preventDefault();
          e.currentTarget.setPointerCapture(e.pointerId);
          resizing.current = { x: e.clientX, width };
        }}
        onPointerMove={(e) => resizing.current && setWidth(clampWidth(resizing.current.width + e.clientX - resizing.current.x))}
        onPointerUp={() => (resizing.current = null)}
        onLostPointerCapture={() => (resizing.current = null)}
        onKeyDown={(e) => {
          if (e.key === "ArrowLeft") setWidth((w) => clampWidth(w - 20));
          else if (e.key === "ArrowRight") setWidth((w) => clampWidth(w + 20));
        }}
      />

      <div className={cx("flex min-w-0 flex-1 flex-col", !open && !openEntry && "max-md:hidden")}>
        {open ? (
          <FileEditor
            key={open}
            base={base}
            path={open}
            managed={managed}
            carry={carried?.path === open ? carried : undefined}
            onDirty={onDirty}
            onState={onState}
            onClose={() => guard(() => openFile(null))}
          />
        ) : openEntry ? (
          <div className="relative flex flex-1 flex-col items-center justify-center gap-3 p-6 text-center">
            {/* On a phone, where it takes the screen. */}
            <Button
              size="sm"
              variant="ghost"
              icon={<ArrowLeft className="size-4" />}
              aria-label="Back to the files"
              className="absolute top-2 left-2 md:hidden"
              onClick={() => setSelected({ path: parentOf(openEntry.path), dir: true })}
            />
            <File className="size-8 text-faint" />
            <div>
              <p className="font-mono text-[13px]">{openEntry.entry.name}</p>
              <p className="mt-1 text-xs text-muted">
                {bytes(openEntry.entry.size)} · changed {ago(openEntry.entry.modTime)}
              </p>
            </div>
            <p className="text-xs text-muted">{openEntry.entry.size > maxEditable ? "Files over 2 MB can't be edited here." : "This kind of file can't be edited here."}</p>
            <Button size="sm" icon={<Download className="size-3.5" />} onClick={() => window.location.assign(`/api/v1${base}/content?${qs({ path: openEntry.path })}&download`)}>
              Download
            </Button>
          </div>
        ) : (
          <p className="flex flex-1 items-center justify-center p-6 text-[13px] text-muted">Choose a file to edit.</p>
        )}
      </div>

      {dialog?.kind === "folder" && (
        <NameDialog
          title="New folder"
          action="Create"
          initial=""
          onClose={() => setDialog(null)}
          onSubmit={async (name) => {
            const path = join(dialog.in, name);
            await api.post(`${base}/mkdir?${qs({ path })}`);
            if (dialog.in !== "/") toggle(dialog.in, true);
            await refresh(dialog.in);
            setSelected({ path, dir: true });
            setFocused(path);
          }}
        />
      )}
      {dialog?.kind === "file" && (
        <NameDialog
          title="New file"
          action="Create"
          initial=""
          onClose={() => setDialog(null)}
          onSubmit={async (name) => {
            const path = join(dialog.in, name);
            await api.putRaw(`${base}/content?${qs({ path })}`, "");
            if (dialog.in !== "/") toggle(dialog.in, true);
            await refresh(dialog.in);
            setSelected({ path, dir: false });
            setFocused(path);
            guard(() => openFile(path));
          }}
        />
      )}
      {dialog?.kind === "rename" && (
        <NameDialog
          title={`Rename ${nameOf(dialog.path)}`}
          action="Rename"
          initial={nameOf(dialog.path)}
          onClose={() => setDialog(null)}
          onSubmit={async (name) => {
            const from = dialog.path;
            const to = join(parentOf(from), name);
            await api.post(`${base}/rename?${qs({ path: from, to })}`);
            await refresh(parentOf(from));
            afterMove(from, to, dialog.dir);
          }}
        />
      )}
      {dialog?.kind === "move" && (
        <NameDialog
          title={`Move ${nameOf(dialog.path)}`}
          action="Move"
          label="To the folder"
          pattern="/.*"
          initial={parentOf(dialog.path)}
          onClose={() => setDialog(null)}
          onSubmit={(to) => moveTo(dialog.path, to.replace(/\/+$/, "") || "/", dialog.dir)}
        />
      )}
      <Confirm
        open={dialog?.kind === "delete"}
        onOpenChange={() => setDialog(null)}
        title={dialog?.kind === "delete" ? `Delete ${nameOf(dialog.path)}?` : ""}
        description={dialog?.kind === "delete" && dialog.dir ? "The folder and everything in it will be deleted. This cannot be undone." : "This cannot be undone."}
        action="Delete"
        loading={remove.isPending}
        onConfirm={() => dialog?.kind === "delete" && remove.mutate({ path: dialog.path, dir: dialog.dir }, { onSuccess: () => setDialog(null) })}
      />
      <Confirm
        open={!!leaving}
        onOpenChange={() => setLeaving(null)}
        title={open ? `Discard your changes to ${nameOf(open)}?` : "Discard your changes?"}
        description="They have not been saved."
        action="Discard"
        onConfirm={() => {
          const go = leaving;
          setLeaving(null);
          go?.();
        }}
      />
    </div>
  );
}
