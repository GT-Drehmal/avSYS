import { useCallback, useEffect, useMemo, useRef, useState } from "react";

export interface Selection {
  /** The selected IDs, in the order the list shows them. */
  ids: string[];
  count: number;
  has: (id: string) => boolean;
  /** Selects or deselects one. With range, everything between it and the one before it follows. */
  toggle: (id: string, range?: boolean) => void;
  /** Selects everything, or nothing if everything already is. */
  toggleAll: () => void;
  clear: () => void;
  /** Whether all, some or none of the list is selected. */
  all: boolean;
  some: boolean;
}

/**
 * Which rows of a list are ticked. ids is the list as shown, in order: a
 * row that leaves it is no longer selected, and a range runs along it.
 * Escape clears the selection.
 */
export function useSelection(ids: string[]): Selection {
  const [picked, setPicked] = useState<ReadonlySet<string>>(new Set());
  const last = useRef<string | null>(null);
  const key = ids.join("\n");
  const order = useMemo(() => ids, [key]); // eslint-disable-line react-hooks/exhaustive-deps

  // What is no longer listed is no longer selected.
  useEffect(() => {
    const listed = new Set(order);
    setPicked((now) => {
      const kept = [...now].filter((id) => listed.has(id));
      return kept.length === now.size ? now : new Set(kept);
    });
  }, [order]);

  const toggle = useCallback(
    (id: string, range?: boolean) => {
      setPicked((now) => {
        const next = new Set(now);
        const on = !now.has(id);
        const from = range && last.current ? order.indexOf(last.current) : -1;
        const to = order.indexOf(id);
        const span = from >= 0 && to >= 0 ? order.slice(Math.min(from, to), Math.max(from, to) + 1) : [id];
        for (const each of span) on ? next.add(each) : next.delete(each);
        return next;
      });
      last.current = id;
    },
    [order],
  );
  const clear = useCallback(() => setPicked(new Set()), []);
  const all = order.length > 0 && order.every((id) => picked.has(id));
  const toggleAll = useCallback(() => setPicked(all ? new Set() : new Set(order)), [all, order]);

  const count = picked.size;
  useEffect(() => {
    if (count === 0) return;
    const onKey = (e: KeyboardEvent) => {
      // A dialog that is open takes Escape for itself.
      if (e.key === "Escape" && !document.querySelector("[role=dialog], [role=alertdialog]")) clear();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [count, clear]);

  return {
    ids: order.filter((id) => picked.has(id)),
    count,
    has: (id) => picked.has(id),
    toggle,
    toggleAll,
    clear,
    all,
    some: count > 0 && !all,
  };
}
