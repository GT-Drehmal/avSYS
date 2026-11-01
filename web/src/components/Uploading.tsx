import { useEffect, useRef, useState } from "react";
import { ApiError, sendInParts, upload, uploadParts, type Progress } from "../lib/api";
import { bytes } from "../lib/format";
import { Spinner } from "./ui";

/** An upload under way: what is being sent, and how far it is. */
export interface Sending {
  label: string;
  progress: Progress;
}

/**
 * Uploads that say how far they are and can be stopped, one at a time. send
 * resolves to the answer, or rejects; one that was stopped rejects with code
 * "aborted", and one asked for while another is under way with "busy".
 */
export function useUpload() {
  const [sending, setSending] = useState<Sending | null>(null);
  const stop = useRef<AbortController | null>(null);
  // Leaving the page stops what it was sending.
  useEffect(() => () => stop.current?.abort(), []);
  const run = async <T,>(label: string, file: Blob, work: (onProgress: (p: Progress) => void, signal: AbortSignal) => Promise<T>): Promise<T> => {
    // One at a time: the progress shown and Cancel are of one upload.
    if (stop.current) throw new ApiError(0, "busy", "Wait for the current upload to finish, or cancel it.");
    const ctl = new AbortController();
    stop.current = ctl;
    setSending({ label, progress: { sent: 0, total: file.size } });
    try {
      return await work((progress) => setSending({ label, progress }), ctl.signal);
    } finally {
      if (stop.current === ctl) {
        stop.current = null;
        setSending(null);
      }
    }
  };
  const send = <T,>(label: string, path: string, file: Blob, method?: "PUT" | "POST") => run<T>(label, file, (p, signal) => upload<T>(path, file, p, signal, method));
  /** Sends an archive to a server in parts, then asks for what finish does. */
  const sendParts = <T,>(label: string, server: string, file: Blob, finish: (upload: string) => Promise<T>) =>
    run<T>(label, file, async (p, signal) => finish(await uploadParts(server, file, p, signal)));
  /** Sends a file in parts to where part says, then asks for what finish does with its name. */
  const sendStaged = <T,>(label: string, file: Blob, part: (q: Record<string, string | number | undefined>) => string, received: (q: Record<string, string | number | undefined>) => string, finish: (upload: string) => Promise<T>) =>
    run<T>(label, file, async (p, signal) => finish(await sendInParts(part, received, file, p, signal)));
  return { sending, send, sendParts, sendStaged, cancel: () => stop.current?.abort() };
}

export const cancelled = (err: unknown) => err instanceof ApiError && err.code === "aborted";

/**
 * How far an upload is: a bar and how much is sent, with a way to stop it.
 * Once all is sent the panel is checking and saving it, which cannot be
 * stopped from here.
 */
export function UploadProgress({ sending, onCancel, leaving = "Leaving this page stops the upload." }: { sending: Sending; onCancel: () => void; leaving?: string }) {
  const { sent, total } = sending.progress;
  const done = total > 0 && sent >= total;
  const pct = total > 0 ? Math.min(100, Math.floor((sent / total) * 100)) : 0;
  return (
    <div className="flex min-w-0 flex-col gap-1">
    <div className="flex min-w-0 items-center gap-2.5 text-xs text-muted" role="status" aria-live="polite">
      {done ? (
        <>
          <Spinner className="size-3.5 shrink-0" />
          <span className="shrink-0">Checking and saving</span>
          <span className="min-w-0 truncate">{sending.label}</span>
        </>
      ) : (
        <>
          <div className="h-1 w-24 shrink-0 overflow-hidden rounded-full bg-line" role="progressbar" aria-label="Upload progress" aria-valuemin={0} aria-valuemax={100} aria-valuenow={pct}>
            <div className="h-full rounded-full bg-accent transition-[width] duration-200" style={{ width: `${pct}%` }} />
          </div>
          <span className="shrink-0 tabular-nums">
            {pct}% · {bytes(sent)} of {bytes(total)}
          </span>
          <span className="min-w-0 truncate">{sending.label}</span>
          <button type="button" className="shrink-0 text-accent hover:underline" onClick={onCancel}>
            Cancel
          </button>
        </>
      )}
    </div>
    {leaving && <p className="text-[11px] text-faint">{leaving}</p>}
    </div>
  );
}
