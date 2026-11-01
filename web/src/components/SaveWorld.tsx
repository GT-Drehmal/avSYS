import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, ApiError, type LibraryWorld, type Server, type Snapshot } from "../lib/api";
import { keys } from "../lib/queries";
import { toast } from "../lib/toast";
import { Button, Field, Input, Modal, Notice, Textarea } from "./ui";

/**
 * Puts a server's world in the organization's library, as it is now (a
 * snapshot is taken first), or a snapshot's.
 */
export function SaveWorld({ org, server, snapshot, onClose }: { org: string; server?: Server; snapshot?: Snapshot; onClose: () => void }) {
  const qc = useQueryClient();
  const named = server ? server.spec.displayName || server.name : (snapshot?.server ?? "");
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const save = async () => {
    setBusy(true);
    setError(null);
    try {
      const made = await api.post<LibraryWorld>(`/orgs/${org}/worlds`, { name: name.trim() || named, description, ...(server ? { server: server.name } : { snapshot: snapshot?.id }) });
      await qc.invalidateQueries({ queryKey: keys.worlds(org) });
      if (server) void qc.invalidateQueries({ queryKey: keys.snapshots(org) });
      toast.info(`Saving ${made.name} to Worlds`);
      onClose();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "Could not save the world.");
    } finally {
      setBusy(false);
    }
  };
  return (
    <Modal
      open
      onOpenChange={(o) => !o && !busy && onClose()}
      title="Save world to library"
      description={server ? "A snapshot is taken first, and its world saved from that." : "Its world is kept in Worlds, to start servers from."}
    >
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          void save();
        }}
      >
        <Field label="Name">{(id) => <Input id={id} maxLength={60} value={name} placeholder={named} onChange={(e) => setName(e.target.value)} />}</Field>
        <Field label="Description">{(id) => <Textarea id={id} rows={2} maxLength={500} value={description} onChange={(e) => setDescription(e.target.value)} />}</Field>
        {error && <Notice tone="danger">{error}</Notice>}
        <div className="flex justify-end gap-2">
          <Button onClick={onClose} disabled={busy}>
            Cancel
          </Button>
          <Button type="submit" variant="primary" loading={busy}>
            Save
          </Button>
        </div>
      </form>
    </Modal>
  );
}
