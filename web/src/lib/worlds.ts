import { useQuery } from "@tanstack/react-query";
import { api, type LibraryWorld, type Modpack, type ModrinthVersion, type ServerSpec } from "./api";
import { bytes } from "./format";

/** Where a world came from, said plainly. */
export function worldSource(w: LibraryWorld) {
  const [kind, name] = w.source.split(/:(.*)/s);
  return kind === "server" ? `from ${name}` : kind === "snapshot" ? `from a snapshot of ${name}` : "uploaded";
}

/**
 * Said wherever a server is to run an older game version than its world was
 * last opened in: the game cannot open the world. The same words everywhere.
 */
export const newerWorldWarning = (world: string, version: string) => `The world was last opened in ${world}. ${version} cannot open the world, so the server will not start.`;

/** The question asked before going ahead with it. */
export const newerWorldQuestion = (world: string, version: string) => `Open a ${world} world in ${version}?`;

/** Whether game version a came out after b, by the game's versions newest first. Unknown is not. */
export const newerThan = (order: readonly string[] | undefined, a: string | undefined, b: string | undefined) => {
  const i = a ? (order?.indexOf(a) ?? -1) : -1;
  const j = b ? (order?.indexOf(b) ?? -1) : -1;
  return i >= 0 && j >= 0 && i < j;
};

/** What a world is, in a line: its version, size, where it came from, and whether it has a resource pack. */
export function worldSummary(w: LibraryWorld) {
  return [w.version, w.ready ? bytes(w.size) : null, worldSource(w), w.resourcePack ? "with a resource pack" : null].filter(Boolean).join(" · ");
}

/**
 * The game version a server runs, as far as it is known: a modpack's is the
 * one the pack is built for, and "Latest release" is the newest there is.
 */
export const runsOn = (spec: Pick<ServerSpec, "modpack" | "version">, latest: string | undefined) =>
  spec.modpack ? spec.modpack.gameVersion : spec.version === "LATEST" ? latest : spec.version;

/** The version of a modpack from Modrinth the panel pins when none is picked: its newest release, else its newest. */
export const pinnedOf = (list: ModrinthVersion[] | undefined, version: string | undefined) => {
  if (!list) return undefined;
  if (version) return list.find((v) => v.id === version);
  const releases = list.filter((v) => v.version_type === "release");
  return (releases.length > 0 ? releases : list)[0];
};

/**
 * The game version a modpack is built for: as the panel recorded it, or for
 * one just picked from Modrinth, that of the version the panel will pin.
 */
export function usePackGame(pack: Modpack | null | undefined) {
  const lookup = !!pack && pack.source === "modrinth" && !pack.gameVersion && !!pack.project;
  const versions = useQuery({
    queryKey: ["modrinth-versions", pack?.project ?? "", {}],
    queryFn: () => api.get<ModrinthVersion[]>(`/catalog/modrinth/projects/${encodeURIComponent(pack?.project ?? "")}/versions?`),
    enabled: lookup,
    staleTime: 5 * 60_000,
  });
  if (!pack) return undefined;
  if (pack.gameVersion || !lookup) return pack.gameVersion;
  return pinnedOf(versions.data, pack.version)?.game_versions.at(-1);
}
