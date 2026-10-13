import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { useNavigate } from "react-router";
import { api, ApiError, type Server, type ServerSpec, type Snapshot, type SnapshotDetail } from "../lib/api";
import { ago, describeSoftware, loaderNames } from "../lib/format";
import { keys, useServers } from "../lib/queries";
import { fromGigabytes, toGigabytes } from "../lib/software";
import { Button, Field, Input, Loading, Modal, Notice, Toggle } from "./ui";

const GiB = 1024 ** 3;

/** The first of name, name-2, name-3… that no server has. Names are at most 40 characters. */
function freeName(wanted: string, taken: Set<string>) {
  for (let n = 1; ; n++) {
    const suffix = n === 1 ? "" : `-${n}`;
    const name = wanted.slice(0, 40 - suffix.length).replace(/-+$/, "") + suffix;
    if (!taken.has(name)) return name;
  }
}

interface Props {
  org: string;
  /** The snapshot to make a server from, or null while the dialog is closed. */
  snapshot: Snapshot | null;
  onClose: () => void;
}

/**
 * Makes a new server out of a snapshot: a deleted server brought back, or a
 * copy of one that is still there.
 */
export function NewFromSnapshot({ org, snapshot, onClose }: Props) {
  const detail = useQuery({
    queryKey: ["snapshot", org, snapshot?.id],
    queryFn: () => api.get<SnapshotDetail>(`/orgs/${org}/snapshots/${snapshot!.id}`),
    enabled: !!snapshot,
    retry: false,
  });
  const servers = useServers(org);
  const software = snapshot ? [loaderNames[snapshot.loader as keyof typeof loaderNames] ?? snapshot.loader, snapshot.version].filter(Boolean).join(" ") : "";
  return (
    <Modal
      open={!!snapshot}
      onOpenChange={(open) => !open && onClose()}
      title="New server from this snapshot"
      description={snapshot ? [`${snapshot.server} as it was ${ago(snapshot.time)}`, software].filter(Boolean).join(" · ") : ""}
    >
      {detail.isPending || servers.isPending ? (
        <Loading />
      ) : detail.data && servers.data ? (
        <Form
          key={detail.data.id}
          org={org}
          source={{ name: detail.data.server, spec: detail.data.spec, size: detail.data.size }}
          taken={new Set(servers.data.map((s) => s.name))}
          from={{ fromSnapshot: detail.data.id }}
          onClose={onClose}
        />
      ) : (
        <Notice tone="danger">{detail.error instanceof ApiError ? detail.error.message : "The snapshot could not be read."}</Notice>
      )}
    </Modal>
  );
}

/**
 * Makes a copy of a server as it is now. A snapshot of it is taken first,
 * unless its newest one already has everything.
 */
export function DuplicateServer({ server, open, onClose }: { server: Server; open: boolean; onClose: () => void }) {
  const servers = useServers(server.org);
  return (
    <Modal open={open} onOpenChange={(o) => !o && onClose()} title={`Duplicate ${server.name}`} description={describeSoftware(server)}>
      {!servers.data ? (
        <Loading />
      ) : (
        <Form
          org={server.org}
          // How large its files are is not known here; its storage is at least that.
          source={{ name: server.name, spec: server.spec, size: 0 }}
          taken={new Set(servers.data.map((s) => s.name))}
          from={{ fromServer: server.name }}
          onClose={onClose}
        />
      )}
    </Modal>
  );
}

interface FormProps {
  org: string;
  /** The server the new one is made of: its name, its settings, and how large its files are. */
  source: { name: string; spec: ServerSpec; size: number };
  taken: Set<string>;
  /** What it is made from: a snapshot, or the server as it is now. */
  from: { fromSnapshot: string } | { fromServer: string };
  onClose: () => void;
}

function Form({ org, source, taken, from, onClose }: FormProps) {
  const navigate = useNavigate();
  const qc = useQueryClient();
  const { spec } = source;
  const detail = { server: source.name, size: source.size };
  const duplicating = "fromServer" in from;
  // A server that still exists is being copied; one that is gone gets its name back.
  const copying = taken.has(detail.server);
  // Whole gigabytes that hold the snapshot's files with some room to spare.
  const leastStorage = Math.max(1, Math.ceil((detail.size * 1.2) / GiB));
  const custom = !!spec.image || (spec.env?.length ?? 0) > 0;
  // Whether it gets a hostname of its own, which is how the router finds it.
  const exposed = (spec.expose?.type ?? "Router") === "Router";

  const [displayName, setDisplayName] = useState((spec.displayName || detail.server) + (copying ? " copy" : ""));
  const [name, setName] = useState(freeName(copying ? `${detail.server}-copy` : detail.server, taken));
  const [memory, setMemory] = useState(toGigabytes(spec.resources.memory) || "4");
  const [storage, setStorage] = useState(String(Math.max(Number(toGigabytes(spec.storage.size)) || 10, leastStorage)));
  const [hostname, setHostname] = useState("");
  const [start, setStart] = useState(true);
  const [keepCustom, setKeepCustom] = useState(custom);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const submit = async (e: React.FormEvent) => {
    e.preventDefault();
    setBusy(true);
    setError(null);
    const request: Partial<ServerSpec> = {
      displayName,
      state: start ? "Running" : "Stopped",
      resources: { memory: fromGigabytes(memory) },
      storage: { size: fromGigabytes(storage) },
      // Reached the way the original is: a server that is not exposed at all
      // is not given an address because it was copied. A fixed address or
      // port is the original's own and is not taken along.
      expose: exposed ? { type: "Router", hostname: hostname.trim() } : { type: spec.expose.type },
      ...(keepCustom ? { image: spec.image, env: spec.env } : {}),
    };
    try {
      const server = await api.post<Server>(`/orgs/${org}/servers`, { name, ...from, spec: request });
      await Promise.all([qc.invalidateQueries({ queryKey: keys.servers(org) }), qc.invalidateQueries({ queryKey: keys.snapshots(org) })]);
      onClose();
      // Its Backups tab is where the copying is shown.
      navigate(`/o/${org}/s/${server.name}/backups`);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not create the server.");
      setBusy(false);
    }
  };

  return (
    <form onSubmit={submit} className="flex flex-col gap-4">
      <p className="text-[13px] leading-relaxed text-muted">
        {duplicating
          ? `A copy of ${detail.server} as it is now. ${detail.server} itself is not touched.`
          : copying
            ? `A copy of ${detail.server} as it was then. ${detail.server} itself is not touched.`
            : `Brings ${detail.server} back as it was then.`}
      </p>
      <div className="grid grid-cols-2 gap-4">
        <Field label="Display name">{(id) => <Input id={id} required value={displayName} onChange={(e) => setDisplayName(e.target.value)} />}</Field>
        <Field label="Short name">
          {(id) => <Input id={id} required value={name} onChange={(e) => setName(e.target.value)} pattern="[a-z]([a-z0-9\-]{0,38}[a-z0-9])?" className="font-mono" spellCheck={false} />}
        </Field>
        <Field label="Memory (GB)">{(id) => <Input id={id} type="number" required min={0.5} step={0.5} value={memory} onChange={(e) => setMemory(e.target.value)} />}</Field>
        <Field label="Storage (GB)">{(id) => <Input id={id} type="number" required min={leastStorage} step={1} value={storage} onChange={(e) => setStorage(e.target.value)} />}</Field>
      </div>
      {exposed && (
        <Field label="Hostname" hint="Optional.">
          {(id) => <Input id={id} value={hostname} onChange={(e) => setHostname(e.target.value)} placeholder="play.example.com" className="font-mono" spellCheck={false} />}
        </Field>
      )}
      <Toggle checked={start} onChange={setStart} label="Start it when its files are in place" />
      {custom && (
        <Toggle
          checked={keepCustom}
          onChange={setKeepCustom}
          label="Keep its custom image and environment"
          hint={
            <>
              {spec.image && (
                <>
                  Image <span className="font-mono">{spec.image}</span>.{" "}
                </>
              )}
              {(spec.env?.length ?? 0) > 0 && (
                <>
                  Variables <span className="font-mono">{spec.env!.map((v) => v.name).join(", ")}</span>.
                </>
              )}
            </>
          }
        />
      )}
      {error && <Notice tone="danger">{error}</Notice>}
      <div className="flex justify-end gap-2">
        <Button onClick={onClose}>Cancel</Button>
        <Button type="submit" variant="primary" loading={busy}>
          {duplicating ? "Duplicate" : "Create server"}
        </Button>
      </div>
    </form>
  );
}
