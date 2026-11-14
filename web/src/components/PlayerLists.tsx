import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { api, ApiError, playerHead, type ListPlayer, type PlayerList, type PlayerListChange } from "../lib/api";
import { keys, usePlayerLists } from "../lib/queries";
import { matches } from "../lib/search";
import { Picture } from "./Picture";
import { Button, cx, Field, Input, Modal, Notice, SearchBox } from "./ui";

/** Names typed or pasted: one a line, or between commas, spaces or semicolons. */
export const typedNames = (text: string) => text.split(/[\s,;]+/).filter(Boolean);

/** "1 player", "3 players". */
export const playerCount = (n: number) => `${n} ${n === 1 ? "player" : "players"}`;

/** A player's head, or their initial for one with no skin of their own. */
export function PlayerHead({ player, size = 20 }: { player: ListPlayer; size?: number }) {
  return <Picture name={player.name} url={playerHead(player.uuid)} size={size} pixelated />;
}

/** The heads of the first few players of a list, in a row. */
export function Heads({ players, max = 5, size = 18 }: { players: ListPlayer[]; max?: number; size?: number }) {
  if (players.length === 0) return null;
  return (
    <span className="flex shrink-0 items-center gap-0.5" aria-hidden>
      {players.slice(0, max).map((p) => (
        <PlayerHead key={p.uuid} player={p} size={size} />
      ))}
      {players.length > max && <span className="pl-1 text-[11px] text-faint">+{players.length - max}</span>}
    </span>
  );
}

/** What is said of names no Minecraft account has. */
export function Missing({ names }: { names: string[] }) {
  if (names.length === 0) return null;
  return <Notice>No Minecraft account: {names.join(", ")}</Notice>;
}

interface PickerProps {
  org: string;
  /** The lists chosen, by ID. One that is not there any more is left out. */
  value: string[];
  onChange: (ids: string[]) => void;
  /** What the lists are for, as "whitelist": names the picker and each box. */
  label: string;
}

/**
 * Saved lists of players to pick from, several at once: each with how many
 * players it has and the heads of a few. Searched once there are more than
 * a handful.
 */
export function SavedListPicker({ org, value, onChange, label }: PickerProps) {
  const lists = usePlayerLists(org);
  const [query, setQuery] = useState("");
  const all = lists.data ?? [];
  const chosen = all.filter((l) => value.includes(l.id));
  const players = new Set(chosen.flatMap((l) => l.players.map((p) => p.uuid))).size;
  const shown = all.filter((l) => matches(query, l.name, ...l.players.map((p) => p.name)));
  if (lists.isPending) return <div className="h-16 rounded-lg border border-line bg-surface" />;
  if (all.length === 0) {
    return (
      <p className="rounded-lg border border-dashed border-line-strong px-3 py-3 text-xs text-muted">
        No saved lists yet.{" "}
        <Link to={`/o/${org}/players`} className="text-accent hover:underline">
          Make one under Players
        </Link>
        .
      </p>
    );
  }
  const toggle = (id: string, on: boolean) => onChange(on ? [...value.filter((x) => x !== id), id] : value.filter((x) => x !== id));
  return (
    <div className="flex flex-col gap-2">
      {all.length > 5 && <SearchBox value={query} onChange={setQuery} label={`Search saved lists for ${label}`} className="w-full" />}
      <ul aria-label={`Saved lists for ${label}`} className="max-h-60 divide-y divide-line overflow-y-auto rounded-lg border border-line bg-surface">
        {shown.map((l) => {
          const on = value.includes(l.id);
          return (
            <li key={l.id}>
              <label className={cx("flex cursor-pointer items-center gap-3 px-3 py-2 text-[13px]", on && "bg-accent-wash")}>
                <input type="checkbox" checked={on} onChange={(e) => toggle(l.id, e.target.checked)} aria-label={`${l.name} on the ${label}`} className="size-3.5 accent-(--color-accent)" />
                <span className="min-w-0 flex-1 truncate font-medium text-fg">{l.name}</span>
                <span className="shrink-0 text-xs text-muted">{playerCount(l.players.length)}</span>
                <Heads players={l.players} max={4} size={16} />
              </label>
            </li>
          );
        })}
        {shown.length === 0 && <li className="px-3 py-3 text-center text-xs text-muted">No lists match.</li>}
      </ul>
      {chosen.length > 0 && (
        <p className="text-xs text-muted">
          {chosen.length} {chosen.length === 1 ? "list" : "lists"} chosen · {playerCount(players)}
        </p>
      )}
    </div>
  );
}

/** A choice of saved lists with what it is for above it, as a form's field. */
export function SavedListsField({ title, hint, ...picker }: PickerProps & { title: string; hint?: string }) {
  return (
    <div className="flex flex-col gap-1.5">
      <div className="text-[13px] font-medium text-fg">{title}</div>
      {hint && <p className="text-xs leading-relaxed text-muted">{hint}</p>}
      <SavedListPicker {...picker} />
    </div>
  );
}

/**
 * Starts a saved list with who is on one of a server's lists now, in game
 * as well as by the panel, and opens it.
 */
export function SaveAsList({ org, server, list, open, onOpenChange }: { org: string; server: string; list: "whitelist" | "ops"; open: boolean; onOpenChange: (open: boolean) => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const what = list === "whitelist" ? "whitelist" : "operators";
  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      const made = await api.post<PlayerListChange>(`/orgs/${org}/player-lists`, { name, fromServer: server, list });
      qc.setQueryData<PlayerList[]>(keys.playerLists(org), (all) => (all ? [...all, made] : all));
      await qc.invalidateQueries({ queryKey: keys.playerLists(org) });
      onOpenChange(false);
      navigate(`/o/${org}/players/${made.id}`, { state: { missing: made.missing } });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not save the list.");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal open={open} onOpenChange={onOpenChange} title={`Save the ${what} as a list`} description="Everyone on it now, added in game too. Use the list on other servers.">
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          save();
        }}
      >
        <Field label="List name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder="Club members" maxLength={60} />}</Field>
        {error && <Notice tone="danger">{error}</Notice>}
        <div className="flex justify-end gap-2">
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!name.trim()} loading={busy}>
            Save list
          </Button>
        </div>
      </form>
    </Modal>
  );
}
