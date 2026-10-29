import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, ApiError, snapshotIcon, type Server, type Snapshot, type Template } from "../lib/api";
import { ago, describeSoftware, displayName } from "../lib/format";
import { matches } from "../lib/search";
import { keys, useTemplates } from "../lib/queries";
import { Picture } from "./Picture";
import { software, triggerLabel } from "./Snapshots";
import { List, ServerRow, SnapshotPicker } from "./StartPicker";
import { Button, cx, Field, Input, Modal, Notice, SearchBox, Select, Textarea } from "./ui";

/**
 * Saves a server or a snapshot as a template: a new one, or the newest
 * version of one there is. With server or snapshot given it is that one;
 * otherwise one is picked. With into given it is a new version of that
 * template. A server's saving goes on on its page, and this closes as it
 * starts; a snapshot's copy is made at once.
 */
export function SaveTemplate({
  org,
  open,
  onOpenChange,
  server,
  snapshot,
  servers = [],
  templates: given,
  into,
}: {
  org: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  server?: Server;
  snapshot?: Snapshot;
  servers?: Server[];
  templates?: Template[];
  into?: Template;
}) {
  const qc = useQueryClient();
  // Asked for here only where the page has not got them.
  const asked = useTemplates(org, !given);
  const templates = given ?? asked.data ?? [];
  // What it is saved from, where that is to be picked.
  const [kind, setKind] = useState<"server" | "snapshot">("server");
  const choosing = !server && !snapshot;
  const [picked, setPicked] = useState(server?.name ?? "");
  const [pickedSnapshot, setPickedSnapshot] = useState<Snapshot | null>(null);
  const [query, setQuery] = useState("");
  const fromServer = server ?? (choosing && kind === "server" ? servers.find((s) => s.name === picked) : undefined);
  const fromSnapshot = snapshot ?? (choosing && kind === "snapshot" ? pickedSnapshot : null);
  // A new one, or a template there is, by its ID.
  const [target, setTarget] = useState<string>(into?.id ?? "new");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const updating = templates.find((t) => t.id === target);
  // A snapshot is called after its server, as it is now if it is still there.
  const snapshotName = (s: Snapshot) => {
    const live = servers.find((x) => x.name === s.server);
    return live ? displayName(live) : s.server;
  };
  const suggested = fromServer ? displayName(fromServer) : fromSnapshot ? snapshotName(fromSnapshot) : "";

  const close = (o: boolean) => {
    if (busy) return;
    onOpenChange(o);
    if (!o) {
      setError(null);
      setName("");
      setDescription("");
      setTarget(into?.id ?? "new");
      setPicked(server?.name ?? "");
      setPickedSnapshot(null);
      setQuery("");
    }
  };

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!fromServer && !fromSnapshot) return setError(kind === "snapshot" ? "Pick a snapshot." : "Pick a server.");
    setBusy(true);
    setError(null);
    const source = fromServer ? { server: fromServer.name } : { snapshot: fromSnapshot!.id };
    try {
      if (updating) await api.post(`/orgs/${org}/templates/${updating.id}/versions`, source);
      else await api.post(`/orgs/${org}/templates`, { name: name.trim() || suggested, description: description.trim(), ...source });
      void qc.invalidateQueries({ queryKey: keys.templates(org) });
      setBusy(false);
      close(false);
    } catch (err) {
      setBusy(false);
      setError(err instanceof ApiError ? err.message : "Could not save the template.");
    }
  };

  const segment = (on: boolean) => cx("h-7 rounded px-3 text-xs font-medium", on ? "bg-raised text-fg" : "text-muted hover:text-fg");
  return (
    <Modal open={open} onOpenChange={close} title={into ? `Update ${into.name}` : "Save as template"}>
      <form className="flex flex-col gap-4" onSubmit={save}>
        {snapshot && (
          <div className="flex items-center gap-3 rounded-md border border-line px-3 py-2.5">
            <Picture name={snapshotName(snapshot)} url={snapshotIcon(org, snapshot.id)} size={32} />
            <div className="min-w-0 flex-1">
              <p className="truncate text-[13px] font-medium">
                {snapshotName(snapshot)}, {ago(snapshot.time)}
              </p>
              <p className="truncate text-xs text-muted">{[software(snapshot), triggerLabel(snapshot)].filter(Boolean).join(" · ")}</p>
            </div>
          </div>
        )}
        {choosing && (
          <div className="flex flex-col gap-1.5">
            <div className="flex w-fit gap-1 rounded-md border border-line bg-bg p-0.5" role="group" aria-label="Save from">
              <button type="button" className={segment(kind === "server")} aria-pressed={kind === "server"} onClick={() => setKind("server")}>
                A server
              </button>
              <button type="button" className={segment(kind === "snapshot")} aria-pressed={kind === "snapshot"} onClick={() => setKind("snapshot")}>
                A snapshot
              </button>
            </div>
            {kind === "snapshot" ? (
              <SnapshotPicker org={org} onPick={setPickedSnapshot} picked={pickedSnapshot?.id} className="max-h-64" />
            ) : servers.length === 0 ? (
              <p className="text-xs text-muted">No servers yet.</p>
            ) : (
              <>
                {servers.length > 5 && <SearchBox value={query} onChange={setQuery} label="Search servers" className="w-full" />}
                <List label="Servers" className="max-h-64">
                  {servers
                    .filter((s) => s.name === picked || matches(query, s.name, displayName(s), describeSoftware(s)))
                    .map((s) => (
                      <ServerRow key={s.name} server={s} selected={s.name === picked} onClick={() => setPicked(s.name)} />
                    ))}
                </List>
              </>
            )}
          </div>
        )}
        {!into && templates.length > 0 && (
          <div className="flex w-fit gap-1 rounded-md border border-line bg-bg p-0.5" role="group" aria-label="Save as">
            <button type="button" className={segment(target === "new")} aria-pressed={target === "new"} onClick={() => setTarget("new")}>
              New template
            </button>
            <button type="button" className={segment(target !== "new")} aria-pressed={target !== "new"} onClick={() => setTarget(templates[0].id)}>
              Update one
            </button>
          </div>
        )}
        {!into && updating && (
          <Field label="Template">
            {(id) => (
              <Select id={id} value={target} onChange={(e) => setTarget(e.target.value)}>
                {templates.map((t) => (
                  <option key={t.id} value={t.id}>
                    {t.name}
                  </option>
                ))}
              </Select>
            )}
          </Field>
        )}
        {updating ? (
          <p className="text-[13px] text-muted">
            {fromSnapshot ? "This snapshot" : `${fromServer ? displayName(fromServer) : "The server"} as it is now`} becomes the newest version of {updating.name}. Earlier versions are kept.
          </p>
        ) : (
          <>
            <Field label="Name">{(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder={suggested} maxLength={60} />}</Field>
            <Field label="Description">{(id) => <Textarea id={id} value={description} onChange={(e) => setDescription(e.target.value)} rows={3} maxLength={500} />}</Field>
            <p className="text-[13px] text-muted">The server's files, world and settings are saved. New servers can be made from the template.</p>
          </>
        )}
        {error && <Notice tone="danger">{error}</Notice>}
        <div className="flex justify-end gap-2">
          <Button onClick={() => close(false)}>Cancel</Button>
          <Button type="submit" variant="primary" loading={busy} disabled={!fromServer && !fromSnapshot}>
            {updating ? "Update" : "Save"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
