import { useQuery } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, ExternalLink, GripVertical, Plus, X } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { api, qs, type ModrinthVersion, type StackedPack } from "../lib/api";
import { ago, bytes } from "../lib/format";
import { stackedTitle } from "./Content";
import { ProjectIcon } from "./ModrinthSearch";
import { Button, Chip, cx, Modal, Spinner } from "./ui";

// What a server's Content page and the form for making a server both show
// content with.

interface VersionDialogProps {
  title: string;
  project: string;
  current?: string;
  filters: { loader?: string; gameVersion?: string };
  onPick: (version: ModrinthVersion | null) => void;
  onClose: () => void;
}

export function VersionDialog({ title, project, current, filters, onPick, onClose }: VersionDialogProps) {
  const versions = useQuery({
    queryKey: ["modrinth-versions", project, filters],
    queryFn: () => api.get<ModrinthVersion[]>(`/catalog/modrinth/projects/${encodeURIComponent(project)}/versions?${qs(filters)}`),
    staleTime: 5 * 60_000,
  });
  const row = "flex w-full items-center gap-3 rounded-md px-2 py-2 text-left text-[13px] hover:bg-raised";
  return (
    <Modal open onOpenChange={onClose} title={title} description="Stays on this version until you change it.">
      <ul className="-mx-2 flex flex-col">
        <li>
          <button className={row} onClick={() => onPick(null)}>
            <span className="min-w-0 flex-1">
              <span className="block font-medium">Update to the best fit</span>
              <span className="block text-xs text-muted">The newest release that fits this server.</span>
            </span>
          </button>
        </li>
        {versions.isPending && (
          <li className="flex justify-center py-6">
            <Spinner />
          </li>
        )}
        {versions.isError && <li className="px-2 py-4 text-[13px] text-danger">{versions.error.message}</li>}
        {versions.data?.length === 0 && <li className="px-2 py-4 text-[13px] text-muted">No published version fits this server's loader and game version.</li>}
        {versions.data?.map((v) => (
          <li key={v.id}>
            <button className={row} onClick={() => onPick(v)}>
              <span className="min-w-0 flex-1">
                <span className="block truncate font-mono">{v.version_number}</span>
                <span className="block truncate text-xs text-muted">
                  {v.game_versions.slice(-3).join(", ")} · {ago(v.date_published)}
                </span>
              </span>
              {v.version_type !== "release" && <Chip>{v.version_type}</Chip>}
              {current === v.id && <Chip className="border-accent/40 text-accent">current</Chip>}
            </button>
          </li>
        ))}
      </ul>
    </Modal>
  );
}

/**
 * The packs a server's pack is joined from, or will be, the top one first, as in the
 * game: where two change the same thing, the one higher up wins. Dragged, or
 * moved with their buttons, into another order. A move shows at once and is
 * saved behind it; those made while one is saved are saved together next.
 */
export function PackStack({
  stack,
  canEdit,
  busy,
  onOrder,
  note,
}: {
  stack: StackedPack[];
  canEdit: boolean;
  busy: boolean;
  onOrder: (order: string[]) => Promise<string[] | void>;
  /** Said of a pack after what it is, where there is more to say. */
  note?: (p: StackedPack) => string | undefined;
}) {
  const key = stack.map((p) => p.sha1).join(",");
  const [order, setOrder] = useState(() => (key ? key.split(",") : []));
  const saving = useRef(false);
  const wanted = useRef<string[] | null>(null);
  // The panel's order, gone back to if one is not saved.
  const panel = useRef(order);
  panel.current = key ? key.split(",") : [];
  useEffect(() => {
    if (!saving.current) setOrder(key ? key.split(",") : []);
  }, [key]);
  const save = async (next: string[]) => {
    wanted.current = next;
    if (saving.current) return;
    saving.current = true;
    try {
      let now: string[] | void = undefined;
      for (let o: string[] | null = next; o; o = wanted.current) {
        wanted.current = null;
        now = await onOrder(o);
      }
      if (now) setOrder(now);
    } catch {
      wanted.current = null;
      setOrder(panel.current);
    } finally {
      saving.current = false;
    }
  };

  // Rows slide from where they were to where they are now.
  const rows = useRef(new Map<string, HTMLLIElement>());
  const was = useRef<Map<string, number> | null>(null);
  const remember = () => {
    was.current = new Map([...rows.current].map(([sha, el]) => [sha, el.getBoundingClientRect().top]));
  };
  useLayoutEffect(() => {
    const tops = was.current;
    if (!tops) return;
    was.current = null;
    for (const [sha, el] of rows.current) {
      const by = (tops.get(sha) ?? 0) - el.getBoundingClientRect().top;
      if (!tops.has(sha) || Math.abs(by) < 0.5) continue;
      el.style.transition = "none";
      el.style.transform = `translateY(${by}px)`;
      el.getBoundingClientRect();
      el.style.transition = "transform 180ms ease";
      el.style.transform = "";
    }
  });
  const move = (next: string[]) => {
    remember();
    setOrder(next);
    void save(next);
  };
  const moveFrom = (from: number, to: number) => {
    const next = order.slice();
    const [moved] = next.splice(from, 1);
    next.splice(to, 0, moved);
    move(next);
  };

  // Dragged by the pointer: the row follows it, and the others make room.
  type Held = { from: number; y: number; tops: number[]; heights: number[]; dy: number; to: number; moved: boolean };
  const held = useRef<Held | null>(null);
  const [drag, setDrag] = useState<Held | null>(null);
  const canMove = canEdit && !busy;
  const grab = (e: React.PointerEvent<HTMLLIElement>, from: number) => {
    const target = e.target as HTMLElement;
    if (!canMove || e.button !== 0 || target.closest("button, a")) return;
    // A finger scrolls the page, but from the grip.
    if (e.pointerType !== "mouse" && !target.closest("[data-grip]")) return;
    const els = order.map((sha) => rows.current.get(sha));
    if (els.some((el) => !el)) return;
    // Not the start of a text selection, nor of the browser's own drag,
    // either of which takes the pointer away from the row.
    e.preventDefault();
    e.currentTarget.setPointerCapture(e.pointerId);
    held.current = { from, y: e.clientY, tops: els.map((el) => el?.offsetTop ?? 0), heights: els.map((el) => el?.offsetHeight ?? 0), dy: 0, to: from, moved: false };
  };
  const pull = (e: React.PointerEvent) => {
    const h = held.current;
    if (!h) return;
    const last = h.tops.length - 1;
    const dy = Math.min(Math.max(e.clientY - h.y, -h.tops[h.from]), h.tops[last] + h.heights[last] - h.tops[h.from] - h.heights[h.from]);
    if (!h.moved && Math.abs(dy) < 4) return;
    // It passes another once its edge is over that one's middle, so it can
    // reach either end of the list.
    const top = h.tops[h.from] + dy;
    const bottom = top + h.heights[h.from];
    const mids = h.tops.map((t, i) => t + h.heights[i] / 2);
    const to = h.from - mids.filter((m, i) => i < h.from && top < m).length + mids.filter((m, i) => i > h.from && bottom > m).length;
    held.current = { ...h, dy, to, moved: true };
    setDrag(held.current);
  };
  const drop = (keep: boolean) => {
    const h = held.current;
    held.current = null;
    if (!h?.moved) return;
    remember();
    setDrag(null);
    if (keep && h.to !== h.from) moveFrom(h.from, h.to);
  };
  const shift = (i: number) => {
    if (!drag) return undefined;
    if (i === drag.from) return drag.dy;
    if (drag.from < i && i <= drag.to) return -drag.heights[drag.from];
    if (drag.to <= i && i < drag.from) return drag.heights[drag.from];
    return 0;
  };

  const by = new Map(stack.map((p) => [p.sha1, p]));
  const titleOf = (sha?: string) => {
    const p = sha ? by.get(sha) : undefined;
    return p ? stackedTitle(p) : "another pack";
  };
  const shown = order.flatMap((sha) => by.get(sha) ?? []);
  return (
    <>
      <ol aria-label="Packs, the top one first" className="relative overflow-hidden rounded-lg border border-line bg-surface">
        {shown.map((p, i) => {
          const title = stackedTitle(p);
          const line = [p.project ? p.project.versionName : p.size ? bytes(p.size) : "", note?.(p), p.hidden ? `${p.hidden} ${p.hidden === 1 ? "file" : "files"} covered by ${titleOf(p.hiddenBy)}` : ""].filter(Boolean).join(" · ");
          const dy = shift(i);
          const lifted = drag?.from === i;
          return (
            <li
              key={p.sha1}
              ref={(el) => {
                if (el) rows.current.set(p.sha1, el);
                else rows.current.delete(p.sha1);
              }}
              onPointerDown={(e) => grab(e, i)}
              onPointerMove={pull}
              onPointerUp={() => drop(true)}
              onPointerCancel={() => drop(false)}
              // Should the row lose the pointer some other way, it is let go
              // where it is, not left held.
              onLostPointerCapture={() => drop(true)}
              style={dy === undefined ? undefined : { transform: `translateY(${dy}px)`, transition: lifted ? "none" : "transform 150ms ease" }}
              className={cx(
                "relative flex items-center gap-3 border-b border-line bg-surface px-3 py-2.5 last:border-b-0",
                canMove && "select-none",
                lifted && "z-10 cursor-grabbing rounded-md shadow-lg ring-1 ring-line",
              )}
            >
              {canEdit && (
                <span data-grip className={cx("-m-1 touch-none p-1", canMove && !lifted && "cursor-grab")}>
                  <GripVertical aria-hidden className="size-4 shrink-0 text-faint" />
                </span>
              )}
              <ProjectIcon url={p.project?.iconUrl} size={32} kind="resourcepack" name={title} />
              <div className="min-w-0 flex-1">
                <p className="flex items-center gap-2 truncate text-[13px] font-medium">
                  {title}
                  {p.project && (
                    <a href={`https://modrinth.com/resourcepack/${p.project.id}`} target="_blank" rel="noreferrer" className="text-faint hover:text-accent" aria-label="Open on Modrinth">
                      <ExternalLink className="size-3" />
                    </a>
                  )}
                </p>
                {line && <p className="truncate text-xs text-faint">{line}</p>}
              </div>
              {canEdit && (
                <div className="flex shrink-0 items-center">
                  <Button size="sm" variant="ghost" icon={<ArrowUp className="size-4" />} aria-label={`Move ${title} up`} disabled={!canMove || i === 0} onClick={() => moveFrom(i, i - 1)} />
                  <Button size="sm" variant="ghost" icon={<ArrowDown className="size-4" />} aria-label={`Move ${title} down`} disabled={!canMove || i === shown.length - 1} onClick={() => moveFrom(i, i + 1)} />
                  <Button size="sm" variant="ghost" icon={<X className="size-4" />} aria-label={`Remove ${title}`} disabled={!canMove} onClick={() => move(order.filter((sha) => sha !== p.sha1))} />
                </div>
              )}
            </li>
          );
        })}
      </ol>
      {shown.length > 1 && <p className="px-1 text-xs text-muted">Joined into one pack. Where two change the same thing, the higher one wins.</p>}
    </>
  );
}

/** A kind of content's heading, with how many there are and a way to add one. Within a section of its own, it is a level down. */
export function SectionHead({ title, count, addLabel, onAdd, addDisabled, sub }: { title: string; count?: number; addLabel?: string; onAdd?: () => void; addDisabled?: boolean; sub?: boolean }) {
  const Heading = sub ? "h3" : "h2";
  return (
    <div className="flex min-h-8 items-center justify-between gap-3">
      <Heading className="text-[13px] font-medium">
        {title}
        {!!count && <span className="ml-1.5 font-normal text-muted">{count}</span>}
      </Heading>
      {onAdd && (
        <Button size="sm" icon={<Plus className="size-4" />} onClick={onAdd} disabled={addDisabled} aria-label={addLabel ?? `Add ${title.toLowerCase()}`}>
          Add
        </Button>
      )}
    </div>
  );
}
