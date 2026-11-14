import type { LibraryWorld } from "./api";
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
