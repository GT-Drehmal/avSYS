import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { Chart, span } from "../../components/Chart";
import { Empty, Spinner } from "../../components/ui";
import { api, serverPath } from "../../lib/api";
import { bytes } from "../../lib/format";
import { useServerContext } from "./ServerPage";

interface Sample {
  t: string;
  /** Thousandths of a core. Missing where the cluster does not say. */
  cpu?: number;
  memory?: number;
  players: number;
  /** Ticks the game ran a second, and those it aims for. Missing where the game does not say. */
  tps?: number;
  tickTarget?: number;
  /** Milliseconds the game took over each tick, where it says. */
  mspt?: number;
}

interface UsageView {
  available: boolean;
  limit: number;
  /** Thousandths of a core, where the server's CPU is limited. */
  cpuLimit?: number;
  /** The game, asked, did not say how fast it ticks. */
  noTicks?: boolean;
  every: number;
  samples: Sample[];
}

const cores = (m: number) => `${(m / 1000).toFixed(m < 10_000 ? 2 : 1)} cores`;
const rate = (n: number) => n.toFixed(1);
const ms = (n: number) => `${n.toFixed(n < 10 ? 1 : 0)} ms`;

/** What a server's game has used over the last hours: memory, CPU, how fast it ticks, and players. */
export function Usage() {
  const { server } = useServerContext();
  const usage = useQuery({
    queryKey: ["usage", server.org, server.name],
    queryFn: () => api.get<UsageView>(`${serverPath(server.org, server.name)}/usage`),
    refetchInterval: 30_000,
  });
  // The moment the pointer is over, on any chart: all of them mark it.
  const [hover, setHover] = useState<number | null>(null);
  if (usage.isPending) {
    return (
      <div className="flex justify-center py-10">
        <Spinner />
      </div>
    );
  }
  if (usage.isError) return <Empty title="Could not load usage">{usage.error.message}</Empty>;
  const { available, limit, cpuLimit, noTicks, every } = usage.data;
  const samples = usage.data.samples ?? [];
  if (samples.length < 2) return <Empty title="Nothing yet">Usage shows here while the server runs.</Empty>;
  const charts = { samples, every, hover, onHover: setHover };
  // The rate the game last said it aims for: 20 unless changed.
  const target = samples.findLast((s) => s.tickTarget)?.tickTarget ?? 20;
  const ticks = samples.some((s) => s.tps !== undefined);
  return (
    <div className="flex flex-col gap-6">
      <p className="text-[13px] text-muted">
        The last {span(samples)}.{!available && " This cluster does not report memory and CPU."}
      </p>
      {available && <Chart title="Memory" {...charts} value={(s) => s.memory} format={bytes} limit={limit} />}
      {available && <Chart title="CPU" {...charts} value={(s) => s.cpu} format={cores} limit={cpuLimit} />}
      {ticks && <Chart title="Ticks per second" {...charts} value={(s) => s.tps} format={rate} limit={target} limitText="target" lowest />}
      {ticks && <Chart title="Time per tick" {...charts} value={(s) => s.mspt} format={ms} limit={1000 / target} />}
      {!ticks && noTicks && (
        <section aria-label="Ticks per second" className="flex flex-col gap-2">
          <h2 className="text-[13px] font-medium">Ticks per second</h2>
          <p className="text-xs text-muted">Not reported by this server.</p>
        </section>
      )}
      <Chart title="Players" {...charts} value={(s) => s.players} format={(n) => String(n)} whole />
    </div>
  );
}
