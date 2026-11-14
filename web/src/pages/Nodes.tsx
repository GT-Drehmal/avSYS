import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Gauge } from "lucide-react";
import { useState } from "react";
import { Link } from "react-router";
import { Chart } from "../components/Chart";
import { Button, cx, Empty, Loading, Notice, PageHeader, Toggle } from "../components/ui";
import { api, ApiError, type NodeInfo, type NodeList } from "../lib/api";
import { ago } from "../lib/format";
import { useMe } from "../lib/queries";
import { toast } from "../lib/toast";

const gb = (bytes: number) => `${(bytes / 1024 ** 3).toFixed(1)} GB`;
const cores = (milli: number) => `${(milli / 1000).toFixed(milli < 10_000 ? 1 : 0)}`;
const players = (n: number) => `${n} ${n === 1 ? "player" : "players"}`;

/** What a node was using in all at one moment. */
interface NodeSample {
  t: string;
  /** Thousandths of a core. */
  cpu: number;
  memory: number;
}

interface NodesUsage {
  available: boolean;
  every: number;
  /** By node name, oldest first. */
  nodes: Record<string, NodeSample[]>;
}

/** A node's memory and CPU over the last hours, against what it has. */
function NodeCharts({ node, samples, every }: { node: NodeInfo; samples: NodeSample[]; every: number }) {
  // The moment the pointer is over: both charts mark it.
  const [hover, setHover] = useState<number | null>(null);
  const charts = { samples, every, hover, onHover: setHover, small: true, limitText: "in all", staleText: "when last measured" };
  return (
    <div className="mt-3 grid gap-3 sm:grid-cols-2">
      <Chart title="Memory" label={`Memory on ${node.name}`} {...charts} value={(s) => s.memory} format={gb} limit={node.memory} />
      <Chart title="CPU" label={`CPU on ${node.name}`} {...charts} value={(s) => s.cpu} format={(m) => `${cores(m)} cores`} limit={node.cores} />
    </div>
  );
}

/**
 * The cluster's nodes, for the panel's administrator: whether each is up,
 * how fast it is, how much memory it has left, and which servers run on it.
 */
export function Nodes() {
  const me = useMe().data;
  const qc = useQueryClient();
  const [asked, setAsked] = useState(false);
  const nodes = useQuery({
    queryKey: ["nodes", "all"],
    queryFn: () => api.get<NodeList>("/nodes"),
    // Looked at more often while it measures, to say when that is done.
    refetchInterval: (q) => (q.state.data?.measuring ? 3_000 : 15_000),
  });
  const usage = useQuery({
    queryKey: ["nodes", "usage"],
    queryFn: () => api.get<NodesUsage>("/nodes/usage"),
    enabled: !!me?.user.isAdmin,
    refetchInterval: 30_000,
  });
  if (me && !me.user.isAdmin) return <Empty title="Admins only">Only the panel's administrator can see nodes.</Empty>;
  if (nodes.isPending) return <Loading />;
  if (!nodes.data) return <Notice tone="danger">{nodes.error instanceof ApiError ? nodes.error.message : "Could not load nodes."}</Notice>;
  const { measuring, measured } = nodes.data;
  const all = nodes.data.nodes;
  // Across the cluster, where every node says what it is using.
  const known = all.every((n) => n.inUse) && all.length > 0;
  const total = (of: (n: NodeInfo) => number) => all.reduce((sum, n) => sum + of(n), 0);

  const measure = async () => {
    setAsked(true);
    try {
      await api.post("/nodes/measure");
      await qc.invalidateQueries({ queryKey: ["nodes"] });
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not start measuring.");
    } finally {
      setAsked(false);
    }
  };
  const setAutomatic = async (name: string, automatic: boolean) => {
    try {
      await api.patch(`/nodes/${name}`, { automatic });
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not change the node.");
    }
    await qc.invalidateQueries({ queryKey: ["nodes"] });
  };

  return (
    <>
      <PageHeader
        title="Nodes"
        description="New servers go to the fastest node with room."
        actions={
          <Button icon={<Gauge className="size-4" />} loading={asked || measuring} onClick={measure}>
            {measuring ? "Measuring…" : "Measure speed"}
          </Button>
        }
      />
      {measuring ? (
        <p className="mb-4 text-xs text-muted" role="status">
          Measuring every node. This takes a few minutes.
        </p>
      ) : (
        measured && (
          <p className="mb-4 text-xs text-muted" role="status">
            {measured.error ? `Could not measure: ${measured.error}` : `Measured ${ago(measured.at)}.`}
            {(measured.failed?.length ?? 0) > 0 && ` Could not measure ${measured.failed?.join(", ")}.`}
          </p>
        )
      )}
      <p className="mb-3 text-xs text-muted" data-testid="cluster-total">
        {all.length} {all.length === 1 ? "node" : "nodes"} · {players(all.reduce((sum, n) => sum + (n.players ?? 0), 0))}
        {known && ` · ${gb(total((n) => n.inUse?.memory ?? 0))} of ${gb(total((n) => n.memory))} memory in use`}
        {known && ` · ${cores(total((n) => n.inUse?.cpu ?? 0))} of ${cores(total((n) => n.cores))} cores`}
        {usage.data && !usage.data.available && ". This cluster does not report memory and CPU in use."}
      </p>
      <ul className="flex flex-col gap-3">
        {all.map((n) => {
          const used = n.memory > 0 ? Math.min(1, (n.memory - n.free) / n.memory) : 0;
          // Over the last hours, where there are some; else as it is now.
          const history = usage.data?.available ? (usage.data.nodes[n.name] ?? []) : [];
          return (
            <li key={n.name} className="rounded-lg border border-line bg-surface px-4 py-3">
              <div className="flex flex-wrap items-center justify-between gap-3">
                <div className="flex items-center gap-2.5">
                  <span className={cx("inline-block size-2 shrink-0 rounded-full", n.ready ? "bg-accent" : "bg-danger")} />
                  <span className="font-mono text-[13px] font-medium">{n.name}</span>
                  {!n.ready && <span className="text-xs text-muted">down</span>}
                </div>
                <div className="flex items-center gap-4 text-xs text-muted">
                  <span>{n.score > 0 ? `speed ${n.score}` : "speed not measured"}</span>
                  <span className="font-mono tabular-nums">
                    {gb(n.free)} free of {gb(n.memory)}
                  </span>
                </div>
              </div>
              <div className="mt-2 h-1 overflow-hidden rounded-full bg-raised" role="meter" aria-label={`Memory promised on ${n.name}`} aria-valuemin={0} aria-valuemax={100} aria-valuenow={Math.round(used * 100)}>
                <div className="h-full bg-accent" style={{ width: `${used * 100}%` }} />
              </div>
              <p className="mt-2 text-xs text-muted" data-testid="node-use">
                {n.inUse && history.length < 2 ? `In use: ${gb(n.inUse.memory)} memory, ${cores(n.inUse.cpu)} of ${cores(n.cores)} cores · ` : ""}
                {players(n.players ?? 0)}
              </p>
              {history.length >= 2 && <NodeCharts node={n} samples={history} every={usage.data?.every ?? 30} />}
              <div className="mt-3">
                <Toggle
                  checked={n.hosts}
                  onChange={(on) => setAutomatic(n.name, on)}
                  label={`New servers on ${n.name}`}
                  hint={n.hosts ? "Picked when this node is the fastest with room." : "Only when picked manually."}
                />
              </div>
              {(n.servers?.length ?? 0) > 0 ? (
                <ul className="mt-3 flex flex-wrap gap-2">
                  {n.servers?.map((s) => (
                    <li key={`${s.org}/${s.name}`}>
                      <Link to={`/o/${s.org}/s/${s.name}`} className="inline-flex items-center gap-1.5 rounded-md border border-line px-2 py-1 font-mono text-xs hover:bg-raised">
                        {s.org}/{s.name}
                        <span className="text-faint">
                          {s.memory}
                          {!!s.players && ` · ${players(s.players)}`}
                        </span>
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
    </>
  );
}
