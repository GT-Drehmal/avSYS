import { useQueryClient } from "@tanstack/react-query";
import { useState } from "react";
import { api, type BackupOverride, type BackupPolicy, type Server } from "../lib/api";
import { ago } from "../lib/format";
import { keys, useAction, useBackupPolicy, useUpdateServer } from "../lib/queries";
import { toast } from "../lib/toast";
import { Button, Input, Select, Toggle } from "./ui";

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

export type When = Pick<BackupPolicy, "minute" | "keep">;

/** The time of day and what to keep, as inputs. */
export function ScheduleFields({ value, onChange }: { value: When; onChange: (patch: Partial<When>) => void }) {
  return (
    <>
      <label className="flex flex-col gap-1.5 text-xs text-muted">
        Time
        <Input type="time" required value={toLocal(value.minute)} onChange={(e) => e.target.value && onChange({ minute: toUTC(e.target.value) })} className="h-8 w-36" />
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
                value={value.keep[key]}
                onChange={(e) => onChange({ keep: { ...value.keep, [key]: Number(e.target.value) } })}
                className="h-8 w-16"
                aria-label={`${unit} to keep`}
              />
              {unit}
            </label>
          ))}
        </div>
      </fieldset>
    </>
  );
}

/** The schedule a server's snapshots follow: its organization's, with what it sets for itself in its place. */
const overdueAfter = 36 * 60 * 60 * 1000;

/** What about a server's backups needs looking at, if anything: the last one failed, or the daily one has not happened. */
export function backupConcern(server: Server, scheduled: boolean): { tone: "danger" | "warn"; text: string } | null {
  const backups = server.status.snapshots;
  if (backups?.error) return { tone: "danger", text: "backup failed" };
  if (!scheduled) return null;
  const last = backups?.lastRun ?? server.createdAt;
  if (Date.now() - new Date(last).getTime() < overdueAfter) return null;
  return { tone: "warn", text: backups?.lastRun ? `last backup ${ago(backups.lastRun)}` : "never backed up" };
}

/** Says so, after other words on a line, when a server's backups need looking at. */
export function BackupMark({ server, scheduled }: { server: Server; scheduled: boolean }) {
  const concern = backupConcern(server, scheduled);
  if (!concern) return null;
  return <span className={concern.tone === "danger" ? "text-danger" : "text-warn"}> · {concern.text}</span>;
}

export function scheduleOf(policy: BackupPolicy, own: BackupOverride | null | undefined): BackupPolicy {
  return { enabled: own?.enabled ?? policy.enabled, minute: own?.minute ?? policy.minute, keep: own?.keep ?? policy.keep };
}

export const describe = (p: BackupPolicy) => (p.enabled ? `Daily at ${toLocal(p.minute)}, kept for ${kept(p.keep)}.` : "Off.");

type Mode = "org" | "own" | "off";

const modeOf = (own: BackupOverride | null | undefined): Mode => (!own ? "org" : own.enabled === false ? "off" : "own");

/**
 * One server's scheduled snapshots: as its organization has them, off, or
 * at a time and kept for as long as it sets for itself.
 */
export function ServerSchedule({ server, canAdmin }: { server: Server; canAdmin: boolean }) {
  const policy = useBackupPolicy(server.org);
  const update = useUpdateServer(server.org, server.name);
  // A schedule of its own while it is being written and not yet saved.
  const [draft, setDraft] = useState<When | null>(null);

  if (!policy.data) return null;
  const own = server.spec.backups;
  const stored = modeOf(own);
  const mode: Mode = draft ? "own" : stored;
  const effective = scheduleOf(policy.data, own);

  if (!canAdmin) {
    return <p className="text-[13px] text-muted">Scheduled snapshots: {describe(effective).replace(/^./, (c) => c.toLowerCase())}</p>;
  }

  // Every part is named, so that none is left over from what was set before.
  const save = (backups: BackupOverride | null, then?: () => void) => update.mutate({ backups }, { onSuccess: then });
  const choose = (next: Mode) => {
    if (next === "own") return setDraft({ minute: effective.minute, keep: effective.keep });
    setDraft(null);
    if (next !== stored) save(next === "off" ? { enabled: false, minute: null, keep: null } : null);
  };
  const shown = draft ?? { minute: effective.minute, keep: effective.keep };
  const hint = mode === "org" ? describe(policy.data) : mode === "off" ? "Only taken by hand." : undefined;

  return (
    <form
      className="flex flex-col gap-4 rounded-lg border border-line bg-surface p-4"
      onSubmit={(e) => {
        e.preventDefault();
        save({ enabled: true, minute: shown.minute, keep: shown.keep }, () => setDraft(null));
      }}
    >
      <div className="flex flex-wrap items-center gap-x-4 gap-y-2">
        <label htmlFor="server-schedule" className="text-[13px] font-medium">
          Scheduled snapshots
        </label>
        <Select id="server-schedule" value={mode} disabled={update.isPending} onChange={(e) => choose(e.target.value as Mode)} className="w-56">
          <option value="org">Same as the organization</option>
          <option value="own">Its own schedule</option>
          <option value="off">Off</option>
        </Select>
        {hint && <span className="text-[13px] text-muted">{hint}</span>}
      </div>
      {mode === "own" && (
        <div className="flex flex-wrap items-end gap-x-6 gap-y-3 border-t border-line pt-4">
          <ScheduleFields value={shown} onChange={(patch) => setDraft({ ...shown, ...patch })} />
          {draft && (
            <div className="ml-auto flex gap-2">
              <Button size="sm" onClick={() => setDraft(null)}>
                Cancel
              </Button>
              <Button size="sm" type="submit" variant="primary" loading={update.isPending}>
                Save
              </Button>
            </div>
          )}
        </div>
      )}
    </form>
  );
}

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
          hint={shown.enabled ? (policy.data.next ? `Every server. Next ${when(policy.data.next)}.` : "Every server.") : "Snapshots are only taken by hand."}
        />
        <Button
          size="sm"
          loading={run.isPending}
          onClick={() => run.mutate(undefined, { onSuccess: () => toast.info("Taking snapshots of every server") })}
        >
          Run now
        </Button>
      </div>
      {shown.enabled && (
        <div className="flex flex-wrap items-end gap-x-6 gap-y-3 border-t border-line pt-4">
          <ScheduleFields value={shown} onChange={change} />
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
