// Completing a command typed at the console, from the game's own tree of
// commands (its commands.json): every command, its subcommands and the
// arguments each takes, as Brigadier, the game's command parser, has them.

/** A node of the game's command tree. */
export interface CommandNode {
  type: "root" | "literal" | "argument";
  children?: Record<string, CommandNode>;
  executable?: boolean;
  /** Where the command goes on, as a path from the root. */
  redirect?: string[];
  parser?: string;
  properties?: Record<string, unknown>;
}

/** An argument expected where the cursor is. */
export interface Hint {
  name: string;
  /** What it is, in a word or two. */
  type: string;
  /** Whether the command can be run without it. */
  optional: boolean;
}

export interface Completion {
  /** Where the word being completed starts. */
  start: number;
  /** The word typed so far. */
  word: string;
  /** What the word can be completed to. */
  suggestions: string[];
  /** The arguments that can go here. */
  hints: Hint[];
}

const none = (start: number, word = ""): Completion => ({ start, word, suggestions: [], hints: [] });

/** What a redirect, or a node with nowhere to go, leads to. */
function follow(root: CommandNode, node: CommandNode): CommandNode {
  if (node.redirect) {
    let at: CommandNode | undefined = root;
    for (const name of node.redirect) at = at?.children?.[name];
    return at ?? node;
  }
  // "execute … run" has no children and no redirect: it goes back to the
  // root, which commands.json does not name.
  if (!node.children && !node.executable && node.type !== "root") return root;
  return node;
}

/**
 * The length of one chunk of an argument at pos: up to the next space that
 * is outside brackets and quotes. 0 when there is none there.
 */
function chunk(text: string, pos: number): number {
  let depth = 0;
  let quote = "";
  let i = pos;
  for (; i < text.length; i++) {
    const c = text[i];
    if (quote) {
      if (c === "\\") i++;
      else if (c === quote) quote = "";
    } else if (c === '"' || c === "'") quote = c;
    else if (c === "[" || c === "{" || c === "(") depth++;
    else if (c === "]" || c === "}" || c === ")") depth = Math.max(0, depth - 1);
    else if (c === " " && depth === 0) break;
  }
  return i - pos;
}

/** How many chunks an argument of a parser takes; Infinity is the rest of the line. */
function chunks(node: CommandNode): number {
  switch (node.parser) {
    case "minecraft:vec3":
    case "minecraft:block_pos":
      return 3;
    case "minecraft:vec2":
    case "minecraft:column_pos":
    case "minecraft:rotation":
      return 2;
    case "minecraft:message":
      return Infinity;
    case "brigadier:string":
      return node.properties?.type === "greedy" ? Infinity : 1;
    default:
      return 1;
  }
}

const coordinate = /^[~^]?-?\d*(\.\d+)?$/;
const selector = /^@[aeprsn](\[.*\])?$/;
const name = /^[\w.+-]+$/;

/** Whether text could be a whole argument of a node: not a full check, enough to choose between two. */
function fits(node: CommandNode, text: string): boolean {
  const parts = text.split(" ");
  switch (node.parser) {
    case "brigadier:bool":
      return text === "true" || text === "false";
    case "brigadier:integer":
    case "brigadier:long":
      return /^-?\d+$/.test(text);
    case "brigadier:float":
    case "brigadier:double":
      return /^-?(\d+\.?\d*|\.\d+)$/.test(text);
    case "minecraft:vec3":
    case "minecraft:block_pos":
    case "minecraft:vec2":
    case "minecraft:column_pos":
    case "minecraft:rotation":
      return parts.every((p) => p !== "" && coordinate.test(p));
    case "minecraft:entity":
    case "minecraft:game_profile":
      return selector.test(text) || name.test(text) || /^[0-9a-f-]{36}$/i.test(text);
    case "minecraft:score_holder":
      return text === "*" || selector.test(text) || /^\S+$/.test(text);
    default:
      return true;
  }
}

/**
 * The argument of a node that starts at pos, and the length of it, when it
 * is followed by a space and so typed in full. 0 when it is not.
 */
function argumentAt(node: CommandNode, text: string, pos: number): number {
  const want = chunks(node);
  if (want === Infinity) return 0;
  let i = pos;
  for (let n = 0; n < want; n++) {
    if (n > 0) {
      if (text[i] !== " ") return 0;
      i++;
    }
    const len = chunk(text, i);
    if (len === 0) return 0;
    i += len;
  }
  if (text[i] !== " " || !fits(node, text.slice(pos, i))) return 0;
  return i - pos;
}

/** What a parser takes, in a word or two. */
export function typeOf(node: CommandNode): string {
  const p = node.properties ?? {};
  const registry = typeof p.registry === "string" ? p.registry.replace(/^minecraft:/, "").replaceAll("_", " ") : "";
  switch (node.parser) {
    case "brigadier:bool":
      return "true or false";
    case "brigadier:integer":
    case "brigadier:long":
      return "whole number";
    case "brigadier:float":
    case "brigadier:double":
      return "number";
    case "brigadier:string":
      return p.type === "word" ? "word" : "text";
    case "minecraft:entity": {
      const players = p.type === "players";
      if (p.amount === "single") return players ? "player" : "entity";
      return players ? "players" : "entities";
    }
    case "minecraft:game_profile":
      return "players";
    case "minecraft:score_holder":
      return "entity or name";
    case "minecraft:vec3":
    case "minecraft:block_pos":
      return "x y z";
    case "minecraft:vec2":
    case "minecraft:column_pos":
      return "x z";
    case "minecraft:rotation":
      return "yaw pitch";
    case "minecraft:message":
      return "message";
    case "minecraft:component":
      return "text component";
    case "minecraft:nbt_compound_tag":
    case "minecraft:nbt_tag":
      return "NBT";
    case "minecraft:nbt_path":
      return "NBT path";
    case "minecraft:item_stack":
    case "minecraft:item_predicate":
      return "item";
    case "minecraft:block_state":
    case "minecraft:block_predicate":
      return "block";
    case "minecraft:resource_location":
      return "ID";
    case "minecraft:gamemode":
      return "game mode";
    case "minecraft:resource":
    case "minecraft:resource_key":
    case "minecraft:resource_or_tag":
    case "minecraft:resource_or_tag_key":
      return registry || "ID";
    default:
      return (node.parser ?? "").replace(/^\w+:/, "").replaceAll("_", " ");
  }
}

const colors = ["aqua", "black", "blue", "dark_aqua", "dark_blue", "dark_gray", "dark_green", "dark_purple", "dark_red", "gold", "gray", "green", "light_purple", "red", "reset", "white", "yellow"];

/** Values an argument can be given, where they are few or known: before the word typed filters them. */
function valuesOf(node: CommandNode, players: string[]): string[] {
  switch (node.parser) {
    case "brigadier:bool":
      return ["true", "false"];
    case "minecraft:gamemode":
      return ["adventure", "creative", "spectator", "survival"];
    case "minecraft:entity":
    case "minecraft:game_profile":
    case "minecraft:score_holder": {
      const players_ = node.properties?.type === "players";
      const selectors = players_ ? ["@a", "@p", "@r", "@s"] : ["@a", "@e", "@p", "@r", "@s"];
      return [...players, ...selectors];
    }
    case "minecraft:vec3":
    case "minecraft:block_pos":
      return ["~ ~ ~"];
    case "minecraft:vec2":
    case "minecraft:column_pos":
    case "minecraft:rotation":
      return ["~ ~"];
    case "minecraft:dimension":
      return ["minecraft:overworld", "minecraft:the_nether", "minecraft:the_end"];
    case "minecraft:color":
      return colors;
    case "minecraft:entity_anchor":
      return ["eyes", "feet"];
    case "minecraft:operation":
      return ["=", "+=", "-=", "*=", "/=", "%=", "<", ">", "><"];
    case "minecraft:template_mirror":
      return ["none", "front_back", "left_right"];
    case "minecraft:template_rotation":
      return ["none", "clockwise_90", "counterclockwise_90", "180"];
    default:
      return [];
  }
}

const startsWith = (s: string, prefix: string) => s.toLowerCase().startsWith(prefix.toLowerCase());

/**
 * What can be typed at the cursor: the commands, subcommands and values
 * that start with the word being typed, and the arguments expected there.
 * players are names to offer where a player or entity goes.
 */
export function complete(root: CommandNode, text: string, cursor = text.length, players: string[] = []): Completion {
  const before = text.slice(0, cursor);
  const first = before.startsWith("/") ? 1 : 0;
  // Nothing typed yet: nothing to offer, as the game does.
  if (before.length === first) return none(first);
  // Every way the words typed in full can be read, as the game tries them
  // all: a literal that matches is taken alone, otherwise each argument the
  // text fits. Each way ends where it can read no further, and those that
  // read furthest say what can come next.
  let ways: { node: CommandNode; pos: number }[] = [{ node: root, pos: first }];
  const ends: typeof ways = [];
  while (ways.length > 0) {
    const next: typeof ways = [];
    for (const { node, pos } of ways) {
      const children = Object.entries(node.children ?? {});
      const literal = children.find(([name, c]) => c.type === "literal" && before.startsWith(`${name} `, pos));
      const moves = literal
        ? [{ node: literal[1], pos: pos + literal[0].length + 1 }]
        : children.flatMap(([, c]) => {
            const len = c.type === "argument" ? argumentAt(c, before, pos) : 0;
            return len > 0 ? [{ node: c, pos: pos + len + 1 }] : [];
          });
      if (moves.length === 0) ends.push({ node, pos });
      for (const m of moves) next.push({ node: follow(root, m.node), pos: m.pos });
    }
    // Ways that meet again go on as one; a line that can be read in very
    // many ways is read in the first few.
    const seen = new Set<string>();
    ways = next
      .filter((w) => {
        const key = `${w.pos}:${idOf(w.node)}`;
        if (seen.has(key)) return false;
        seen.add(key);
        return true;
      })
      .slice(0, 32);
  }
  const pos = Math.max(...ends.map((e) => e.pos));
  const word = before.slice(pos);
  const out: Completion = none(pos, word);
  const named = new Set<string>();
  for (const { node } of ends.filter((e) => e.pos === pos)) {
    const c = offer(node, word, players);
    for (const s of c.suggestions) if (!out.suggestions.includes(s)) out.suggestions.push(s);
    for (const h of c.hints) {
      const key = `${h.name}:${h.type}`;
      if (!named.has(key)) {
        named.add(key);
        out.hints.push(h);
      }
    }
  }
  return out;
}

/** Something to tell nodes apart by, for ways that meet again. */
const ids = new WeakMap<CommandNode, number>();
let lastId = 0;
function idOf(node: CommandNode): number {
  let id = ids.get(node);
  if (id === undefined) {
    id = ++lastId;
    ids.set(node, id);
  }
  return id;
}

/** What can come at a node, for the word typed so far there. */
function offer(node: CommandNode, word: string, players: string[]): { suggestions: string[]; hints: Hint[] } {
  const children = Object.entries(node.children ?? {});
  const args = children.filter(([, c]) => c.type === "argument");
  const optional = !!node.executable;
  const hints = args.map(([name, c]) => ({ name, type: typeOf(c), optional }));
  // An argument of more than one word, or the rest of the line, being typed.
  if (word.includes(" ")) {
    const going = new Set(args.filter(([, c]) => chunks(c) > 1).map(([n]) => n));
    return { suggestions: [], hints: hints.filter((h) => going.has(h.name)) };
  }
  const suggestions: string[] = [];
  const add = (s: string) => {
    if (startsWith(s, word) && s !== word && !suggestions.includes(s)) suggestions.push(s);
  };
  const literals = children
    .filter(([, c]) => c.type === "literal")
    .map(([n]) => n)
    .sort();
  for (const n of literals) add(n);
  for (const [, c] of args) for (const v of valuesOf(c, players)) add(v);
  // A word that is nothing here, with no argument it could be, is past
  // what is known.
  if (args.length === 0 && suggestions.length === 0) return { suggestions: [], hints: [] };
  return { suggestions, hints };
}

/** The rest of a suggestion, after what is typed of it: shown faded after the cursor. */
export function ghostOf(c: Completion, suggestion: string | undefined): string {
  if (suggestion && startsWith(suggestion, c.word)) return suggestion.slice(c.word.length);
  if (!suggestion && c.word === "" && c.hints.length > 0) {
    const h = c.hints[0];
    return h.optional ? `[<${h.name}>]` : `<${h.name}>`;
  }
  return "";
}

/** The text and cursor once a suggestion is put in place of the word being typed, with a space after it. */
export function insert(text: string, cursor: number, c: Completion, suggestion: string): { value: string; cursor: number } {
  const rest = text.slice(cursor).replace(/^\S*/, "");
  const head = text.slice(0, c.start) + suggestion;
  return { value: head + (rest.startsWith(" ") ? rest : ` ${rest}`), cursor: head.length + 1 };
}

/** One state of the line, for undoing. */
export interface Edit {
  value: string;
  cursor: number;
}

/** How a change was made: typing, deleting, completing, or anything else. */
export type EditKind = "type" | "delete" | "complete" | "other";

/** What can be undone and redone in the line. */
export interface Undo {
  past: Edit[];
  future: Edit[];
  last: EditKind | null;
  at: number;
}

export const emptyUndo: Undo = { past: [], future: [], last: null, at: 0 };

/** Typing is undone a word at a time, and so is deleting; a pause also ends one. */
const pause = 1000;

/**
 * The undo history once a change is made: before is the line as it was.
 * Typing runs on into the same step up to a space, a pause, or a change of
 * another kind; a completion is always a step of its own.
 */
export function record(u: Undo, before: Edit, kind: EditKind, now: number, typed = ""): Undo {
  const joins = kind !== "complete" && kind !== "other" && u.last === kind && now - u.at < pause;
  return {
    past: joins ? u.past : [...u.past, before].slice(-100),
    future: [],
    last: typed.endsWith(" ") ? null : kind,
    at: now,
  };
}

/** Undoes the last step: the line it goes back to, or null if there is none. */
export function undo(u: Undo, current: Edit): [Undo, Edit | null] {
  const prev = u.past.at(-1);
  if (!prev) return [u, null];
  return [{ past: u.past.slice(0, -1), future: [...u.future, current], last: null, at: 0 }, prev];
}

/** Does again what was undone. */
export function redo(u: Undo, current: Edit): [Undo, Edit | null] {
  const next = u.future.at(-1);
  if (!next) return [u, null];
  return [{ past: [...u.past, current], future: u.future.slice(0, -1), last: null, at: 0 }, next];
}
