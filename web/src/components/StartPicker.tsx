import { useState, type ReactNode } from "react";
import { Link, useNavigate } from "react-router";
import { ApiError, serverIcon, snapshotIcon, type Server } from "../lib/api";
import { ago, bytes, describeSoftware, displayName } from "../lib/format";
import { newFrom } from "../lib/origin";
import { useOrgSnapshots, useServers, useTemplates } from "../lib/queries";
import { ready, templateSummary } from "../lib/templates";
import { Picture } from "./Picture";
import { software, triggerLabel } from "./Snapshots";
import { cx, Loading, Select } from "./ui";

/** What else a new server can start from than nothing: each fills in the form. */
export type StartKind = "template" | "server" | "snapshot";

/** The templates, servers or snapshots a new server can be made from; picking one opens the form filled in from it. */
export function StartPicker({ org, kind }: { org: string; kind: StartKind }) {
  if (kind === "template") return <Templates org={org} />;
  if (kind === "server") return <Servers org={org} />;
  return <Snapshots org={org} />;
}

function Templates({ org }: { org: string }) {
  const navigate = useNavigate();
  const templates = useTemplates(org);
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
  return (
    <List label="Templates">
      {templates.data.map((t) => (
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
  );
}

/** A server as a row to pick: its picture, its name and what it runs. */
export function ServerRow({ server, selected, onClick }: { server: Server; selected?: boolean; onClick: () => void }) {
  return <Row onClick={onClick} selected={selected} picture={<Picture name={displayName(server)} url={serverIcon(server)} size={32} />} title={displayName(server)} sub={[server.name, describeSoftware(server)].join(" · ")} />;
}

function Servers({ org }: { org: string }) {
  const navigate = useNavigate();
  const servers = useServers(org);
  if (servers.isPending) return <Loading />;
  if (!servers.data) return <Empty>{failed(servers.error, "Could not load servers.")}</Empty>;
  if (servers.data.length === 0) return <Empty>No servers yet.</Empty>;
  return (
    <List label="Servers">
      {servers.data.map((s) => (
        <ServerRow key={s.name} server={s} onClick={() => navigate(newFrom(org, { kind: "server", name: s.name }))} />
      ))}
    </List>
  );
}

function Snapshots({ org }: { org: string }) {
  const navigate = useNavigate();
  const snapshots = useOrgSnapshots(org);
  const servers = useServers(org);
  const all = (snapshots.data?.snapshots ?? []).filter((s) => s.trigger !== "template").sort((a, b) => b.time.localeCompare(a.time));
  // By server, the one with the newest snapshot first.
  const names = [...new Set(all.map((s) => s.server))];
  const [picked, setPicked] = useState<string | null>(null);
  const server = picked && names.includes(picked) ? picked : names[0];
  if (snapshots.isPending) return <Loading />;
  if (!snapshots.data) return <Empty>{failed(snapshots.error, "Could not load snapshots.")}</Empty>;
  if (all.length === 0) return <Empty>No snapshots yet.</Empty>;
  // Its display name while it exists, said beside the name where they differ.
  const label = (name: string) => {
    const live = servers.data?.find((s) => s.name === name);
    if (!live) return servers.data ? `${name} (deleted)` : name;
    return displayName(live) === name ? name : `${displayName(live)} (${name})`;
  };
  return (
    <>
      <Select aria-label="Server" className="w-full sm:w-64" value={server} onChange={(e) => setPicked(e.target.value)}>
        {names.map((name) => (
          <option key={name} value={name}>
            {label(name)}
          </option>
        ))}
      </Select>
      <List label="Snapshots">
        {all
          .filter((s) => s.server === server)
          .map((s) => (
            <Row
              key={s.id}
              onClick={() => navigate(newFrom(org, { kind: "snapshot", id: s.id }))}
              picture={<Picture name={s.server} url={snapshotIcon(org, s.id)} size={32} />}
              title={new Date(s.time).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}
              sub={[ago(s.time), software(s), triggerLabel(s), bytes(s.size)].filter(Boolean).join(" · ")}
            />
          ))}
      </List>
    </>
  );
}

const failed = (err: unknown, otherwise: string) => (err instanceof ApiError ? err.message : otherwise);

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

export function Row({ picture, title, sub, disabled, selected, onClick }: { picture: ReactNode; title: string; sub: string; disabled?: boolean; selected?: boolean; onClick: () => void }) {
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
          <span className="block truncate text-[13px] font-medium">{title}</span>
          <span className="block truncate text-xs text-muted">{sub}</span>
        </span>
      </button>
    </li>
  );
}
