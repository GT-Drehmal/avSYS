import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, ApiError, type Server, type Template } from "../lib/api";
import { displayName } from "../lib/format";
import { keys } from "../lib/queries";
import { List, ServerRow } from "./StartPicker";
import { Button, cx, Field, Input, Modal, Notice, Select, Textarea } from "./ui";

/**
 * Saves a server as a template: a new one, or the newest version of one
 * there is. With server given it is that server's; otherwise one is picked.
 * With into given it is a new version of that template. The saving goes on
 * on the server's page, and this closes as it starts.
 */
export function SaveTemplate({
  org,
  open,
  onOpenChange,
  server,
  servers,
  templates,
  into,
}: {
  org: string;
  open: boolean;
  onOpenChange: (open: boolean) => void;
  server?: Server;
  servers: Server[];
  templates: Template[];
  into?: Template;
}) {
  const qc = useQueryClient();
  const [picked, setPicked] = useState(server?.name ?? "");
  const from = server ?? servers.find((s) => s.name === picked);
  // A new one, or a template there is, by its ID.
  const [target, setTarget] = useState<string>(into?.id ?? "new");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const updating = templates.find((t) => t.id === target);

  const close = (o: boolean) => {
    if (busy) return;
    onOpenChange(o);
    if (!o) {
      setError(null);
      setName("");
      setDescription("");
      setTarget(into?.id ?? "new");
      setPicked(server?.name ?? "");
    }
  };

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!from) return setError("Pick a server.");
    setBusy(true);
    setError(null);
    try {
      if (updating) await api.post(`/orgs/${org}/templates/${updating.id}/versions`, { server: from.name });
      else await api.post(`/orgs/${org}/templates`, { name: name.trim() || displayName(from), description: description.trim(), server: from.name });
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
        {!server && (
          <div className="flex flex-col gap-1.5">
            <p className="text-[13px] font-medium text-fg">Server</p>
            {servers.length === 0 ? (
              <p className="text-xs text-muted">No servers yet.</p>
            ) : (
              <List label="Servers" className="max-h-64">
                {servers.map((s) => (
                  <ServerRow key={s.name} server={s} selected={s.name === picked} onClick={() => setPicked(s.name)} />
                ))}
              </List>
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
            {from ? displayName(from) : "The server"} as it is now becomes the newest version of {updating.name}. Its earlier versions are kept.
          </p>
        ) : (
          <>
            <Field label="Name">
              {(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder={from ? displayName(from) : ""} maxLength={60} />}
            </Field>
            <Field label="Description">{(id) => <Textarea id={id} value={description} onChange={(e) => setDescription(e.target.value)} rows={3} maxLength={500} />}</Field>
            <p className="text-[13px] text-muted">Its files and settings are saved. New servers can be made from it.</p>
          </>
        )}
        {error && <Notice tone="danger">{error}</Notice>}
        <div className="flex justify-end gap-2">
          <Button onClick={() => close(false)}>Cancel</Button>
          <Button type="submit" variant="primary" loading={busy} disabled={!from}>
            {updating ? "Update" : "Save"}
          </Button>
        </div>
      </form>
    </Modal>
  );
}
