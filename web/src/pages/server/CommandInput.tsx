import { useQuery } from "@tanstack/react-query";
import { useEffect, useId, useLayoutEffect, useMemo, useRef, useState } from "react";
import { cx } from "../../components/ui";
import { api, type Server, serverPath } from "../../lib/api";
import { askAgainIn, type CommandNode, complete, type Edit, type EditKind, emptyUndo, ghostOf, insert, record, redo, undo } from "../../lib/completion";

/**
 * The game's command tree for the version a server runs, with the commands
 * its mods and plugins add while it runs, for completing commands; until
 * the game's own tree is there, those the running server's help lists.
 * Undefined while the panel makes it, and where there is none. Asked again
 * each time the console opens or the server starts (the panel answers "not
 * changed" if it is the same), as the server's own commands are read anew
 * each time it starts.
 */
export function useCommandTree(server: Server, enabled: boolean): CommandNode | undefined {
  const version = `${server.spec.modpack?.gameVersion ?? server.spec.version ?? ""}|${server.status.version ?? ""}`;
  const q = useQuery({
    queryKey: ["commands", server.org, server.name, version],
    queryFn: () => api.get<CommandNode | { making: true }>(`${serverPath(server.org, server.name)}/console/commands`),
    enabled,
    retry: false,
    staleTime: 0,
    refetchOnWindowFocus: false,
    // Being made, or from the server's help alone: asked again until the
    // game's own are there.
    refetchInterval: (query) => askAgainIn(query.state.data),
  });
  // While asked again, the one there was stays.
  const last = useRef<{ key: string; tree: CommandNode } | null>(null);
  const key = `${server.org}/${server.name}|${version}`;
  const data = q.data;
  if (data && !("making" in data)) last.current = { key, tree: data };
  return last.current?.key === key ? last.current.tree : undefined;
}

let measurer: CanvasRenderingContext2D | null = null;
/** How wide text is in an input's font. */
function widthOf(input: HTMLInputElement | null, text: string): number {
  if (!input || !text) return 0;
  measurer ??= document.createElement("canvas").getContext("2d");
  if (!measurer) return 0;
  measurer.font = getComputedStyle(input).font;
  return measurer.measureText(text).width;
}

const isMac = typeof navigator !== "undefined" && /Mac|iPhone|iPad/.test(navigator.platform);

interface Props {
  value: string;
  onChange: (value: string) => void;
  /** The command run before (-1, older) or after (+1, newer) the one shown, or null if there is none. */
  onHistory: (step: 1 | -1) => string | null;
  tree: CommandNode | undefined;
  /** Who is on, offered where a player goes. */
  players: string[];
  disabled: boolean;
  placeholder: string;
}

/**
 * The console's command line. As a command is typed, what it can go on
 * with is offered above it, as the game's chat does: the commands and
 * subcommands that start with the word typed, and the argument expected.
 * The one chosen shows faded after the cursor; Tab or a click puts it in,
 * and undo takes it out again.
 */
export function CommandInput({ value, onChange, onHistory, tree, players, disabled, placeholder }: Props) {
  const input = useRef<HTMLInputElement>(null);
  const listId = useId();
  const [cursor, setCursor] = useState(value.length);
  const [open, setOpen] = useState(false);
  const [selected, setSelected] = useState(0);
  const [scroll, setScroll] = useState(0);
  const history = useRef(emptyUndo);
  // The line as this last made it, and where to put the cursor once it is
  // drawn; a value from outside, such as the line emptied once run, starts
  // afresh.
  const own = useRef(value);
  const place = useRef<number | null>(null);
  const selection = useRef<Edit>({ value, cursor: value.length });

  useEffect(() => {
    if (value === own.current) return;
    own.current = value;
    history.current = emptyUndo;
    setOpen(false);
    setCursor(value.length);
    selection.current = { value, cursor: value.length };
  }, [value]);

  useLayoutEffect(() => {
    const el = input.current;
    if (el && place.current !== null) {
      el.setSelectionRange(place.current, place.current);
      place.current = null;
    }
    if (el) setScroll(el.scrollLeft);
  });

  const c = useMemo(() => (tree && value ? complete(tree, value, cursor, players) : null), [tree, value, cursor, players]);
  const suggestions = c?.suggestions ?? [];
  const shown = open && !!c && (suggestions.length > 0 || c.hints.length > 0);
  // A new list starts at its first.
  const listKey = c ? `${c.start}:${suggestions.join(" ")}` : "";
  const [shownKey, setShownKey] = useState(listKey);
  if (shownKey !== listKey) {
    setShownKey(listKey);
    setSelected(0);
  }
  const choice = suggestions[Math.min(selected, suggestions.length - 1)];
  const ghost = shown && c && cursor === value.length ? ghostOf(c, choice) : "";

  /** Puts a line in place, as typed or taken back. */
  const set = (e: Edit, kind: EditKind | null, show: boolean, typed = "") => {
    if (kind) history.current = record(history.current, selection.current, kind, Date.now(), typed, e);
    own.current = e.value;
    place.current = e.cursor;
    selection.current = e;
    setCursor(e.cursor);
    setOpen(show && e.value !== "");
    onChange(e.value);
  };

  const take = (suggestion: string) => {
    if (!c) return;
    set(insert(value, cursor, c, suggestion), "complete", true);
    input.current?.focus();
  };

  const step = (back: boolean) => {
    const current = { value, cursor };
    const [u, e] = back ? undo(history.current, current) : redo(history.current, current);
    history.current = u;
    if (e) set(e, null, true);
  };

  // Undo from the browser's own menu, too.
  useEffect(() => {
    const el = input.current;
    if (!el) return;
    const before = (e: InputEvent) => {
      if (e.inputType === "historyUndo" || e.inputType === "historyRedo") {
        e.preventDefault();
        step(e.inputType === "historyUndo");
      }
    };
    el.addEventListener("beforeinput", before);
    return () => el.removeEventListener("beforeinput", before);
  });

  const onKeyDown = (e: React.KeyboardEvent<HTMLInputElement>) => {
    const mod = isMac ? e.metaKey : e.ctrlKey;
    const key = e.key.toLowerCase();
    if (mod && !e.altKey && (key === "z" || (key === "y" && !isMac))) {
      e.preventDefault();
      step(key === "z" && !e.shiftKey);
      return;
    }
    const list = shown && suggestions.length > 0;
    if (list && (e.key === "ArrowDown" || e.key === "ArrowUp")) {
      e.preventDefault();
      const n = suggestions.length;
      setSelected((i) => (Math.min(i, n - 1) + (e.key === "ArrowDown" ? 1 : n - 1)) % n);
      return;
    }
    if (list && e.key === "Tab" && !e.shiftKey && choice) {
      e.preventDefault();
      take(choice);
      return;
    }
    if (shown && e.key === "Escape") {
      e.preventDefault();
      setOpen(false);
      return;
    }
    if (e.key === "ArrowUp" || e.key === "ArrowDown") {
      const line = onHistory(e.key === "ArrowUp" ? -1 : 1);
      if (line !== null) {
        e.preventDefault();
        set({ value: line, cursor: line.length }, "other", false);
      }
    }
  };

  const onInput = (e: React.ChangeEvent<HTMLInputElement>) => {
    const el = e.target;
    const native = e.nativeEvent as InputEvent;
    const kind: EditKind = native.inputType === "insertText" ? "type" : native.inputType?.startsWith("delete") ? "delete" : "other";
    const at = el.selectionStart ?? el.value.length;
    set({ value: el.value, cursor: at }, kind, true, native.data ?? "");
    // Typed, the cursor is where the browser put it already.
    place.current = null;
  };

  const onSelect = (e: React.SyntheticEvent<HTMLInputElement>) => {
    const el = e.currentTarget;
    const at = el.selectionStart ?? el.value.length;
    selection.current = { value: el.value, cursor: at };
    setCursor(at);
    setScroll(el.scrollLeft);
  };

  // The list stands over the word being completed, as far right as it fits.
  const left = c ? Math.max(0, widthOf(input.current, value.slice(0, c.start)) - scroll) : 0;
  const option = (i: number) => `${listId}-${i}`;

  return (
    <div className="relative h-full min-w-0 flex-1">
      {shown && c && (
        <div
          className="absolute bottom-full z-20 mb-[7px] max-w-[min(32rem,100%)] min-w-44 overflow-hidden rounded-md border border-line-strong bg-raised font-mono text-xs shadow-lg shadow-shade/40"
          style={{ left: `min(${left}px, calc(100% - 11rem))` }}
        >
          {suggestions.length > 0 && (
            <ul id={listId} role="listbox" aria-label="Suggestions" className="max-h-[13.5rem] overflow-y-auto py-1">
              {suggestions.map((s, i) => (
                <Suggestion key={s} id={option(i)} text={s} typed={c.word} chosen={s === choice} onPick={() => take(s)} />
              ))}
            </ul>
          )}
          {c.hints.length > 0 && (
            <div data-testid="hints" aria-live="polite" className={cx("flex flex-wrap gap-x-3 gap-y-0.5 px-2.5 py-1.5", suggestions.length > 0 && "border-t border-line")}>
              {c.hints.map((h) => (
                <span key={`${h.name}:${h.type}`} className="whitespace-nowrap">
                  <span className="text-muted">{h.optional ? `[<${h.name}>]` : `<${h.name}>`}</span>
                  {h.type && (
                    <>
                      {" "}
                      <span className="font-sans text-faint">{h.type}</span>
                    </>
                  )}
                </span>
              ))}
            </div>
          )}
        </div>
      )}
      <div aria-hidden className="pointer-events-none absolute inset-0 flex items-center overflow-hidden font-mono text-xs whitespace-pre">
        <span style={{ transform: `translateX(${-scroll}px)` }}>
          <span className="invisible">{value}</span>
          <span data-testid="ghost" className="text-faint">
            {ghost}
          </span>
        </span>
      </div>
      <input
        ref={input}
        value={value}
        onChange={onInput}
        onKeyDown={onKeyDown}
        onSelect={onSelect}
        onScroll={(e) => setScroll(e.currentTarget.scrollLeft)}
        onBlur={() => setOpen(false)}
        disabled={disabled}
        placeholder={placeholder}
        aria-label="Server command"
        role="combobox"
        aria-autocomplete="list"
        aria-expanded={shown && suggestions.length > 0}
        aria-controls={shown && suggestions.length > 0 ? listId : undefined}
        aria-activedescendant={shown && choice ? option(suggestions.indexOf(choice)) : undefined}
        autoComplete="off"
        autoCapitalize="off"
        autoCorrect="off"
        spellCheck={false}
        className="relative h-full w-full bg-transparent font-mono text-xs text-fg placeholder:text-faint focus:outline-none disabled:cursor-not-allowed"
      />
    </div>
  );
}

function Suggestion({ id, text, typed, chosen, onPick }: { id: string; text: string; typed: string; chosen: boolean; onPick: () => void }) {
  const ref = useRef<HTMLLIElement>(null);
  useLayoutEffect(() => {
    if (chosen) ref.current?.scrollIntoView({ block: "nearest" });
  }, [chosen]);
  const n = text.toLowerCase().startsWith(typed.toLowerCase()) ? typed.length : 0;
  return (
    <li
      ref={ref}
      id={id}
      role="option"
      aria-selected={chosen}
      // Clicked, the line keeps the focus.
      onMouseDown={(e) => e.preventDefault()}
      onClick={onPick}
      className={cx("cursor-pointer px-2.5 py-[3px] whitespace-pre", chosen ? "bg-accent-wash text-accent-strong" : "text-fg hover:bg-hover")}
    >
      <span className={chosen ? "text-accent" : "text-muted"}>{text.slice(0, n)}</span>
      {text.slice(n)}
    </li>
  );
}
