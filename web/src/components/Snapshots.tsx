import { CopyPlus, History, Trash2 } from "lucide-react";
import { useState } from "react";
import { api, type Operation, type Snapshot } from "../lib/api";
import { ago, bytes, loaderNames } from "../lib/format";
import { keys, useAction } from "../lib/queries";
import { NewFromSnapshot } from "./NewFromSnapshot";
import { Button, Chip, Confirm, CopyText } from "./ui";

const triggerLabel = (s: Snapshot) => {
  if (s.trigger === "scheduled") return "Scheduled";
  if (s.trigger === "pre-restore") return "Before a restore";
  if (s.trigger === "final") return s.by ? `Before ${s.by} deleted it` : "Before it was deleted";
  return s.by ? `By ${s.by}` : "Manual";
};

/** "Paper 1.21.4", from what a snapshot recorded about its server. */
const software = (s: Snapshot) => [loaderNames[s.loader as keyof typeof loaderNames] ?? s.loader, s.version].filter(Boolean).join(" ");

const doing: Record<Operation["kind"], [title: string, noun: string]> = {
  snapshot: ["Taking a snapshot of", "Snapshot"],
  restore: ["Restoring", "Restore"],
  create: ["Copying a snapshot into", "Creation"],
  "delete-server": ["Deleting", "Deletion"],
};

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

interface RowsProps {
  org: string;
  snapshots: Snapshot[];
  /** Admins can delete and make a new server; restore is offered where onRestore is given. */
  canAdmin: boolean;
  /** True while something is running on the server, which rules out a restore. */
  busy?: boolean;
  onRestore?: (snapshot: Snapshot) => void;
}

/** A list of snapshots, newest first, as rows of one bordered box. */
export function SnapshotRows({ org, snapshots, canAdmin, busy, onRestore }: RowsProps) {
  const [deleting, setDeleting] = useState<Snapshot | null>(null);
  const [creating, setCreating] = useState<Snapshot | null>(null);
  const remove = useAction((id: string) => api.del(`/orgs/${org}/snapshots/${id}`), [keys.snapshots(org)]);
  return (
    <>
      <ul className="overflow-hidden rounded-lg border border-line bg-surface">
        {snapshots.map((s) => (
          <li key={s.id} className="flex items-center gap-4 border-b border-line px-4 py-2.5 last:border-b-0">
            <div className="min-w-0 flex-1">
              <p className="truncate text-[13px] font-medium" title={new Date(s.time).toLocaleString()}>
                {ago(s.time)}
                <span className="ml-2 font-normal text-muted">{new Date(s.time).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}</span>
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
            {s.trigger === "pre-restore" && <Chip className="max-md:hidden">undo point</Chip>}
            {canAdmin && onRestore && (
              <Button size="sm" icon={<History className="size-3.5" />} disabled={busy} onClick={() => onRestore(s)}>
                Restore
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
      <NewFromSnapshot org={org} snapshot={creating} onClose={() => setCreating(null)} />
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
