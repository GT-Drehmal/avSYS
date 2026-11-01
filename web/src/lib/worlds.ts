import type { LibraryWorld } from "./api";
import { bytes } from "./format";

/** Where a world came from, said plainly. */
export function worldSource(w: LibraryWorld) {
  const [kind, name] = w.source.split(/:(.*)/s);
  return kind === "server" ? `from ${name}` : kind === "snapshot" ? `from a snapshot of ${name}` : "uploaded";
}

/** What a world is, in a line: its version, size and where it came from. */
export function worldSummary(w: LibraryWorld) {
  return [w.version, w.ready ? bytes(w.size) : null, worldSource(w)].filter(Boolean).join(" · ");
}
