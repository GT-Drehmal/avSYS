import { useQueryClient } from "@tanstack/react-query";
import { useRef, useState } from "react";
import { api, ApiError, type LibraryWorld, qs, serverPath, type Server, worldIcon } from "../lib/api";
import { ArchiveError } from "../lib/archive";
import { bytes } from "../lib/format";
import { keys, useMeta, watchReplace } from "../lib/queries";
import { useGameVersions } from "../lib/software";
import { type FoundWorld, inspectWorld } from "../lib/serverImport";
import { worldSummary } from "../lib/worlds";
import { Picture } from "./Picture";
import { WorldPicker } from "./StartPicker";
import { Button, cx, Modal, Notice } from "./ui";
import { cancelled, UploadProgress, useUpload } from "./Uploading";

/**
 * Puts a world one has, a save or a world folder in an archive, in place of
 * a stopped server's. A snapshot is taken first, so it can be undone from
 * the server's backups.
 */
export function ReplaceWorld({ server }: { server: Server }) {
  const qc = useQueryClient();
  const [open, setOpen] = useState(false);
  const meta = useMeta().data;
  // One of one's own, uploaded, or one from the organization's library.
  const [from, setFrom] = useState<"upload" | "library">("upload");
  const [picked, setPicked] = useState<LibraryWorld | null>(null);
  const [file, setFile] = useState<File | null>(null);
  const [world, setWorld] = useState<FoundWorld | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const { sending, sendParts, cancel } = useUpload();
  const reads = useRef(0);
  const stopped = server.spec.state === "Stopped" && server.status.phase === "Stopped";
  // Stopped, but held while the panel works on its files, which it says.
  const held = stopped && server.status.message ? server.status.message : null;

  const pick = async (picked: File | undefined) => {
    setWorld(null);
    setError(null);
    setFile(picked ?? null);
    if (!picked) return;
    const read = ++reads.current;
    try {
      const found = await inspectWorld(picked);
      if (read === reads.current) setWorld(found);
    } catch (err) {
      if (read === reads.current) setError(err instanceof ArchiveError ? err.message : "Could not read this archive.");
    }
  };

  const close = (o: boolean) => {
    // An upload under way is stopped by Cancel on it, not by closing.
    if (busy) return;
    setOpen(o);
    if (!o) {
      setFile(null);
      setWorld(null);
      setPicked(null);
      setFrom("upload");
      setError(null);
    }
  };

  const replace = async () => {
    if (from === "library" ? !picked : !file || !world) return;
    setBusy(true);
    setError(null);
    // Answered once the archive is in. The panel does the rest, which the
    // server's page shows, and this tab says how it went.
    const watch = watchReplace(server.org, server.name, server.spec.displayName || server.name);
    try {
      const path = serverPath(server.org, server.name);
      if (from === "library" && picked) await api.put<Server>(`${path}/import?${qs({ world: "true", replace: "true", library: picked.id })}`, undefined);
      else if (file && world) await sendParts<Server>(file.name, path, file, (upload) => api.put<Server>(`${path}/import?${qs({ root: world.root, world: "true", replace: "true", from: "upload", upload, size: file.size })}`, {}));
      watch.taken();
      setBusy(false);
      close(false);
    } catch (err) {
      watch.refused();
      setBusy(false);
      setError(cancelled(err) ? "The upload was stopped. Nothing was changed." : err instanceof ApiError ? err.message : "Could not replace the world.");
    } finally {
      void qc.invalidateQueries({ queryKey: keys.servers(server.org) });
      void qc.invalidateQueries({ queryKey: keys.snapshots(server.org) });
    }
  };

  // Told newer by the order the game's versions came out in, newest first.
  const versions = useGameVersions();
  const version = server.spec.version === "LATEST" ? (versions.data?.latest ?? "") : server.spec.version;
  const order = versions.data?.ordered ?? [];
  const given = from === "library" ? picked?.version : world?.version;
  const newer = !!given && order.includes(given) && order.includes(version) && order.indexOf(given) < order.indexOf(version);
  const segment = (on: boolean) => cx("h-7 rounded-sm px-3 text-xs font-medium", on ? "bg-hover text-fg" : "text-muted hover:text-fg");
  return (
    <>
      <div className="flex flex-wrap items-center gap-3">
        <Button onClick={() => setOpen(true)} disabled={!stopped || !!held}>
          Replace the world
        </Button>
        {!stopped && <p className="text-xs text-muted">Stop the server first.</p>}
        {held && <p className="text-xs text-muted">{held}</p>}
      </div>
      <Modal open={open} onOpenChange={close} title="Replace the world?" description="A snapshot is taken first. The server's other files stay as they are.">
        <form
          className="flex flex-col gap-4"
          onSubmit={(e) => {
            e.preventDefault();
            void replace();
          }}
        >
          {meta?.snapshots && (
            <div role="group" aria-label="From" className="flex w-fit gap-1 rounded-md border border-line bg-bg p-0.5">
              <button type="button" aria-pressed={from === "upload"} className={segment(from === "upload")} disabled={busy} onClick={() => setFrom("upload")}>
                Upload
              </button>
              <button type="button" aria-pressed={from === "library"} className={segment(from === "library")} disabled={busy} onClick={() => setFrom("library")}>
                Library
              </button>
            </div>
          )}
          {from === "library" ? (
            picked ? (
              <div className="flex items-center gap-3 rounded-md border border-accent bg-accent-wash px-3 py-2.5">
                <Picture name={picked.name} url={worldIcon(server.org, picked.id)} size={32} />
                <div className="min-w-0 flex-1">
                  <p className="truncate text-[13px] font-medium">{picked.name}</p>
                  <p className="truncate text-xs text-muted" data-testid="world-summary">
                    {worldSummary(picked)}
                  </p>
                </div>
                <Button size="sm" disabled={busy} onClick={() => setPicked(null)}>
                  Change
                </Button>
              </div>
            ) : (
              <WorldPicker org={server.org} onPick={setPicked} />
            )
          ) : (
          <>
          <label className="flex flex-col gap-1.5 text-[13px]">
            <span className="font-medium">World archive</span>
            <input
              type="file"
              accept=".zip,.tar,.tar.gz,.tgz"
              disabled={busy}
              onChange={(e) => void pick(e.target.files?.[0])}
              className="text-xs text-muted file:mr-3 file:rounded-md file:border file:border-line-strong file:bg-raised file:px-3 file:py-1.5 file:text-fg"
            />
          </label>
          {world && (
            <p className="text-[13px] text-muted" data-testid="world-summary">
              World "{world.name}"{world.version ? ` · last opened in ${world.version}` : ""} · {bytes(world.size)}
            </p>
          )}
          </>
          )}
          {newer && <Notice>This server runs {version}, older than the version this world was last opened in. The game cannot open the world.</Notice>}
          {error && <Notice tone="danger">{error}</Notice>}
          {sending && <UploadProgress sending={sending} onCancel={cancel} />}
          <div className="flex justify-end gap-2">
            <Button onClick={() => close(false)} disabled={busy}>
              Cancel
            </Button>
            <Button type="submit" variant="danger" disabled={from === "library" ? !picked : !world || !file} loading={busy}>
              Replace the world
            </Button>
          </div>
        </form>
      </Modal>
    </>
  );
}
