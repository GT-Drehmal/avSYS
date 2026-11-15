import { Plus, X } from "lucide-react";
import type { ContentRef, ExposeType, ExtraPort, PortStatus } from "../lib/api";
import { Button, CopyText, Input, Select } from "./ui";

/** A port being edited: its number is text while it is typed. */
export interface PortDraft {
  name: string;
  port: string;
  protocol: "TCP" | "UDP";
  /** The mod it was opened for, by its Modrinth slug. */
  for?: string;
}

/** A server's ports, to edit. */
export const portDrafts = (ports: ExtraPort[] | null | undefined): PortDraft[] =>
  (ports ?? []).map((p) => ({ name: p.name, port: String(p.port), protocol: p.protocol ?? "TCP", ...(p.for ? { for: p.for } : {}) }));

/** The ports edited, as a server is given them: rows left empty are none. */
export const portsOf = (drafts: PortDraft[]): ExtraPort[] =>
  drafts.filter((p) => p.name.trim() || p.port.trim()).map((p) => ({ name: p.name.trim(), port: Number(p.port), protocol: p.protocol, ...(p.for ? { for: p.for } : {}) }));

/** A mod or plugin that listens on ports of its own. */
export interface KnownMod {
  /** Its Modrinth slug, which ports opened for it name. */
  slug: string;
  /** Its Modrinth project ID, which content found in an imported server's files is listed by. */
  id: string;
  label: string;
  ports: PortDraft[];
  /** It serves a web map on its port. */
  map?: boolean;
}

/**
 * The mods and plugins the panel opens ports for, as the panel's own list
 * has them (internal/controller/mods.go).
 */
export const knownMods: KnownMod[] = [
  {
    slug: "geyser",
    id: "wKkoqHrH",
    label: "Geyser",
    ports: [
      { name: "geyser", port: "19132", protocol: "UDP" },
      { name: "geyser-tcp", port: "19132", protocol: "TCP" },
    ],
  },
  { slug: "simple-voice-chat", id: "9eGKb6K1", label: "Simple Voice Chat", ports: [{ name: "voice-chat", port: "24454", protocol: "UDP" }] },
  { slug: "plasmo-voice", id: "1bZhdhsH", label: "Plasmo Voice", ports: [{ name: "plasmo-voice", port: "60606", protocol: "UDP" }] },
  { slug: "bluemap", id: "swbUV1cr", label: "BlueMap", ports: [{ name: "bluemap", port: "8100", protocol: "TCP" }], map: true },
  { slug: "dynmap", id: "fRQREgAc", label: "Dynmap", ports: [{ name: "dynmap", port: "8123", protocol: "TCP" }], map: true },
  { slug: "squaremap", id: "PFb7ZqK6", label: "squaremap", ports: [{ name: "squaremap", port: "8080", protocol: "TCP" }], map: true },
];

/** The known mod an entry of a server's content is, if it is one. */
export const knownModOf = (m: ContentRef): KnownMod | undefined => (m.source === "modrinth" ? knownMods.find((k) => m.project === k.slug || m.project === k.id) : undefined);

const knownIn = (mods: ContentRef[]) => new Set(mods.flatMap((m) => knownModOf(m)?.slug ?? []));

/** A mod's ports, as opened for it. */
const portsFor = (k: KnownMod): PortDraft[] => k.ports.map((p) => ({ ...p, for: k.slug }));

/**
 * A server's ports once its content goes from before to after, as the
 * panel fits them: a known mod added gets its ports, each unless one of
 * that name or number is there or there are 8 already, and ports opened
 * for a mod go with it. Ports of the server's own stay as they are.
 */
export function fitModPorts(before: ContentRef[], after: ContentRef[], ports: PortDraft[]): PortDraft[] {
  const had = knownIn(before);
  const has = knownIn(after);
  const out = ports.filter((p) => !p.for || has.has(p.for) || !knownMods.some((k) => k.slug === p.for));
  for (const k of knownMods) {
    if (!has.has(k.slug) || had.has(k.slug)) continue;
    for (const p of portsFor(k)) {
      if (out.length < 8 && !out.some((q) => q.name === p.name || (q.port === p.port && q.protocol === p.protocol))) out.push(p);
    }
  }
  return out;
}

/** Whether a change of content touches a mod the panel opens ports for. */
export const touchesKnownMods = (before: ContentRef[], after: ContentRef[]) => knownIn(before).size + knownIn(after).size > 0;

/** "UDP 24454", or "UDP and TCP 19132" for a mod's ports on one number. */
export function describePorts(ports: { port: string | number; protocol?: string }[]): string {
  const numbers = [...new Set(ports.map((p) => String(p.port)))];
  if (numbers.length === 1) return `${[...new Set(ports.map((p) => p.protocol ?? "TCP"))].join(" and ")} ${numbers[0]}`;
  return ports.map((p) => `${p.protocol ?? "TCP"} ${p.port}`).join(", ");
}

/** What is said under a known mod in a list of content: the ports open for it. */
export function modPortsNote(m: ContentRef, ports: ExtraPort[] | null | undefined): string | undefined {
  const k = knownModOf(m);
  const open = k ? (ports ?? []).filter((p) => p.for === k.slug) : [];
  return open.length > 0 ? `${open.length === 1 ? "Port" : "Ports"} open: ${describePorts(open)}` : undefined;
}

/**
 * Where players reach a port opened for a known mod that is one of these,
 * once it is open: the first such, by the server's list of ports.
 */
export function modReached(ports: ExtraPort[] | null | undefined, status: PortStatus[] | null | undefined, which: (k: KnownMod, p: ExtraPort) => boolean): { mod: KnownMod; address: string } | undefined {
  for (const p of ports ?? []) {
    const mod = knownMods.find((k) => k.slug === p.for);
    if (!mod || !which(mod, p)) continue;
    const st = (status ?? []).find((s) => s.name === p.name && s.port === p.port && s.protocol === (p.protocol ?? "TCP"));
    if (st?.address && !st.message) return { mod, address: st.address };
  }
  return undefined;
}

/**
 * The ports a server's mods and plugins listen on, opened to players beside
 * the game's. Installed ones that need a port and have none are said, with
 * a button that adds them; where each is reached is shown once it is open.
 */
export function ExtraPorts({ ports, onChange, mods, status, expose, sleeps }: { ports: PortDraft[]; onChange: (ports: PortDraft[]) => void; mods: ContentRef[]; status: PortStatus[]; expose: ExposeType; sleeps?: boolean }) {
  // As many as a server may have: presets too stop there.
  const full = ports.length >= 8;
  const set = (i: number, change: Partial<PortDraft>) => onChange(ports.map((p, j) => (j === i ? { ...p, ...change } : p)));
  // Had already: that port, or one by its name, as one set to another number.
  const has = (p: PortDraft) => ports.some((q) => (q.port === p.port && q.protocol === p.protocol) || q.name === p.name);
  const installed = knownIn(mods);
  const needed = knownMods.filter((k) => installed.has(k.slug) && !ports.some((p) => p.for === k.slug) && !k.ports.some(has));
  const add = (opened: PortDraft[]) => onChange([...ports, ...opened.filter((p) => !has(p))].slice(0, Math.max(8, ports.length)));
  const where = (p: PortDraft) => status.find((s) => s.name === p.name && String(s.port) === p.port && s.protocol === p.protocol);
  const labelOf = (slug: string) => knownMods.find((k) => k.slug === slug)?.label ?? slug;
  if (expose === "None") return <p className="text-xs text-muted">Not reachable from outside.</p>;
  return (
    <div className="flex flex-col gap-2.5">
      {needed.map((k) => (
        <div key={k.label} className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-accent/40 bg-accent-wash px-3 py-2 text-[13px]">
          <span>
            {k.label} is installed and listens on {describePorts(k.ports)}.
          </span>
          <Button size="sm" disabled={full} title={full ? "A server has up to 8 extra ports." : undefined} onClick={() => add(portsFor(k))}>
            Open it
          </Button>
        </div>
      ))}
      {ports.map((p, i) => {
        const open = where(p);
        return (
          <div key={i} className="flex flex-col gap-1">
            <div className="flex items-center gap-2">
              <Input aria-label="Port name" value={p.name} onChange={(e) => set(i, { name: e.target.value })} placeholder="voice-chat" className="min-w-0 flex-1 font-mono" spellCheck={false} />
              <Input aria-label="Port number" type="number" min={1} max={65535} value={p.port} onChange={(e) => set(i, { port: e.target.value })} className="w-24 font-mono" />
              <Select aria-label="Protocol" value={p.protocol} onChange={(e) => set(i, { protocol: e.target.value as "TCP" | "UDP" })} className="w-20">
                <option>TCP</option>
                <option>UDP</option>
              </Select>
              <Button size="sm" aria-label={`Remove ${p.name || "this port"}`} icon={<X className="size-3.5" />} onClick={() => onChange(ports.filter((_, j) => j !== i))} />
            </div>
            {(p.for || open) && (
              <div className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs">
                {p.for && <span className="text-muted">For {labelOf(p.for)}.</span>}
                {open?.message ? (
                  <span className="text-warn">{open.message}</span>
                ) : open?.address ? (
                  <span className="flex items-center gap-1.5 text-muted">
                    Reached at <CopyText value={open.address} />
                  </span>
                ) : open?.publicPort ? (
                  <span className="text-muted">Reached on port {open.publicPort} of the server's address.</span>
                ) : open ? (
                  <span className="text-muted">Being opened.</span>
                ) : null}
              </div>
            )}
          </div>
        );
      })}
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" icon={<Plus className="size-3.5" />} disabled={full} onClick={() => onChange([...ports, { name: "", port: "", protocol: "TCP" }])}>
          Add a port
        </Button>
        {!full &&
          knownMods
            .filter((k) => !k.ports.some(has) && !needed.includes(k))
            .slice(0, 3)
            .map((k) => (
              <button key={k.label} type="button" className="text-xs text-accent hover:underline" onClick={() => add(k.ports)}>
                {k.label}
              </button>
            ))}
      </div>
      {ports.length > 0 && sleeps && (
        <p className="text-xs text-muted">While the server sleeps, these ports still answer: a Bedrock player joining, or a visit to a map, wakes the server.</p>
      )}
      {ports.length > 0 && (
        <p className="text-xs text-muted">
          {expose === "Router"
            ? "Each gets a public port of its own. Allow those through your firewall."
            : "Opened on the server's address."}
        </p>
      )}
    </div>
  );
}
