import { useQuery } from "@tanstack/react-query";
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

/** Released game versions, newest first, from Mojang's manifest. */
export function useGameVersions() {
  return useQuery({
    queryKey: ["minecraft-versions"],
    queryFn: () => api.get<MinecraftVersions>("/catalog/minecraft/versions"),
    staleTime: 60 * 60_000,
    select: (m) => ({ latest: m.latest.release, releases: m.versions.filter((v) => v.type === "release").map((v) => v.id) }),
  });
}

/** Whole or half gigabytes from a Kubernetes quantity, for a number input. */
export function toGigabytes(q: string | undefined): string {
  const m = /^(\d+(?:\.\d+)?)(Mi|Gi|M|G)$/.exec(q ?? "");
  if (!m) return "";
  const n = Number(m[1]);
  return String(m[2].startsWith("G") ? n : Math.round((n / 1024) * 100) / 100);
}

export const fromGigabytes = (gb: string) => `${Math.round(Number(gb) * 1024)}Mi`;
