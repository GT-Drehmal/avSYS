import { useQuery } from "@tanstack/react-query";
import { useState } from "react";
import { api, serverPath, type NodeInfo, type Placement, type Server } from "../lib/api";
import { keys, useAction } from "../lib/queries";
import { toGigabytes } from "../lib/software";
import { toast } from "../lib/toast";
import { Button, Confirm, Select } from "./ui";

const gb = (bytes: number) => `${(bytes / 1024 ** 3).toFixed(1)} GB`;

/**
 * Where a server runs, and moving it to another node. Files on the node's
 * own disk go with it in a snapshot; a server kept to nodes by labels set
 * outside the panel follows those instead.
 */
export function NodeMove({ server }: { server: Server }) {
  const path = serverPath(server.org, server.name);
  const memory = server.spec.resources.memory;
  const placement = useQuery({ queryKey: ["placement", server.org, server.name], queryFn: () => api.get<Placement>(`${path}/placement`) });
  const nodes = useQuery({ queryKey: ["nodes", memory], queryFn: () => api.get<{ nodes: NodeInfo[] }>(`/nodes?memory=${encodeURIComponent(memory)}`) });
  const [target, setTarget] = useState("");
  const [asking, setAsking] = useState(false);
  const move = useAction(() => api.post(`${path}/move`, { node: target }), [keys.servers(server.org), keys.serverSnapshots(server.org, server.name)]);

  const here = placement.data?.node || server.status.node;
  if (placement.isPending || nodes.isPending) return <p className="text-[13px] text-muted">Loading…</p>;
  if (placement.data?.labels && Object.keys(placement.data.labels).length > 0) {
    const labels = Object.entries(placement.data.labels)
      .map(([k, v]) => `${k}=${v}`)
      .join(", ");
    return (
      <p className="text-[13px] text-muted">
        {here ? `On ${here}. ` : ""}Kept to nodes labelled <span className="font-mono">{labels}</span>, set outside the panel.
      </p>
    );
  }
  const others = (nodes.data?.nodes ?? []).filter((n) => n.name !== here);
  // What its pod asks a node for: its memory and a little for its workspace.
  const need = (Number(toGigabytes(memory)) || 0) * 1024 ** 3 + 32 * 1024 ** 2;
  const fits = (n: NodeInfo) => n.ready && n.free >= need;
  const local = placement.data?.local ?? true;
  return (
    <div className="flex flex-col gap-3">
      <p className="text-[13px]">
        {here ? (
          <>
            Runs on <span className="font-medium">{here}</span>.
          </>
        ) : (
          "Not placed on a node yet."
        )}
      </p>
      {others.length === 0 ? (
        <p className="text-xs text-muted">No other node to move the server to.</p>
      ) : (
        <div className="flex flex-wrap items-center gap-2">
          <Select aria-label="Move to" value={target} onChange={(e) => setTarget(e.target.value)} className="w-72 max-sm:w-full">
            <option value="">Choose a node…</option>
            {others.map((n) => (
              <option key={n.name} value={n.name} disabled={!fits(n)}>
                {n.name} · {n.score > 0 ? `speed ${n.score.toFixed(2)}` : "speed not measured"} · {gb(n.free)} free
                {!n.ready ? " · down" : n.free < need ? " · too little memory" : ""}
              </option>
            ))}
          </Select>
          <Button disabled={!target} onClick={() => setAsking(true)}>
            Move
          </Button>
        </div>
      )}
      <Confirm
        open={asking}
        onOpenChange={setAsking}
        title={`Move ${server.name} to ${target}?`}
        description={
          local
            ? `The server stops, its files move to ${target}, and it starts again if it was running. Big worlds take longer.`
            : `The server starts again on ${target}.`
        }
        action="Move"
        loading={move.isPending}
        onConfirm={() =>
          move.mutate(undefined, {
            onSuccess: () => {
              setAsking(false);
              setTarget("");
              toast.info(`Moving ${server.name} to ${target}`);
            },
          })
        }
      />
    </div>
  );
}
