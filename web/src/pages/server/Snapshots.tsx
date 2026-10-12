import { useQueryClient } from "@tanstack/react-query";
import { Camera } from "lucide-react";
import { useState } from "react";
import { ServerSchedule } from "../../components/BackupSchedule";
import { OperationRow, SnapshotRows } from "../../components/Snapshots";
import { Button, Empty, Loading, Modal, Notice } from "../../components/ui";
import { api, atLeast, serverPath, type Operation, type Snapshot } from "../../lib/api";
import { ago } from "../../lib/format";
import { keys, useAction, useServerSnapshots } from "../../lib/queries";
import { useServerContext } from "./ServerPage";

export function Snapshots() {
  const { server, role } = useServerContext();
  const qc = useQueryClient();
  const list = useServerSnapshots(server.org, server.name);
  const [restoring, setRestoring] = useState<Snapshot | null>(null);
  const canOperate = atLeast(role, "operator");
  const canAdmin = atLeast(role, "admin");

  // Once something has been started the list is asked for again, and shows
  // it as running. The answer to the request is not put into the list
  // directly: an operation that failed at once may already have been
  // reported as over, and would then be shown as running for ever.
  const started = () => qc.invalidateQueries({ queryKey: keys.serverSnapshots(server.org, server.name) });
  const snapshot = useAction(() => api.post<Operation>(`${serverPath(server.org, server.name)}/snapshots`));
  const restore = useAction((id: string) => api.post<Operation>(`${serverPath(server.org, server.name)}/snapshots/${id}/restore`));

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
      <div className="flex flex-wrap items-center justify-between gap-3">
        <p className="text-[13px] text-muted">
          {snapshots.length === 0 ? "No snapshots yet." : `${snapshots.length} snapshot${snapshots.length === 1 ? "" : "s"}, the newest from ${ago(snapshots[0].time)}.`}
        </p>
        {canOperate && (
          <Button variant="primary" icon={<Camera className="size-4" />} disabled={!!op} loading={snapshot.isPending} onClick={() => snapshot.mutate(undefined, { onSuccess: started })}>
            Snapshot now
          </Button>
        )}
      </div>
      <ServerSchedule server={server} canAdmin={canAdmin} />
      {failure && !op && <Notice tone="danger">The last snapshot or restore failed: {failure}</Notice>}
      {op && <OperationRow op={op} />}
      {snapshots.length === 0 ? (
        !op && (
          <Empty title="Nothing saved yet">
            A snapshot saves this server's files and settings so they can be put back later.
          </Empty>
        )
      ) : (
        <SnapshotRows org={server.org} snapshots={snapshots} canAdmin={canAdmin} busy={!!op} onRestore={setRestoring} />
      )}

      <Modal
        open={!!restoring}
        onOpenChange={(open) => !open && setRestoring(null)}
        title={`Restore ${server.name}?`}
        description={restoring ? `To how it was ${ago(restoring.time)}, on ${new Date(restoring.time).toLocaleString()}.` : ""}
      >
        <div className="flex flex-col gap-4">
          <ul className="flex list-disc flex-col gap-1.5 pl-4 text-[13px] leading-relaxed text-muted">
            <li>The server is stopped while its files are put back{server.spec.state === "Stopped" ? "" : ", then started again"}.</li>
            <li>Its world, files, game version and mods return to how they were. Anything added since is removed.</li>
            <li>What is there now is saved as a snapshot first, so a restore can be undone.</li>
          </ul>
          <div className="flex justify-end gap-2">
            <Button onClick={() => setRestoring(null)}>Cancel</Button>
            <Button
              variant="danger"
              loading={restore.isPending}
              onClick={() =>
                restoring &&
                restore.mutate(restoring.id, {
                  onSuccess: () => {
                    started();
                    setRestoring(null);
                  },
                })
              }
            >
              Restore
            </Button>
          </div>
        </div>
      </Modal>
    </div>
  );
}
