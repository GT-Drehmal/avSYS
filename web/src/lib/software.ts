import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { api, type Loader, type MinecraftVersions } from "./api";

export const loaders: { id: Loader; name: string; blurb: string }[] = [
  { id: "vanilla", name: "Vanilla", blurb: "The unmodified game" },
  { id: "paper", name: "Paper", blurb: "Fast, runs plugins" },
  { id: "fabric", name: "Fabric", blurb: "Lightweight mods" },
  { id: "neoforge", name: "NeoForge", blurb: "Modern Forge mods" },
  { id: "forge", name: "Forge", blurb: "Classic mods" },
  { id: "purpur", name: "Purpur", blurb: "Paper with more options" },
  { id: "quilt", name: "Quilt", blurb: "Fabric-compatible mods" },
  { id: "folia", name: "Folia", blurb: "Paper for huge servers" },
];

/**
 * Game versions, newest first. Given a kind of server, only the ones it has
 * a build for, and "latest" is the newest release of those.
 */
export function useGameVersions(loader?: Loader | null) {
  return useQuery({
    queryKey: ["minecraft-versions", loader ?? ""],
    queryFn: () => api.get<MinecraftVersions>(`/catalog/minecraft/versions${loader ? `?loader=${loader}` : ""}`),
    staleTime: 10 * 60_000,
    // The list for the previous kind stays up while the next one loads.
    placeholderData: keepPreviousData,
    select: (m) => ({
      latest: m.latest.release,
      releases: m.versions.filter((v) => v.type === "release").map((v) => v.id),
      previews: previews(m.versions),
      // Snapshots and pre-releases too, for telling whether a version has a build at all.
      all: new Set(m.versions.map((v) => v.id)),
    }),
  });
}

/**
 * The snapshots, pre-releases and release candidates worth offering: those
 * of the release to come and of the newest one. versions is newest first.
 * Older ones are in the list too, by the hundred, and are not what anyone
 * setting up a server is looking for.
 */
function previews(versions: MinecraftVersions["versions"]) {
  const out: string[] = [];
  let releases = 0;
  for (const v of versions) {
    if (v.type === "release") {
      if (++releases === 2) break;
    } else out.push(v.id);
  }
  return out;
}

/** Whole or half gigabytes from a Kubernetes quantity, for a number input. */
export function toGigabytes(q: string | undefined): string {
  const m = /^(\d+(?:\.\d+)?)(Mi|Gi|M|G)$/.exec(q ?? "");
  if (!m) return "";
  const n = Number(m[1]);
  return String(m[2].startsWith("G") ? n : Math.round((n / 1024) * 100) / 100);
}

export const fromGigabytes = (gb: string) => `${Math.round(Number(gb) * 1024)}Mi`;
