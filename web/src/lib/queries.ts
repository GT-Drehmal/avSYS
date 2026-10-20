import { QueryClient, useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";
import { api, ApiError, serverPath, type BackupPolicy, type Me, type Meta, type Offsite, type Operation, type OperationDone, type Server, type SnapshotList, type SpecPatch } from "./api";
import { toast } from "./toast";

export const queryClient = new QueryClient({
  defaultOptions: {
    queries: {
      // Server state arrives over the event stream, so cached data is
      // trusted until an event or a mutation says otherwise.
      staleTime: 30_000,
      retry: (count, err) => !(err instanceof ApiError && err.status >= 400 && err.status < 500) && count < 2,
      refetchOnWindowFocus: false,
    },
  },
});

export const keys = {
  meta: ["meta"] as const,
  me: ["me"] as const,
  servers: (org: string) => ["servers", org] as const,
  members: (org: string) => ["members", org] as const,
  invites: (org: string) => ["invites", org] as const,
  snapshots: (org: string) => ["snapshots", org] as const,
  serverSnapshots: (org: string, server: string) => ["snapshots", org, server] as const,
  /** What is being done to a server's snapshots now, as the event stream says, or null. */
  running: (org: string, server: string) => ["running", org, server] as const,
  backupPolicy: (org: string) => ["backup-policy", org] as const,
  offsite: (org: string) => ["offsite", org] as const,
};

export const useMeta = () => useQuery({ queryKey: keys.meta, queryFn: () => api.get<Meta>("/meta"), staleTime: Infinity });

export const useMe = () =>
  useQuery({
    queryKey: keys.me,
    queryFn: async () => {
      try {
        return await api.get<Me>("/me");
      } catch (err) {
        // Signed out is an answer, not a failure.
        if (err instanceof ApiError && err.status === 401) return null;
        throw err;
      }
    },
    staleTime: 60_000,
  });

export const useServers = (org: string) =>
  useQuery({ queryKey: keys.servers(org), queryFn: () => api.get<Server[]>(`/orgs/${org}/servers`) });

/** One server, read from the organization's list so both views share a cache entry. */
export function useServer(org: string, name: string) {
  const q = useServers(org);
  return { ...q, data: q.data?.find((s) => s.name === name) };
}

// A response to a change and the event stream both report server state, and
// either can arrive first. Revisions are Kubernetes resource versions, which
// are integers that only grow.
function isOlder(incoming: Server, held: Server) {
  try {
    return BigInt(incoming.revision) < BigInt(held.revision);
  } catch {
    return false;
  }
}

function upsert(list: Server[] | undefined, server: Server) {
  if (!list) return list;
  const i = list.findIndex((s) => s.name === server.name);
  if (i === -1) return [...list, server].sort((a, b) => a.name.localeCompare(b.name));
  if (isOlder(server, list[i])) return list;
  const next = list.slice();
  next[i] = server;
  return next;
}

export const applyServer = (qc: QueryClient, server: Server) =>
  qc.setQueryData<Server[]>(keys.servers(server.org), (list) => upsert(list, server));

/**
 * Keeps server lists current from the panel's event stream. Mounted once for
 * the whole signed-in app; nothing polls.
 */
export function useLiveServers() {
  const qc = useQueryClient();
  useEffect(() => {
    // Everything the stream keeps current: servers, and snapshots with the
    // operations running on them.
    const refetchAll = () => {
      // What was running may have finished unheard: it is asked for again.
      qc.invalidateQueries({ queryKey: ["running"] });
      // Lists kept from a page left earlier are not asked for again until
      // it is opened; what they say is running may have finished too.
      qc.setQueriesData<SnapshotList>({ queryKey: ["snapshots"] }, (list) => list && { ...list, operations: [] });
      qc.invalidateQueries({ queryKey: ["servers"] });
      qc.invalidateQueries({ queryKey: ["snapshots"] });
      qc.invalidateQueries({ queryKey: ["offsite"] });
      // A move may have finished unheard.
      qc.invalidateQueries({ queryKey: ["placement"] });
      qc.invalidateQueries({ queryKey: ["nodes"] });
    };
    const source = new EventSource("/api/v1/events");
    let opened = false;
    source.onopen = () => {
      // Anything that changed while disconnected was missed, so refetch.
      if (opened) refetchAll();
      opened = true;
    };
    source.addEventListener("server.updated", (e) => applyServer(qc, JSON.parse(e.data)));
    source.addEventListener("server.deleted", (e) => {
      const gone: Server = JSON.parse(e.data);
      qc.setQueryData<Server[]>(keys.servers(gone.org), (list) => list?.filter((s) => s.name !== gone.name));
    });
    source.addEventListener("resync", refetchAll);
    source.addEventListener("operation.progress", (e) => applyOperation(qc, JSON.parse(e.data)));
    source.addEventListener("operation.done", (e) => {
      const done: OperationDone = JSON.parse(e.data);
      // Over: taken out of every list that shows it, also those not asked
      // for again below because no page has them open.
      const without = (list: SnapshotList | undefined) => list && { ...list, operations: list.operations.filter((o) => o.server !== done.server) };
      qc.setQueryData<SnapshotList>(keys.snapshots(done.org), without);
      qc.setQueryData<SnapshotList>(keys.serverSnapshots(done.org, done.server), without);
      // Only the end of what was running: a snapshot deleted meanwhile is
      // done too, with the server's snapshot still going.
      qc.cancelQueries({ queryKey: keys.running(done.org, done.server) });
      qc.setQueryData<Operation | null>(keys.running(done.org, done.server), (running) => (running && running.kind !== done.kind ? running : null));
      // Both lists are refetched: the snapshot is new to them, or gone.
      qc.invalidateQueries({ queryKey: keys.snapshots(done.org) });
      // And it is new to the offsite copy, or still to be removed there.
      qc.invalidateQueries({ queryKey: keys.offsite(done.org) });
      if (done.calledOff || (done.scheduled && !done.error)) return;
      if (done.kind === "create") {
        if (done.error) toast.error(`${done.server}: ${done.error}`);
        else toast.info(`${done.server} is ready`);
      } else if (done.kind === "delete-server") {
        // Only ever sent with an error, which says the whole of it.
        if (done.error) toast.error(done.error);
      } else if (done.kind === "import") {
        // The page that sent it says how it went.
      } else if (done.kind === "move") {
        qc.invalidateQueries({ queryKey: ["placement", done.org, done.server] });
        qc.invalidateQueries({ queryKey: ["nodes"] });
        if (done.error) toast.error(`Moving ${done.server} failed: ${done.error}`);
        else toast.info(`${done.server} was moved`);
      } else if (done.error) toast.error(`${done.kind === "restore" ? "Restore" : "Snapshot"} of ${done.server} failed: ${done.error}`);
      else if (done.kind === "restore") toast.info(`${done.server} was restored`);
      else if (done.kind === "snapshot") toast.info(`Snapshot of ${done.server} taken`);
    });
    source.addEventListener("offsite.done", (e) => {
      const done: { org: string } = JSON.parse(e.data);
      qc.invalidateQueries({ queryKey: keys.offsite(done.org) });
      // After snapshots were brought back there are some where there were none.
      qc.invalidateQueries({ queryKey: keys.snapshots(done.org) });
    });
    return () => source.close();
  }, [qc]);
}

/**
 * Puts a running operation's progress into every snapshot list that shows it,
 * and notes it for the server whether or not any list of it was loaded.
 */
function applyOperation(qc: QueryClient, op: Operation) {
  // Newer than any answer still on its way, which is not to overwrite it.
  qc.cancelQueries({ queryKey: keys.running(op.org, op.server) });
  qc.setQueryData<Operation | null>(keys.running(op.org, op.server), op);
  const merge = (list: SnapshotList | undefined) =>
    list && { ...list, operations: [...list.operations.filter((o) => o.server !== op.server), op] };
  qc.setQueryData<SnapshotList>(keys.snapshots(op.org), merge);
  qc.setQueryData<SnapshotList>(keys.serverSnapshots(op.org, op.server), merge);
}

/**
 * What is being done to a server's snapshots now: asked of the panel each
 * time a page of it opens, which is quick, and kept current by the event
 * stream meanwhile. An event cancels an answer still on its way, which is
 * older than it.
 */
export const useRunning = (org: string, server: string) =>
  useQuery({ queryKey: keys.running(org, server), queryFn: () => api.get<Operation | null>(`${serverPath(org, server)}/operation`), refetchOnMount: "always" }).data ?? null;

export const useOrgSnapshots = (org: string) =>
  useQuery({ queryKey: keys.snapshots(org), queryFn: () => api.get<SnapshotList>(`/orgs/${org}/snapshots`) });

export const useBackupPolicy = (org: string) =>
  useQuery({ queryKey: keys.backupPolicy(org), queryFn: () => api.get<BackupPolicy>(`/orgs/${org}/backup-policy`) });

export const useOffsite = (org: string) => useQuery({ queryKey: keys.offsite(org), queryFn: () => api.get<Offsite>(`/orgs/${org}/offsite`) });

export const useServerSnapshots = (org: string, server: string, enabled = true) =>
  useQuery({ queryKey: keys.serverSnapshots(org, server), queryFn: () => api.get<SnapshotList>(`${serverPath(org, server)}/snapshots`), enabled });

const onError = (err: unknown) => toast.error(err instanceof Error ? err.message : "Something went wrong.");

export function useServerAction(org: string, name: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (action: "start" | "stop" | "restart") => api.post<Server>(`${serverPath(org, name)}/${action}`),
    onSuccess: (server) => applyServer(qc, server),
    onError,
  });
}

export function useUpdateServer(org: string, name: string) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: (spec: SpecPatch) => api.patch<Server>(serverPath(org, name), { spec }),
    onSuccess: (server) => applyServer(qc, server),
    onError,
  });
}

/** A mutation that reports failures as a toast and refreshes the given queries. */
export function useAction<TArgs, TResult>(fn: (args: TArgs) => Promise<TResult>, invalidate: readonly (readonly unknown[])[] = []) {
  const qc = useQueryClient();
  return useMutation({
    mutationFn: fn,
    onSuccess: () => Promise.all(invalidate.map((queryKey) => qc.invalidateQueries({ queryKey }))),
    onError,
  });
}
