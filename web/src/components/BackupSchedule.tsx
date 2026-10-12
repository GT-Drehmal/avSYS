import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, type BackupPolicy } from "../lib/api";
import { keys, useAction, useBackupPolicy } from "../lib/queries";
import { toast } from "../lib/toast";
import { Button, Input, Toggle } from "./ui";

const pad = (n: number) => String(n).padStart(2, "0");

/** A minute of the UTC day as the time it is on this device, "HH:MM". */
function toLocal(minute: number) {
  const d = new Date();
  d.setUTCHours(0, minute, 0, 0);
  return `${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** The reverse: a time on this device as a minute of the UTC day. */
function toUTC(time: string) {
  const [h, m] = time.split(":").map(Number);
  const d = new Date();
  d.setHours(h || 0, m || 0, 0, 0);
  return d.getUTCHours() * 60 + d.getUTCMinutes();
}

const when = (iso: string) => new Date(iso).toLocaleString(undefined, { weekday: "short", hour: "2-digit", minute: "2-digit" });

const kept = (k: BackupPolicy["keep"]) =>
  (
    [
      [k.daily, "day"],
      [k.weekly, "week"],
      [k.monthly, "month"],
      [k.yearly, "year"],
    ] as const
  )
    .filter(([n]) => n > 0)
    .map(([n, unit]) => `${n} ${unit}${n === 1 ? "" : "s"}`)
    .join(", ");

const periods = [
  ["daily", "days", 90],
  ["weekly", "weeks", 104],
  ["monthly", "months", 120],
  ["yearly", "years", 20],
] as const;

/** The organization's daily snapshots: on or off, when, and how long they are kept. */
export function BackupSchedule({ org, canAdmin }: { org: string; canAdmin: boolean }) {
  const qc = useQueryClient();
  const policy = useBackupPolicy(org);
  // The time and what to keep, while they are being changed and not yet
  // saved. Whether the snapshots are on is never part of it: the switch acts
  // at once, on what is stored, and leaves an edit in progress alone.
  const [draft, setDraft] = useState<Pick<BackupPolicy, "minute" | "keep"> | null>(null);

  const save = useAction((p: BackupPolicy) => api.put<BackupPolicy>(`/orgs/${org}/backup-policy`, { enabled: p.enabled, minute: p.minute, keep: p.keep }));
  const run = useAction(() => api.post(`/orgs/${org}/backup-policy/run`));

  if (!policy.data) return null;
  const stored = policy.data;
  const shown = { ...stored, ...draft };
  const change = (patch: Partial<Pick<BackupPolicy, "minute" | "keep">>) => setDraft({ minute: shown.minute, keep: shown.keep, ...patch });
  const store = (p: BackupPolicy, then?: () => void) =>
    save.mutate(p, {
      onSuccess: (now) => {
        qc.setQueryData(keys.backupPolicy(org), now);
        then?.();
      },
    });

  if (!canAdmin) {
    return (
      <p className="text-[13px] text-muted">
        {shown.enabled ? `Every server is snapshotted daily at ${toLocal(shown.minute)}. Kept for ${kept(shown.keep)}.` : "Daily snapshots are off."}
      </p>
    );
  }

  return (
    <form
      className="flex flex-col gap-4 rounded-lg border border-line bg-surface p-4"
      onSubmit={(e) => {
        e.preventDefault();
        store(shown, () => setDraft(null));
      }}
    >
      <div className="flex flex-wrap items-start justify-between gap-3">
        {/* Switching on or off takes effect at once; the rest waits for Save. */}
        <Toggle
          checked={shown.enabled}
          onChange={(enabled) => store({ ...stored, enabled })}
          disabled={save.isPending}
          label="Daily snapshots"
          hint={shown.enabled ? (policy.data.next ? `Every server. Next ${when(policy.data.next)}.` : "Every server.") : "Off. Servers are only snapshotted by hand."}
        />
        <Button
          size="sm"
          loading={run.isPending}
          onClick={() => run.mutate(undefined, { onSuccess: () => toast.info("Taking scheduled snapshots of every server") })}
        >
          Run now
        </Button>
      </div>
      {shown.enabled && (
        <div className="flex flex-wrap items-end gap-x-6 gap-y-3 border-t border-line pt-4">
          <label className="flex flex-col gap-1.5 text-xs text-muted">
            Time
            <Input type="time" required value={toLocal(shown.minute)} onChange={(e) => e.target.value && change({ minute: toUTC(e.target.value) })} className="h-8 w-28" />
          </label>
          <fieldset className="flex flex-col gap-1.5">
            <legend className="mb-1.5 text-xs text-muted">Keep the newest of each of the last</legend>
            <div className="flex flex-wrap gap-3">
              {periods.map(([key, unit, most]) => (
                <label key={key} className="flex items-center gap-1.5 text-[13px] text-muted">
                  <Input
                    type="number"
                    required
                    min={key === "daily" ? 1 : 0}
                    max={most}
                    value={shown.keep[key]}
                    onChange={(e) => change({ keep: { ...shown.keep, [key]: Number(e.target.value) } })}
                    className="h-8 w-16"
                    aria-label={`${unit} to keep`}
                  />
                  {unit}
                </label>
              ))}
            </div>
          </fieldset>
          {draft && (
            <div className="ml-auto flex gap-2">
              <Button size="sm" onClick={() => setDraft(null)}>
                Cancel
              </Button>
              <Button size="sm" type="submit" variant="primary" loading={save.isPending}>
                Save
              </Button>
            </div>
          )}
        </div>
      )}
    </form>
  );
}
