import { useQuery } from "@tanstack/react-query";
import { ChevronLeft } from "lucide-react";
import { Link } from "react-router";
import { Loading, Notice, PageHeader } from "../components/ui";
import { api, ApiError, type SnapshotDetail } from "../lib/api";
import type { Origin } from "../lib/origin";
import { useOrgSnapshots, useServers } from "../lib/queries";
import { ServerForm, type Source } from "./ServerForm";

/**
 * Making a server out of one that exists or a snapshot: the server form,
 * filled in with what the original has, once that has been read.
 */
export function NewFromSource({ org, origin }: { org: string; origin: Origin }) {
  const servers = useServers(org);
  // Names that snapshots are under go with them: a new server cannot take one.
  const snapshots = useOrgSnapshots(org);
  const snapshot = useQuery({
    queryKey: ["snapshot", org, origin.kind === "snapshot" ? origin.id : ""],
    queryFn: () => api.get<SnapshotDetail>(`/orgs/${org}/snapshots/${origin.kind === "snapshot" ? origin.id : ""}`),
    enabled: origin.kind === "snapshot",
    retry: false,
  });
  const back = (
    <Link to={`/o/${org}`} className="mb-3 inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
      <ChevronLeft className="size-3.5" />
      Servers
    </Link>
  );
  if (servers.isPending || snapshots.isPending || (origin.kind === "snapshot" && snapshot.isPending)) return <Loading />;
  // Without the list of servers it cannot be said which names are taken, or
  // whether the original still exists: nothing is guessed from its absence.
  if (!servers.data) {
    return (
      <>
        {back}
        <PageHeader title="New server" />
        <Notice tone="danger">{servers.error instanceof ApiError ? servers.error.message : "Could not load servers."}</Notice>
      </>
    );
  }
  const taken = new Set(servers.data.map((s) => s.name));
  // Without the snapshots (backups switched off, say) none are known of: the
  // panel says so if a name turns out to have some.
  const history = new Set((snapshots.data?.snapshots ?? []).map((s) => s.server));
  const live = origin.kind === "server" ? servers.data?.find((s) => s.name === origin.name) : undefined;
  const source: Source | null =
    origin.kind === "server"
      ? live
        ? { origin, name: live.name, spec: live.spec, size: 0, live }
        : null
      : snapshot.data
        ? { origin, name: snapshot.data.server, spec: snapshot.data.spec, size: snapshot.data.size, taken: snapshot.data.time, live: servers.data?.find((s) => s.name === snapshot.data.server) }
        : null;
  if (!source) {
    return (
      <>
        {back}
        <PageHeader title="New server" />
        <Notice tone="danger">
          {origin.kind === "server"
            ? `There is no server named ${origin.name} to copy.`
            : snapshot.error instanceof ApiError
              ? snapshot.error.message
              : "Could not load the snapshot."}
        </Notice>
      </>
    );
  }
  return <ServerForm key={`${origin.kind}:${source.name}`} org={org} source={source} taken={taken} history={history} />;
}
