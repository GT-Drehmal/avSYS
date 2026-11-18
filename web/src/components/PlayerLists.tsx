import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { Link, useNavigate } from "react-router";
import { api, ApiError, type ContentRef, playerHead, type ListPlayer, type Member, type PlayerList, type PlayerListChange } from "../lib/api";
import { keys, usePlayerLists } from "../lib/queries";
import { matches } from "../lib/search";
import { Picture } from "./Picture";
import { Button, cx, Field, Input, Modal, Notice, SearchBox, Textarea } from "./ui";

/** Names typed or pasted: one a line, or between commas, spaces or semicolons. */
export const typedNames = (text: string) => text.split(/[\s,;]+/).filter(Boolean);

/** Gamertags typed or pasted: one a line, or between commas or semicolons. A gamertag can have spaces. */
export const typedGamertags = (text: string) =>
  text
    .split(/[\n,;]+/)
    .map((t) => t.trim())
    .filter(Boolean);

/** Which edition names typed are of. */
export type Edition = "java" | "bedrock";

/** The names typed, as the API takes them. */
export const namesOf = (edition: Edition, text: string) => (edition === "bedrock" ? { names: [], bedrock: typedGamertags(text) } : { names: typedNames(text), bedrock: [] });

/** "1 player", "3 players". */
export const playerCount = (n: number) => `${n} ${n === 1 ? "player" : "players"}`;

/** Says a player plays Bedrock Edition. */
export function BedrockMark() {
  return <span className="shrink-0 rounded border border-line-strong px-1 py-px text-[10px] font-medium leading-none text-muted">Bedrock</span>;
}

/**
 * Names to add to a list, typed or pasted, with which edition they are of:
 * Java names, or Bedrock gamertags, which can have spaces.
 */
export function PlayerNamesField({ label, edition, onEdition, value, onChange, optional, rows = 3 }: { label: string; edition: Edition; onEdition: (edition: Edition) => void; value: string; onChange: (text: string) => void; optional?: boolean; rows?: number }) {
  const segment = (on: boolean) => cx("h-6 rounded px-2.5 text-xs font-medium", on ? "bg-raised text-fg" : "text-muted hover:text-fg");
  const hint = edition === "bedrock" ? "Xbox gamertags, one per line or separated by commas." : "Minecraft names, one per line or separated by commas.";
  return (
    <Field label={label} hint={optional ? `Optional. ${hint}` : hint}>
      {(id) => (
        <div className="flex flex-col gap-1.5">
          <div className="flex w-fit gap-1 rounded-md border border-line bg-bg p-0.5" role="group" aria-label="Edition">
            <button type="button" className={segment(edition === "java")} aria-pressed={edition === "java"} onClick={() => onEdition("java")}>
              Java
            </button>
            <button type="button" className={segment(edition === "bedrock")} aria-pressed={edition === "bedrock"} onClick={() => onEdition("bedrock")}>
              Bedrock
            </button>
          </div>
          <Textarea id={id} rows={rows} value={value} onChange={(e) => onChange(e.target.value)} placeholder={edition === "bedrock" ? "Steve\nAlex Smith" : "Steve\nAlex"} className="font-mono" spellCheck={false} />
        </div>
      )}
    </Field>
  );
}

/** A player, by name, and by UUID where it is known. */
export type Player = Pick<ListPlayer, "name"> & { uuid?: string };

/** The account a member plays as: their main one. */
export const mainAccount = (m: Pick<Member, "minecraftName" | "minecraftAccounts">): Player =>
  m.minecraftAccounts?.find((a) => a.main) ?? m.minecraftAccounts?.[0] ?? { name: m.minecraftName ?? "" };

/**
 * A player's head, or their initial for one with no skin of their own, or
 * whose UUID is not known. Drawn beside their name: it says nothing more.
 */
export function PlayerHead({ player, size = 20 }: { player: Player; size?: number }) {
  return <Picture name={player.name} url={player.uuid ? playerHead(player.uuid) : undefined} size={size} pixelated />;
}

/** The heads of the first few players of a list, in a row. */
export function Heads({ players, max = 5, size = 18 }: { players: Player[]; max?: number; size?: number }) {
  if (players.length === 0) return null;
  return (
    <span className="flex shrink-0 items-center gap-0.5" aria-hidden>
      {players.slice(0, max).map((p, i) => (
        // Two hidden players have neither a UUID nor a name of their own.
        <PlayerHead key={`${p.uuid ?? p.name}-${i}`} player={p} size={size} />
      ))}
      {players.length > max && <span className="pl-1 text-[11px] text-faint">+{players.length - max}</span>}
    </span>
  );
}

/** What is said of names no Minecraft account has, and of gamertags no Bedrock player was found for. */
export function Missing({ names, bedrock = [] }: { names: string[]; bedrock?: string[] }) {
  if (names.length === 0 && bedrock.length === 0) return null;
  return (
    <>
      {names.length > 0 && <Notice>No Minecraft account: {names.join(", ")}</Notice>}
      {bedrock.length > 0 && <Notice>No Bedrock player found: {bedrock.join(", ")}. A Bedrock player can be found once they have played on any server with Geyser.</Notice>}
    </>
  );
}

/** Whether a server's content has a mod or plugin, by its Modrinth slug or ID, or by its name for one from elsewhere. */
const hasMod = (mods: ContentRef[], slug: string, id: string) =>
  mods.some((m) => m.project === slug || m.project === id || [m.project, m.title].some((s) => s?.toLowerCase().includes(slug)));

/** Whether Bedrock players can join a server with this content: through Geyser, let in by Floodgate. */
export const takesBedrock = (mods: ContentRef[]) => hasMod(mods, "geyser", "wKkoqHrH") && hasMod(mods, "floodgate", "bWrNNfkb");

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
      navigate(`/o/${org}/players/${made.id}`, { state: { missing: made.missing, missingBedrock: made.missingBedrock } });
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

/**
 * Says plainly when a server uses saved lists with Bedrock players on them
 * but cannot let Bedrock players in: without Geyser and Floodgate.
 */
export function BedrockNeedsMods({ org, server, mods, ids }: { org: string; server: string; mods: ContentRef[]; ids: string[] }) {
  const lists = usePlayerLists(org);
  const bedrock = (lists.data ?? []).some((l) => ids.includes(l.id) && l.players.some((p) => p.kind === "bedrock"));
  if (!bedrock || takesBedrock(mods)) return null;
  return (
    <Notice
      action={
        <Link to={`/o/${org}/s/${server}/content`} className="shrink-0 text-xs font-medium text-accent hover:underline">
          Go to Content
        </Link>
      }
    >
      Bedrock players can't join this server until Geyser and Floodgate are added.
    </Notice>
  );
}
