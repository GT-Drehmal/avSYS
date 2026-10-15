import { ChevronDown, ChevronRight } from "lucide-react";
import { useQuery } from "@tanstack/react-query";
import type { ReactNode } from "react";
import { api, type NodeInfo } from "../lib/api";
import { difficulties, gameModes } from "../lib/properties";
import { fromGigabytes } from "../lib/software";
import { Field, Input, Select } from "./ui";

/** The server.properties entries that have a control of their own here. */
export interface Gameplay {
  difficulty: string;
  gamemode: string;
  maxPlayers: string;
  pvp: string;
}

export const gameplayKeys: Record<keyof Gameplay, string> = { difficulty: "difficulty", gamemode: "gamemode", maxPlayers: "max-players", pvp: "pvp" };

export const gameplayOf = (properties: Record<string, string> | undefined): Gameplay => ({
  difficulty: properties?.["difficulty"] ?? "",
  gamemode: properties?.["gamemode"] ?? "",
  maxPlayers: properties?.["max-players"] ?? "",
  pvp: properties?.["pvp"] ?? "",
});

export const triState = (
  <>
    <option value="">Server default</option>
    <option value="true">On</option>
    <option value="false">Off</option>
  </>
);

/**
 * Difficulty, game mode, player limit and PvP, for a server that exists or
 * is being made of one. Each left on its default leaves server.properties
 * as it is. Meant for a two-column grid.
 */
export function GameplayFields({ value, onChange, only }: { value: Gameplay; onChange: (key: keyof Gameplay, value: string) => void; only?: (keyof Gameplay)[] }) {
  const fields: Record<keyof Gameplay, ReactNode> = {
    difficulty: (
      <Field key="difficulty" label="Difficulty">
        {(id) => (
          <Select id={id} value={value.difficulty} onChange={(e) => onChange("difficulty", e.target.value)}>
            <option value="">Server default</option>
            {difficulties.map(([v, label]) => (
              <option key={v} value={v}>
                {label}
              </option>
            ))}
          </Select>
        )}
      </Field>
    ),
    gamemode: (
      <Field key="gamemode" label="Game mode">
        {(id) => (
          <Select id={id} value={value.gamemode} onChange={(e) => onChange("gamemode", e.target.value)}>
            <option value="">Server default</option>
            {gameModes.map(([v, label]) => (
              <option key={v} value={v}>
                {label}
              </option>
            ))}
          </Select>
        )}
      </Field>
    ),
    maxPlayers: (
      <Field key="maxPlayers" label="Player limit">
        {(id) => <Input id={id} type="number" min={1} value={value.maxPlayers} onChange={(e) => onChange("maxPlayers", e.target.value)} placeholder="20" />}
      </Field>
    ),
    pvp: (
      <Field key="pvp" label="PvP">
        {(id) => (
          <Select id={id} value={value.pvp} onChange={(e) => onChange("pvp", e.target.value)}>
            {triState}
          </Select>
        )}
      </Field>
    ),
  };
  // All four, or the ones asked for in the order asked.
  return <>{(only ?? (["difficulty", "gamemode", "maxPlayers", "pvp"] as const)).map((key) => fields[key])}</>;
}

/** From which role members are operators in game. */
export const autoOpOptions = (
  <>
    <option value="operator">Operators and above</option>
    <option value="admin">Admins and above</option>
    <option value="owner">Owners</option>
    <option value="off">Nobody</option>
  </>
);

/** From which role members are on the whitelist, short of nobody. */
export const memberRoleOptions = (
  <>
    <option value="viewer">Every member</option>
    <option value="operator">Operators and above</option>
    <option value="admin">Admins and above</option>
    <option value="owner">Owners</option>
  </>
);

/** How players reach a server. */
export const exposeOptions = (
  <>
    <option value="Router">Shared address, routed by hostname</option>
    <option value="LoadBalancer">Its own IP address (LoadBalancer)</option>
    <option value="NodePort">A port on every node (NodePort)</option>
    <option value="None">Inside the cluster only</option>
  </>
);

/** Who may join: anyone, the organization's members from a role up, or only who is on the whitelist. */
export type Join = "anyone" | "viewer" | "operator" | "admin" | "owner" | "list";

/** What "who can join" says under its control. */
export const joinHint = (join: Join) =>
  join === "anyone" ? "No whitelist." : join === "list" ? "The players on its whitelist." : "Those who have linked a Minecraft account, and the players on its whitelist.";

/** The choices for who can join. */
export const joinOptions = (
  <>
    <option value="anyone">Anyone with the address</option>
    {memberRoleOptions}
    <option value="list">Only its whitelist</option>
  </>
);

/** A part of a form that is closed until asked for, and says how much in it was changed. */
export function More({ label, changed = 0, open, onToggle }: { label: string; changed?: number; open: boolean; onToggle: () => void }) {
  return (
    <button type="button" aria-expanded={open} onClick={onToggle} className="flex w-fit items-center gap-1 text-[13px] text-muted hover:text-fg">
      {open ? <ChevronDown className="size-3.5" /> : <ChevronRight className="size-3.5" />}
      {label}
      {changed > 0 && <span className="text-accent">· {changed} changed</span>}
    </button>
  );
}

const gb = (bytes: number) => `${(bytes / 1024 ** 3).toFixed(1)} GB`;

/**
 * Where a new server goes: where the panel would put it, or a node chosen by
 * hand, slow ones included. Its world lives on that node's disk.
 */
export function NodeChoice({ memory, value, kept, onChange }: { memory: string; value: string; kept?: Record<string, string>; onChange: (node: string) => void }) {
  const wanted = fromGigabytes(memory);
  // The last answer stays up while the memory typed is asked about, so the
  // field does not blink away and back.
  const nodes = useQuery({ queryKey: ["nodes", wanted], queryFn: () => api.get<{ nodes: NodeInfo[]; automatic?: string }>(`/nodes?memory=${encodeURIComponent(wanted)}`), placeholderData: (last) => last });
  const list = nodes.data?.nodes ?? [];
  // A copy of a server kept to some nodes is kept to them too, unless asked.
  const keptTo = kept && Object.keys(kept).length > 0 ? Object.entries(kept).map(([k, v]) => `${k}=${v}`).join(", ") : "";
  // Shown with one node too, so that where it goes is never a guess.
  if (list.length === 0 && !value && !keptTo) return null;
  // What the server's pod asks for: its memory and a little for its workspace.
  const need = (Number(memory) || 0) * 1024 ** 3 + 32 * 1024 ** 2;
  const automatic = nodes.data?.automatic;
  return (
    <Field
      label="Node"
      hint={
        value && value !== "auto"
          ? "Its world is kept on this node's disk."
          : keptTo && !value
            ? "Where the original may run, as the nodes are labelled. Its world is kept on that node's disk."
            : list.length === 1
              ? "The only node there is. Its world is kept on its disk."
              : "Chosen by speed and free memory. Its world is kept on that node's disk."
      }
    >
      {(id) => (
        <Select id={id} value={value} onChange={(e) => onChange(e.target.value)}>
          {keptTo && <option value="">As the original: nodes with {keptTo}</option>}
          <option value={keptTo ? "auto" : ""}>{automatic ? `Automatic (now ${automatic})` : "Automatic"}</option>
          {list.map((n) => (
            <option key={n.name} value={n.name} disabled={!n.ready || n.free < need}>
              {n.name} · {n.score > 0 ? `speed ${n.score.toFixed(2)}` : "speed not measured"} · {gb(n.free)} free
              {!n.ready ? " · down" : n.free < need ? " · too little memory" : !n.automatic ? " · not used automatically" : ""}
            </option>
          ))}
        </Select>
      )}
    </Field>
  );
}
