import { useQuery } from "@tanstack/react-query";
import { ArrowDown, ChevronRight, Download, Search } from "lucide-react";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { cx, Input, Select } from "../../components/ui";
import { toast } from "../../lib/toast";
import { api, ApiError, atLeast, serverPath } from "../../lib/api";
import { bytes, plain } from "../../lib/format";
import { useServerContext } from "./ServerPage";

interface Line {
  id: number;
  text: string;
  kind: "log" | "input" | "output" | "error";
}

const maxLines = 3000;
let nextLineId = 1;
const toLines = (texts: string[], kind: Line["kind"]): Line[] => texts.map((text) => ({ id: nextLineId++, text: plain(text), kind }));

// Every command run from the panel connects over RCON, which the game logs
// twice. That is the panel's own noise, not something the reader asked about.
const rconNoise = /Thread RCON Client \S+ (started|shutting down)$/;

// Matches both log layouts: "[12:00:00 INFO]: ..." (Paper) and
// "[12:00:00] [Server thread/INFO]: ..." (vanilla, Fabric, Forge).
const logPattern = /^(\[\d{2}:\d{2}:\d{2}(?:\] \[[^\]]*\/| ))(INFO|WARN|ERROR|FATAL|DEBUG)(\]:?)(.*)$/s;

function LogLine({ line }: { line: Line }) {
  if (line.kind === "input") return <div className="text-accent">&gt; {line.text}</div>;
  if (line.kind === "output") return <div className="text-accent-strong">{line.text}</div>;
  if (line.kind === "error") return <div className="text-danger">{line.text}</div>;
  const m = logPattern.exec(line.text);
  if (!m) return <div className="text-muted">{line.text || " "}</div>;
  const level = m[2];
  const tone = level === "WARN" ? "text-warn" : level === "ERROR" || level === "FATAL" ? "text-danger" : "text-fg";
  return (
    <div className={tone}>
      <span className="text-faint">
        {m[1]}
        {level}
        {m[3]}
      </span>
      {m[4]}
    </div>
  );
}

interface LogEntry {
  name: string;
  kind: "current" | "log" | "crash";
  size: number;
  modTime: string;
}

interface LogPage {
  name: string;
  total: number;
  truncated: boolean;
  lines: { n: number; text: string; match?: boolean }[];
  more?: boolean;
  search?: string;
  capped?: boolean;
  /** The first line, in all of the log, that tells of an error; 0 if none does. */
  firstError: number;
}

/** How an earlier log or crash report is named in the picker. */
function logLabel(e: LogEntry) {
  const file = e.name.split("/").pop() ?? e.name;
  if (e.kind === "current") return `latest.log, this run (${bytes(e.size)})`;
  if (e.kind === "crash") return `Crash report, ${new Date(e.modTime).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}`;
  return `${file.replace(/\.log(\.gz)?$/, "")} (${bytes(e.size)})`;
}

/**
 * The Console tab: the game as it runs, and what it wrote before. The game
 * keeps a log of each run and a report of each crash; any of them can be
 * opened here, searched and downloaded.
 */
export function Console() {
  const { server } = useServerContext();
  const path = serverPath(server.org, server.name);
  const [chosen, setChosen] = useState("");
  const logs = useQuery({ queryKey: ["logs", server.org, server.name], queryFn: () => api.get<{ logs: LogEntry[] }>(`${path}/logs`), retry: false });
  const earlier = logs.data?.logs ?? [];
  return (
    <div className="flex flex-col gap-3">
      <div className="flex flex-wrap items-center gap-2">
        <Select aria-label="Log" value={chosen} onChange={(e) => setChosen(e.target.value)} onFocus={() => logs.refetch()} className="w-auto max-w-full">
          <option value="">Live console</option>
          {earlier.length > 0 && (
            <optgroup label="Earlier runs and crashes">
              {earlier.map((e) => (
                <option key={e.name} value={e.name}>
                  {logLabel(e)}
                </option>
              ))}
            </optgroup>
          )}
        </Select>
        {logs.isError && <span className="text-xs text-muted">Earlier logs can be read once the server's files are reachable.</span>}
      </div>
      {chosen ? <EarlierLog key={chosen} path={path} name={chosen} /> : <LiveConsole />}
    </div>
  );
}

/** One of the game's own logs or crash reports, to read, search and download. */
function EarlierLog({ path, name }: { path: string; name: string }) {
  const [typed, setTyped] = useState("");
  const [search, setSearch] = useState("");
  // Pages read going back from the end; or, once jumped into the middle,
  // the pages from there.
  const [pages, setPages] = useState<LogPage[]>([]);
  const [jumped, setJumped] = useState<LogPage[] | null>(null);
  const [target, setTarget] = useState(0);
  const scroller = useRef<HTMLDivElement>(null);
  const url = `${path}/logs/file?name=${encodeURIComponent(name)}`;
  const first = useQuery({
    queryKey: ["log", path, name, search],
    queryFn: () => api.get<LogPage>(search ? `${url}&q=${encodeURIComponent(search)}` : url),
    retry: false,
  });
  // A new search starts again from the end; a page asked for before it is
  // not added to what it finds.
  const generation = useRef(0);
  useEffect(() => {
    generation.current++;
    setPages([]);
    setJumped(null);
  }, [search]);
  const shown = jumped ?? [...pages, ...(first.data ? [first.data] : [])];
  const lines = shown.flatMap((p) => p.lines);
  const total = first.data?.total ?? 0;
  const earlierToLoad = !search && (lines[0]?.n ?? 1) > 1;
  const laterToLoad = !!jumped && (lines.at(-1)?.n ?? total) < total;
  const [loading, setLoading] = useState(false);
  const load = async (query: string, place: (page: LogPage) => void) => {
    const asked = generation.current;
    setLoading(true);
    try {
      const page = await api.get<LogPage>(`${url}&${query}`);
      if (asked === generation.current) place(page);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not load that part of the log.");
    } finally {
      setLoading(false);
    }
  };
  const loadEarlier = () => {
    const top = lines[0]?.n;
    if (!top) return;
    return load(`before=${top}`, (page) => (jumped ? setJumped((j) => [page, ...(j ?? [])]) : setPages((p) => [page, ...p])));
  };
  const loadLater = () => {
    const bottom = lines.at(-1)?.n;
    if (!bottom) return;
    return load(`from=${bottom + 1}`, (page) => setJumped((j) => [...(j ?? []), page]));
  };
  // The newest is at the end, which is where reading starts.
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && first.data && !jumped) el.scrollTop = el.scrollHeight;
  }, [first.data, jumped]);
  // A line jumped to is brought into view once it is there.
  useLayoutEffect(() => {
    if (!target) return;
    const el = scroller.current?.querySelector(`[data-n="${target}"]`);
    if (el) {
      el.scrollIntoView({ block: "center" });
      setTarget(0);
    }
  }, [target, lines]);
  const errorAt = first.data?.firstError ?? 0;
  const firstError = () => {
    if (lines.some((l) => l.n === errorAt)) return setTarget(errorAt);
    // Earlier than what is read: from a little before it, onwards.
    void load(`from=${Math.max(1, errorAt - 20)}`, (page) => {
      setJumped([page]);
      setTarget(errorAt);
    });
  };
  const gz = name.endsWith(".gz");
  return (
    <div className="flex h-[calc(100dvh-380px)] min-h-80 flex-col overflow-hidden rounded-lg border border-line bg-bg">
      <form
        className="flex flex-wrap items-center gap-2 border-b border-line bg-surface px-3 py-2"
        onSubmit={(e) => {
          e.preventDefault();
          setSearch(typed.trim());
        }}
      >
        <div className="relative min-w-48 flex-1">
          <Search className="pointer-events-none absolute top-1/2 left-2.5 size-3.5 -translate-y-1/2 text-faint" />
          <Input aria-label="Search this log" value={typed} onChange={(e) => setTyped(e.target.value)} placeholder="Search, then Enter" className="h-7 pl-8 font-mono text-xs" spellCheck={false} />
        </div>
        <button
          type="button"
          onClick={firstError}
          disabled={!errorAt || !!search || loading}
          title={
            search
              ? "Clear the search to go to the first error."
              : errorAt
                ? `Line ${errorAt}`
                : first.data?.truncated
                  ? "No errors in the lines shown."
                  : "No errors in this log."
          }
          className="h-7 rounded-md px-2.5 text-xs font-medium text-muted hover:bg-raised hover:text-fg disabled:opacity-50 disabled:hover:bg-transparent disabled:hover:text-muted"
        >
          First error
        </button>
        <a href={`/api/v1${url}&download=${gz ? "gz" : "text"}`} className="inline-flex h-7 items-center gap-1.5 rounded-md px-2.5 text-xs font-medium text-muted hover:bg-raised hover:text-fg">
          <Download className="size-3.5" />
          Download
        </a>
        {gz && (
          <a href={`/api/v1${url}&download=text`} className="inline-flex h-7 items-center rounded-md px-2.5 text-xs font-medium text-muted hover:bg-raised hover:text-fg">
            Unpacked
          </a>
        )}
      </form>
      <div ref={scroller} className="flex-1 overflow-y-auto px-4 py-3 font-mono text-xs leading-[1.65] break-words whitespace-pre-wrap">
        {earlierToLoad && (
          <button type="button" onClick={loadEarlier} disabled={loading} className="mb-2 font-sans text-xs text-accent hover:underline disabled:opacity-50">
            {loading ? "Loading…" : "Load earlier lines"}
          </button>
        )}
        {search && first.data?.capped && (
          <p className="mb-2 font-sans text-xs text-muted">Only the last matches are shown. Search for something narrower to see earlier ones.</p>
        )}
        {first.isPending && <p className="font-sans text-[13px] text-muted">Reading the log…</p>}
        {first.isError && <p className="font-sans text-[13px] text-danger">{first.error instanceof ApiError ? first.error.message : "The log could not be read."}</p>}
        {search && first.data && lines.length === 0 && <p className="font-sans text-[13px] text-muted">Nothing in this log matches “{search}”.</p>}
        {lines.map((l, i) => (
          <div key={`${l.n}-${i}`} data-n={l.n} className={cx("flex gap-3", l.match && "bg-accent-wash")}>
            <span className="w-12 shrink-0 text-right text-faint select-none">{l.n}</span>
            <div className="min-w-0 flex-1">
              <LogLine line={{ id: l.n, text: plain(l.text), kind: "log" }} />
            </div>
          </div>
        ))}
        {jumped && (
          <div className="mt-2 flex gap-4 font-sans text-xs">
            {laterToLoad && (
              <button type="button" onClick={loadLater} disabled={loading} className="text-accent hover:underline disabled:opacity-50">
                {loading ? "Loading…" : "Load later lines"}
              </button>
            )}
            <button type="button" onClick={() => setJumped(null)} className="text-accent hover:underline">
              Go to the end
            </button>
          </div>
        )}
        {first.data?.truncated && <p className="mt-2 font-sans text-xs text-muted">Only the first 64 MB of this log are shown. Download it for the rest.</p>}
      </div>
    </div>
  );
}

function LiveConsole() {
  const { server, role } = useServerContext();
  const [lines, setLines] = useState<Line[]>([]);
  const [waiting, setWaiting] = useState(false);
  const [command, setCommand] = useState("");
  const [busy, setBusy] = useState(false);
  const [pinned, setPinned] = useState(true);
  const scroller = useRef<HTMLDivElement>(null);
  const history = useRef<{ entries: string[]; index: number }>({ entries: [], index: -1 });
  const path = serverPath(server.org, server.name);
  const running = server.status.phase === "Running";
  const canRun = atLeast(role, "operator");

  const append = useCallback((more: Line[]) => setLines((prev) => [...prev, ...more].slice(-maxLines)), []);

  useEffect(() => {
    setLines([]);
    const source = new EventSource(`/api/v1${path}/console`);
    source.addEventListener("reset", () => {
      setWaiting(false);
      setLines([]);
    });
    source.addEventListener("lines", (e) => {
      setWaiting(false);
      append(toLines((JSON.parse(e.data) as string[]).filter((l) => !rconNoise.test(l)), "log"));
    });
    source.addEventListener("waiting", () => setWaiting(true));
    return () => source.close();
  }, [path, append]);

  // Follow new output unless the reader has scrolled up to look at something.
  useLayoutEffect(() => {
    const el = scroller.current;
    if (el && pinned) el.scrollTop = el.scrollHeight;
  }, [lines, pinned]);

  const onScroll = () => {
    const el = scroller.current!;
    setPinned(el.scrollHeight - el.scrollTop - el.clientHeight < 40);
  };

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    const text = command.trim();
    if (!text || busy) return;
    history.current.entries.unshift(text);
    history.current.index = -1;
    setCommand("");
    setPinned(true);
    append(toLines([text], "input"));
    setBusy(true);
    try {
      // Typed at the server console, so that players see it come from the
      // server. What the command prints shows up in the log above like
      // anything else; only a server that has not been restarted since the
      // panel learned to do this still answers here.
      const { output } = await api.post<{ output: string }>(`${path}/console`, { command: text, as: "server" });
      if (output.trim()) append(toLines(output.replace(/\n$/, "").split("\n"), "output"));
    } catch (err) {
      append(toLines([err instanceof ApiError ? err.message : "The command failed."], "error"));
    } finally {
      setBusy(false);
    }
  };

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    const h = history.current;
    if (e.key === "ArrowUp" && h.index < h.entries.length - 1) {
      e.preventDefault();
      setCommand(h.entries[++h.index]);
    } else if (e.key === "ArrowDown" && h.index >= 0) {
      e.preventDefault();
      h.index--;
      setCommand(h.index === -1 ? "" : h.entries[h.index]);
    }
  };

  return (
    <div className="relative flex h-[calc(100dvh-330px)] min-h-80 flex-col overflow-hidden rounded-lg border border-line bg-bg">
      <div ref={scroller} onScroll={onScroll} className="flex-1 overflow-y-auto px-4 py-3 font-mono text-xs leading-[1.65] break-words whitespace-pre-wrap">
        {lines.map((line) => (
          <LogLine key={line.id} line={line} />
        ))}
        {lines.length === 0 && (
          <p className="font-sans text-[13px] text-muted">
            {!waiting
              ? "Waiting for output…"
              : server.status.phase === "Starting"
                ? "The server is starting."
                : "The server is not running."}
          </p>
        )}
      </div>
      {!pinned && (
        <button
          onClick={() => setPinned(true)}
          className="absolute right-4 bottom-14 flex h-7 items-center gap-1.5 rounded-full border border-line-strong bg-raised px-3 text-xs font-medium shadow-lg shadow-black/40 hover:bg-hover"
        >
          <ArrowDown className="size-3.5" />
          Latest
        </button>
      )}
      {canRun && (
        <form onSubmit={submit} className="flex h-10 shrink-0 items-center gap-2 border-t border-line bg-surface px-3">
          <ChevronRight className={cx("size-4", running ? "text-accent" : "text-faint")} />
          <input
            value={command}
            onChange={(e) => setCommand(e.target.value)}
            onKeyDown={onKeyDown}
            disabled={!running}
            placeholder={running ? "Run a server command, e.g. list" : "Start the server to run commands"}
            aria-label="Server command"
            autoComplete="off"
            spellCheck={false}
            className="h-full flex-1 bg-transparent font-mono text-xs text-fg placeholder:text-faint focus:outline-none disabled:cursor-not-allowed"
          />
        </form>
      )}
    </div>
  );
}
