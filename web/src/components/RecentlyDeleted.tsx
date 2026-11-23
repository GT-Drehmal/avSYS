import { ChevronDown, ChevronRight, Trash2, Undo2 } from "lucide-react";
import { useState } from "react";
import { api, serverIcon, type BinnedSnapshot, type Server } from "../lib/api";
import { ago, bytes, displayName } from "../lib/format";
import { keys, useAction, useBin } from "../lib/queries";
import { matches } from "../lib/search";
import { toast } from "../lib/toast";
import { colorName, software, SnapshotColorDot, triggerLabel } from "./Snapshots";
import { Picture } from "./Picture";
import { Button, Chip, Confirm, Input } from "./ui";

const day = 24 * 60 * 60 * 1000;

/** How long until a deleted snapshot is removed for good, in a few words. */
function left(until: string) {
  const ms = Date.parse(until) - Date.now();
  if (ms <= 0) return "Removed soon";
  const days = Math.ceil(ms / day);
  return days === 1 ? "1 day left" : `${days} days left`;
}

/** More than this many, and they can be searched. */
const searchFrom = 5;

/**
 * An organization's recently deleted snapshots: kept a while, then removed
 * for good. An admin puts one back; only the panel's administrator removes
 * one sooner. They still take storage until then, which is said.
 */
export function RecentlyDeleted({ org, canAdmin, servers }: { org: string; canAdmin: boolean; servers?: Server[] }) {
  const bin = useBin(org);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  // One to remove for good, or all of them.
  const [emptying, setEmptying] = useState<BinnedSnapshot | "all" | null>(null);
  const putBack = useAction((s: BinnedSnapshot) => api.post(`/orgs/${org}/bin/${s.id}/put-back`), [keys.snapshots(org)]);
  const empty = useAction((which: BinnedSnapshot | "all") => api.del<{ removed: number }>(which === "all" ? `/orgs/${org}/bin` : `/orgs/${org}/bin/${which.id}`), [keys.snapshots(org)]);
  const list = bin.data?.snapshots ?? [];
  if (!bin.data?.available || list.length === 0) return null;
  const { days, canEmpty } = bin.data;
  const byName = new Map(servers?.map((s) => [s.name, s]));
  const shown = list.filter((s) => matches(query, s.server, byName.get(s.server) && displayName(byName.get(s.server)!), s.name, colorName(s.color), s.deletedBy, triggerLabel(s)));
  const kept = days === 1 ? "1 day" : `${days} days`;
  return (
    <section aria-label="Recently deleted" className="overflow-hidden rounded-lg border border-line bg-surface">
      <div className="flex items-center hover:bg-raised">
        <button type="button" aria-expanded={open} onClick={() => setOpen(!open)} className="flex min-w-0 flex-1 items-center gap-3 px-4 py-3 text-left">
          {open ? <ChevronDown className="size-4 shrink-0 text-muted" /> : <ChevronRight className="size-4 shrink-0 text-muted" />}
          <Trash2 className="size-4 shrink-0 text-muted" />
          <span className="min-w-0 flex-1">
            <span className="block text-[13px] font-medium">
              Recently deleted <span className="font-normal text-muted">· {list.length}</span>
            </span>
            <span className="block text-xs text-muted">{days > 0 ? `Kept ${kept}, then removed for good. They use storage until then.` : "Removed for good soon. They use storage until then."}</span>
          </span>
        </button>
        {canEmpty && (
          <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} onClick={() => setEmptying("all")} className="mr-3 shrink-0">
            Empty
          </Button>
        )}
      </div>
      {open && (
        <div className="flex flex-col gap-2 border-t border-line bg-bg p-3">
          {list.length > searchFrom && <Input type="search" aria-label="Search recently deleted" placeholder="Search" value={query} onChange={(e) => setQuery(e.target.value)} className="max-w-xs" />}
          {shown.length === 0 ? (
            <p className="px-1 py-4 text-center text-[13px] text-muted">No matching snapshots.</p>
          ) : (
            <ul className="overflow-hidden rounded-lg border border-line bg-surface">
              {shown.map((s) => {
                const server = byName.get(s.server);
                return (
                  <li key={s.id} className="flex items-center gap-4 border-b border-line px-4 py-2.5 last:border-b-0">
                    <span className="flex w-36 shrink-0 items-center gap-2 max-sm:w-24">
                      {/* A deleted server's picture is in its snapshots, which are not read while they are here. */}
                      <Picture name={server ? displayName(server) : s.server} url={server ? serverIcon(server) : undefined} size={24} />
                      <span className="truncate text-[13px] font-medium" title={s.server}>
                        {s.server}
                      </span>
                    </span>
                    <div className="min-w-0 flex-1">
                      <p className="flex items-center gap-2 truncate text-[13px] font-medium" title={new Date(s.time).toLocaleString()}>
                        {s.color && <SnapshotColorDot color={s.color} />}
                        <span className="min-w-0 truncate">{s.name || ago(s.time)}</span>
                        <span className="truncate font-normal text-muted">{new Date(s.time).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}</span>
                        {!server && servers && <Chip className="max-md:hidden">deleted server</Chip>}
                      </p>
                      <p className="truncate text-xs text-faint" title={new Date(s.deletedAt).toLocaleString()}>
                        {[`Deleted ${ago(s.deletedAt)}${s.deletedBy ? ` by ${s.deletedBy}` : ""}`, software(s), triggerLabel(s)].filter(Boolean).join(" · ")}
                      </p>
                    </div>
                    <div className="text-right max-sm:hidden">
                      <p className="font-mono text-[13px] tabular-nums">{bytes(s.size)}</p>
                      <p className="text-xs text-faint" title={new Date(s.until).toLocaleString()}>
                        {left(s.until)}
                      </p>
                    </div>
                    {canAdmin && (
                      <Button
                        size="sm"
                        icon={<Undo2 className="size-3.5" />}
                        aria-label={`Put back the snapshot of ${s.server} from ${ago(s.time)}`}
                        loading={putBack.isPending && putBack.variables?.id === s.id}
                        onClick={() => putBack.mutate(s, { onSuccess: () => toast.info(`Put back the snapshot of ${s.server}`) })}
                      >
                        <span className="max-lg:hidden">Put back</span>
                      </Button>
                    )}
                    {canEmpty && (
                      <Button size="sm" variant="ghost" icon={<Trash2 className="size-3.5" />} aria-label={`Remove the snapshot of ${s.server} from ${ago(s.time)} for good`} onClick={() => setEmptying(s)} />
                    )}
                  </li>
                );
              })}
            </ul>
          )}
        </div>
      )}
      <Confirm
        open={!!emptying}
        onOpenChange={(o) => !o && !empty.isPending && setEmptying(null)}
        title={emptying === "all" ? "Empty Recently deleted?" : "Remove this snapshot for good?"}
        description={
          emptying === "all"
            ? `${list.length === 1 ? "Its snapshot is" : `All ${list.length} snapshots are`} removed now. This can't be undone.`
            : emptying
              ? `The snapshot of ${emptying.server} from ${new Date(emptying.time).toLocaleString()} is removed now. This can't be undone.`
              : ""
        }
        action={emptying === "all" ? "Empty" : "Remove for good"}
        loading={empty.isPending}
        onConfirm={() => emptying && empty.mutate(emptying, { onSuccess: () => setEmptying(null) })}
      />
    </section>
  );
}
