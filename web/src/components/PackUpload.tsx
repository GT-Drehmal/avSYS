import { useQueryClient } from "@tanstack/react-query";
import { FileArchive, Upload } from "lucide-react";
import { useRef } from "react";
import { api, serverPath, type Server } from "../lib/api";
import { ago, bytes } from "../lib/format";
import { applyServer, useAction } from "../lib/queries";
import { toast } from "../lib/toast";
import { Button } from "./ui";
import { cancelled, UploadProgress, useUpload } from "./Uploading";

/**
 * A resource pack uploaded to the panel, which keeps it with the server's
 * files and serves it to players: what it is, and replacing or removing it.
 * With none, a way to upload one.
 */
export function PackUpload({ server, onChanged }: { server: Server; onChanged?: (next: Server) => void }) {
  const qc = useQueryClient();
  const input = useRef<HTMLInputElement>(null);
  const path = `${serverPath(server.org, server.name)}/resource-pack`;
  const uploaded = server.spec.resourcePack?.uploaded;
  // The answer is the server as it is now, which every page showing it takes.
  const done = (next: Server) => {
    applyServer(qc, next);
    onChanged?.(next);
  };
  const { sending, send, cancel } = useUpload();
  const remove = useAction(() => api.del<Server>(path));
  const upload = async (file: File) => {
    try {
      done(await send<Server>(file.name, `${path}?name=${encodeURIComponent(file.name)}`, file));
      toast.info(`${file.name} is ${server.name}'s resource pack`);
    } catch (err) {
      if (!cancelled(err)) toast.error(err instanceof Error ? err.message : "Upload failed.");
    }
  };
  const progress = sending && <UploadProgress sending={sending} onCancel={cancel} />;

  const choose = (
    <input
      ref={input}
      type="file"
      accept=".zip,application/zip"
      className="hidden"
      aria-label="Resource pack file"
      onChange={(e) => {
        const file = e.target.files?.[0];
        e.target.value = "";
        if (file) void upload(file);
      }}
    />
  );
  if (!uploaded) {
    return (
      <div className="flex flex-wrap items-center gap-3">
        {choose}
        <Button icon={<Upload className="size-3.5" />} loading={!!sending} onClick={() => input.current?.click()}>
          Upload a pack
        </Button>
        {progress || <span className="text-xs text-muted">A .zip with pack.mcmeta at its top. The panel serves it to players.</span>}
      </div>
    );
  }
  return (
    <div className="flex flex-wrap items-center gap-3 rounded-lg border border-line bg-surface px-3 py-2.5">
      {choose}
      <FileArchive className="size-4 shrink-0 text-muted" />
      <div className="min-w-0 flex-1">
        <div className="truncate text-[13px] font-medium">{uploaded.name || "Uploaded pack"}</div>
        {progress || (
          <div className="text-xs text-muted">
            {bytes(uploaded.size)} · uploaded {ago(uploaded.time)} · served by the panel
          </div>
        )}
      </div>
      <div className="flex gap-2">
        <Button size="sm" loading={!!sending} onClick={() => input.current?.click()}>
          Replace
        </Button>
        <Button
          size="sm"
          variant="danger"
          loading={remove.isPending}
          disabled={!!sending}
          onClick={() =>
            remove.mutate(undefined, {
              onSuccess: (next) => {
                done(next);
                toast.info("Resource pack removed");
              },
            })
          }
        >
          Remove
        </Button>
      </div>
    </div>
  );
}
