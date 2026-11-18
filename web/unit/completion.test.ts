// The console's command completion, against the command tree the game
// wrote out for 1.21.4 (tests/upstream/commands). Run with `bun test unit`.
import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { gunzipSync } from "node:zlib";
import { askAgainIn, type CommandNode, complete, emptyUndo, ghostOf, insert, record, redo, undo } from "../src/lib/completion";

const tree: CommandNode = JSON.parse(gunzipSync(readFileSync(new URL("../tests/upstream/commands/1.21.4.json.gz", import.meta.url))).toString());

describe("commands", () => {
  test("nothing typed offers nothing", () => {
    expect(complete(tree, "").suggestions).toEqual([]);
    expect(complete(tree, "/").suggestions).toEqual([]);
  });

  test("a letter offers the commands that start with it, in order", () => {
    const c = complete(tree, "ti");
    expect(c.suggestions).toEqual(["tick", "time", "title"]);
    expect(c.start).toBe(0);
    expect(c.word).toBe("ti");
  });

  test("a slash before the command is allowed", () => {
    const c = complete(tree, "/ti");
    expect(c.suggestions).toEqual(["tick", "time", "title"]);
    expect(c.start).toBe(1);
  });

  test("case does not matter for what is offered", () => {
    expect(complete(tree, "TI").suggestions).toEqual(["tick", "time", "title"]);
  });

  test("a command typed in full is not offered again", () => {
    expect(complete(tree, "list").suggestions).toEqual([]);
  });

  test("after a command and a space, its subcommands are offered", () => {
    expect(complete(tree, "time ").suggestions).toEqual(["add", "query", "set"]);
    expect(complete(tree, "time s").suggestions).toEqual(["set"]);
    expect(complete(tree, "weather ").suggestions).toEqual(["clear", "rain", "thunder"]);
  });

  test("subcommands go on, level by level", () => {
    const c = complete(tree, "time set ");
    expect(c.suggestions).toEqual(["day", "midnight", "night", "noon"]);
    expect(c.hints).toEqual([{ name: "time", type: "time", optional: false }]);
    expect(complete(tree, "worldborder warning ").suggestions).toEqual(["distance", "time"]);
  });

  test("an alias goes where it leads", () => {
    // tp is teleport's.
    expect(complete(tree, "tp ").hints.map((h) => h.name)).toEqual(complete(tree, "teleport ").hints.map((h) => h.name));
  });

  test("execute runs any command after run", () => {
    expect(complete(tree, "execute run ti").suggestions).toEqual(["tick", "time", "title"]);
    expect(complete(tree, "execute as @a at @s run time ").suggestions).toEqual(["add", "query", "set"]);
  });

  test("an argument of several words is taken whole", () => {
    expect(complete(tree, "execute positioned ~ ~1 ~ run wea").suggestions).toEqual(["weather"]);
    expect(complete(tree, "execute as @e[type=cow, limit=1] run wea").suggestions).toEqual(["weather"]);
  });

  test("an unknown command offers nothing after it", () => {
    const c = complete(tree, "nope ");
    expect(c.suggestions).toEqual([]);
    expect(c.hints).toEqual([]);
    expect(complete(tree, "time nope ").suggestions).toEqual([]);
  });

  test("only what is before the cursor counts", () => {
    expect(complete(tree, "time set day", 6).suggestions).toEqual(["set"]);
  });
});

describe("arguments", () => {
  test("the argument expected is named, with what it takes", () => {
    const c = complete(tree, "give ");
    expect(c.hints).toEqual([{ name: "targets", type: "players", optional: false }]);
    expect(complete(tree, "give @p ").hints).toEqual([{ name: "item", type: "item", optional: false }]);
    expect(complete(tree, "give @p diamond ").hints).toEqual([{ name: "count", type: "whole number", optional: true }]);
  });

  test("players and selectors are offered for an entity", () => {
    const c = complete(tree, "kill ", undefined, ["Steve", "Alex"]);
    expect(c.suggestions).toEqual(["Steve", "Alex", "@a", "@e", "@p", "@r", "@s"]);
    expect(complete(tree, "kill St", undefined, ["Steve", "Alex"]).suggestions).toEqual(["Steve"]);
    // Only players where only players go.
    expect(complete(tree, "give ").suggestions).toEqual(["@a", "@p", "@r", "@s"]);
  });

  test("known values are offered", () => {
    expect(complete(tree, "gamemode ").suggestions).toEqual(["adventure", "creative", "spectator", "survival"]);
    expect(complete(tree, "gamemode c").suggestions).toEqual(["creative"]);
    expect(complete(tree, "gamerule keepInventory ").suggestions).toEqual(["true", "false"]);
  });

  test("a message runs to the end of the line", () => {
    const c = complete(tree, "say hello the");
    expect(c.suggestions).toEqual([]);
    expect(c.hints).toEqual([{ name: "message", type: "message", optional: false }]);
  });

  test("a position being typed is still the position", () => {
    expect(complete(tree, "setblock ~ ~").hints.map((h) => h.name)).toEqual(["pos"]);
    expect(complete(tree, "setblock ~ ~ ~ ").hints.map((h) => h.name)).toEqual(["block"]);
  });

  test("of two arguments, the one the text fits is taken", () => {
    // teleport <destination>, teleport <location>, or teleport <targets>
    // and then either: after one entity, both ways are open.
    expect(complete(tree, "teleport @s ").hints.map((h) => h.name)).toEqual(["destination", "location"]);
    expect(complete(tree, "tp @s ").hints.map((h) => h.name)).toEqual(["destination", "location"]);
    // Coordinates are a location, where nothing comes after.
    expect(complete(tree, "teleport ~ ~ ~ ").hints).toEqual([]);
    const c = complete(tree, "teleport @s ~ ~ ~ ");
    expect(c.suggestions).toEqual(["facing", "~ ~"]);
    expect(c.hints).toEqual([{ name: "rotation", type: "yaw pitch", optional: true }]);
  });

  test("an argument after a command that runs without it is optional", () => {
    expect(complete(tree, "weather clear ").hints).toEqual([{ name: "duration", type: "time", optional: true }]);
  });
});

// The game's tree with the commands of a server with Yet Another Bingo, as
// the panel reads them from its help (tests/upstream/commands/help) and adds
// them after the game's own.
const bingo: CommandNode = JSON.parse(readFileSync(new URL("../tests/upstream/commands/help/bingo.tree.json", import.meta.url), "utf8"));
const modded: CommandNode = { ...tree, children: { ...bingo.children, ...tree.children } };

describe("commands of mods and plugins", () => {
  test("are offered with the game's own", () => {
    expect(complete(modded, "bin").suggestions).toEqual(["bingo", "bingodata", "bingoprefs", "bingostats"]);
    expect(complete(modded, "gl").suggestions).toEqual(["global"]);
    expect(complete(modded, "ga").suggestions).toEqual(["gamemode", "gamerule"]);
  });

  test("their subcommands and the values they choose from are offered", () => {
    expect(complete(modded, "bingo ").suggestions).toEqual(["book", "card", "difficulty", "filter", "goal", "lobby", "mode", "options", "reroll", "shuffleteams", "start", "timelimit"]);
    expect(complete(modded, "bingo difficulty ha").suggestions).toEqual(["hard"]);
    expect(complete(modded, "bingo difficulty ").suggestions).toEqual(["easy", "extreme", "hard", "impossible", "medium"]);
    expect(complete(modded, "bingo card stack ").suggestions).toEqual(["pop", "push"]);
    expect(complete(modded, "bingo options play_to ").suggestions).toEqual(["cards", "infinite_cards", "replace_goals"]);
  });

  test("their arguments are hinted by name, with no kind", () => {
    const c = complete(modded, "bingo timelimit ");
    expect(c.hints).toEqual([{ name: "minutes", type: "", optional: false }]);
    expect(c.suggestions).toEqual(["off"]);
    expect(complete(modded, "bingo shuffleteams ").hints).toEqual([{ name: "number_of_teams", type: "", optional: false }]);
    expect(ghostOf(complete(modded, "bingo shuffleteams "), undefined)).toBe("<number_of_teams>");
    // One the command runs without is optional.
    expect(complete(modded, "bingostats ").hints).toEqual([{ name: "player", type: "", optional: true }]);
  });

  test("any word goes for an argument of no known kind, and what follows it is offered", () => {
    expect(complete(modded, "bingo card set 3 r").suggestions).toEqual(["random"]);
    expect(complete(modded, "bingo difficulty 1 2 ").hints).toEqual([{ name: "B", type: "", optional: false }]);
    expect(complete(modded, "join red ").hints).toEqual([{ name: "player", type: "", optional: true }]);
  });

  test("past what is known, nothing is offered and nothing is wrong", () => {
    for (const line of ["g hello there ", "bingo reroll ", "bingo nonsense ", "bingostats Steve x"]) {
      const c = complete(modded, line);
      expect(c.suggestions).toEqual([]);
      expect(c.hints).toEqual([]);
    }
  });

  test("the game's own commands are as the game has them", () => {
    expect(complete(modded, "ti").suggestions).toEqual(["tick", "time", "title"]);
    expect(complete(modded, "give ").hints[0]).toEqual({ name: "targets", type: "players", optional: false });
  });
});

describe("ghost text", () => {
  test("is the rest of the suggestion chosen", () => {
    const c = complete(tree, "ti");
    expect(ghostOf(c, "time")).toBe("me");
    expect(ghostOf(c, "title")).toBe("tle");
  });

  test("keeps to the suggestion's case where the typed word's differs", () => {
    expect(ghostOf(complete(tree, "TI"), "time")).toBe("me");
  });

  test("is the argument expected when nothing is offered", () => {
    expect(ghostOf(complete(tree, "say "), undefined)).toBe("<message>");
    expect(ghostOf(complete(tree, "weather clear "), undefined)).toBe("[<duration>]");
    expect(ghostOf(complete(tree, "say hi"), undefined)).toBe("");
  });
});

describe("insert", () => {
  test("puts the suggestion in place of the word, and a space", () => {
    const text = "ti";
    expect(insert(text, 2, complete(tree, text), "time")).toEqual({ value: "time ", cursor: 5 });
  });

  test("keeps the slash and what comes before", () => {
    const text = "/execute run ti";
    expect(insert(text, text.length, complete(tree, text), "time")).toEqual({ value: "/execute run time ", cursor: 18 });
  });

  test("a position put in the middle takes the place of the one there, and keeps what is after it", () => {
    const text = "setblock ~ ~ ~ stone";
    const c = complete(tree, text, 10);
    expect(c.suggestions).toContain("~ ~ ~");
    expect(insert(text, 10, c, "~ ~ ~")).toEqual({ value: "setblock ~ ~ ~ stone", cursor: 15 });
    // Only coordinates are taken: what is not one stays.
    expect(insert("setblock ~ stone", 10, complete(tree, "setblock ~ stone", 10), "~ ~ ~")).toEqual({ value: "setblock ~ ~ ~ stone", cursor: 15 });
    expect(insert("setblock 1 -2.5 ^3 stone", 10, complete(tree, "setblock 1 -2.5 ^3 stone", 10), "~ ~ ~")).toEqual({
      value: "setblock ~ ~ ~ stone",
      cursor: 15,
    });
  });

  test("replaces the rest of the word at the cursor, and keeps what is after it", () => {
    const text = "time sxx day";
    const c = complete(tree, text, 6);
    expect(insert(text, 6, c, "set")).toEqual({ value: "time set day", cursor: 9 });
  });
});

describe("undo", () => {
  const at = (value: string) => ({ value, cursor: value.length });

  test("undoing a completion keeps what was typed", () => {
    let u = emptyUndo;
    u = record(u, at(""), "type", 0, "t");
    u = record(u, at("t"), "type", 100, "i");
    // Tab: "ti" becomes "time ".
    u = record(u, at("ti"), "complete", 200);
    const [u2, back] = undo(u, at("time "));
    expect(back).toEqual(at("ti"));
    // Typing is one step: undoing again empties the line.
    const [u3, first] = undo(u2, at("ti"));
    expect(first).toEqual(at(""));
    expect(undo(u3, at(""))[1]).toBeNull();
  });

  test("two completions are undone one at a time", () => {
    let u = emptyUndo;
    u = record(u, at(""), "type", 0, "t");
    u = record(u, at("t"), "complete", 10);
    u = record(u, at("tick "), "type", 20, "f");
    u = record(u, at("tick f"), "complete", 30);
    let back: ReturnType<typeof undo>[1];
    [u, back] = undo(u, at("tick freeze "));
    expect(back).toEqual(at("tick f"));
    [u, back] = undo(u, at("tick f"));
    expect(back).toEqual(at("tick "));
    [u, back] = undo(u, at("tick "));
    expect(back).toEqual(at("t"));
  });

  test("a space or a pause ends a step of typing", () => {
    let u = emptyUndo;
    u = record(u, at(""), "type", 0, "a");
    u = record(u, at("a"), "type", 10, " ");
    u = record(u, at("a "), "type", 20, "b");
    u = record(u, at("a b"), "type", 5000, "c");
    expect(u.past.map((e) => e.value)).toEqual(["", "a ", "a b"]);
  });

  test("moving the cursor ends a step of typing", () => {
    const edit = (value: string, cursor = value.length) => ({ value, cursor });
    let u = emptyUndo;
    u = record(u, edit("say hell"), "type", 0, "o", edit("say hello"));
    // Home, then x, at once.
    u = record(u, edit("say hello", 0), "type", 100, "x", edit("xsay hello", 1));
    expect(undo(u, edit("xsay hello", 1))[1]).toEqual(edit("say hello", 0));
    // Typing on where the last left off is the same step.
    let v = emptyUndo;
    v = record(v, edit("sa"), "type", 0, "y", edit("say"));
    v = record(v, edit("say"), "type", 100, "s", edit("says"));
    expect(v.past).toEqual([edit("sa")]);
  });

  test("redo does again what was undone, and a change forgets it", () => {
    let u = emptyUndo;
    u = record(u, at("ti"), "complete", 0);
    let e: ReturnType<typeof undo>[1];
    [u, e] = undo(u, at("time "));
    expect(e).toEqual(at("ti"));
    [u, e] = redo(u, at("ti"));
    expect(e).toEqual(at("time "));
    [u] = undo(u, at("time "));
    u = record(u, at("ti"), "type", 10, "x");
    expect(redo(u, at("tix"))[1]).toBeNull();
  });
});

// Where the game's own tree is not there yet, as when Mojang's site does not
// answer, the commands are those the server's help lists, the game's
// included, read as the panel reads a mod's: names and choices, arguments by
// name.
const fromHelp: CommandNode = {
  type: "root",
  partial: "later",
  children: {
    execute: {
      type: "literal",
      children: { run: { type: "literal" }, as: { type: "literal", children: { targets: { type: "argument" } } } },
    },
    experience: {
      type: "literal",
      children: { add: { type: "literal", executable: true }, query: { type: "literal", executable: true }, set: { type: "literal", executable: true } },
    },
    xp: { type: "literal", redirect: ["experience"] },
    give: { type: "literal", children: { targets: { type: "argument", children: { item: { type: "argument", executable: true } } } } },
    time: {
      type: "literal",
      children: { set: { type: "literal", executable: true }, add: { type: "literal", executable: true }, query: { type: "literal", executable: true } },
    },
    title: { type: "literal", children: { targets: { type: "argument" } } },
    ...bingo.children,
  },
};

describe("commands from the server's help alone", () => {
  test("the game's own are offered, by name and choices", () => {
    expect(complete(fromHelp, "ti").suggestions).toEqual(["time", "title"]);
    expect(complete(fromHelp, "time ").suggestions).toEqual(["add", "query", "set"]);
    expect(complete(fromHelp, "xp ").suggestions).toEqual(["add", "query", "set"]);
    expect(complete(fromHelp, "execute run ti").suggestions).toEqual(["time", "title"]);
    expect(complete(fromHelp, "give ").hints).toEqual([{ name: "targets", type: "", optional: false }]);
  });

  test("with those of the server's mods", () => {
    expect(complete(fromHelp, "bingo difficulty ").suggestions).toEqual(["easy", "extreme", "hard", "impossible", "medium"]);
  });

  test("are asked for again until the game's own are there", () => {
    expect(askAgainIn(fromHelp)).toBe(15_000);
    expect(askAgainIn({ ...fromHelp, partial: "making" })).toBe(3000);
    expect(askAgainIn({ making: true })).toBe(3000);
    expect(askAgainIn(tree)).toBe(false);
    expect(askAgainIn(undefined)).toBe(false);
  });
});
