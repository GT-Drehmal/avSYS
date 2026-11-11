import { useCallback } from "react";
import { useSearchParams } from "react-router";
import { matches, useSearch } from "./search";

/** One way of putting a list in order. */
export interface ListSort<T> {
  /** As it is in the page's address. */
  key: string;
  label: string;
  compare: (a: T, b: T) => number;
}

/** One way of narrowing a list. Ticking several of its values shows what has any of them. */
export interface ListFilter<T> {
  /** As it is in the page's address. */
  key: string;
  label: string;
  /** What a thing is, by this filter: anything left out says nothing. */
  values: (item: T) => (string | null | undefined | false)[];
  /** What a value is called, where that is not the value itself. */
  name?: (value: string) => string;
  /** The order values are offered in, where it is not by name. */
  order?: string[];
}

export interface ListOptions<T> {
  /** What is said of a thing, for the search to find. */
  said: (item: T) => (string | null | undefined | false)[];
  /** The first is how the list is put until another is chosen. */
  sorts: ListSort<T>[];
  filters?: ListFilter<T>[];
  /** Between things a sort says nothing of. */
  then?: (a: T, b: T) => number;
}

export interface FilterChoice {
  value: string;
  label: string;
  on: boolean;
}

export interface List<T> {
  /** What is left, in order. */
  shown: T[];
  /** How many there are before any search or filter. */
  total: number;
  query: string;
  setQuery: (query: string) => void;
  sorts: Pick<ListSort<T>, "key" | "label">[];
  sort: string;
  setSort: (key: string) => void;
  /** Each filter with what can be chosen of it. */
  filters: { key: string; label: string; choices: FilterChoice[] }[];
  toggle: (key: string, value: string) => void;
}

const collator = new Intl.Collator(undefined, { numeric: true, sensitivity: "base" });

/** By a name, with the numbers in it taken as numbers. */
export const byText =
  <T>(text: (item: T) => string) =>
  (a: T, b: T) =>
    collator.compare(text(a), text(b));

/** Newest first, anything without a time last. */
export const byTime =
  <T>(time: (item: T) => string | null | undefined) =>
  (a: T, b: T) =>
    Date.parse(time(b) ?? "") - Date.parse(time(a) ?? "") || Number(!!time(b)) - Number(!!time(a));

/** Largest first. */
export const byCount =
  <T>(count: (item: T) => number | null | undefined) =>
  (a: T, b: T) =>
    (count(b) ?? 0) - (count(a) ?? 0);

/** Newest game version first, anything without one last. */
export const byVersion =
  <T>(version: (item: T) => string | null | undefined) =>
  (a: T, b: T) =>
    collator.compare(version(b) ?? "", version(a) ?? "");

/** By where it is in a list of them. */
export const byPlace =
  <T>(place: (item: T) => string, order: string[]) =>
  (a: T, b: T) =>
    order.indexOf(place(a)) - order.indexOf(place(b));

/**
 * A list as a page shows it: searched, narrowed and put in order, all kept in
 * the page's address, so that going back to it or sharing it keeps them. What
 * it returns is what can be ticked: a row hidden is not acted on.
 */
export function useList<T>(items: T[], { said, sorts, filters = [], then }: ListOptions<T>): List<T> {
  const [query, setQuery] = useSearch();
  const [params, setParams] = useSearchParams();
  const sort = sorts.find((s) => s.key === params.get("sort")) ?? sorts[0];

  const setSort = useCallback(
    (key: string) =>
      setParams(
        (now) => {
          const p = new URLSearchParams(now);
          if (key === sorts[0].key) p.delete("sort");
          else p.set("sort", key);
          return p;
        },
        { replace: true },
      ),
    [setParams, sorts],
  );
  const toggle = useCallback(
    (key: string, value: string) =>
      setParams(
        (now) => {
          const p = new URLSearchParams(now);
          const had = p.getAll(key).filter(Boolean);
          p.delete(key);
          for (const each of had.includes(value) ? had.filter((v) => v !== value) : [...had, value]) p.append(key, each);
          return p;
        },
        { replace: true },
      ),
    [setParams],
  );

  // What an address asks for, less anything empty, as ?role= is: that asks for nothing.
  const asked = (key: string) => params.getAll(key).filter(Boolean);
  const wanted = filters.map((f) => ({ filter: f, values: asked(f.key) })).filter((w) => w.values.length > 0);
  const shown = items
    .filter((item) => matches(query, ...said(item)) && wanted.every(({ filter, values }) => filter.values(item).some((v) => !!v && values.includes(v))))
    .sort((a, b) => sort.compare(a, b) || (then?.(a, b) ?? 0));

  return {
    shown,
    total: items.length,
    query,
    setQuery,
    sorts,
    sort: sort.key,
    setSort,
    filters: filters.map((f) => {
      const on = asked(f.key);
      const nameOf = (value: string) => f.name?.(value) ?? value;
      // A value asked for in the address stays, for it to be taken off.
      const values = new Set([...items.flatMap((item) => f.values(item).filter((v): v is string => !!v)), ...on]);
      const place = (value: string) => f.order?.indexOf(value) ?? -1;
      const choices = [...values].sort((a, b) => place(a) - place(b) || collator.compare(nameOf(a), nameOf(b))).map((value) => ({ value, label: nameOf(value), on: on.includes(value) }));
      return { key: f.key, label: f.label, choices };
    }),
    toggle,
  };
}
