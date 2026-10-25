import { LayoutTemplate } from "lucide-react";
import { useState } from "react";
import { ServerSchedule } from "../../components/BackupSchedule";
import { SaveTemplate } from "../../components/SaveTemplate";
import { holdsTheServer, OperationRow, RestoreDialog, SnapshotRows, SnapshotSelectionBar } from "../../components/Snapshots";
import { Button, Empty, Loading, Notice } from "../../components/ui";
import { atLeast, type Snapshot } from "../../lib/api";
import { ago } from "../../lib/format";
import { useServers, useServerSnapshots, useTemplates } from "../../lib/queries";
import { useSelection } from "../../lib/selection";
import { useServerContext } from "./ServerPage";

export function Snapshots() {
  const { server, role } = useServerContext();
  const list = useServerSnapshots(server.org, server.name);
  const [restoring, setRestoring] = useState<Snapshot | null>(null);
  const canAdmin = atLeast(role, "admin");
  const [templating, setTemplating] = useState(false);
  const servers = useServers(server.org);
  const templates = useTemplates(server.org);

  const selection = useSelection(list.data?.snapshots.map((s) => s.id) ?? [], `${server.org}/${server.name}`);

  if (list.isPending) return <Loading />;
  if (!list.data) return <Notice tone="danger">Could not load snapshots.</Notice>;
  if (!list.data.available) {
    return <Notice>Snapshots are not set up on this panel.</Notice>;
  }
  const { snapshots, operations } = list.data;
  const op = operations[0];
  const failure = server.status.snapshots?.error;

  return (
    <div className="flex flex-col gap-4">
      {/* A snapshot is taken from the server's header, above every tab. */}
      <div className="flex items-center justify-between gap-4">
        <p className="text-[13px] text-muted">
          {snapshots.length === 0 ? "No snapshots yet." : `${snapshots.length} snapshot${snapshots.length === 1 ? "" : "s"}, the newest from ${ago(snapshots[0].time)}.`}
        </p>
        {/* Kept to make others like it. */}
        {canAdmin && (
          <Button size="sm" icon={<LayoutTemplate className="size-3.5" />} disabled={!!op} onClick={() => setTemplating(true)}>
            Save as template
          </Button>
        )}
      </div>
      <ServerSchedule server={server} canAdmin={canAdmin} />
      {failure && !op && <Notice tone="danger">The last snapshot or restore failed: {failure}</Notice>}
      {/* One that holds the server is shown above every tab. */}
      {op && !holdsTheServer(op) && <OperationRow op={op} />}
      {snapshots.length === 0 ? (
        !op && (
          <Empty title="Nothing saved yet">
            A snapshot saves this server's files and settings.
          </Empty>
        )
      ) : (
        <SnapshotRows org={server.org} snapshots={snapshots} canAdmin={canAdmin} busy={!!op} onRestore={setRestoring} selection={selection} />
      )}
      {canAdmin && <SnapshotSelectionBar org={server.org} snapshots={snapshots} selection={selection} />}

      <RestoreDialog server={server} snapshot={restoring} onClose={() => setRestoring(null)} />
      {canAdmin && <SaveTemplate org={server.org} open={templating} onOpenChange={setTemplating} server={server} servers={servers.data ?? []} templates={templates.data ?? []} />}
    </div>
  );
}
