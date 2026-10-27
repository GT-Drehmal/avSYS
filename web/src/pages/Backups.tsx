import { useQueryClient } from "@tanstack/react-query";
import { Camera, ChevronDown, ChevronRight } from "lucide-react";
import { useState } from "react";
import { Link, useParams } from "react-router";
import { backupConcern, BackupSchedule, scheduleOf } from "../components/BackupSchedule";
import { OffsiteCopy } from "../components/OffsiteCopy";
import { Picture } from "../components/Picture";
import { useOrg } from "../components/Shell";
import { OperationRow, RestoreDialog, SnapshotRows, SnapshotSelectionBar, software, triggerLabel, type RowsProps } from "../components/Snapshots";
import { Button, Chip, cx, Empty, Loading, Notice, PageHeader, SearchBox, Select } from "../components/ui";
import { api, atLeast, serverIcon, serverPath, snapshotIcon, type Operation, type Server, type Snapshot } from "../lib/api";
import { ago, bytes, displayName } from "../lib/format";
import { keys, useAction, useBackupPolicy, useOrgSnapshots, useServers } from "../lib/queries";
import { matches, useSearch } from "../lib/search";
import { useSelection, type Selection } from "../lib/selection";

/** How the page lays the snapshots out: a row to a server, or all of them in order of time. */
type View = "servers" | "time";

const viewKey = "avsys.backups.view";

function storedView(): View {
  try {
    return localStorage.getItem(viewKey) === "time" ? "time" : "servers";
  } catch {
    return "servers";
  }
}

/** Why a snapshot was taken, as the filter names it. */
const reasons: [trigger: string, label: string][] = [
  ["scheduled", "Scheduled"],
  ["manual", "By hand"],
  ["pre-restore", "Before a restore"],
  ["pre-replace", "Before the world was replaced"],
  ["final", "Before a delete"],
  ["copy", "For a copy"],
  ["move", "For a move"],
];

/** How many snapshots are shown of a server before "show all". */
const firstFew = 5;

/**
 * An organization's snapshots. By server, it says at a glance how each
 * server's backups stand, and a server is opened to see its own. By time, it
 * is everything in the order it happened, which is how one night's run or a
 * clear-out is looked at.
 */
export function Backups() {
  const { org = "" } = useParams();
  const membership = useOrg();
  const list = useOrgSnapshots(org);
  const servers = useServers(org);
  const policy = useBackupPolicy(org);
  const canAdmin = atLeast(membership?.role, "admin");
  const canOperate = atLeast(membership?.role, "operator");
  const [view, setView] = useState<View>(storedView);
  const [open, setOpen] = useState<ReadonlySet<string>>(new Set());
  const [all, setAll] = useState<ReadonlySet<string>>(new Set());
  const [onlyServer, setOnlyServer] = useState("");
  const [onlyReason, setOnlyReason] = useState("");
  const [query, setQuery] = useSearch();

  const snapshots = list.data?.snapshots ?? [];
  const byName = new Map(servers.data?.map((s) => [s.name, s]));
  // A snapshot of a server that still exists can put it back. One that is
  // gone is brought back as a new server instead.
  const [restoring, setRestoring] = useState<Snapshot | null>(null);
  const running = new Set(list.data?.operations.map((op) => op.server));
  const restoreProps = {
    onRestore: setRestoring,
    restorable: (s: Snapshot) => byName.has(s.server),
    busy: (s: Snapshot) => running.has(s.server),
  };
  // Newest first within a server, as the list comes.
  const groups = new Map<string, Snapshot[]>();
  for (const s of snapshots) groups.set(s.server, [...(groups.get(s.server) ?? []), s]);
  // By server, every server is there, one with none yet too: it can be
  // snapshotted from here.
  const byServer = new Map(groups);
  for (const s of servers.data ?? []) if (!byServer.has(s.name)) byServer.set(s.name, []);
  const concernOf = (name: string) => {
    const server = byName.get(name);
    return server ? backupConcern(server, !!policy.data && scheduleOf(policy.data, server.spec.backups).enabled) : null;
  };
  // Servers that need looking at come first, then whichever was snapshotted last.
  const newestTime = (items: Snapshot[]) => (items[0] ? new Date(items[0].time).getTime() : 0);
  // A server by its names; a snapshot by those, when and why it was taken and what ran.
  const serverSaid = (name: string) => {
    const s = byName.get(name);
    return [name, s && displayName(s), !s && servers.data && "deleted"];
  };
  const ordered = [...byServer].filter(([name]) => matches(query, ...serverSaid(name))).sort(([a, as], [b, bs]) => Number(!!concernOf(b)) - Number(!!concernOf(a)) || newestTime(bs) - newestTime(as) || a.localeCompare(b));
  const filtered = snapshots.filter(
    (s) =>
      (!onlyServer || s.server === onlyServer) &&
      (!onlyReason || s.trigger === onlyReason) &&
      matches(query, ...serverSaid(s.server), triggerLabel(s), software(s), new Date(s.time).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })),
  );
  // What can be ticked is what is on the page, in its order.
  const listed = view === "time" ? filtered : ordered.flatMap(([name, items]) => (open.has(name) ? (all.has(name) ? items : items.slice(0, firstFew)) : []));
  const selection = useSelection(listed.map((s) => s.id), `${org}/${view}`);

  // Which servers there are decides the rows and what can be done to them.
  if (list.isPending || servers.isPending) return <Loading />;
  const header = <PageHeader title="Backups" description={membership?.name} />;
  if (!list.data) {
    return (
      <>
        {header}
        <Notice tone="danger">Could not load snapshots.</Notice>
      </>
    );
  }
  if (!list.data.available) {
    return (
      <>
        {header}
        <Notice>Snapshots are not set up on this panel.</Notice>
      </>
    );
  }
  const { operations, usage } = list.data;
  const choose = (next: View) => {
    setView(next);
    try {
      localStorage.setItem(viewKey, next);
    } catch {
      // Not remembered, then.
    }
  };
  const toggle = (set: ReadonlySet<string>, name: string) => {
    const next = new Set(set);
    next.has(name) ? next.delete(name) : next.add(name);
    return next;
  };
  const tab = (active: boolean) => cx("h-7 rounded-sm px-2.5 text-xs font-medium", active ? "bg-hover text-fg" : "text-muted hover:text-fg");

  return (
    <>
      {header}
      <div className="flex flex-col gap-6">
        <div className="flex flex-col gap-2">
          <p className="text-[13px] text-muted">
            {snapshots.length === 0
              ? "No snapshots yet."
              : `${snapshots.length} snapshot${snapshots.length === 1 ? "" : "s"} of ${groups.size} server${groups.size === 1 ? "" : "s"}, using ${bytes(usage?.bytes ?? 0)}.`}
          </p>
          <OffsiteCopy org={org} canAdmin={canAdmin} empty={snapshots.length === 0 && operations.length === 0} />
        </div>
        {servers.isError && <Notice tone="danger">Could not load servers. Only servers with snapshots are shown.</Notice>}
        <BackupSchedule org={org} canAdmin={canAdmin} />
        {operations.map((op) => (
          <OperationRow key={op.server} op={op} />
        ))}
        {byServer.size === 0 && operations.length === 0 && <Empty title="Nothing saved yet" />}

        {byServer.size > 0 && (
          <div className="flex flex-col gap-3">
            <div className="flex flex-wrap items-center justify-between gap-3">
              <div className="flex flex-wrap items-center gap-2">
              <div role="group" aria-label="Arrange by" className="flex gap-1 rounded-md border border-line bg-bg p-0.5">
                <button type="button" aria-pressed={view === "servers"} className={tab(view === "servers")} onClick={() => choose("servers")}>
                  By server
                </button>
                <button type="button" aria-pressed={view === "time"} className={tab(view === "time")} onClick={() => choose("time")}>
                  By time
                </button>
              </div>
              <SearchBox value={query} onChange={setQuery} label="Search backups" className="w-full sm:w-56" />
              </div>
              {view === "time" && (
                <div className="flex gap-2">
                  <Select aria-label="Server" value={onlyServer} onChange={(e) => setOnlyServer(e.target.value)} className="w-44">
                    <option value="">Every server</option>
                    {[...groups.keys()].sort().map((name) => (
                      <option key={name} value={name}>
                        {name}
                      </option>
                    ))}
                  </Select>
                  <Select aria-label="Why it was taken" value={onlyReason} onChange={(e) => setOnlyReason(e.target.value)} className="w-44">
                    <option value="">Any reason</option>
                    {reasons.map(([trigger, label]) => (
                      <option key={trigger} value={trigger}>
                        {label}
                      </option>
                    ))}
                  </Select>
                </div>
              )}
            </div>

            {view === "time" ? (
              filtered.length === 0 ? (
                <p className="rounded-lg border border-line bg-surface px-4 py-8 text-center text-[13px] text-muted">No matching snapshots.</p>
              ) : (
                <SnapshotRows
                  org={org}
                  snapshots={filtered}
                  canAdmin={canAdmin}
                  canSaveWorld={canOperate}
                  selection={selection}
                  {...restoreProps}
                  lead={(s) => (
                    <span className="flex w-36 shrink-0 items-center gap-2 max-sm:w-24">
                      <ServerPicture org={org} name={s.server} server={byName.get(s.server)} known={!!servers.data} newest={groups.get(s.server)?.[0]?.id} size={24} />
                      <span className="truncate text-[13px] font-medium">{s.server}</span>
                    </span>
                  )}
                />
              )
            ) : ordered.length === 0 ? (
              <p className="rounded-lg border border-line bg-surface px-4 py-8 text-center text-[13px] text-muted">No servers match.</p>
            ) : (
              <ul className="flex flex-col gap-2">
                {ordered.map(([name, items]) => (
                  <ServerGroup
                    key={name}
                    org={org}
                    name={name}
                    server={byName.get(name)}
                    known={!!servers.data}
                    items={items}
                    concern={concernOf(name)}
                    open={open.has(name)}
                    onToggle={() => setOpen(toggle(open, name))}
                    showAll={all.has(name)}
                    onShowAll={() => setAll(toggle(all, name))}
                    canAdmin={canAdmin}
                    canOperate={canOperate}
                    busy={running.has(name)}
                    selection={selection}
                    restore={restoreProps}
                  />
                ))}
              </ul>
            )}
          </div>
        )}
      </div>
      {canAdmin && <SnapshotSelectionBar org={org} snapshots={snapshots} selection={selection} />}
      {restoring && byName.get(restoring.server) && <RestoreDialog server={byName.get(restoring.server) as Server} snapshot={restoring} onClose={() => setRestoring(null)} />}
    </>
  );
}

/**
 * A server's picture: its own while it exists, and once it is gone the one in
 * its newest snapshot, which is what bringing it back would give it. Its
 * initial for one that has none. Nothing is guessed before the list of
 * servers has said whether it exists.
 */
function ServerPicture({ org, name, server, known, newest, size }: { org: string; name: string; server?: Server; known: boolean; newest?: string; size: number }) {
  const url = server ? serverIcon(server) : known && newest ? snapshotIcon(org, newest) : undefined;
  return <Picture name={server ? displayName(server) : name} url={url} size={size} retryKey={server ? `${server.status.phase}|${server.status.message ?? ""}` : ""} />;
}

interface GroupProps {
  org: string;
  name: string;
  /** The server itself, if it still exists. */
  server?: Server;
  /** Whether the list of servers has arrived, without which "gone" cannot be said. */
  known: boolean;
  items: Snapshot[];
  concern: ReturnType<typeof backupConcern>;
  open: boolean;
  onToggle: () => void;
  showAll: boolean;
  onShowAll: () => void;
  canAdmin: boolean;
  /** Whether a snapshot can be asked for here. */
  canOperate: boolean;
  /** Whether something is being done to the server's snapshots now. */
  busy: boolean;
  selection: Selection;
  /** Restoring a snapshot of the server, where it still exists. */
  restore: Pick<RowsProps, "onRestore" | "restorable" | "busy">;
}

/**
 * One server's snapshots: a line that says how they stand, opened to see
 * them, with a snapshot of it to be taken from there.
 */
function ServerGroup({ org, name, server, known, items, concern, open, onToggle, showAll, onShowAll, canAdmin, canOperate, busy, selection, restore }: GroupProps) {
  const qc = useQueryClient();
  const newest = items[0];
  const shown = showAll ? items : items.slice(0, firstFew);
  // Once started, the list is asked for again and shows it as running.
  const snapshot = useAction(() => api.post<Operation>(`${serverPath(org, name)}/snapshots`));
  const take = () => snapshot.mutate(undefined, { onSuccess: () => qc.invalidateQueries({ queryKey: keys.snapshots(org) }) });
  const summary = (
    <>
      <ServerPicture org={org} name={name} server={server} known={known} newest={newest?.id} size={32} />
      <span className="min-w-0 flex-1">
        <span className="flex items-center gap-2">
          <span className="truncate text-[13px] font-medium">{server ? displayName(server) : name}</span>
          {!server && known && <Chip>deleted server</Chip>}
        </span>
        <span className="block truncate text-xs text-muted">
          {newest ? `Newest ${ago(newest.time)}` : "No snapshots yet"}
          {concern && <span className={concern.tone === "danger" ? "text-danger" : "text-warn"}> · {concern.text}</span>}
        </span>
      </span>
      {newest && (
        <span className="text-right max-sm:hidden">
          <span className="block font-mono text-[13px] tabular-nums">{bytes(newest.size)}</span>
          <span className="block text-xs text-faint">
            {items.length} snapshot{items.length === 1 ? "" : "s"}
          </span>
        </span>
      )}
    </>
  );
  return (
    <li className="overflow-hidden rounded-lg border border-line bg-surface">
      <div className="flex items-center hover:bg-raised">
        {newest ? (
          <button type="button" aria-expanded={open} onClick={onToggle} className="flex min-w-0 flex-1 items-center gap-3 px-4 py-3 text-left">
            {open ? <ChevronDown className="size-4 shrink-0 text-muted" /> : <ChevronRight className="size-4 shrink-0 text-muted" />}
            {summary}
          </button>
        ) : (
          <div className="flex min-w-0 flex-1 items-center gap-3 px-4 py-3">
            <span className="size-4 shrink-0" />
            {summary}
          </div>
        )}
        {canOperate && (
          <Button
            size="sm"
            icon={<Camera className="size-3.5" />}
            aria-label={`Snapshot ${name} now`}
            title={busy ? "Busy with another task." : undefined}
            disabled={busy || !server}
            loading={snapshot.isPending}
            onClick={take}
            // Kept in place, unseen, on a deleted server's row, so that the
            // rows' sizes stay in line.
            className={cx("mr-3 shrink-0", !server && "invisible")}
          >
            <span className="max-sm:hidden">Snapshot</span>
          </Button>
        )}
      </div>
      {open && newest && (
        <div className="flex flex-col gap-2 border-t border-line bg-bg p-3">
          <SnapshotRows org={org} snapshots={shown} canAdmin={canAdmin} canSaveWorld={canOperate} selection={selection} {...restore} />
          <div className="flex items-center gap-4 px-1 text-xs">
            {items.length > firstFew && (
              <button type="button" onClick={onShowAll} className="text-accent hover:underline">
                {showAll ? `Show the newest ${firstFew}` : `Show all ${items.length}`}
              </button>
            )}
            {server && (
              <Link to={`/o/${org}/s/${name}/backups`} className="text-muted hover:text-fg">
                Open {name}'s Backups tab
              </Link>
            )}
          </div>
        </div>
      )}
    </li>
  );
}
