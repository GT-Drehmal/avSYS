import { useQueryClient } from "@tanstack/react-query";
import { ArrowLeft, MoreHorizontal, Pencil, Plus, Trash2, X } from "lucide-react";
import { useState } from "react";
import { Link, useLocation, useNavigate, useParams } from "react-router";
import { BedrockMark, type Edition, Heads, NotAddedNotices, namesOf, notAddedOf, PlayerHead, PlayerNamesField, playerCount } from "../components/PlayerLists";
import { ListBar } from "../components/ListBar";
import { useOrg } from "../components/Shell";
import { Button, Confirm, Empty, Field, Input, Loading, Menu, MenuContent, MenuItem, MenuTrigger, Modal, Notice, PageHeader, SearchBox } from "../components/ui";
import { api, ApiError, atLeast, type NotAdded, type PlayerList, type PlayerListChange } from "../lib/api";
import { byCount, byText, byTime, type ListOptions, useList } from "../lib/list";
import { keys, usePlayerLists } from "../lib/queries";
import { matches } from "../lib/search";

const listOptions: ListOptions<PlayerList> = {
  said: (l) => [l.name, ...l.players.map((p) => p.name)],
  then: byText((l) => l.name),
  sorts: [
    { key: "name", label: "Name", compare: byText((l) => l.name) },
    { key: "changed", label: "Last changed", compare: byTime((l) => l.updatedAt) },
    { key: "size", label: "Players", compare: byCount((l) => l.players.length) },
  ],
};

/** "Used by 1 server", "Not used by any server". */
const usedBy = (l: PlayerList) => (l.servers.length === 0 ? "Not used by any server" : `Used by ${l.servers.length} ${l.servers.length === 1 ? "server" : "servers"}`);

/** An organization's saved lists of players, to put on its servers' whitelists and operators. */
export function Players() {
  const { org = "" } = useParams();
  const membership = useOrg();
  const canEdit = !!membership && atLeast(membership.role, "admin");
  const lists = usePlayerLists(org);
  const listed = useList(lists.data ?? [], listOptions);
  const [making, setMaking] = useState(false);

  const header = (
    <PageHeader
      title="Players"
      description="Saved lists of players for whitelists and operators."
      actions={
        canEdit && (
          <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setMaking(true)}>
            New list
          </Button>
        )
      }
    />
  );
  if (lists.isPending) return <Loading />;
  if (!lists.data) {
    return (
      <>
        {header}
        <Notice tone="danger">{lists.error instanceof ApiError ? lists.error.message : "Could not load the lists."}</Notice>
      </>
    );
  }
  const { shown } = listed;
  return (
    <div>
      {header}
      <div className="flex flex-col gap-3">
        {lists.data.length === 0 ? (
          <Empty title="No lists yet">Save the players of your club or an event once, and whitelist them on any server.</Empty>
        ) : (
          <>
            <ListBar list={listed} label="Search lists and players" />
            {shown.length === 0 ? (
              <p className="rounded-lg border border-line bg-surface px-4 py-8 text-center text-[13px] text-muted">No lists match.</p>
            ) : (
              <ul aria-label="Player lists" className="flex flex-col divide-y divide-line overflow-hidden rounded-lg border border-line bg-surface">
                {shown.map((l) => (
                  <li key={l.id} aria-label={l.name}>
                    <Link to={l.id} className="flex items-center gap-3 px-4 py-3 hover:bg-raised">
                      <div className="min-w-0 flex-1">
                        <p className="truncate text-[13px] font-medium">{l.name}</p>
                        <p className="mt-0.5 text-xs text-faint">
                          {playerCount(l.players.length)} · {usedBy(l)}
                        </p>
                      </div>
                      <Heads players={l.players} max={6} size={20} />
                    </Link>
                  </li>
                ))}
              </ul>
            )}
          </>
        )}
      </div>
      {making && <NewList org={org} onClose={() => setMaking(false)} />}
    </div>
  );
}

/** A new list, with any players pasted in. */
function NewList({ org, onClose }: { org: string; onClose: () => void }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const [name, setName] = useState("");
  const [names, setNames] = useState("");
  const [edition, setEdition] = useState<Edition>("java");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const make = async () => {
    setBusy(true);
    setError(null);
    try {
      const made = await api.post<PlayerListChange>(`/orgs/${org}/player-lists`, { name, ...namesOf(edition, names) });
      await qc.invalidateQueries({ queryKey: keys.playerLists(org) });
      navigate(made.id, { state: notAddedOf(made) });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not make the list.");
      setBusy(false);
    }
  };
  return (
    <Modal open onOpenChange={(o) => !o && onClose()} title="New list">
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          make();
        }}
      >
        <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder="Club members" maxLength={60} />}</Field>
        <PlayerNamesField label="Players" edition={edition} onEdition={setEdition} value={names} onChange={setNames} optional rows={5} />
        {error && <Notice tone="danger">{error}</Notice>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!name.trim()} loading={busy}>
            Make list
          </Button>
        </div>
      </form>
    </Modal>
  );
}

/** One saved list: its players, who to add, and the servers that use it. */
export function PlayerListPage() {
  const { org = "", list: id = "" } = useParams();
  const membership = useOrg();
  const canEdit = !!membership && atLeast(membership.role, "admin");
  const location = useLocation();
  const navigate = useNavigate();
  const qc = useQueryClient();
  const lists = usePlayerLists(org);
  const list = lists.data?.find((l) => l.id === id);
  const [query, setQuery] = useState("");
  const [adding, setAdding] = useState("");
  const [edition, setEdition] = useState<Edition>("java");
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  // Names not added, from what was last added: on making the list too.
  const [notAdded, setNotAdded] = useState(() => notAddedOf(location.state as Partial<NotAdded> | null));
  const [renaming, setRenaming] = useState(false);
  const [deleting, setDeleting] = useState(false);

  const back = (
    <Link to={`/o/${org}/players`} className="mb-4 inline-flex items-center gap-1 text-[13px] text-muted hover:text-fg">
      <ArrowLeft className="size-3.5" />
      Players
    </Link>
  );
  if (lists.isPending) return <Loading />;
  if (!list) {
    return (
      <div>
        {back}
        <Empty title="No such list">It may have been deleted.</Empty>
      </div>
    );
  }

  /** Puts the answer to a change in place of the list as it was. */
  const changed = (now: PlayerListChange) => {
    qc.setQueryData<PlayerList[]>(keys.playerLists(org), (all) => all?.map((l) => (l.id === now.id ? now : l)));
    return qc.invalidateQueries({ queryKey: keys.playerLists(org) });
  };
  const act = async (what: string, work: () => Promise<PlayerListChange>) => {
    setBusy(what);
    setError(null);
    try {
      const now = await work();
      await changed(now);
      return now;
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "That did not work.");
      return null;
    } finally {
      setBusy(null);
    }
  };
  const add = async () => {
    const now = await act("add", () => api.post<PlayerListChange>(`/orgs/${org}/player-lists/${id}/players`, namesOf(edition, adding)));
    if (now) {
      setNotAdded(notAddedOf(now));
      setAdding([...now.missing, ...now.missingBedrock].join("\n"));
    }
  };
  /** Asks again about the names that could not be checked. */
  const tryAgain = async () => {
    const now = await act("again", () => api.post<PlayerListChange>(`/orgs/${org}/player-lists/${id}/players`, { names: notAdded.unchecked, bedrock: notAdded.uncheckedBedrock }));
    if (now) {
      setNotAdded((was) => ({
        missing: [...was.missing, ...now.missing],
        missingBedrock: [...was.missingBedrock, ...now.missingBedrock],
        unchecked: now.unchecked,
        uncheckedBedrock: now.uncheckedBedrock,
      }));
    }
  };
  const shown = list.players.filter((p) => matches(query, p.name));

  return (
    <div>
      {back}
      <PageHeader
        title={list.name}
        description={`${playerCount(list.players.length)} · ${usedBy(list)}`}
        actions={
          canEdit && (
            <Menu>
              <MenuTrigger asChild>
                <Button variant="ghost" icon={<MoreHorizontal className="size-4" />} aria-label={`Actions for ${list.name}`} />
              </MenuTrigger>
              <MenuContent>
                <MenuItem icon={<Pencil />} onSelect={() => setRenaming(true)}>
                  Rename
                </MenuItem>
                <MenuItem icon={<Trash2 />} danger onSelect={() => setDeleting(true)}>
                  Delete
                </MenuItem>
              </MenuContent>
            </Menu>
          )
        }
      />
      <div className="flex max-w-2xl flex-col gap-5">
        {list.servers.length > 0 && (
          <p className="text-[13px] text-muted">
            On{" "}
            {list.servers.map((s, i) => (
              <span key={s.name}>
                {i > 0 && ", "}
                <Link to={`/o/${org}/s/${s.name}/settings`} className="text-accent hover:underline">
                  {s.displayName || s.name}
                </Link>{" "}
                ({[s.whitelist && "whitelist", s.ops && "operators"].filter(Boolean).join(" and ")})
              </span>
            ))}
          </p>
        )}
        {canEdit && (
          <form
            className="flex flex-col gap-2"
            onSubmit={(e) => {
              e.preventDefault();
              add();
            }}
          >
            <PlayerNamesField label="Add players" edition={edition} onEdition={setEdition} value={adding} onChange={setAdding} />
            <div className="flex items-center gap-3">
              <Button type="submit" variant="primary" disabled={Object.values(namesOf(edition, adding)).every((n) => n.length === 0)} loading={busy === "add"}>
                Add
              </Button>
            </div>
          </form>
        )}
        <NotAddedNotices names={notAdded} onTryAgain={canEdit ? tryAgain : undefined} trying={busy === "again"} />
        {error && <Notice tone="danger">{error}</Notice>}
        {list.players.length === 0 ? (
          <Empty title="No players yet">{canEdit ? "Add some above." : "An admin can add some."}</Empty>
        ) : (
          <div className="flex flex-col gap-2">
            <SearchBox value={query} onChange={setQuery} label="Search players" />
            {shown.length === 0 ? (
              <p className="rounded-lg border border-line bg-surface px-4 py-6 text-center text-[13px] text-muted">No players match.</p>
            ) : (
              <ul aria-label="Players" className="flex flex-col divide-y divide-line overflow-hidden rounded-lg border border-line bg-surface">
                {shown.map((p) => (
                  <li key={p.uuid} aria-label={p.name} className="flex items-center gap-3 px-3 py-1.5">
                    <PlayerHead player={p} size={24} />
                    <span className="min-w-0 flex-1 truncate font-mono text-[13px]">{p.name}</span>
                    {p.kind === "bedrock" && <BedrockMark />}
                    {canEdit && (
                      <Button
                        size="sm"
                        variant="ghost"
                        icon={<X className="size-3.5" />}
                        aria-label={`Remove ${p.name}`}
                        loading={busy === p.uuid}
                        disabled={busy !== null}
                        onClick={() => act(p.uuid, () => api.del<PlayerListChange>(`/orgs/${org}/player-lists/${id}/players/${p.uuid}`))}
                      />
                    )}
                  </li>
                ))}
              </ul>
            )}
          </div>
        )}
      </div>

      {renaming && <RenameList org={org} list={list} onClose={() => setRenaming(false)} onRenamed={changed} />}
      <Confirm
        open={deleting}
        onOpenChange={setDeleting}
        title={`Delete ${list.name}?`}
        description={
          list.servers.length === 0
            ? "Not used by any server."
            : `${usedBy(list)}. Its players come off ${list.servers.length === 1 ? "that server" : "those servers"}, unless they are on there another way.`
        }
        action="Delete list"
        loading={busy === "delete"}
        onConfirm={async () => {
          setBusy("delete");
          try {
            await api.del(`/orgs/${org}/player-lists/${id}`);
            await qc.invalidateQueries({ queryKey: keys.playerLists(org) });
            navigate(`/o/${org}/players`);
          } catch (err) {
            setDeleting(false);
            setError(err instanceof ApiError ? err.message : "Could not delete the list.");
          } finally {
            setBusy(null);
          }
        }}
      />
    </div>
  );
}

function RenameList({ org, list, onClose, onRenamed }: { org: string; list: PlayerList; onClose: () => void; onRenamed: (now: PlayerListChange) => Promise<unknown> }) {
  const [name, setName] = useState(list.name);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  return (
    <Modal open onOpenChange={(o) => !o && onClose()} title="Rename list">
      <form
        className="flex flex-col gap-4"
        onSubmit={async (e) => {
          e.preventDefault();
          setBusy(true);
          try {
            await onRenamed(await api.patch<PlayerListChange>(`/orgs/${org}/player-lists/${list.id}`, { name }));
            onClose();
          } catch (err) {
            setError(err instanceof ApiError ? err.message : "Could not rename the list.");
            setBusy(false);
          }
        }}
      >
        <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} maxLength={60} />}</Field>
        {error && <Notice tone="danger">{error}</Notice>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose}>Cancel</Button>
          <Button type="submit" variant="primary" disabled={!name.trim() || name.trim() === list.name} loading={busy}>
            Rename
          </Button>
        </div>
      </form>
    </Modal>
  );
}
