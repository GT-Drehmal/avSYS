import { useQuery } from "@tanstack/react-query";
import { Globe } from "lucide-react";
import { Link } from "react-router";
import { api, qs, serverPath, type LibraryWorld, type Server, type WorldFacts, type WorldOrigin, worldIcon } from "../lib/api";
import { ago, bytes } from "../lib/format";
import { useServers } from "../lib/queries";
import { Picture } from "./Picture";

/** What a world came by way of, in a few words: the server copied, a snapshot of one, or a template. */
export function viaWords(o: WorldOrigin, nameOf: (server: string) => string = (s) => s): string | null {
  if (o.template) return `From the template ${o.template.name || "that was deleted"}`;
  if (o.snapshot && o.server) return `From a snapshot of ${nameOf(o.server)}`;
  if (o.server) return `Copied from ${nameOf(o.server)}`;
  return null;
}

/** Where a world came from, in a line. */
export function originWords(o: WorldOrigin, nameOf: (server: string) => string = (s) => s): string {
  switch (o.kind) {
    case "library":
      return `${o.library?.name || "A world"} · from the world library`;
    case "upload":
      return o.file ? `Uploaded · ${o.file}` : "Uploaded";
    case "archive":
      return o.file ? `Imported · ${o.file}` : "Imported";
    case "new":
      return o.seed ? `New world · seed ${o.seed}` : "New world";
    default:
      return viaWords(o, nameOf) ?? "Copied";
  }
}

/** The library world a world came from, if it is still there. */
export const libraryWorldOf = (o: WorldOrigin | null | undefined, worlds: LibraryWorld[] | undefined) =>
  o?.kind === "library" && o.library ? (worlds?.find((w) => w.id === o.library?.id && w.ready) ?? null) : null;

/** What a server's world says of itself, read from its files: again once a world is put in, or the game stops or starts. */
export const useWorldFacts = (server: Server) =>
  useQuery({
    queryKey: ["world-facts", server.org, server.name, server.spec.worldOrigin?.time ?? "", server.status.phase ?? ""],
    queryFn: () => api.get<WorldFacts>(`${serverPath(server.org, server.name)}/world`),
    retry: false,
    staleTime: 30_000,
    enabled: !server.unfinished,
  });

/**
 * What a server's world is: where it came from, as the panel put it in, and
 * what the world says of itself, read from its files after the page is up.
 */
export function WorldNow({ server, library }: { server: Server; library: LibraryWorld | null }) {
  const o = server.spec.worldOrigin ?? null;
  const servers = useServers(server.org).data;
  const nameOf = (name: string) => servers?.find((s) => s.name === name)?.spec.displayName || name;
  const f = useWorldFacts(server).data;
  if (!o && !f) return null;
  // What the world says, less what the line above says already.
  const said = f
    ? f.exists
      ? [
          // Its own name, where the line above is not it already.
          o && f.name && (o.kind !== "library" || f.name !== o.library?.name) ? `"${f.name}"` : null,
          f.seed && f.seed !== o?.seed ? `Seed ${f.seed}` : null,
          f.version ? `Last opened in ${f.version}` : null,
          f.lastPlayed ? `Played ${ago(f.lastPlayed)}` : null,
          f.size != null ? bytes(f.size) : null,
        ].filter(Boolean)
      : ["Made when the server starts"]
    : [];
  const title = o ? originWords(o, nameOf) : f?.name || f?.level || "World";
  const via = o && !["copy", "snapshot", "template"].includes(o.kind) ? viaWords(o, nameOf) : null;
  const when = o?.time ? `Put in ${ago(o.time)}` : null;
  const lines = [[via, when].filter(Boolean).join(" · "), said.join(" · ")].filter(Boolean);
  return (
    <div className="flex items-center gap-3 rounded-md border border-line bg-raised px-3 py-2.5" data-testid="world-now">
      {library ? (
        <Picture name={library.name} url={worldIcon(server.org, library.id)} size={32} />
      ) : (
        <span className="flex size-8 shrink-0 items-center justify-center rounded-md bg-hover text-muted">
          <Globe className="size-4" />
        </span>
      )}
      <div className="min-w-0 flex-1">
        <p className="truncate text-[13px] font-medium">
          {library && o?.kind === "library" ? (
            <>
              <Link to={`/o/${server.org}/worlds?${qs({ q: library.name })}`} className="underline-offset-2 hover:underline">
                {o.library?.name || library.name}
              </Link>
              {" · from the world library"}
            </>
          ) : (
            title
          )}
        </p>
        {lines.map((line) => (
          <p key={line} className="text-xs break-words text-muted">
            {line}
          </p>
        ))}
      </div>
    </div>
  );
}
