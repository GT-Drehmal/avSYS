/** What a new server is made from: a server as it is now, or a snapshot. */
export type Origin = { kind: "server"; name: string } | { kind: "snapshot"; id: string };

/** Reads "server:creative" or "snapshot:1a2b3c…" out of the page's address. */
export function originOf(from: string | null): Origin | null {
  const [kind, ...rest] = (from ?? "").split(":");
  const value = rest.join(":");
  if (!value) return null;
  if (kind === "server") return { kind, name: value };
  if (kind === "snapshot") return { kind, id: value };
  return null;
}

/** Where the page for making a server out of something is. */
export const newFrom = (org: string, origin: Origin) => `/o/${org}/new?from=${origin.kind}:${encodeURIComponent(origin.kind === "server" ? origin.name : origin.id)}`;
