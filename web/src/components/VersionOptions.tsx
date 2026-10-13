import type { useGameVersions } from "../lib/software";

type Versions = NonNullable<ReturnType<typeof useGameVersions>["data"]>;

interface Props {
  versions: Versions | undefined;
  /** Whether snapshots, pre-releases and release candidates are listed too. */
  previews: boolean;
  /** A version to keep selectable though the list does not have it, with how to show it. */
  keep?: { value: string; label: string };
}

/** The choices of a game version picker: the latest, then every release. */
export function VersionOptions({ versions, previews, keep }: Props) {
  const shown = previews ? (versions?.previews ?? []) : [];
  const listed = keep && (versions?.releases.includes(keep.value) || shown.includes(keep.value));
  return (
    <>
      <option value="LATEST">Latest release{versions ? ` (${versions.latest})` : ""}</option>
      {keep && !listed && <option value={keep.value}>{keep.label}</option>}
      {shown.length > 0 && (
        <optgroup label="Snapshots and pre-releases">
          {shown.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
        </optgroup>
      )}
      {/* Grouped only when there is another group to tell them from. */}
      {shown.length > 0 ? (
        <optgroup label="Releases">
          {versions?.releases.map((v) => (
            <option key={v} value={v}>
              {v}
            </option>
          ))}
        </optgroup>
      ) : (
        versions?.releases.map((v) => (
          <option key={v} value={v}>
            {v}
          </option>
        ))
      )}
    </>
  );
}

/** Says what choosing a snapshot costs, and switches them in and out of the list. */
export function PreviewsHint({ versions, previews, onChange, selected }: { versions: Versions | undefined; previews: boolean; onChange: (show: boolean) => void; selected: string }) {
  // Nothing to offer: this kind of server has no build for any of them.
  if (!versions || versions.previews.length === 0) return null;
  const onPreview = versions.previews.includes(selected);
  return (
    <>
      {onPreview && "A world opened in a snapshot may not open in an earlier version again. "}
      {/* While one is selected the list has to keep showing them. */}
      {!onPreview && (
        <button type="button" className="text-accent hover:underline" onClick={() => onChange(!previews)}>
          {previews ? "Hide snapshots and pre-releases" : "Show snapshots and pre-releases"}
        </button>
      )}
    </>
  );
}
