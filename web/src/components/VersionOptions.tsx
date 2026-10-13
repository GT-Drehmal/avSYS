import type { useGameVersions } from "../lib/software";

type Versions = NonNullable<ReturnType<typeof useGameVersions>["data"]>;

interface Props {
  versions: Versions | undefined;
  /** Whether snapshots, pre-releases and release candidates are listed too. */
  previews: boolean;
  /** A version to keep selectable though the list does not have it, with how to show it. */
  keep?: { value: string; label: string };
}

/** The choices of a game version picker: the latest, then every version, newest first. */
export function VersionOptions({ versions, previews, keep }: Props) {
  // With snapshots in, each release is followed by the ones that led to it.
  const shown = (previews ? versions?.ordered : versions?.releases) ?? [];
  return (
    <>
      <option value="LATEST">Latest release{versions ? ` (${versions.latest})` : ""}</option>
      {keep && !shown.includes(keep.value) && <option value={keep.value}>{keep.label}</option>}
      {shown.map((v) => (
        <option key={v} value={v}>
          {v}
        </option>
      ))}
    </>
  );
}

interface ToggleProps {
  versions: Versions | undefined;
  previews: boolean;
  onChange: (show: boolean) => void;
  /** The version picked. While it is a snapshot the list has to keep showing them. */
  selected: string;
}

/** Whether a snapshot is picked: the list then shows them whatever the box says. */
export const onPreview = (versions: Versions | undefined, selected: string) => !!versions?.previews.includes(selected);

/** The box beside a version picker that lets snapshots and pre-releases into it. */
export function PreviewsToggle({ versions, previews, onChange, selected }: ToggleProps) {
  // Nothing to offer: this kind of server has no build for any of them.
  if (!versions || versions.previews.length === 0) return null;
  const held = onPreview(versions, selected);
  return (
    <label className="flex h-8 items-center gap-2 text-[13px] whitespace-nowrap text-muted">
      <input type="checkbox" checked={previews || held} disabled={held} onChange={(e) => onChange(e.target.checked)} className="size-3.5 accent-(--color-accent)" />
      Snapshots and pre-releases
    </label>
  );
}

/** What choosing a snapshot costs, said while one is picked. */
export const previewWarning = "A world opened in a snapshot may not open in an earlier version again.";
