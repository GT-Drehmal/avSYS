import { keepPreviousData, useQuery } from "@tanstack/react-query";
import { Check, Download, Search } from "lucide-react";
import { useEffect, useState } from "react";
import { api, qs, type ModrinthHit } from "../lib/api";
import { compact } from "../lib/format";
import { cx, Input, Spinner } from "./ui";

export function useDebounced<T>(value: T, ms: number): T {
  const [debounced, setDebounced] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setDebounced(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return debounced;
}

export function ProjectIcon({ url, size = 36 }: { url?: string | null; size?: number }) {
  const style = { width: size, height: size };
  if (!url) return <span className="shrink-0 rounded-md border border-line bg-raised" style={style} />;
  return <img src={url} alt="" loading="lazy" className="shrink-0 rounded-md bg-raised object-cover" style={style} />;
}

interface Props {
  type: "mod" | "plugin" | "datapack" | "modpack";
  loader?: string;
  gameVersion?: string;
  /** Project IDs and slugs already chosen, shown as such. */
  picked?: Set<string>;
  onPick: (hit: ModrinthHit) => void;
  /** Disables picking, while a previous pick is being saved. */
  busy?: boolean;
  placeholder?: string;
}

/**
 * A search box over Modrinth with results filtered to what fits the server.
 * It fills its parent and scrolls its results inside it, so the parent must
 * have a height.
 */
export function ModrinthSearch({ type, loader, gameVersion, picked, onPick, busy, placeholder }: Props) {
  const [query, setQuery] = useState("");
  const q = useDebounced(query, 200);
  const results = useQuery({
    queryKey: ["modrinth", type, loader, gameVersion, q],
    queryFn: () => api.get<{ hits: ModrinthHit[] }>(`/catalog/modrinth/search?${qs({ type, loader, gameVersion, q })}`),
    placeholderData: keepPreviousData,
    staleTime: 5 * 60_000,
  });

  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      <div className="relative">
        <Search className="pointer-events-none absolute top-2 left-2.5 size-4 text-faint" />
        <Input autoFocus value={query} onChange={(e) => setQuery(e.target.value)} placeholder={placeholder ?? "Search Modrinth"} className="pl-8" />
        {results.isFetching && <Spinner className="absolute top-2 right-2.5" />}
      </div>
      <ul className="-mx-2 flex min-h-0 flex-1 flex-col overflow-y-auto">
        {results.data?.hits.map((hit) => {
          const chosen = picked?.has(hit.project_id) || picked?.has(hit.slug);
          return (
            <li key={hit.project_id}>
              <button
                type="button"
                disabled={chosen || busy}
                onClick={() => onPick(hit)}
                className={cx("flex w-full items-center gap-3 rounded-md px-2 py-2 text-left", chosen ? "opacity-60" : "hover:bg-raised")}
              >
                <ProjectIcon url={hit.icon_url} />
                <span className="min-w-0 flex-1">
                  <span className="flex items-baseline gap-2">
                    <span className="truncate text-[13px] font-medium">{hit.title}</span>
                    <span className="shrink-0 text-xs text-faint">by {hit.author}</span>
                  </span>
                  <span className="block truncate text-xs text-muted">{hit.description}</span>
                </span>
                {chosen ? (
                  <Check className="size-4 shrink-0 text-accent" />
                ) : (
                  <span className="flex shrink-0 items-center gap-1 font-mono text-[11px] text-faint">
                    <Download className="size-3" />
                    {compact.format(hit.downloads)}
                  </span>
                )}
              </button>
            </li>
          );
        })}
        {results.data?.hits.length === 0 && <li className="px-2 py-8 text-center text-[13px] text-muted">Nothing on Modrinth matches that for this server.</li>}
        {results.isError && <li className="px-2 py-8 text-center text-[13px] text-danger">{results.error.message}</li>}
      </ul>
    </div>
  );
}
