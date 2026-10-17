import { Plus, X } from "lucide-react";
import type { ContentRef, ExposeType, PortStatus } from "../lib/api";
import { Button, CopyText, Input, Select } from "./ui";

/** A port being edited: its number is text while it is typed. */
export interface PortDraft {
  name: string;
  port: string;
  protocol: "TCP" | "UDP";
}

/** What well-known mods and plugins listen on, by their Modrinth slug. */
const known: { slugs: string[]; label: string; port: PortDraft }[] = [
  { slugs: ["simple-voice-chat"], label: "Simple Voice Chat", port: { name: "voice-chat", port: "24454", protocol: "UDP" } },
  { slugs: ["plasmo-voice"], label: "Plasmo Voice", port: { name: "plasmo-voice", port: "60606", protocol: "UDP" } },
  { slugs: ["geyser"], label: "Geyser", port: { name: "geyser", port: "19132", protocol: "UDP" } },
  { slugs: ["bluemap"], label: "BlueMap", port: { name: "bluemap", port: "8100", protocol: "TCP" } },
  { slugs: ["dynmap"], label: "Dynmap", port: { name: "dynmap", port: "8123", protocol: "TCP" } },
  { slugs: ["squaremap"], label: "squaremap", port: { name: "squaremap", port: "8080", protocol: "TCP" } },
];

/**
 * The ports a server's mods and plugins listen on, opened to players beside
 * the game's. Installed ones that need a port are said, with a button that
 * adds it; where each is reached is shown once it is open.
 */
export function ExtraPorts({ ports, onChange, mods, status, expose, sleeps }: { ports: PortDraft[]; onChange: (ports: PortDraft[]) => void; mods: ContentRef[]; status: PortStatus[]; expose: ExposeType; sleeps?: boolean }) {
  // As many as a server may have: presets too stop there.
  const full = ports.length >= 8;
  const set = (i: number, change: Partial<PortDraft>) => onChange(ports.map((p, j) => (j === i ? { ...p, ...change } : p)));
  // Had already: that port, or one by its name, as one set to another number.
  const has = (p: PortDraft) => ports.some((q) => (q.port === p.port && q.protocol === p.protocol) || q.name === p.name);
  const installed = new Set(mods.map((m) => m.project));
  const needed = known.filter((k) => k.slugs.some((s) => installed.has(s)) && !has(k.port));
  const where = (p: PortDraft) => status.find((s) => s.name === p.name && String(s.port) === p.port && s.protocol === p.protocol);
  if (expose === "None") return <p className="text-xs text-muted">Not reachable from outside.</p>;
  return (
    <div className="flex flex-col gap-2.5">
      {needed.map((k) => (
        <div key={k.label} className="flex flex-wrap items-center justify-between gap-2 rounded-md border border-accent/40 bg-accent-wash px-3 py-2 text-[13px]">
          <span>
            {k.label} is installed and listens on {k.port.protocol} {k.port.port}.
          </span>
          <Button size="sm" disabled={full} title={full ? "A server has up to 8 extra ports." : undefined} onClick={() => onChange([...ports, k.port])}>
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
            {open?.message ? (
              <span className="text-xs text-warn">{open.message}</span>
            ) : open?.address ? (
              <span className="flex items-center gap-1.5 text-xs text-muted">
                Reached at <CopyText value={open.address} />
              </span>
            ) : open?.publicPort ? (
              <span className="text-xs text-muted">Reached on port {open.publicPort} of the server's address.</span>
            ) : open ? (
              <span className="text-xs text-muted">Being opened.</span>
            ) : null}
          </div>
        );
      })}
      <div className="flex flex-wrap items-center gap-2">
        <Button size="sm" icon={<Plus className="size-3.5" />} disabled={full} onClick={() => onChange([...ports, { name: "", port: "", protocol: "TCP" }])}>
          Add a port
        </Button>
        {!full &&
          known
          .filter((k) => !has(k.port) && !needed.includes(k))
          .slice(0, 3)
          .map((k) => (
            <button key={k.label} type="button" className="text-xs text-accent hover:underline" onClick={() => onChange([...ports, k.port])}>
              {k.label}
            </button>
          ))}
      </div>
      {ports.length > 0 && sleeps && (
        <p className="text-xs text-warn">These ports do not work while the server sleeps. Bedrock players through Geyser cannot wake it.</p>
      )}
      {ports.length > 0 && (
        <p className="text-xs text-muted">
          {expose === "Router"
            ? "Each gets its own public port. Allow it through your firewall."
            : "Opened on the server's address."}
        </p>
      )}
    </div>
  );
}
