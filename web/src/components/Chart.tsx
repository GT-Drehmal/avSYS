import { cx } from "./ui";

/** One moment of something measured over time. */
export interface Timed {
  t: string;
}

/** How long a run of samples covers, in words: "40 minutes", "2 hours". */
export function span(samples: Timed[]) {
  const ms = new Date(samples[samples.length - 1].t).getTime() - new Date(samples[0].t).getTime();
  const minutes = Math.max(1, Math.round(ms / 60_000));
  if (minutes < 90) return `${minutes} minutes`;
  return `${Math.round(minutes / 60)} hours`;
}

const width = 600;
const height = 120;

interface ChartProps<T extends Timed> {
  title: string;
  /** What the chart is called to a screen reader, where the title alone is not enough. */
  label?: string;
  samples: T[];
  /** Seconds between samples. */
  every: number;
  value: (s: T) => number | undefined;
  format: (n: number) => string;
  /** Drawn as a dashed line: what the server may use, or what a node has. */
  limit?: number;
  /** Said after the limit's figure. */
  limitText?: string;
  /** Said of the last figure where it is old: no longer measured. */
  staleText?: string;
  /** Counted in whole numbers, so the scale is too. */
  whole?: boolean;
  /** Lower is worse: the lowest figure is said rather than the highest. */
  lowest?: boolean;
  /** A shorter chart, for several side by side. */
  small?: boolean;
  /** The moment the pointer is over, in milliseconds. */
  hover: number | null;
  onHover: (at: number | null) => void;
}

/**
 * A figure over time, as a line over a shaded area, with the moment pointed
 * at marked. Charts given the same hover mark the same moment.
 */
export function Chart<T extends Timed>({ title, label, samples: all, every, value, format, limit, limitText = "allowed", staleText = "when the server last ran", whole, lowest, small, hover, onHover }: ChartProps<T>) {
  // Only the samples that have this figure: memory and CPU are missing from
  // those taken while the cluster did not say.
  const samples = all.filter((s) => value(s) !== undefined);
  if (samples.length === 0) return null;
  const values = samples.map((s) => value(s) ?? 0);
  const now = values[values.length - 1];
  // No longer measured, as a server that is not running now.
  const when = Date.now() - new Date(samples[samples.length - 1].t).getTime() > every * 3000 ? staleText : "now";
  const peak = Math.max(...values);
  // The figure to watch: the highest, or where lower is worse, the lowest.
  const worst = lowest ? Math.min(...values) : peak;
  const worstText = lowest ? "lowest" : "at most";
  const top = Math.max(limit ?? 0, peak, whole ? 1 : 0) * 1.1 || 1;
  // Over the same hours as the other charts, so they line up.
  const start = new Date(all[0].t).getTime();
  const end = new Date(all[all.length - 1].t).getTime();
  const x = (s: T) => ((new Date(s.t).getTime() - start) / (end - start || 1)) * width;
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
  // The sample nearest the moment pointed at, unless that is in a gap.
  let marked = -1;
  if (hover !== null) {
    for (let i = 0; i < samples.length; i++) {
      if (marked === -1 || Math.abs(new Date(samples[i].t).getTime() - hover) < Math.abs(new Date(samples[marked].t).getTime() - hover)) marked = i;
    }
    if (Math.abs(new Date(samples[marked].t).getTime() - hover) > every * 1500) marked = -1;
  }
  const area = runs.map((run) => `M${x(samples[run[0]]).toFixed(1)},${height} ${run.map((i) => `L${point(i)}`).join(" ")} L${x(samples[run[run.length - 1]]).toFixed(1)},${height} Z`).join(" ");
  const name = label ?? title;
  return (
    <section aria-label={name} className="flex min-w-0 flex-col gap-2">
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5">
        <h2 className={cx("font-medium", small ? "text-xs" : "text-[13px]")}>{title}</h2>
        <p className="text-xs text-muted">
          <span className="text-fg">{format(now)}</span> {when} · {format(worst)} {worstText}
          {limit ? (
            <>
              {" · "}
              <span className="whitespace-nowrap">
                <span aria-hidden className="mr-1 inline-block w-3 border-t border-dashed border-warn align-middle" />
                {format(limit)} {limitText}
              </span>
            </>
          ) : null}
        </p>
      </div>
      <div
        className="relative"
        onPointerMove={(e) => {
          const box = e.currentTarget.getBoundingClientRect();
          onHover(start + Math.min(1, Math.max(0, (e.clientX - box.left) / box.width)) * (end - start));
        }}
        // A finger lifted leaves the mark where it was.
        onPointerLeave={(e) => e.pointerType === "mouse" && onHover(null)}
      >
        <svg
          viewBox={`0 0 ${width} ${height}`}
          preserveAspectRatio="none"
          className={cx("w-full rounded-md border border-line bg-surface", small ? "h-16" : "h-28")}
          role="img"
          aria-label={`${name}: ${format(now)} ${when}, ${format(worst)} ${worstText}${limit ? `, ${format(limit)} ${limitText}` : ""}`}
        >
          <path d={area} className="fill-accent/10" />
          <path d={line} className="fill-none stroke-accent" strokeWidth={1.5} vectorEffect="non-scaling-stroke" />
          {!!limit && <line x1={0} x2={width} y1={y(limit)} y2={y(limit)} className="stroke-warn" strokeWidth={1} strokeDasharray="4 4" vectorEffect="non-scaling-stroke" />}
        </svg>
        {marked !== -1 && <Mark across={x(samples[marked]) / width} up={y(values[marked]) / height} label={format(values[marked])} at={samples[marked].t} />}
      </div>
    </section>
  );
}

/** A line through one moment on a chart, with what it was then. */
function Mark({ across, up, label, at }: { across: number; up: number; label: string; at: string }) {
  const left = `${across * 100}%`;
  return (
    <div className="pointer-events-none absolute inset-0">
      <div className="absolute inset-y-0 w-px bg-line-strong" style={{ left }} />
      <div className="absolute size-2 -translate-x-1/2 -translate-y-1/2 rounded-full bg-accent" style={{ left, top: `${up * 100}%` }} />
      {/* Kept inside the chart at either end, and off the point. */}
      <div
        role="tooltip"
        className={cx("absolute rounded border border-line-strong bg-raised px-1.5 py-0.5 text-xs whitespace-nowrap", up < 0.4 ? "bottom-1.5" : "top-1.5")}
        style={{ left, transform: `translateX(${-across * 100}%)` }}
      >
        <span className="text-fg">{label}</span> <span className="text-muted">{new Date(at).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" })}</span>
      </div>
    </div>
  );
}
