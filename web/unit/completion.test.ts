// The console's command completion, against the command tree the game
// wrote out for 1.21.4 (tests/upstream/commands). Run with `bun test unit`.
import { describe, expect, test } from "bun:test";
import { readFileSync } from "node:fs";
import { gunzipSync } from "node:zlib";
import { type CommandNode, complete, emptyUndo, ghostOf, insert, record, redo, undo } from "../src/lib/completion";

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
