import { keepPreviousData, useInfiniteQuery, useQuery } from "@tanstack/react-query";
import { Check, ChevronLeft, Download, ExternalLink, Heart, Info, Search, SlidersHorizontal } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { api, qs, type ModrinthHit, type ModrinthProject, type ModrinthResults, type ModrinthVersion } from "../lib/api";
import { ago, compact } from "../lib/format";
import { Button, Chip, cx, Input, Select, Spinner } from "./ui";

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

type Kind = "mod" | "plugin" | "datapack" | "modpack";
type Order = "relevance" | "downloads" | "follows" | "newest" | "updated";

const orders: [Order, string][] = [
  ["relevance", "Relevance"],
  ["downloads", "Most downloaded"],
  ["follows", "Most followed"],
  ["updated", "Recently updated"],
  ["newest", "Newest"],
];

/** The furthest into a search's results a page may start. */
const lastOffset = 9_984;

/** "world-generation" as "World generation". */
const tagName = (tag: string) => (tag === "worldgen" ? "World generation" : tag.charAt(0).toUpperCase() + tag.slice(1).replace(/-/g, " "));

interface Props {
  type: Kind;
  loader?: string;
  gameVersion?: string;
  /** What the results are narrowed to, in words: "Paper 1.21.4". */
  fits?: string;
  /** Project IDs and slugs already chosen, shown as such. */
  picked?: Set<string>;
  onPick: (hit: ModrinthHit) => void;
  /** Disables picking, while a previous pick is being saved. */
  busy?: boolean;
  placeholder?: string;
}

/**
 * Modrinth, narrowed to what fits the server: a search box, categories and
 * an order to browse by, and a closer look at any result before adding it.
 * It fills its parent and scrolls its results inside it, so the parent must
 * have a height.
 */
export function ModrinthSearch({ type, loader, gameVersion, fits, picked, onPick, busy, placeholder }: Props) {
  const [query, setQuery] = useState("");
  const q = useDebounced(query, 200);
  // With nothing typed there is nothing to be relevant to, so the most
  // downloaded come first until an order is chosen.
  const [order, setOrder] = useState<Order | null>(null);
  const sort = order ?? (q ? "relevance" : "downloads");
  const [filtering, setFiltering] = useState(false);
  const [chosen, setChosen] = useState<string[]>([]);
  const [clientToo, setClientToo] = useState(false);
  const [looking, setLooking] = useState<ModrinthHit | null>(null);

  const categories = useQuery({
    queryKey: ["modrinth-categories", type],
    queryFn: () => api.get<string[]>(`/catalog/modrinth/categories?${qs({ type })}`),
    staleTime: 60 * 60_000,
  });
  const filters = { type, loader, gameVersion, q, sort, categories: chosen.join(",") || undefined, side: clientToo ? "any" : undefined };
  const results = useInfiniteQuery({
    queryKey: ["modrinth", filters],
    queryFn: ({ pageParam }) => api.get<ModrinthResults>(`/catalog/modrinth/search?${qs({ ...filters, offset: pageParam || undefined })}`),
    initialPageParam: 0,
    getNextPageParam: (last) => {
      const next = last.offset + last.hits.length;
      // A search can be followed as far as its first ten thousand results.
      return last.hits.length > 0 && next < last.total_hits && next <= lastOffset ? next : undefined;
    },
    placeholderData: keepPreviousData,
    staleTime: 5 * 60_000,
  });
  const hits = results.data?.pages.flatMap((p) => p.hits) ?? [];
  const total = results.data?.pages[0]?.total_hits;

  // More are fetched as the end of the list comes into view.
  const list = useRef<HTMLUListElement>(null);
  const end = useRef<HTMLLIElement>(null);
  const { hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage } = results;
  useEffect(() => {
    // After a failure the next try is asked for by hand: the end of the list
    // is still in view, and would otherwise ask again without pause.
    if (!end.current || !hasNextPage || isFetchingNextPage || isFetchNextPageError) return;
    const seen = new IntersectionObserver((entries) => entries[0].isIntersecting && fetchNextPage(), { root: list.current, rootMargin: "200px" });
    seen.observe(end.current);
    return () => seen.disconnect();
  }, [hasNextPage, isFetchingNextPage, isFetchNextPageError, fetchNextPage, hits.length]);
  // A different search starts from the top.
  const filterKey = JSON.stringify(filters);
  useEffect(() => list.current?.scrollTo({ top: 0 }), [filterKey]);

  const isPicked = (hit: ModrinthHit) => !!(picked?.has(hit.project_id) || picked?.has(hit.slug));
  if (looking) {
    return (
      <ProjectDetail
        hit={looking}
        loader={loader}
        gameVersion={gameVersion}
        fits={fits}
        // Modrinth counts loaders among a project's categories; only its own are shown.
        tags={(looking.display_categories ?? []).filter((t) => categories.data?.includes(t))}
        picked={isPicked(looking)}
        has={(project) => !!(picked?.has(project.id) || picked?.has(project.slug))}
        busy={busy}
        onBack={() => setLooking(null)}
        onPick={() => onPick(looking)}
      />
    );
  }

  const narrowed = chosen.length + (clientToo ? 1 : 0);
  return (
    <div className="flex h-full min-h-0 flex-col gap-2.5">
      <div className="flex gap-2">
        <div className="relative min-w-0 flex-1">
          <Search className="pointer-events-none absolute top-2 left-2.5 size-4 text-faint" />
          <Input autoFocus value={query} onChange={(e) => setQuery(e.target.value)} placeholder={placeholder ?? "Search Modrinth"} className="pl-8" />
          {results.isFetching && !results.isFetchingNextPage && <Spinner className="absolute top-2 right-2.5" />}
        </div>
        <Select aria-label="Sort by" value={sort} onChange={(e) => setOrder(e.target.value as Order)} className="w-44 shrink-0 max-sm:w-32">
          {orders.map(([value, label]) => (
            <option key={value} value={value}>
              {label}
            </option>
          ))}
        </Select>
        <Button icon={<SlidersHorizontal className="size-3.5" />} aria-expanded={filtering} onClick={() => setFiltering(!filtering)} className="shrink-0">
          <span className="max-sm:hidden">Filters</span>
          {narrowed > 0 && <span className="text-accent">{narrowed}</span>}
        </Button>
      </div>
      {filtering && (
        <div className="flex flex-col gap-2 rounded-md border border-line p-2.5">
          <div role="group" aria-label="Categories" className="flex flex-wrap gap-1.5">
            {categories.data?.map((c) => {
              const on = chosen.includes(c);
              return (
                <button
                  key={c}
                  type="button"
                  aria-pressed={on}
                  onClick={() => setChosen(on ? chosen.filter((x) => x !== c) : [...chosen, c])}
                  className={cx("h-6 rounded-full border px-2.5 text-xs", on ? "border-accent bg-accent-wash text-fg" : "border-line-strong text-muted hover:bg-raised hover:text-fg")}
                >
                  {tagName(c)}
                </button>
              );
            })}
            {categories.isError && <span className="text-xs text-muted">The categories could not be loaded.</span>}
          </div>
          {type === "mod" && (
            <label className="flex w-fit items-center gap-2 text-xs text-muted">
              <input type="checkbox" checked={clientToo} onChange={(e) => setClientToo(e.target.checked)} className="size-3.5 accent-(--color-accent)" />
              Also show mods that only do something on the player's own game
            </label>
          )}
          {narrowed > 0 && (
            <button
              type="button"
              className="w-fit text-xs text-accent hover:underline"
              onClick={() => {
                setChosen([]);
                setClientToo(false);
              }}
            >
              Clear filters
            </button>
          )}
        </div>
      )}
      {total !== undefined && (
        <p className="text-xs text-faint" aria-live="polite">
          {total.toLocaleString()} {total === 1 ? "result" : "results"}
          {fits && ` for ${fits}`}
        </p>
      )}
      <ul ref={list} className="-mx-2 flex min-h-0 flex-1 flex-col overflow-y-auto">
        {hits.map((hit) => {
          const chosen = isPicked(hit);
          return (
            <li key={hit.project_id} className="flex items-center gap-1">
              <button
                type="button"
                disabled={chosen || busy}
                onClick={() => onPick(hit)}
                className={cx("flex min-w-0 flex-1 items-center gap-3 rounded-md px-2 py-2 text-left", chosen ? "opacity-60" : "hover:bg-raised")}
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
              <Button size="sm" variant="ghost" icon={<Info className="size-4" />} aria-label={`About ${hit.title}`} title="About this" onClick={() => setLooking(hit)} className="mr-1 shrink-0" />
            </li>
          );
        })}
        {results.data && hits.length === 0 && (
          <li className="px-2 py-8 text-center text-[13px] text-muted">Nothing on Modrinth matches that{fits ? ` for ${fits}` : " for this server"}.</li>
        )}
        {results.isError && !isFetchNextPageError && <li className="px-2 py-8 text-center text-[13px] text-danger">{results.error.message}</li>}
        {hasNextPage &&
          (isFetchNextPageError ? (
            <li className="flex items-center justify-center gap-3 py-3 text-xs text-muted">
              More could not be loaded.
              <Button size="sm" onClick={() => fetchNextPage()}>
                Try again
              </Button>
            </li>
          ) : (
            <li ref={end} className="flex justify-center py-3">
              <Spinner />
            </li>
          ))}
      </ul>
    </div>
  );
}

interface DetailProps {
  hit: ModrinthHit;
  loader?: string;
  gameVersion?: string;
  fits?: string;
  tags: string[];
  picked: boolean;
  /** Whether a project it needs is already chosen. */
  has: (project: ModrinthProject) => boolean;
  busy?: boolean;
  onBack: () => void;
  onPick: () => void;
}

/** One result looked at closely: what it is, which versions fit, and what it needs. */
function ProjectDetail({ hit, loader, gameVersion, fits, tags, picked, has, busy, onBack, onPick }: DetailProps) {
  const versions = useQuery({
    queryKey: ["modrinth-versions", hit.project_id, { loader, gameVersion }],
    queryFn: () => api.get<ModrinthVersion[]>(`/catalog/modrinth/projects/${encodeURIComponent(hit.project_id)}/versions?${qs({ loader, gameVersion })}`),
    staleTime: 5 * 60_000,
  });
  // What its newest version for this server needs beside it. The panel
  // installs that one unless it needs a newer Java than the server has.
  const newest = versions.data?.find((v) => v.version_type === "release") ?? versions.data?.[0];
  const required = (newest?.dependencies ?? []).filter((d) => d.dependency_type === "required");
  const needed = [...new Set(required.filter((d) => d.project_id).map((d) => d.project_id as string))];
  // Some are given as a version of a project and not the project, and are not looked up by name.
  const unnamed = required.filter((d) => !d.project_id).length;
  const needs = useQuery({
    queryKey: ["modrinth-projects", needed],
    queryFn: () => api.get<ModrinthProject[]>(`/catalog/modrinth/projects?${qs({ ids: needed.join(",") })}`),
    enabled: needed.length > 0,
    staleTime: 5 * 60_000,
  });
  return (
    <div className="flex h-full min-h-0 flex-col gap-3">
      <button type="button" onClick={onBack} className="flex w-fit items-center gap-1 text-xs text-muted hover:text-fg">
        <ChevronLeft className="size-3.5" />
        Results
      </button>
      <div className="flex min-h-0 flex-1 flex-col gap-4 overflow-y-auto pr-1">
        <div className="flex items-start gap-3">
          <ProjectIcon url={hit.icon_url} size={56} />
          <div className="min-w-0 flex-1">
            <p className="text-[15px] font-semibold">{hit.title}</p>
            <p className="text-xs text-faint">by {hit.author}</p>
            <p className="mt-1.5 text-[13px] leading-relaxed text-muted">{hit.description}</p>
          </div>
        </div>
        <div className="flex flex-wrap items-center gap-x-4 gap-y-1.5 text-xs text-muted">
          <span className="flex items-center gap-1">
            <Download className="size-3.5" />
            {compact.format(hit.downloads)} downloads
          </span>
          {hit.follows !== undefined && (
            <span className="flex items-center gap-1">
              <Heart className="size-3.5" />
              {compact.format(hit.follows)} followers
            </span>
          )}
          {hit.date_modified && <span>Updated {ago(hit.date_modified)}</span>}
          {hit.license && <span>Licence {hit.license}</span>}
        </div>
        {tags.length > 0 && (
          <div className="flex flex-wrap gap-1.5">
            {tags.map((t) => (
              <Chip key={t}>{tagName(t)}</Chip>
            ))}
          </div>
        )}
        <section className="flex flex-col gap-1.5">
          <h3 className="text-[13px] font-medium">Versions{fits ? ` for ${fits}` : ""}</h3>
          {versions.isPending ? (
            <Spinner />
          ) : versions.isError ? (
            <p className="text-xs text-danger">{versions.error.message}</p>
          ) : versions.data.length === 0 ? (
            <p className="text-xs text-muted">None. It cannot be added to this server.</p>
          ) : (
            <ul className="flex flex-col text-xs">
              {versions.data.slice(0, 5).map((v) => (
                <li key={v.id} className="flex items-baseline gap-2 py-0.5">
                  <span className="truncate font-mono">{v.version_number}</span>
                  {v.version_type !== "release" && <span className="shrink-0 text-warn">{v.version_type}</span>}
                  <span className="ml-auto shrink-0 text-faint">{ago(v.date_published)}</span>
                </li>
              ))}
              {versions.data.length > 5 && <li className="py-0.5 text-faint">and {versions.data.length - 5} older</li>}
            </ul>
          )}
        </section>
        {needed.length + unnamed > 0 && (
          <section className="flex flex-col gap-1.5">
            <h3 className="text-[13px] font-medium">Needs</h3>
            <p className="text-xs text-muted">What its newest version{fits ? ` for ${fits}` : ""} does not work without. They are not added for you.</p>
            <ul className="flex flex-col gap-1.5">
              {needs.data?.map((p) => (
                <li key={p.id} className="flex items-center gap-2 text-[13px]">
                  <ProjectIcon url={p.icon_url} size={20} />
                  {p.title}
                  {has(p) && (
                    <span className="flex items-center gap-1 text-xs text-muted">
                      <Check className="size-3.5 text-accent" />
                      added
                    </span>
                  )}
                </li>
              ))}
              {needed.length > 0 && needs.isPending && <Spinner />}
              {unnamed > 0 && (
                <li className="text-xs text-muted">
                  {needed.length > 0 ? "And " : ""}
                  {unnamed === 1 ? "one other project" : `${unnamed} other projects`}, named on its Modrinth page.
                </li>
              )}
              {needs.isError && <li className="text-xs text-muted">{needed.length === 1 ? "One other project" : `${needed.length} other projects`}, named on its Modrinth page.</li>}
            </ul>
          </section>
        )}
      </div>
      <div className="flex items-center justify-between gap-3 border-t border-line pt-3">
        <a href={`https://modrinth.com/${hit.project_type}/${hit.slug}`} target="_blank" rel="noreferrer" className="flex items-center gap-1 text-xs text-accent hover:underline">
          Open on Modrinth
          <ExternalLink className="size-3" />
        </a>
        {picked ? (
          <span className="flex items-center gap-1.5 text-[13px] text-muted">
            <Check className="size-4 text-accent" />
            Added
          </span>
        ) : (
          <Button variant="primary" loading={busy} disabled={versions.data?.length === 0} onClick={onPick}>
            Add
          </Button>
        )}
      </div>
    </div>
  );
}
