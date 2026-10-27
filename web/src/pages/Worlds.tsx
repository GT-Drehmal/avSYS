import { useQueryClient } from "@tanstack/react-query";
import { Download, MoreHorizontal, Pencil, Plus, Trash2, Upload } from "lucide-react";
import { useRef, useState } from "react";
import { useParams } from "react-router";
import { Picture, PictureChange } from "../components/Picture";
import { useOrg } from "../components/Shell";
import { ServerPicker, SnapshotPicker } from "../components/StartPicker";
import { Button, Confirm, cx, Empty, Field, Input, Loading, Menu, MenuContent, MenuItem, MenuTrigger, Modal, Notice, PageHeader, SearchBox, Spinner, Textarea } from "../components/ui";
import { cancelled, UploadProgress, useUpload } from "../components/Uploading";
import { api, ApiError, atLeast, qs, type LibraryWorld, worldIcon } from "../lib/api";
import { ArchiveError } from "../lib/archive";
import { ago, bytes } from "../lib/format";
import { keys, useMeta, useWorlds } from "../lib/queries";
import { matches, useSearch } from "../lib/search";
import { type FoundWorld, inspectWorld } from "../lib/serverImport";

/** Where a world came from, said plainly. */
export function worldSource(w: LibraryWorld) {
  const [kind, name] = w.source.split(/:(.*)/s);
  return kind === "server" ? `from ${name}` : kind === "snapshot" ? `from a snapshot of ${name}` : "uploaded";
}

/** What a world is, in a line: its version, size and where it came from. */
export function worldSummary(w: LibraryWorld) {
  return [w.version, w.ready ? bytes(w.size) : null, worldSource(w)].filter(Boolean).join(" · ");
}

/** An organization's library of worlds: maps kept to start servers from. */
export function Worlds() {
  const { org = "" } = useParams();
  const membership = useOrg();
  const meta = useMeta().data;
  const worlds = useWorlds(org);
  const [query, setQuery] = useSearch();
  const [adding, setAdding] = useState(false);
  const [editing, setEditing] = useState<LibraryWorld | null>(null);
  const [deleting, setDeleting] = useState<LibraryWorld | null>(null);
  const canAdd = !!membership && atLeast(membership.role, "operator");
  const canDelete = !!membership && atLeast(membership.role, "admin");

  const header = (
    <PageHeader
      title="Worlds"
      description="Maps kept to start servers from."
      actions={
        canAdd && (
          <Button variant="primary" icon={<Plus className="size-4" />} disabled={meta && !meta.snapshots} onClick={() => setAdding(true)}>
            Add world
          </Button>
        )
      }
    />
  );
  if (worlds.isPending) return <Loading />;
  if (!worlds.data) {
    return (
      <>
        {header}
        <Notice tone="danger">{worlds.error instanceof ApiError ? worlds.error.message : "Could not load worlds."}</Notice>
      </>
    );
  }
  const list = worlds.data;
  const shown = list.filter((w) => matches(query, w.name, w.description, worldSummary(w)));
  const latest = (w: LibraryWorld | null) => (w ? (list.find((x) => x.id === w.id) ?? null) : null);

  return (
    <div className="flex flex-col gap-4">
      {header}
      {meta && !meta.snapshots && <Notice>Worlds are kept with snapshots, which are not set up on this panel.</Notice>}
      {list.length === 0 ? (
        <Empty title="No worlds yet">Upload a map, or save a server's world.</Empty>
      ) : (
        <>
          <SearchBox value={query} onChange={setQuery} label="Search worlds" />
          {shown.length === 0 ? (
            <p className="rounded-lg border border-line bg-surface px-4 py-8 text-center text-[13px] text-muted">No worlds match.</p>
          ) : (
            <ul className="flex flex-col divide-y divide-line rounded-lg border border-line bg-surface" aria-label="Worlds">
              {shown.map((w) => (
                <li key={w.id} className="flex items-center gap-3 px-4 py-3" aria-label={w.name}>
                  <Picture name={w.name} url={worldIcon(org, w.id)} retryKey={w.updatedAt} size={36} />
                  <div className="min-w-0 flex-1">
                    <p className="truncate text-[13px] font-medium">{w.name}</p>
                    {w.description && <p className="line-clamp-2 text-xs text-muted">{w.description}</p>}
                    <p className={cx("mt-0.5 text-xs text-faint", !w.ready && "flex items-center gap-1.5")}>
                      {w.ready ? (
                        `${worldSummary(w)} · added ${ago(w.createdAt)}`
                      ) : w.saving ? (
                        <>
                          <Spinner className="size-3" />
                          Saving
                        </>
                      ) : (
                        "Not saved"
                      )}
                    </p>
                  </div>
                  {canAdd && (
                    <Menu>
                      <MenuTrigger asChild>
                        <Button size="sm" variant="ghost" icon={<MoreHorizontal className="size-4" />} aria-label={`Actions for ${w.name}`} />
                      </MenuTrigger>
                      <MenuContent>
                        <MenuItem icon={<Pencil />} onSelect={() => setEditing(w)}>
                          Edit
                        </MenuItem>
                        <MenuItem icon={<Download />} disabled={!w.ready} onSelect={() => window.location.assign(`/api/v1/orgs/${org}/worlds/${w.id}/archive`)}>
                          Download
                        </MenuItem>
                        {canDelete && (
                          <MenuItem icon={<Trash2 />} danger disabled={w.saving} onSelect={() => setDeleting(w)}>
                            Delete
                          </MenuItem>
                        )}
                      </MenuContent>
                    </Menu>
                  )}
                </li>
              ))}
            </ul>
          )}
        </>
      )}
      {adding && <AddWorld org={org} onClose={() => setAdding(false)} />}
      {latest(editing) && <EditWorld key={editing?.id} org={org} world={latest(editing)!} onClose={() => setEditing(null)} />}
      <DeleteWorld org={org} world={deleting} onClose={() => setDeleting(null)} />
    </div>
  );
}

type From = "upload" | "server" | "snapshot";

/** Puts a world in the library: uploaded, or saved from a server or a snapshot. */
function AddWorld({ org, onClose }: { org: string; onClose: () => void }) {
  const qc = useQueryClient();
  const [from, setFrom] = useState<From>("upload");
  const [file, setFile] = useState<File | null>(null);
  const [found, setFound] = useState<FoundWorld | null>(null);
  const [reading, setReading] = useState(false);
  const [picked, setPicked] = useState<{ server?: string; snapshot?: string; label: string } | null>(null);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [picture, setPicture] = useState<PictureChange>({ kind: "none" });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const { sending, send, cancel } = useUpload();
  const input = useRef<HTMLInputElement>(null);
  const reads = useRef(0);

  const choose = (next: From) => {
    setFrom(next);
    setError(null);
    setPicked(null);
    setFile(null);
    setFound(null);
  };
  const pickFile = async (chosen: File) => {
    setError(null);
    setFile(chosen);
    setFound(null);
    setReading(true);
    const read = ++reads.current;
    try {
      const world = await inspectWorld(chosen);
      if (read !== reads.current) return;
      setFound(world);
      setName((was) => was || world.name);
      if (world.icon) setPicture({ kind: "file", file: new File([world.icon.slice()], "icon.png", { type: "image/png" }) });
    } catch (err) {
      if (read === reads.current) setError(err instanceof ArchiveError ? err.message : "Could not read this archive.");
    } finally {
      if (read === reads.current) setReading(false);
    }
  };

  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      let made: LibraryWorld;
      if (from === "upload") {
        if (!file || !found) return setError("Choose a world archive.");
        made = await send<LibraryWorld>(file.name, `/orgs/${org}/worlds/upload?${qs({ name, description, root: found.root, version: found.version, file: file.name })}`, file, "POST");
      } else {
        if (!picked) return setError(from === "server" ? "Pick a server." : "Pick a snapshot.");
        made = await api.post<LibraryWorld>(`/orgs/${org}/worlds`, { name, description, ...(picked.server ? { server: picked.server } : { snapshot: picked.snapshot }) });
      }
      if (picture.kind === "file") await api.putRaw(`/orgs/${org}/worlds/${made.id}/icon`, picture.file).catch(() => undefined);
      await qc.invalidateQueries({ queryKey: keys.worlds(org) });
      onClose();
    } catch (err) {
      setError(cancelled(err) ? "The upload was stopped. Nothing was added." : err instanceof ApiError ? err.message : "Could not add the world.");
    } finally {
      setBusy(false);
    }
  };

  const segment = (on: boolean) => cx("h-7 rounded-sm px-3 text-xs font-medium", on ? "bg-hover text-fg" : "text-muted hover:text-fg");
  const ready = from === "upload" ? !!found : !!picked;
  return (
    <Modal open onOpenChange={(o) => !o && !busy && onClose()} title="Add a world" size="lg">
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <div role="group" aria-label="From" className="flex w-fit flex-wrap gap-1 rounded-md border border-line bg-bg p-0.5">
          {(
            [
              ["upload", "Upload"],
              ["server", "Server"],
              ["snapshot", "Snapshot"],
            ] as const
          ).map(([f, label]) => (
            <button key={f} type="button" aria-pressed={from === f} className={segment(from === f)} disabled={busy} onClick={() => choose(f)}>
              {label}
            </button>
          ))}
        </div>
        {from === "upload" ? (
          <>
            <input
              ref={input}
              type="file"
              accept=".zip,.tar,.tgz,.gz"
              className="hidden"
              aria-label="World archive"
              onChange={(e) => {
                const chosen = e.target.files?.[0];
                e.target.value = "";
                if (chosen) void pickFile(chosen);
              }}
            />
            {file ? (
              <div className="flex items-center gap-3 rounded-md border border-accent bg-accent-wash px-3 py-2.5">
                <div className="min-w-0 flex-1">
                  <p className="truncate text-[13px] font-medium">{file.name}</p>
                  <p className="truncate text-xs text-muted" data-testid="world-summary">
                    {found ? [`World "${found.name}"`, found.version && `last opened in ${found.version}`, bytes(found.size)].filter(Boolean).join(" · ") : reading ? "Reading…" : ""}
                  </p>
                </div>
                <Button size="sm" disabled={busy || reading} onClick={() => input.current?.click()}>
                  Change
                </Button>
              </div>
            ) : (
              <div className="flex flex-col items-start gap-2 rounded-md border border-dashed border-line-strong px-4 py-4">
                <Button icon={<Upload className="size-4" />} loading={reading} onClick={() => input.current?.click()}>
                  Choose a world
                </Button>
                <p className="text-xs leading-relaxed text-muted">A .zip or .tar.gz of a world or a save, up to 2 GB.</p>
              </div>
            )}
          </>
        ) : picked ? (
          <div className="flex items-center gap-3 rounded-md border border-accent bg-accent-wash px-3 py-2.5">
            <p className="min-w-0 flex-1 truncate text-[13px] font-medium">{picked.label}</p>
            <Button size="sm" disabled={busy} onClick={() => setPicked(null)}>
              Change
            </Button>
          </div>
        ) : from === "server" ? (
          <ServerPicker
            org={org}
            onPick={(s) => {
              setPicked({ server: s.name, label: s.spec.displayName || s.name });
              setName((was) => was || s.spec.displayName || s.name);
            }}
          />
        ) : (
          <SnapshotPicker
            org={org}
            onPick={(s) => {
              setPicked({ snapshot: s.id, label: `${s.server} · ${new Date(s.time).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}` });
              setName((was) => was || s.server);
            }}
          />
        )}
        {from === "server" && picked && <p className="text-xs text-muted">A snapshot of it is taken first, and the world saved from that.</p>}
        <Field label="Name">{(id) => <Input id={id} required maxLength={60} value={name} onChange={(e) => setName(e.target.value)} />}</Field>
        <Field label="Description">{(id) => <Textarea id={id} rows={2} maxLength={500} value={description} onChange={(e) => setDescription(e.target.value)} />}</Field>
        {from === "upload" && <PictureChange name={name} value={picture} onChange={setPicture} optional="Optional. Shown in the library." />}
        {error && <Notice tone="danger">{error}</Notice>}
        {sending && <UploadProgress sending={sending} onCancel={cancel} />}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" loading={busy} disabled={!ready || !name.trim()}>
            Add world
          </Button>
        </div>
      </form>
    </Modal>
  );
}

/** A world's name, description and picture. */
function EditWorld({ org, world, onClose }: { org: string; world: LibraryWorld; onClose: () => void }) {
  const qc = useQueryClient();
  const [name, setName] = useState(world.name);
  const [description, setDescription] = useState(world.description);
  const [picture, setPicture] = useState<PictureChange>({ kind: "keep" });
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      await api.patch(`/orgs/${org}/worlds/${world.id}`, { name, description });
      if (picture.kind === "file") await api.putRaw(`/orgs/${org}/worlds/${world.id}/icon`, picture.file);
      else if (picture.kind === "none") await api.del(`/orgs/${org}/worlds/${world.id}/icon`);
      await qc.invalidateQueries({ queryKey: keys.worlds(org) });
      onClose();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not save the world.");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open onOpenChange={(o) => !o && !busy && onClose()} title={`Edit ${world.name}`}>
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <PictureChange name={name} current={worldIcon(org, world.id)} value={picture} onChange={setPicture} optional="Optional. Shown in the library." kept="" />
        <Field label="Name">{(id) => <Input id={id} required maxLength={60} value={name} onChange={(e) => setName(e.target.value)} />}</Field>
        <Field label="Description">{(id) => <Textarea id={id} rows={2} maxLength={500} value={description} onChange={(e) => setDescription(e.target.value)} />}</Field>
        {error && <Notice tone="danger">{error}</Notice>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" loading={busy} disabled={!name.trim()}>
            Save
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function DeleteWorld({ org, world, onClose }: { org: string; world: LibraryWorld | null; onClose: () => void }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  return (
    <Confirm
      open={!!world}
      onOpenChange={(o) => {
        if (!o && !busy) {
          setError(null);
          onClose();
        }
      }}
      title={world ? `Delete ${world.name}?` : ""}
      description={error ?? "Servers made with it keep theirs. This cannot be undone."}
      action="Delete"
      loading={busy}
      onConfirm={async () => {
        if (!world) return;
        setBusy(true);
        try {
          await api.del(`/orgs/${org}/worlds/${world.id}`);
          await qc.invalidateQueries({ queryKey: keys.worlds(org) });
          setBusy(false);
          onClose();
        } catch (err) {
          setBusy(false);
          setError(err instanceof ApiError ? err.message : "Could not delete it.");
        }
      }}
    />
  );
}
