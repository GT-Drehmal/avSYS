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
}

interface UsageView {
  available: boolean;
  limit: number;
  /** Thousandths of a core, where the server's CPU is limited. */
  cpuLimit?: number;
  every: number;
  samples: Sample[];
}

const cores = (m: number) => `${(m / 1000).toFixed(m < 10_000 ? 2 : 1)} cores`;

/** What a server's game has used over the last hours: memory, CPU and players. */
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
  const { available, limit, cpuLimit, every } = usage.data;
  const samples = usage.data.samples ?? [];
  if (samples.length < 2) return <Empty title="Nothing yet">Usage shows here while the server runs.</Empty>;
  const charts = { samples, every, hover, onHover: setHover };
  return (
    <div className="flex flex-col gap-6">
      <p className="text-[13px] text-muted">
        The last {span(samples)}.{!available && " This cluster does not report memory and CPU."}
      </p>
      {available && <Chart title="Memory" {...charts} value={(s) => s.memory} format={bytes} limit={limit} />}
      {available && <Chart title="CPU" {...charts} value={(s) => s.cpu} format={cores} limit={cpuLimit} />}
      <Chart title="Players" {...charts} value={(s) => s.players} format={(n) => String(n)} whole />
    </div>
  );
}
