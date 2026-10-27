import { ChevronLeft } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link, useNavigate } from "react-router";
import { ApiError, serverIcon, snapshotIcon, type Server, type Snapshot } from "../lib/api";
import { ago, bytes, describeSoftware, displayName } from "../lib/format";
import { newFrom } from "../lib/origin";
import { useOrgSnapshots, useServers, useTemplates } from "../lib/queries";
import { matches } from "../lib/search";
import { ready, templateSummary } from "../lib/templates";
import { Picture } from "./Picture";
import { software, triggerLabel } from "./Snapshots";
import { Chip, cx, Loading, SearchBox } from "./ui";

/** What else a new server can start from than nothing: each fills in the form. */
export type StartKind = "template" | "server" | "snapshot";

/** The templates, servers or snapshots a new server can be made from; picking one opens the form filled in from it. */
export function StartPicker({ org, kind }: { org: string; kind: StartKind }) {
  if (kind === "template") return <Templates org={org} />;
  if (kind === "server") return <Servers org={org} />;
  return <SnapshotsToStartFrom org={org} />;
}

function Templates({ org }: { org: string }) {
  const navigate = useNavigate();
  const templates = useTemplates(org);
  const [query, setQuery] = useState("");
  if (templates.isPending) return <Loading />;
  if (!templates.data) return <Empty>{failed(templates.error, "Could not load templates.")}</Empty>;
  if (templates.data.length === 0)
    return (
      <Empty>
        No templates yet. Make one on the{" "}
        <Link to={`/o/${org}/templates`} className="text-fg underline underline-offset-2">
          Templates
        </Link>{" "}
        page.
      </Empty>
    );
  const shown = templates.data.filter((t) => matches(query, t.name, t.description, templateSummary(t)));
  return (
    <Searched query={query} onQuery={setQuery} label="Search templates" none={shown.length === 0}>
    <List label="Templates">
      {shown.map((t) => (
        <Row
          key={t.id}
          disabled={!ready(t)}
          onClick={() => navigate(newFrom(org, { kind: "template", id: t.id }))}
          picture={<Picture name={t.name} url={t.current ? snapshotIcon(org, t.current.id) : undefined} size={32} />}
          title={t.name}
          sub={templateSummary(t) ?? "Saving"}
        />
      ))}
    </List>
    </Searched>
  );
}

/** A server as a row to pick: its picture, its name and what it runs. */
export function ServerRow({ server, selected, onClick }: { server: Server; selected?: boolean; onClick: () => void }) {
  return <Row onClick={onClick} selected={selected} picture={<Picture name={displayName(server)} url={serverIcon(server)} size={32} />} title={displayName(server)} sub={[server.name, describeSoftware(server)].join(" · ")} />;
}

function Servers({ org }: { org: string }) {
  const navigate = useNavigate();
  const servers = useServers(org);
  const [query, setQuery] = useState("");
  if (servers.isPending) return <Loading />;
  if (!servers.data) return <Empty>{failed(servers.error, "Could not load servers.")}</Empty>;
  if (servers.data.length === 0) return <Empty>No servers yet.</Empty>;
  const shown = servers.data.filter((s) => matches(query, s.name, displayName(s), describeSoftware(s)));
  return (
    <Searched query={query} onQuery={setQuery} label="Search servers" none={shown.length === 0}>
      <List label="Servers">
        {shown.map((s) => (
          <ServerRow key={s.name} server={s} onClick={() => navigate(newFrom(org, { kind: "server", name: s.name }))} />
        ))}
      </List>
    </Searched>
  );
}

function SnapshotsToStartFrom({ org }: { org: string }) {
  const navigate = useNavigate();
  return <SnapshotPicker org={org} onPick={(s) => navigate(newFrom(org, { kind: "snapshot", id: s.id }))} />;
}

/**
 * An organization's snapshots to pick one of: the servers that have some
 * first, then that server's. A template's versions are not among them.
 */
export function SnapshotPicker({ org, onPick, picked: chosen, className }: { org: string; onPick: (s: Snapshot) => void; picked?: string; className?: string }) {
  const snapshots = useOrgSnapshots(org);
  const servers = useServers(org);
  const all = (snapshots.data?.snapshots ?? []).filter((s) => s.trigger !== "template").sort((a, b) => b.time.localeCompare(a.time));
  // By server, the one with the newest snapshot first.
  const names = [...new Set(all.map((s) => s.server))];
  const [picked, setPicked] = useState<string | null>(null);
  const [query, setQuery] = useState("");
  const [within, setWithin] = useState("");
  // With snapshots of one server only, there is no server to pick.
  const server = picked && names.includes(picked) ? picked : names.length === 1 ? names[0] : null;
  if (snapshots.isPending) return <Loading />;
  if (!snapshots.data) return <Empty>{failed(snapshots.error, "Could not load snapshots.")}</Empty>;
  if (all.length === 0) return <Empty>No snapshots yet.</Empty>;
  const live = (name: string) => servers.data?.find((s) => s.name === name);
  const title = (name: string) => {
    const s = live(name);
    return s ? displayName(s) : name;
  };
  if (!server) {
    const shown = names.filter((name) => matches(query, name, title(name), !live(name) && "deleted"));
    return (
      <Searched query={query} onQuery={setQuery} label="Search servers" none={shown.length === 0}>
      <List label="Servers with snapshots" className={className}>
        {shown.map((name) => {
          const of = all.filter((s) => s.server === name);
          const s = live(name);
          return (
            <Row
              key={name}
              onClick={() => {
                setWithin("");
                setPicked(name);
              }}
              picture={<Picture name={title(name)} url={s ? serverIcon(s) : snapshotIcon(org, of[0].id)} size={32} />}
              title={title(name)}
              chip={servers.data && !s ? "deleted" : undefined}
              sub={[name, count(of.length, "snapshot"), `newest ${ago(of[0].time)}`].join(" · ")}
            />
          );
        })}
      </List>
      </Searched>
    );
  }
  const when = (time: string) => new Date(time).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" });
  const about = (s: Snapshot) => [ago(s.time), software(s), triggerLabel(s), bytes(s.size)].filter(Boolean).join(" · ");
  const shown = all.filter((s) => s.server === server && matches(within, when(s.time), about(s)));
  return (
    <>
      {names.length > 1 && (
        <p className="flex min-w-0 items-center gap-1 text-xs text-muted">
          <button type="button" className="inline-flex shrink-0 items-center gap-1 hover:text-fg" onClick={() => setPicked(null)}>
            <ChevronLeft className="size-3.5" />
            All servers
          </button>
          <span>/</span>
          <span className="truncate text-fg">{title(server)}</span>
        </p>
      )}
      <Searched query={within} onQuery={setWithin} label="Search snapshots" none={shown.length === 0}>
        <List label="Snapshots" className={className}>
          {shown.map((s) => (
            <Row
              key={s.id}
              onClick={() => onPick(s)}
              selected={s.id === chosen}
              picture={<Picture name={title(s.server)} url={snapshotIcon(org, s.id)} size={32} />}
              title={when(s.time)}
              sub={about(s)}
            />
          ))}
        </List>
      </Searched>
    </>
  );
}

const count = (n: number, noun: string) => `${n} ${noun}${n === 1 ? "" : "s"}`;

const failed = (err: unknown, otherwise: string) => (err instanceof ApiError ? err.message : otherwise);

/** A list with a search box above it, and a word for when nothing matches. */
function Searched({ query, onQuery, label, none, children }: { query: string; onQuery: (q: string) => void; label: string; none: boolean; children: ReactNode }) {
  return (
    <>
      <SearchBox value={query} onChange={onQuery} label={label} />
      {none ? <Empty>Nothing matches.</Empty> : children}
    </>
  );
}

function Empty({ children }: { children: ReactNode }) {
  return <p className="text-xs text-muted">{children}</p>;
}

/** A list of things to pick, each a row of a picture, a name and a line about it. */
export function List({ label, children, className }: { label: string; children: ReactNode; className?: string }) {
  return (
    <ul aria-label={label} className={cx("flex max-h-96 flex-col divide-y divide-line overflow-y-auto rounded-md border border-line", className)}>
      {children}
    </ul>
  );
}

export function Row({ picture, title, chip, sub, disabled, selected, onClick }: { picture: ReactNode; title: string; chip?: string; sub: string; disabled?: boolean; selected?: boolean; onClick: () => void }) {
  return (
    <li>
      <button
        type="button"
        disabled={disabled}
        onClick={onClick}
        aria-pressed={selected}
        className={cx(
          "flex w-full items-center gap-3 px-3 py-2.5 text-left transition-colors disabled:opacity-50 disabled:hover:bg-transparent",
          selected ? "bg-accent-wash" : "hover:bg-hover",
        )}
      >
        {picture}
        <span className="min-w-0 flex-1">
          <span className="flex min-w-0 items-center gap-2 text-[13px] font-medium">
            <span className="truncate">{title}</span>
            {chip && <Chip>{chip}</Chip>}
          </span>
          <span className="block truncate text-xs text-muted">{sub}</span>
        </span>
      </button>
    </li>
  );
}
