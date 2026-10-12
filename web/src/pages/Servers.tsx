import { Plus } from "lucide-react";
import { Link, useParams } from "react-router";
import { scheduleOf } from "../components/BackupSchedule";
import { ServerActions } from "../components/ServerActions";
import { useOrg } from "../components/Shell";
import { CopyText, Empty, PageHeader, StatusDot, Loading } from "../components/ui";
import { atLeast, type Server } from "../lib/api";
import { ago, describeSoftware, displayName } from "../lib/format";
import { useBackupPolicy, useServers } from "../lib/queries";

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

const columns = "grid grid-cols-[minmax(0,1.6fr)_minmax(0,1fr)_72px_minmax(0,1.3fr)_88px] items-center gap-4 px-4";

export function Servers() {
  const { org = "" } = useParams();
  const membership = useOrg();
  const servers = useServers(org);
  const policy = useBackupPolicy(org);
  const canCreate = atLeast(membership?.role, "admin");
  const canOperate = atLeast(membership?.role, "operator");

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
        <div className="overflow-hidden rounded-lg border border-line bg-surface">
          <div className={`${columns} label-mono hidden h-9 border-b border-line md:grid`}>
            <span>Server</span>
            <span>Software</span>
            <span>Players</span>
            <span>Address</span>
            <span />
          </div>
          <ul>
            {servers.data.map((s) => (
              <li key={s.name} className="border-b border-line last:border-b-0">
                <Link to={`s/${s.name}`} className={`${columns} min-h-14 py-2.5 text-[13px] transition-colors hover:bg-raised max-md:grid-cols-[minmax(0,1fr)_auto]`}>
                  <span className="flex min-w-0 items-center gap-3">
                    <StatusDot phase={s.status.phase} />
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
    </>
  );
}
