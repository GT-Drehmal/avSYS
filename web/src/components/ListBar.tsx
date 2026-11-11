import { ArrowUpDown, ListFilter, SlidersHorizontal, X } from "lucide-react";
import type { ReactNode } from "react";
import type { List } from "../lib/list";
import { Button, Menu, MenuCheck, MenuContent, MenuLabel, MenuRadio, MenuRadioGroup, MenuSeparator, MenuTrigger, SearchBox } from "./ui";

/** What goes above a list: its search, and how it is put in order and narrowed. */
export function ListBar<T>({ list, label, plain, children }: { list: List<T>; label: string; /** The search alone. */ plain?: boolean; children?: ReactNode }) {
  const filters = list.filters.filter((f) => f.choices.length > 1 || f.choices.some((c) => c.on));
  const chips = filters.flatMap((f) => f.choices.filter((c) => c.on).map((c) => ({ ...c, key: f.key, filter: f.label })));
  const sorting = list.total > 1 && list.sorts.length > 1;
  const current = list.sorts.find((s) => s.key === list.sort)?.label;

  const sortItems = (
    <>
      <MenuLabel>Sort by</MenuLabel>
      <MenuRadioGroup value={list.sort} onValueChange={list.setSort}>
        {list.sorts.map((s) => (
          <MenuRadio key={s.key} value={s.key}>
            {s.label}
          </MenuRadio>
        ))}
      </MenuRadioGroup>
    </>
  );
  const filterItems = filters.map((f, i) => (
    <div key={f.key}>
      {i > 0 && <MenuSeparator />}
      <MenuLabel>{f.label}</MenuLabel>
      {f.choices.map((c) => (
        <MenuCheck key={c.value} checked={c.on} onChange={() => list.toggle(f.key, c.value)}>
          {c.label}
        </MenuCheck>
      ))}
    </div>
  ));

  return (
    <div className="flex flex-col gap-2">
      <div className="flex flex-wrap items-center gap-2">
        {children}
        <SearchBox value={list.query} onChange={list.setQuery} label={label} className="min-w-0 flex-1 basis-40 sm:w-64 sm:flex-none sm:basis-auto" />
        {!plain && sorting && (
          <>
            <div className="hidden gap-2 sm:flex">
              <Menu>
                <MenuTrigger asChild>
                  <Button icon={<ArrowUpDown className="size-4" />}>Sort: {current}</Button>
                </MenuTrigger>
                <MenuContent align="start">{sortItems}</MenuContent>
              </Menu>
              {filters.length > 0 && (
                <Menu>
                  <MenuTrigger asChild>
                    <Button icon={<ListFilter className="size-4" />}>Filter</Button>
                  </MenuTrigger>
                  <MenuContent align="start">{filterItems}</MenuContent>
                </Menu>
              )}
            </div>
            {/* One on a phone, where there is room for little beside the search. */}
            <div className="sm:hidden">
              <Menu>
                <MenuTrigger asChild>
                  <Button icon={<SlidersHorizontal className="size-4" />} aria-label="Sort and filter" />
                </MenuTrigger>
                <MenuContent>
                  {sortItems}
                  {filters.length > 0 && <MenuSeparator />}
                  {filterItems}
                </MenuContent>
              </Menu>
            </div>
          </>
        )}
      </div>
      {!plain && chips.length > 0 && (
        <div className="flex flex-wrap items-center gap-1.5">
          {chips.map((c) => (
            <button
              key={`${c.key}=${c.value}`}
              type="button"
              aria-label={`Stop filtering by ${c.label}`}
              onClick={() => list.toggle(c.key, c.value)}
              className="inline-flex h-6 items-center gap-1 rounded-sm border border-line-strong bg-raised px-1.5 text-xs text-muted hover:text-fg"
            >
              {c.filter}: {c.label}
              <X className="size-3" />
            </button>
          ))}
        </div>
      )}
    </div>
  );
}
