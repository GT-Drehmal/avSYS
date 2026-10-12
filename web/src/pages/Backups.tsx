import { Link, useParams } from "react-router";
import { useOrg } from "../components/Shell";
import { BackupSchedule } from "../components/BackupSchedule";
import { OffsiteCopy } from "../components/OffsiteCopy";
import { OperationRow, SnapshotRows } from "../components/Snapshots";
import { Chip, Empty, Loading, Notice, PageHeader } from "../components/ui";
import { atLeast, type Snapshot } from "../lib/api";
import { ago, bytes } from "../lib/format";
import { useOrgSnapshots, useServers } from "../lib/queries";

/** Every snapshot in the organization, grouped by the server it is of. */
export function Backups() {
  const { org = "" } = useParams();
  const membership = useOrg();
  const list = useOrgSnapshots(org);
  const servers = useServers(org);
  const canAdmin = atLeast(membership?.role, "admin");

  if (list.isPending) return <Loading />;
  const header = <PageHeader title="Backups" description={membership?.name} />;
  if (!list.data) {
    return (
      <>
        {header}
        <Notice tone="danger">The snapshots could not be loaded.</Notice>
      </>
    );
  }
  if (!list.data.available) {
    return (
      <>
        {header}
        <Notice>Snapshots are not available: restic is not installed on the panel.</Notice>
      </>
    );
  }
  const { snapshots, operations, usage } = list.data;
  const existing = new Set(servers.data?.map((s) => s.name));
  // Newest first within a server, and servers in order of their newest.
  const groups = new Map<string, Snapshot[]>();
  for (const s of snapshots) groups.set(s.server, [...(groups.get(s.server) ?? []), s]);

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
        <BackupSchedule org={org} canAdmin={canAdmin} />
        {operations.map((op) => (
          <OperationRow key={op.server} op={op} />
        ))}
        {snapshots.length === 0 && operations.length === 0 && (
          <Empty title="Nothing saved yet">Take one from a server's Backups tab.</Empty>
        )}
        {[...groups].map(([name, items]) => (
          <section key={name} className="flex flex-col gap-2.5">
            <h2 className="flex items-center gap-2 text-[13px] font-semibold">
              {existing.has(name) ? (
                <Link to={`/o/${org}/s/${name}/backups`} className="hover:text-accent">
                  {name}
                </Link>
              ) : (
                <>
                  {name}
                  {servers.data && <Chip>deleted server</Chip>}
                </>
              )}
              <span className="font-normal text-faint">newest {ago(items[0].time)}</span>
            </h2>
            <SnapshotRows org={org} snapshots={items} canAdmin={canAdmin} />
          </section>
        ))}
      </div>
    </>
  );
}
