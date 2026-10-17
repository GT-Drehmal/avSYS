import { useQuery } from "@tanstack/react-query";
import { Link } from "react-router";
import { cx, Empty, Loading, Notice, PageHeader } from "../components/ui";
import { api, ApiError, type NodeInfo } from "../lib/api";
import { useMe } from "../lib/queries";

const gb = (bytes: number) => `${(bytes / 1024 ** 3).toFixed(1)} GB`;

/**
 * The cluster's nodes, for the panel's administrator: whether each is up,
 * how fast it is, how much memory it has left, and which servers run on it.
 */
export function Nodes() {
  const me = useMe().data;
  const nodes = useQuery({ queryKey: ["nodes", "all"], queryFn: () => api.get<{ nodes: NodeInfo[] }>("/nodes"), refetchInterval: 15_000 });
  if (me && !me.user.isAdmin) return <Empty title="Admins only">Only the panel's administrator can see nodes.</Empty>;
  if (nodes.isPending) return <Loading />;
  if (!nodes.data) return <Notice tone="danger">{nodes.error instanceof ApiError ? nodes.error.message : "Could not load nodes."}</Notice>;
  return (
    <>
      <PageHeader title="Nodes" description="New servers go to the fastest node with room." />
      <ul className="flex flex-col gap-3">
        {nodes.data.nodes.map((n) => {
          const used = n.memory > 0 ? Math.min(1, (n.memory - n.free) / n.memory) : 0;
          return (
            <li key={n.name} className="rounded-lg border border-line bg-surface px-4 py-3">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div className="flex items-center gap-2.5">
                  <span className={cx("inline-block size-2 shrink-0 rounded-full", n.ready ? "bg-accent" : "bg-danger")} />
                  <span className="font-mono text-[13px] font-medium">{n.name}</span>
                  <span className="text-xs text-muted">{n.ready ? (n.automatic ? "takes new servers" : "only when chosen") : "down"}</span>
                </div>
                <div className="flex items-center gap-4 text-xs text-muted">
                  <span>{n.score > 0 ? `speed ${n.score.toFixed(2)}` : "speed not measured"}</span>
                  <span className="font-mono tabular-nums">
                    {gb(n.free)} free of {gb(n.memory)}
                  </span>
                </div>
              </div>
              <div className="mt-2 h-1 overflow-hidden rounded-full bg-raised" role="meter" aria-label={`Memory promised on ${n.name}`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(used * 100)}>
                <div className="h-full bg-accent" style={{ width: `${used * 100}%` }} />
              </div>
              {(n.servers?.length ?? 0) > 0 ? (
                <ul className="mt-3 flex flex-wrap gap-2">
                  {n.servers?.map((s) => (
                    <li key={`${s.org}/${s.name}`}>
                      <Link to={`/o/${s.org}/s/${s.name}`} className="inline-flex items-center gap-1.5 rounded-md border border-line px-2 py-1 font-mono text-xs hover:bg-raised">
                        {s.org}/{s.name}
                        <span className="text-faint">{s.memory}</span>
                      </Link>
                    </li>
                  ))}
                </ul>
              ) : (
                <p className="mt-3 text-xs text-muted">No servers.</p>
              )}
            </li>
          );
        })}
      </ul>
      <p className="mt-6 text-xs leading-relaxed text-muted">
        To keep new servers off a node, run <span className="font-mono">kubectl label node NAME avsys.dev/game-servers=false</span>. Measure speed with <span className="font-mono">hack/rank-nodes.sh</span>.
      </p>
    </>
  );
}
