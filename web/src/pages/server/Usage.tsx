import { useQuery } from "@tanstack/react-query";
import { Empty, Spinner } from "../../components/ui";
import { api, serverPath } from "../../lib/api";
import { bytes } from "../../lib/format";
import { useServerContext } from "./ServerPage";

interface Sample {
  t: string;
  /** Thousandths of a core. */
  cpu: number;
  memory: number;
  players: number;
}

interface UsageView {
  available: boolean;
  limit: number;
  every: number;
  samples: Sample[];
}

/** What a server's game has used over the last hours: memory, CPU and players. */
export function Usage() {
  const { server } = useServerContext();
  const usage = useQuery({
    queryKey: ["usage", server.org, server.name],
    queryFn: () => api.get<UsageView>(`${serverPath(server.org, server.name)}/usage`),
    refetchInterval: 30_000,
  });
  if (usage.isPending) {
    return (
      <div className="flex justify-center py-10">
        <Spinner />
      </div>
    );
  }
  if (usage.isError) return <Empty title="Could not load usage">{usage.error.message}</Empty>;
  const { available, limit, every, samples } = usage.data;
  if (!available) return <Empty title="Usage is not known">This cluster does not report what servers use.</Empty>;
  if (samples.length < 2) return <Empty title="Nothing yet">Usage shows here while the server runs.</Empty>;
  const cores = (m: number) => `${(m / 1000).toFixed(m < 10_000 ? 2 : 1)} cores`;
  return (
    <div className="flex flex-col gap-6">
      <p className="text-[13px] text-muted">The last {span(samples)}.</p>
      <Chart title="Memory" samples={samples} every={every} value={(s) => s.memory} format={bytes} limit={limit} />
      <Chart title="CPU" samples={samples} every={every} value={(s) => s.cpu} format={cores} />
      <Chart title="Players" samples={samples} every={every} value={(s) => s.players} format={(n) => String(n)} whole />
    </div>
  );
}

function span(samples: Sample[]) {
  const ms = new Date(samples[samples.length - 1].t).getTime() - new Date(samples[0].t).getTime();
  const minutes = Math.max(1, Math.round(ms / 60_000));
  if (minutes < 90) return `${minutes} minutes`;
  return `${Math.round(minutes / 60)} hours`;
}

const width = 600;
const height = 120;

interface ChartProps {
  title: string;
  samples: Sample[];
  /** Seconds between samples. */
  every: number;
  value: (s: Sample) => number;
  format: (n: number) => string;
  /** Drawn as a line: what the server may use. */
  limit?: number;
  /** Counted in whole numbers, so the scale is too. */
  whole?: boolean;
}

function Chart({ title, samples, every, value, format, limit, whole }: ChartProps) {
  const values = samples.map(value);
  const now = values[values.length - 1];
  const peak = Math.max(...values);
  const top = Math.max(limit ?? 0, peak, whole ? 1 : 0) * 1.1 || 1;
  const start = new Date(samples[0].t).getTime();
  const end = new Date(samples[samples.length - 1].t).getTime();
  const x = (s: Sample) => ((new Date(s.t).getTime() - start) / (end - start || 1)) * width;
  const y = (v: number) => height - (v / top) * height;
  // A run of samples with none missing, as while the server was up: a
  // stop leaves a gap rather than a line across it.
  const runs: number[][] = [];
  samples.forEach((s, i) => {
    const gap = i > 0 && new Date(s.t).getTime() - new Date(samples[i - 1].t).getTime() > every * 3000;
    if (i === 0 || gap) runs.push([]);
    runs[runs.length - 1].push(i);
  });
  const point = (i: number) => `${x(samples[i]).toFixed(1)},${y(values[i]).toFixed(1)}`;
  const line = runs.map((run) => run.map((i, j) => `${j === 0 ? "M" : "L"}${point(i)}`).join(" ")).join(" ");
  const area = runs.map((run) => `M${x(samples[run[0]]).toFixed(1)},${height} ${run.map((i) => `L${point(i)}`).join(" ")} L${x(samples[run[run.length - 1]]).toFixed(1)},${height} Z`).join(" ");
  return (
    <section aria-label={title} className="flex flex-col gap-2">
      <div className="flex items-baseline justify-between gap-3">
        <h2 className="text-[13px] font-medium">{title}</h2>
        <p className="text-xs text-muted">
          <span className="text-fg">{format(now)}</span> now · {format(peak)} at most
          {limit ? ` · ${format(limit)} allowed` : ""}
        </p>
      </div>
      <svg viewBox={`0 0 ${width} ${height}`} preserveAspectRatio="none" className="h-28 w-full rounded-md border border-line bg-surface" role="img" aria-label={`${title}: ${format(now)} now, ${format(peak)} at most`}>
        <path d={area} className="fill-accent/10" />
        <path d={line} className="fill-none stroke-accent" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
        {!!limit && <line x1={0} x2={width} y1={y(limit)} y2={y(limit)} className="stroke-warn" strokeWidth={1} strokeDasharray="4 4" vectorEffect="non-scaling-stroke" />}
      </svg>
    </section>
  );
}
