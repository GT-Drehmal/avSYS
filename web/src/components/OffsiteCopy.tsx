import { useQuery, useQueryClient } from "@tanstack/react-query";
import { api, ApiError, type Snapshot } from "../lib/api";
import { ago } from "../lib/format";
import { keys, useAction, useMe, useOffsite } from "../lib/queries";
import { toast } from "../lib/toast";
import { Button, Notice } from "./ui";

const count = (n: number) => `${n} snapshot${n === 1 ? "" : "s"}`;

/**
 * Whether an organization's snapshots are also somewhere else, and how
 * current that copy is. With no snapshots here and some there, whoever runs
 * the install can bring them back.
 */
export function OffsiteCopy({ org, canAdmin, empty }: { org: string; canAdmin: boolean; empty: boolean }) {
  const qc = useQueryClient();
  const offsite = useOffsite(org);
  const me = useMe().data;
  const configured = !!offsite.data?.configured;
  // Looking at what is there means reaching it, so only when it could matter.
  const lost = useQuery({
    queryKey: [...keys.offsite(org), "snapshots"],
    queryFn: () => api.get<{ snapshots: Snapshot[] }>(`/orgs/${org}/offsite/snapshots`),
    enabled: configured && empty && !!me?.user.isAdmin,
    retry: false,
  });
  const copy = useAction(() => api.post(`/orgs/${org}/offsite/run`), [keys.offsite(org)]);
  const fetch = useAction(() => api.post<{ fetched: number }>(`/orgs/${org}/offsite/fetch`), [keys.offsite(org), keys.snapshots(org)]);

  if (!offsite.data) return null;
  if (!configured) {
    return <Notice tone="info">Snapshots are only on this cluster, with no offsite copy.</Notice>;
  }
  const { error, pending, running, lastSuccess, snapshots = 0 } = offsite.data;
  const there = lost.data?.snapshots.length ?? 0;
  if (empty && there > 0) {
    return (
      <Notice
        tone="info"
        action={
          <Button
            size="sm"
            variant="primary"
            loading={fetch.isPending}
            onClick={() => fetch.mutate(undefined, { onSuccess: (r) => toast.info(`Brought back ${count(r.fetched)}`), onSettled: () => qc.invalidateQueries({ queryKey: keys.offsite(org) }) })}
          >
            Bring them back
          </Button>
        }
      >
        The offsite copy has {count(there)}, the newest from {ago(lost.data!.snapshots[0].time)}.
      </Notice>
    );
  }
  if (empty && lost.error) {
    return <Notice tone="danger">Could not read the offsite copy: {lost.error instanceof ApiError ? lost.error.message : "no answer."}</Notice>;
  }
  const again = canAdmin && (
    <Button size="sm" loading={copy.isPending || running} onClick={() => copy.mutate()}>
      Copy now
    </Button>
  );
  if (error && !running) {
    return (
      <Notice tone="danger" action={again}>
        Copying offsite is failing: {error} {lastSuccess ? `Last copied ${ago(lastSuccess)}.` : "Nothing has been copied yet."}
      </Notice>
    );
  }
  return (
    <div className="flex flex-wrap items-center justify-between gap-3 text-[13px] text-muted">
      <p>
        Offsite copy:{" "}
        {running
          ? "copying now."
          : pending
            ? lastSuccess
              ? `${count(snapshots)} there, newer ones waiting. Last copied ${ago(lastSuccess)}.`
              : "nothing copied yet."
            : `up to date, ${count(snapshots)}.`}
      </p>
      {(pending || running) && again}
    </div>
  );
}
