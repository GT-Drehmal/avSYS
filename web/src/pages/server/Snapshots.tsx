import { useState } from "react";
import { ServerSchedule } from "../../components/BackupSchedule";
import { holdsTheServer, OperationRow, RestoreDialog, SnapshotRows, SnapshotSelectionBar } from "../../components/Snapshots";
import { Empty, Loading, Notice } from "../../components/ui";
import { atLeast, type Snapshot } from "../../lib/api";
import { ago } from "../../lib/format";
import { useServerSnapshots } from "../../lib/queries";
import { useSelection } from "../../lib/selection";
import { useServerContext } from "./ServerPage";

export function Snapshots() {
  const { server, role } = useServerContext();
  const list = useServerSnapshots(server.org, server.name);
  const [restoring, setRestoring] = useState<Snapshot | null>(null);
  const canAdmin = atLeast(role, "admin");

  const selection = useSelection(list.data?.snapshots.map((s) => s.id) ?? [], `${server.org}/${server.name}`);

  if (list.isPending) return <Loading />;
  if (!list.data) return <Notice tone="danger">The snapshots could not be loaded.</Notice>;
  if (!list.data.available) {
    return <Notice>Snapshots are not available: restic is not installed on the panel.</Notice>;
  }
  const { snapshots, operations } = list.data;
  const op = operations[0];
  const failure = server.status.snapshots?.error;

  return (
    <div className="flex flex-col gap-4">
      {/* A snapshot is taken from the server's header, above every tab. */}
      <p className="text-[13px] text-muted">
        {snapshots.length === 0 ? "No snapshots yet." : `${snapshots.length} snapshot${snapshots.length === 1 ? "" : "s"}, the newest from ${ago(snapshots[0].time)}.`}
      </p>
      <ServerSchedule server={server} canAdmin={canAdmin} />
      {failure && !op && <Notice tone="danger">The last snapshot or restore failed: {failure}</Notice>}
      {/* One that holds the server is shown above every tab. */}
      {op && !holdsTheServer(op) && <OperationRow op={op} />}
      {snapshots.length === 0 ? (
        !op && (
          <Empty title="Nothing saved yet">
            A snapshot saves this server's files and settings so they can be put back later.
          </Empty>
        )
      ) : (
        <SnapshotRows org={server.org} snapshots={snapshots} canAdmin={canAdmin} busy={!!op} onRestore={setRestoring} selection={selection} />
      )}
      {canAdmin && <SnapshotSelectionBar org={server.org} snapshots={snapshots} selection={selection} />}

      <RestoreDialog server={server} snapshot={restoring} onClose={() => setRestoring(null)} />
    </div>
  );
}
