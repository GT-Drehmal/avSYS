import { useQuery, useQueryClient } from "@tanstack/react-query";
import { CopyPlus, History, Trash2 } from "lucide-react";
import { useState, type ReactNode } from "react";
import { useNavigate } from "react-router";
import { newFrom } from "../lib/origin";
import { api, ApiError, serverPath, snapshotIcon, type Operation, type Server, type Snapshot, type SnapshotDetail } from "../lib/api";
import { ago, bytes, loaderNames } from "../lib/format";
import { keys, useAction, useOffsite } from "../lib/queries";
import { toGigabytes } from "../lib/software";
import { ContentList, packTitle } from "./Content";
import type { Selection } from "../lib/selection";
import { toast } from "../lib/toast";
import { Picture } from "./Picture";
import { Button, Chip, Confirm, CopyText, Loading, Modal, Notice, RowCheckbox, SelectionBar } from "./ui";

export const triggerLabel = (s: Snapshot) => {
  if (s.trigger === "scheduled") return "Scheduled";
  if (s.trigger === "pre-restore") return "Before a restore";
  if (s.trigger === "pre-replace") return "Before the world was replaced";
  if (s.trigger === "final") return s.by ? `Before ${s.by} deleted it` : "Before it was deleted";
  if (s.trigger === "copy") return s.by ? `For a copy ${s.by} made` : "For a copy";
  if (s.trigger === "move") return s.by ? `For a move ${s.by} made` : "For a move";
  return s.by ? `By ${s.by}` : "Manual";
};

/** "Paper 1.21.4", from what a snapshot recorded about its server. */
const software = (s: Snapshot) => [loaderNames[s.loader as keyof typeof loaderNames] ?? s.loader, s.version].filter(Boolean).join(" ");

const doing: Record<Operation["kind"], [title: string, noun: string]> = {
  snapshot: ["Taking a snapshot of", "Snapshot"],
  restore: ["Restoring", "Restore"],
  create: ["Copying a snapshot into", "Creation"],
  "delete-server": ["Deleting", "Deletion"],
  move: ["Moving", "Move"],
  import: ["Importing files into", "Import"],
};

/** Whether an operation holds its server stopped, and is shown on all of its pages rather than with its snapshots. */
export const holdsTheServer = (op: Operation) => op.kind === "create" || op.kind === "restore" || op.kind === "move" || op.kind === "import";

/** A snapshot or restore that is running, with how far along it is. */
export function OperationRow({ op }: { op: Operation }) {
  const [title, noun] = doing[op.kind];
  return (
    <div className="flex flex-col gap-2 rounded-lg border border-accent/40 bg-accent-wash px-4 py-3" data-operation={op.kind}>
      <div className="flex items-center justify-between gap-4 text-[13px]">
        <span className="font-medium">
          {title} {op.server}
        </span>
        <span className="text-muted">
          {op.stage}
          {op.percent > 0 && op.percent < 100 && <span className="ml-2 font-mono tabular-nums">{Math.round(op.percent)}%</span>}
        </span>
      </div>
      <div
        className="h-1 overflow-hidden rounded-full bg-raised"
        role="progressbar"
        aria-label={`${noun} of ${op.server}: ${op.stage}`}
        aria-valuemin={0}
        aria-valuemax={100}
        aria-valuenow={op.percent > 0 ? Math.round(op.percent) : undefined}
      >
        <div className={op.percent > 0 ? "h-full bg-accent transition-[width] duration-500" : "h-full w-1/3 animate-pulse-soft bg-accent"} style={op.percent > 0 ? { width: `${Math.min(op.percent, 100)}%` } : undefined} />
      </div>
    </div>
  );
}

export interface RowsProps {
  org: string;
  snapshots: Snapshot[];
  /** Admins can delete and make a new server; restore is offered where onRestore is given. */
  canAdmin: boolean;
  /** True while something is running on its server, which rules out a restore. */
  busy?: boolean | ((snapshot: Snapshot) => boolean);
  /** Offers a restore, of the snapshots it is asked for; on a list that mixes servers, only those whose server still exists. */
  onRestore?: (snapshot: Snapshot) => void;
  restorable?: (snapshot: Snapshot) => boolean;
  /** Lets admins tick rows, to act on several at once. Shared by every list on a page. */
  selection?: Selection;
  /** Put before each row, for a list that mixes servers: which one it is of. */
  lead?: (snapshot: Snapshot) => ReactNode;
}

/** A list of snapshots, newest first, as rows of one bordered box. */
export function SnapshotRows({ org, snapshots, canAdmin, busy, onRestore, restorable, selection, lead }: RowsProps) {
  const isBusy = (s: Snapshot) => (typeof busy === "function" ? busy(s) : !!busy);
  const canRestore = (s: Snapshot) => canAdmin && !!onRestore && (restorable?.(s) ?? true);
  const [deleting, setDeleting] = useState<Snapshot | null>(null);
  const navigate = useNavigate();
  // Making a server out of a snapshot is a page of its own, the same one a new server is made on.
  const setCreating = (s: Snapshot) => navigate(newFrom(org, { kind: "snapshot", id: s.id }));
  const [looking, setLooking] = useState<Snapshot | null>(null);
  const remove = useAction((id: string) => api.del(`/orgs/${org}/snapshots/${id}`), [keys.snapshots(org)]);
  return (
    <>
      <ul className="overflow-hidden rounded-lg border border-line bg-surface">
        {snapshots.map((s) => (
          <li key={s.id} className={`group flex items-center gap-4 border-b border-line px-4 py-2.5 last:border-b-0 ${selection?.has(s.id) ? "bg-accent-wash" : ""}`}>
            {canAdmin && selection && (
              <RowCheckbox
                checked={selection.has(s.id)}
                quiet={selection.count === 0}
                label={`Select the snapshot of ${s.server} from ${ago(s.time)}`}
                onChange={(range) => selection.toggle(s.id, range)}
                className="-mr-1"
              />
            )}
            {lead?.(s)}
            <div className="min-w-0 flex-1">
              <p className="truncate text-[13px] font-medium" title={new Date(s.time).toLocaleString()}>
                {/* The way into what the snapshot holds. */}
                <button type="button" onClick={() => setLooking(s)} className="rounded-sm text-left hover:text-accent" aria-label={`About the snapshot of ${s.server} from ${ago(s.time)}`}>
                  {ago(s.time)}
                  <span className="ml-2 font-normal text-muted">{new Date(s.time).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}</span>
                </button>
              </p>
              <p className="flex items-center gap-1.5 truncate text-xs text-faint">
                <span className="truncate">{[software(s), triggerLabel(s)].filter(Boolean).join(" · ")}</span>
                {/* The start of the snapshot's ID, which is what the API and plain restic take. Copies the whole of it. */}
                <span className="shrink-0">· ID</span>
                <CopyText value={s.id} display={s.id.slice(0, 8)} className="shrink-0 text-xs! text-faint! hover:text-accent!" />
              </p>
            </div>
            <div className="text-right max-sm:hidden">
              <p className="font-mono text-[13px] tabular-nums">{bytes(s.size)}</p>
              <p className="text-xs text-faint" title="What this snapshot added to storage when it was taken">
                +{bytes(s.added)} stored
              </p>
            </div>
            {(s.trigger === "pre-restore" || s.trigger === "pre-replace") && <Chip className="max-md:hidden">undo point</Chip>}
            {canRestore(s) && (
              <Button
                size="sm"
                icon={<History className="size-3.5" />}
                disabled={isBusy(s)}
                aria-label={`Restore ${s.server} to the snapshot from ${ago(s.time)}`}
                onClick={() => onRestore?.(s)}
              >
                <span className="max-lg:hidden">Restore</span>
              </Button>
            )}
            {canAdmin && (
              <Button size="sm" icon={<CopyPlus className="size-3.5" />} title="Make a new server from this snapshot" aria-label={`New server from the snapshot of ${s.server} from ${ago(s.time)}`} onClick={() => setCreating(s)}>
                <span className="max-lg:hidden">New server</span>
              </Button>
            )}
            {canAdmin && <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} aria-label={`Delete the snapshot of ${s.server} from ${ago(s.time)}`} onClick={() => setDeleting(s)} />}
          </li>
        ))}
      </ul>
      <SnapshotDetails
        org={org}
        snapshot={looking}
        onClose={() => setLooking(null)}
        actions={
          looking &&
          canAdmin && (
            <>
              {canRestore(looking) && (
                <Button
                  icon={<History className="size-3.5" />}
                  disabled={isBusy(looking)}
                  onClick={() => {
                    setLooking(null);
                    onRestore?.(looking);
                  }}
                >
                  Restore
                </Button>
              )}
              <Button
                icon={<CopyPlus className="size-3.5" />}
                onClick={() => {
                  setLooking(null);
                  setCreating(looking);
                }}
              >
                New server
              </Button>
              <Button
                variant="danger"
                icon={<Trash2 className="size-3.5" />}
                onClick={() => {
                  setLooking(null);
                  setDeleting(looking);
                }}
              >
                Delete
              </Button>
            </>
          )
        }
      />
      <Confirm
        open={!!deleting}
        onOpenChange={(open) => !open && setDeleting(null)}
        title="Delete this snapshot?"
        description={deleting ? `The snapshot of ${deleting.server} from ${new Date(deleting.time).toLocaleString()} is removed for good.` : ""}
        action="Delete snapshot"
        loading={remove.isPending}
        onConfirm={() => deleting && remove.mutate(deleting.id, { onSuccess: () => setDeleting(null) })}
      />
    </>
  );
}

const ruleNames = { daily: "day", weekly: "week", monthly: "month", yearly: "year" } as const;

/** "day, week and month" */
const listed = (words: string[]) => (words.length < 2 ? words.join("") : `${words.slice(0, -1).join(", ")} and ${words[words.length - 1]}`);

/** How long a snapshot will be here, in a sentence. */
function keptFor(detail: SnapshotDetail) {
  const { scheduled, kept = [], paused } = detail.retention;
  if (!scheduled) {
    return "Until you delete it.";
  }
  const rules = listed(kept.map((k) => ruleNames[k]));
  // Off is not the same as never: running the schedule by hand applies its rules.
  if (paused) {
    return kept.length === 0
      ? "Scheduled snapshots are off, so it stays for now. It is removed when they run again."
      : `Scheduled snapshots are off, so it stays for now. When they run again, it is kept as the newest of its ${rules}.`;
  }
  if (kept.length === 0) return "Removed the next time the schedule runs.";
  return `Kept by the schedule, as the newest of its ${rules}.`;
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="grid grid-cols-[7.5rem_minmax(0,1fr)] gap-3 py-1.5 text-[13px]">
      <dt className="text-muted">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  );
}

interface DetailsProps {
  org: string;
  /** The snapshot to show, or null while the dialog is closed. */
  snapshot: Snapshot | null;
  onClose: () => void;
  /** What can be done with it, shown along the bottom. */
  actions?: ReactNode;
}

/**
 * One snapshot opened up: when and why it was taken, how long it is kept,
 * whether it has reached the offsite copy, and the server as it was.
 */
export function SnapshotDetails({ org, snapshot, onClose, actions }: DetailsProps) {
  const detail = useQuery({
    queryKey: ["snapshot", org, snapshot?.id],
    queryFn: () => api.get<SnapshotDetail>(`/orgs/${org}/snapshots/${snapshot!.id}`),
    enabled: !!snapshot,
    retry: false,
  });
  const offsite = useOffsite(org);
  const s = snapshot;
  const spec = detail.data?.spec;
  const properties = Object.entries(spec?.properties ?? {}).sort(([a], [b]) => a.localeCompare(b));
  // Everything that was here when a copy last went through is there.
  const copied = !!s && !!offsite.data?.lastSuccess && new Date(s.time) <= new Date(offsite.data.lastSuccess);
  return (
    <Modal open={!!s} onOpenChange={(open) => !open && onClose()} size="lg" title={s ? `Snapshot of ${s.server}` : ""} description={s ? `${ago(s.time)} · ${triggerLabel(s)}` : ""}>
      {s && (
        <div className="flex flex-col gap-5">
          <dl className="divide-y divide-line">
            <Fact label="Taken">{new Date(s.time).toLocaleString(undefined, { dateStyle: "full", timeStyle: "medium" })}</Fact>
            <Fact label="Why">{s.trigger === "manual" ? (s.by ? `Taken by hand, by ${s.by}` : "Taken by hand") : triggerLabel(s)}</Fact>
            <Fact label="Size">
              {bytes(s.size)} in {s.files.toLocaleString()} file{s.files === 1 ? "" : "s"}.{" "}
              <span className="text-muted">It added {bytes(s.added)} to storage.</span>
            </Fact>
            {detail.data && <Fact label="Kept">{keptFor(detail.data)}</Fact>}
            {offsite.data?.configured && <Fact label="Offsite">{copied ? "In the offsite copy." : "Not in the offsite copy yet."}</Fact>}
            <Fact label="ID">
              <CopyText value={s.id} display={s.id.slice(0, 16)} />
            </Fact>
          </dl>

          <section className="flex flex-col gap-2">
            <h3 className="flex items-center gap-2 text-[13px] font-semibold">
              {/* The picture among its files, which a server made from it has. */}
              <Picture name={detail.data?.spec.displayName || s.server} url={snapshotIcon(org, s.id)} size={24} />
              {s.server} as it was
            </h3>
            {detail.isPending ? (
              <Loading />
            ) : !spec ? (
              <Notice tone="danger">{detail.error instanceof ApiError ? detail.error.message : "Could not load its details."}</Notice>
            ) : (
              <>
                <dl className="divide-y divide-line">
                  <Fact label="Software">
                    {spec.modpack ? `${spec.modpack.title || spec.modpack.project} modpack` : `${loaderNames[spec.loader] ?? spec.loader} ${spec.version}`}
                    {spec.loaderVersion && <span className="text-muted"> · build {spec.loaderVersion}</span>}
                  </Fact>
                  <Fact label="Memory">{toGigabytes(spec.resources.memory)} GB</Fact>
                  <Fact label="Storage">{toGigabytes(spec.storage.size)} GB</Fact>
                  {properties.length > 0 && (
                    <Fact label="Game settings">
                      <ul className="flex flex-col gap-0.5 font-mono text-xs">
                        {properties.map(([name, value]) => (
                          <li key={name} className="truncate">
                            <span className="text-muted">{name}=</span>
                            {value}
                          </li>
                        ))}
                      </ul>
                    </Fact>
                  )}
                  {(spec.ops?.length ?? 0) > 0 && <Fact label="Operators">{spec.ops!.join(", ")}</Fact>}
                  {(spec.whitelist?.length ?? 0) > 0 && <Fact label="Whitelist">{spec.whitelist!.join(", ")}</Fact>}
                  {spec.resourcePack?.url && (
                    <Fact label="Resource pack">
                      {spec.resourcePack.project || spec.resourcePack.uploaded ? (
                        <>
                          {packTitle(spec.resourcePack)}
                          <span className="text-muted">{spec.resourcePack.uploaded ? " · uploaded" : ` · ${spec.resourcePack.project?.versionName ?? "Modrinth"}`}</span>
                        </>
                      ) : (
                        <span className="font-mono text-xs break-all">{spec.resourcePack.url}</span>
                      )}
                    </Fact>
                  )}
                </dl>
                {(spec.mods?.length ?? 0) > 0 && <ContentList mods={spec.mods!} extra={(m) => (m.versionName || m.version) && <Chip className="max-sm:hidden">{m.versionName || m.version}</Chip>} />}
              </>
            )}
          </section>

          {actions && <div className="flex flex-wrap justify-end gap-2 border-t border-line pt-4">{actions}</div>}
        </div>
      )}
    </Modal>
  );
}

/** How many snapshots one request may delete. */
const atOnce = 500;

/**
 * What can be done to several snapshots at once, which is deleting them.
 * They go together or not at all, in one pass over the repository.
 */
export function SnapshotSelectionBar({ org, snapshots, selection }: { org: string; snapshots: Snapshot[]; selection: Selection }) {
  const [confirming, setConfirming] = useState(false);
  const chosen = snapshots.filter((s) => selection.has(s.id));
  // One request takes so many; more than that go in as many requests as it
  // takes, one after another. If one is refused, those before it are gone
  // and the list, asked for again either way, shows what is left.
  const qc = useQueryClient();
  const remove = useAction(async () => {
    let deleted = 0;
    try {
      for (let i = 0; i < chosen.length; i += atOnce) {
        deleted += (await api.del<{ deleted: number }>(`/orgs/${org}/snapshots`, { ids: chosen.slice(i, i + atOnce).map((s) => s.id) })).deleted;
      }
    } finally {
      await qc.invalidateQueries({ queryKey: keys.snapshots(org) });
    }
    return { deleted };
  });
  // "3 of survival, 1 of creative"
  const perServer = new Map<string, number>();
  for (const s of chosen) perServer.set(s.server, (perServer.get(s.server) ?? 0) + 1);
  const breakdown = [...perServer].map(([server, n]) => `${n} of ${server}`).join(", ");
  const many = `${chosen.length} snapshot${chosen.length === 1 ? "" : "s"}`;
  return (
    <>
      <SelectionBar count={selection.count} noun="snapshot" onClear={selection.clear}>
        <Button size="sm" variant="danger" icon={<Trash2 className="size-3.5" />} onClick={() => setConfirming(true)}>
          Delete
        </Button>
      </SelectionBar>
      <Confirm
        open={confirming}
        onOpenChange={(open) => !remove.isPending && setConfirming(open)}
        title={`Delete ${many}?`}
        description={`${breakdown}. They are removed for good.`}
        action={`Delete ${many}`}
        loading={remove.isPending}
        onConfirm={() =>
          remove.mutate(undefined, {
            onSuccess: ({ deleted }) => {
              setConfirming(false);
              selection.clear();
              toast.info(`Deleted ${deleted} snapshot${deleted === 1 ? "" : "s"}`);
            },
          })
        }
      />
    </>
  );
}

/**
 * Asks before a server is put back to one of its snapshots, and starts it.
 * The same from the server's own Backups tab and the organization's.
 */
export function RestoreDialog({ server, snapshot, onClose }: { server: Server; snapshot: Snapshot | null; onClose: () => void }) {
  const qc = useQueryClient();
  const restore = useAction((id: string) => api.post<Operation>(`${serverPath(server.org, server.name)}/snapshots/${id}/restore`));
  return (
    <Modal
      open={!!snapshot}
      onOpenChange={(open) => !open && onClose()}
      title={`Restore ${server.name}?`}
      description={snapshot ? `To how it was ${ago(snapshot.time)}, on ${new Date(snapshot.time).toLocaleString()}.` : ""}
    >
      <div className="flex flex-col gap-4">
        <ul className="flex list-disc flex-col gap-1.5 pl-4 text-[13px] leading-relaxed text-muted">
          <li>The server stops while its files are restored{server.spec.state === "Stopped" ? "" : ", then starts again"}.</li>
          <li>Its world, files, version and mods go back to how they were. Anything newer is removed.</li>
          <li>A snapshot is taken first, so you can undo this.</li>
        </ul>
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button
            variant="danger"
            loading={restore.isPending}
            onClick={() =>
              snapshot &&
              restore.mutate(snapshot.id, {
                onSuccess: () => {
                  // Once it has been started the lists are asked for again, and
                  // show it as running. The answer is not put into them
                  // directly: an operation that failed at once may already
                  // have been reported as over, and would show as running for ever.
                  qc.invalidateQueries({ queryKey: keys.serverSnapshots(server.org, server.name) });
                  qc.invalidateQueries({ queryKey: keys.snapshots(server.org) });
                  onClose();
                },
              })
            }
          >
            Restore
          </Button>
        </div>
      </div>
    </Modal>
  );
}
