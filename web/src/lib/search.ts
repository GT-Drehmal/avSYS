import { useCallback } from "react";
import { useSearchParams } from "react-router";

/** Whether every word typed is somewhere in what is said of a thing, whatever the case. */
export function matches(query: string, ...said: (string | null | undefined | false)[]) {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const text = said.filter(Boolean).join(" ").toLowerCase();
  return words.every((w) => text.includes(w));
}

/** What a page's list is searched for, kept in its address so that going back to it keeps it. */
export function useSearch(): [string, (query: string) => void] {
  const [params, setParams] = useSearchParams();
  const query = params.get("q") ?? "";
  const set = useCallback(
    (next: string) =>
      setParams(
        (now) => {
          const p = new URLSearchParams(now);
          if (next) p.set("q", next);
          else p.delete("q");
          return p;
        },
        { replace: true },
      ),
    [setParams],
  );
  return [query, set];
}
