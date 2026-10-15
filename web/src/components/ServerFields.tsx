import { ChevronDown, ChevronRight } from "lucide-react";
import type { ReactNode } from "react";
import { atLeast, type Member, type Role } from "../lib/api";
import { difficulties, gameModes } from "../lib/properties";
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

/** Members put on a list, or left off it, whatever their role. */
export interface Picks {
  also: string[];
  except: string[];
}

export const noPicks: Picks = { also: [], except: [] };

/** Whether a member is on a list: by their role, unless picked otherwise. */
const onList = (byRole: boolean, picks: Picks, id: string) => picks.also.includes(id) || (byRole && !picks.except.includes(id));

/** Puts a member on a list or takes them off, as a pick only where their role says otherwise. */
function toggled(byRole: boolean, picks: Picks, id: string, on: boolean): Picks {
  const also = picks.also.filter((x) => x !== id);
  const except = picks.except.filter((x) => x !== id);
  if (on && !byRole) also.push(id);
  if (!on && byRole) except.push(id);
  return { also, except };
}

const roles: Role[] = ["owner", "admin", "operator", "viewer"];
const byRoleThenName = (a: Member, b: Member) =>
  roles.indexOf(a.role) - roles.indexOf(b.role) || (a.displayName || a.username).localeCompare(b.displayName || b.username);

interface MemberListsProps {
  members: Member[];
  /** The role from which members are on each list, or "" for nobody. */
  whitelistFrom: Role | "";
  opFrom: Role | "";
  whitelist: Picks;
  ops: Picks;
  onChange: (lists: { whitelist: Picks; ops: Picks }) => void;
}

/**
 * Each member of the organization, on or off a server's whitelist and its
 * operators. Their role decides, unless they are picked otherwise here.
 */
export function MemberLists({ members, whitelistFrom, opFrom, whitelist, ops, onChange }: MemberListsProps) {
  const columns = [
    { key: "whitelist" as const, label: "Whitelisted", from: whitelistFrom, picks: whitelist },
    { key: "ops" as const, label: "Operator", from: opFrom, picks: ops },
  ];
  return (
    <div className="overflow-hidden rounded-lg border border-line">
      <table className="w-full text-[13px]">
        <thead className="bg-surface text-left text-xs text-muted">
          <tr>
            <th className="px-3 py-2 font-medium">Member</th>
            {columns.map((c) => (
              <th key={c.key} className="w-28 px-3 py-2 font-medium">
                {c.label}
              </th>
            ))}
          </tr>
        </thead>
        <tbody>
          {[...members].sort(byRoleThenName).map((m) => {
            const name = m.displayName || m.username;
            return (
              <tr key={m.userId} className="border-t border-line">
                <td className="px-3 py-2">
                  <div className="font-medium text-fg">{name}</div>
                  <div className="text-xs text-muted">
                    {m.role} · {m.minecraftName ? <span className="font-mono">{m.minecraftName}</span> : "no Minecraft account linked"}
                  </div>
                </td>
                {columns.map((c) => {
                  const byRole = !!c.from && atLeast(m.role, c.from);
                  const on = onList(byRole, c.picks, m.userId);
                  return (
                    <td key={c.key} className="px-3 py-2 align-top">
                      <label className="flex items-center gap-2">
                        <input
                          type="checkbox"
                          aria-label={`${c.label}: ${name}`}
                          checked={on}
                          onChange={(e) => {
                            const next = toggled(byRole, c.picks, m.userId, e.target.checked);
                            onChange({ whitelist, ops, [c.key]: next });
                          }}
                          className="size-3.5 accent-(--color-accent)"
                        />
                        <span className="text-xs text-faint">{on === byRole ? "by role" : on ? "picked" : "left out"}</span>
                      </label>
                    </td>
                  );
                })}
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
