import { ArrowDown, ChevronRight } from "lucide-react";
import { useCallback, useEffect, useLayoutEffect, useRef, useState } from "react";
import { cx } from "../../components/ui";
import { api, ApiError, atLeast, serverPath } from "../../lib/api";
import { plain } from "../../lib/format";
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

export function Console() {
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
                ? "The game is starting. Its log appears here as soon as it is up."
                : "This server is not running. Its log appears here once it starts."}
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
