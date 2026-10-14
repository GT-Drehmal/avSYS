import { useQueryClient } from "@tanstack/react-query";
import { Play, Plus, RotateCw, Square, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link, useParams } from "react-router";
import { scheduleOf } from "../components/BackupSchedule";
import { Picture } from "../components/Picture";
import { ServerActions } from "../components/ServerActions";
import { useOrg } from "../components/Shell";
import { Button, Confirm, CopyText, Empty, PageHeader, RowCheckbox, SelectionBar, StatusDot, Loading, Toggle } from "../components/ui";
import { api, ApiError, atLeast, serverIcon, serverPath, type Server } from "../lib/api";
import { ago, describeSoftware, displayName } from "../lib/format";
import { keys, useBackupPolicy, useMeta, useServers } from "../lib/queries";
import { useSelection } from "../lib/selection";
import { toast } from "../lib/toast";

function Players({ server }: { server: Server }) {
  const p = server.status.players;
  if (server.status.phase !== "Running" || !p) return <span className="text-faint">–</span>;
  return (
    <span className="font-mono tabular-nums">
      <span className={p.online > 0 ? "text-fg" : "text-muted"}>{p.online}</span>
      <span className="text-faint"> / {p.max}</span>
    </span>
  );
}

const phaseWords: Record<string, string> = {
  Running: "Online",
  Starting: "Starting",
  Stopping: "Stopping",
  Sleeping: "Asleep",
  Stopped: "Offline",
  Error: "Error",
};

const overdueAfter = 36 * 60 * 60 * 1000;

/** Says so when a server's backups need looking at: the last one failed, or the daily one has not happened. */
function BackupMark({ server, scheduled }: { server: Server; scheduled: boolean }) {
  const backups = server.status.snapshots;
  if (backups?.error) return <span className="text-danger"> · backup failed</span>;
  if (!scheduled) return null;
  const last = backups?.lastRun ?? server.createdAt;
  if (Date.now() - new Date(last).getTime() < overdueAfter) return null;
  return <span className="text-warn"> · {backups?.lastRun ? `last backup ${ago(backups.lastRun)}` : "never backed up"}</span>;
}

const columns = "grid grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)_72px_minmax(0,1.3fr)_112px] items-center gap-4 px-4";

type Act = "start" | "stop" | "restart" | "delete";

const done: Record<Act, string> = { start: "Started", stop: "Stopped", restart: "Restarted", delete: "Deleted" };

/** Which of the selected servers an action makes sense for: one that is stopped is not stopped again. */
const fits: Record<Act, (s: Server) => boolean> = {
  start: (s) => s.spec.state === "Stopped" || s.status.phase === "Sleeping",
  stop: (s) => s.spec.state !== "Stopped",
  restart: (s) => s.spec.state !== "Stopped" && s.status.phase !== "Sleeping",
  delete: () => true,
};

const plural = (n: number, noun: string) => `${n} ${noun}${n === 1 ? "" : "s"}`;

/**
 * What can be done to several servers at once. Each is asked on its own, so
 * one that fails does not hold back the rest, and is named afterwards.
 */
function BulkActions({ org, chosen, canAdmin, onDone }: { org: string; chosen: Server[]; canAdmin: boolean; onDone: (failed: string[]) => void }) {
  const qc = useQueryClient();
  const meta = useMeta();
  const [running, setRunning] = useState<Act | null>(null);
  const [deleting, setDeleting] = useState(false);
  // A last snapshot of each is taken unless this is switched off.
  const [finalSnapshot, setFinalSnapshot] = useState(true);
  const keeps = !!meta.data?.snapshots && finalSnapshot;

  const run = async (act: Act) => {
    const targets = chosen.filter(fits[act]);
    setRunning(act);
    const results = await Promise.allSettled(
      targets.map((s) => (act === "delete" ? api.del(serverPath(org, s.name) + (keeps ? "" : "?snapshot=false")) : api.post(`${serverPath(org, s.name)}/${act}`))),
    );
    await Promise.all([qc.invalidateQueries({ queryKey: keys.servers(org) }), act === "delete" ? qc.invalidateQueries({ queryKey: keys.snapshots(org) }) : null]);
    setRunning(null);
    setDeleting(false);
    const failed = targets.filter((_, i) => results[i].status === "rejected");
    const reasons = results.flatMap((r, i) => (r.status === "rejected" ? [`${targets[i].name}: ${r.reason instanceof ApiError ? r.reason.message : "it could not be reached"}`] : []));
    const succeeded = targets.length - failed.length;
    if (succeeded > 0) toast.info(`${done[act]} ${plural(succeeded, "server")}`);
    if (failed.length > 0) toast.error(`${failed.length} of ${targets.length} failed. ${reasons.join(" ")}`);
    // What failed stays selected, to be tried again or looked at.
    onDone(failed.map((s) => s.name));
  };

  const button = (act: Exclude<Act, "delete">, label: string, icon: React.ReactNode) => {
    const n = chosen.filter(fits[act]).length;
    return (
      <Button size="sm" icon={icon} disabled={n === 0 || (running !== null && running !== act)} loading={running === act} onClick={() => run(act)}>
        {label}
        {n > 0 && n < chosen.length && ` ${n} of ${chosen.length}`}
      </Button>
    );
  };
  return (
    <>
      {button("start", "Start", <Play className="size-3.5" />)}
      {button("restart", "Restart", <RotateCw className="size-3.5" />)}
      {button("stop", "Stop", <Square className="size-3" />)}
      {canAdmin && (
        <Button size="sm" variant="danger" icon={<Trash2 className="size-3.5" />} disabled={running !== null} onClick={() => setDeleting(true)}>
          Delete
        </Button>
      )}
      <Confirm
        open={deleting}
        onOpenChange={(open) => running === null && setDeleting(open)}
        title={`Delete ${plural(chosen.length, "server")}?`}
        description={
          <>
            <span className="font-medium text-fg">{chosen.map((s) => s.name).join(", ")}</span>.{" "}
            {keeps
              ? "Each is stopped and a last snapshot of it is taken, so it can be brought back from Backups."
              : "The world and every file of each are deleted with it. This cannot be undone."}
          </>
        }
        action={running === "delete" ? (keeps ? "Taking snapshots" : "Deleting") : `Delete ${plural(chosen.length, "server")}`}
        typeToConfirm={`delete ${plural(chosen.length, "server")}`}
        loading={running === "delete"}
        onConfirm={() => run("delete")}
      >
        {meta.data?.snapshots && <Toggle checked={finalSnapshot} onChange={setFinalSnapshot} disabled={running !== null} label="Take a final snapshot of each" />}
      </Confirm>
    </>
  );
}

export function Servers() {
  const { org = "" } = useParams();
  const membership = useOrg();
  const servers = useServers(org);
  const policy = useBackupPolicy(org);
  const canCreate = atLeast(membership?.role, "admin");
  const canOperate = atLeast(membership?.role, "operator");
  const selection = useSelection(servers.data?.map((s) => s.name) ?? [], org);
  const chosen = servers.data?.filter((s) => selection.has(s.name)) ?? [];

  const newButton = canCreate && (
    <Link to="new" className="inline-flex h-8 items-center gap-1.5 rounded-md bg-accent px-3 text-[13px] font-medium text-accent-ink hover:bg-accent-strong">
      <Plus className="size-4" />
      New server
    </Link>
  );

  return (
    <>
      <PageHeader title="Servers" description={membership?.name} actions={newButton} />
      {servers.isPending ? (
        <Loading />
      ) : servers.isError ? (
        <Empty title="Could not load servers">{servers.error.message}</Empty>
      ) : servers.data.length === 0 ? (
        <Empty title="No servers yet" action={newButton}>
          {canCreate ? "Create one from a game version or a modpack." : "An admin of this organization can create one."}
        </Empty>
      ) : (
        // Room for the bar that the selection brings up, so it covers nothing.
        // On a narrow screen its buttons wrap onto a second row.
        <div className={`overflow-hidden rounded-lg border border-line bg-surface ${selection.count > 0 ? "mb-16 max-md:mb-28" : ""}`}>
          <div className={`${columns} label-mono relative hidden h-9 border-b border-line md:grid ${canOperate ? "pl-10" : ""}`}>
            {canOperate && (
              <label className="absolute inset-y-0 left-0 flex w-10 cursor-pointer items-center justify-center">
                <RowCheckbox checked={selection.all} partial={selection.some} quiet={selection.count === 0} label="Select every server" onChange={selection.toggleAll} />
              </label>
            )}
            <span>Server</span>
            <span>Software</span>
            <span>Players</span>
            <span>Address</span>
            <span />
          </div>
          <ul>
            {servers.data.map((s) => (
              <li key={s.name} className={`group relative border-b border-line last:border-b-0 ${selection.has(s.name) ? "bg-accent-wash" : ""}`}>
                {canOperate && (
                  <label className="absolute inset-y-0 left-0 z-10 flex w-10 cursor-pointer items-center justify-center">
                    <RowCheckbox checked={selection.has(s.name)} quiet={selection.count === 0} label={`Select ${s.name}`} onChange={(range) => selection.toggle(s.name, range)} />
                  </label>
                )}
                <Link
                  to={`s/${s.name}`}
                  className={`${columns} min-h-14 py-2.5 text-[13px] transition-colors hover:bg-raised max-md:grid-cols-[minmax(0,1fr)_auto] ${canOperate ? "pl-10" : ""}`}
                >
                  <span className="flex min-w-0 items-center gap-3">
                    <StatusDot phase={s.status.phase} />
                    <Picture name={displayName(s)} url={serverIcon(s)} size={32} retryKey={s.status.phase} />
                    <span className="min-w-0">
                      <span className="block truncate font-medium">{displayName(s)}</span>
                      <span className="block truncate text-xs text-muted">
                        {phaseWords[s.status.phase ?? "Stopped"]}
                        {s.status.pendingRestart && <span className="text-warn"> · restart to apply changes</span>}
                        <BackupMark server={s} scheduled={!!policy.data && scheduleOf(policy.data, s.spec.backups).enabled} />
                      </span>
                    </span>
                  </span>
                  <span className="truncate text-muted max-md:hidden">{describeSoftware(s)}</span>
                  <span className="max-md:hidden">
                    <Players server={s} />
                  </span>
                  <span className="min-w-0 max-md:hidden">{s.status.address ? <CopyText value={s.status.address} className="text-muted" /> : <span className="text-faint">No address</span>}</span>
                  <span className="flex justify-end">{canOperate && <ServerActions server={s} compact />}</span>
                </Link>
              </li>
            ))}
          </ul>
        </div>
      )}
      <SelectionBar count={selection.count} noun="server" onClear={selection.clear}>
        <BulkActions org={org} chosen={chosen} canAdmin={canCreate} onDone={selection.keep} />
      </SelectionBar>
    </>
  );
}
