import { expect, test } from "@playwright/test";
import { contrast, gameColours, parseMc, plainMc, readable, shownColours } from "../src/lib/mctext";

// The game's formatting codes, read as the game reads them. No page: these
// are of lib/mctext.ts alone, run once.
test.skip(({ browserName }) => browserName !== "chromium", "Not of a browser");

test("each colour code colours what follows, in either case", () => {
  for (const code of "0123456789abcdef") {
    expect(parseMc(`§${code}x`)).toEqual([{ text: "x", style: { colour: code } }]);
    expect(parseMc(`§${code.toUpperCase()}x`)).toEqual([{ text: "x", style: { colour: code } }]);
  }
});

test("each style code styles what follows, scrambled text shown as it is", () => {
  expect(parseMc("§lx")).toEqual([{ text: "x", style: { bold: true } }]);
  expect(parseMc("§ox")).toEqual([{ text: "x", style: { italic: true } }]);
  expect(parseMc("§nx")).toEqual([{ text: "x", style: { underline: true } }]);
  expect(parseMc("§mx")).toEqual([{ text: "x", style: { strike: true } }]);
  expect(parseMc("§kx")).toEqual([{ text: "x", style: { obfuscated: true } }]);
});

test("styles add up; a colour ends them, as §r ends everything", () => {
  expect(parseMc("§6§lDrehmal： §6§lAPOTHEOSIS")).toEqual([{ text: "Drehmal： APOTHEOSIS", style: { colour: "6", bold: true } }]);
  expect(parseMc("§l§obold italic§cred§rplain")).toEqual([
    { text: "bold italic", style: { bold: true, italic: true } },
    { text: "red", style: { colour: "c" } },
    { text: "plain", style: {} },
  ]);
  expect(parseMc("§n§m§lall§r")).toEqual([{ text: "all", style: { underline: true, strike: true, bold: true } }]);
  // A style after a colour keeps the colour.
  expect(parseMc("§9blue §lbold blue")).toEqual([
    { text: "blue ", style: { colour: "9" } },
    { text: "bold blue", style: { colour: "9", bold: true } },
  ]);
  expect(parseMc("plain §ebright")).toEqual([
    { text: "plain ", style: {} },
    { text: "bright", style: { colour: "e" } },
  ]);
});

test("codes that change nothing do not cut the text", () => {
  expect(parseMc("§l§la§lb")).toEqual([{ text: "ab", style: { bold: true } }]);
  expect(parseMc("§6a§6b")).toEqual([{ text: "ab", style: { colour: "6" } }]);
  expect(parseMc("a§rb")).toEqual([{ text: "ab", style: {} }]);
});

test("a § at the end, or before a letter that is no code, is dropped with what it takes", () => {
  expect(parseMc("name§")).toEqual([{ text: "name", style: {} }]);
  expect(parseMc("§")).toEqual([]);
  expect(parseMc("a§zb")).toEqual([{ text: "ab", style: {} }]);
  expect(parseMc("§6a§ §6b")).toEqual([{ text: "ab", style: { colour: "6" } }]);
  expect(parseMc("§6§")).toEqual([]);
  expect(plainMc("§§§6x")).toBe("x");
});

test("a server's own colour, §x and six digits, and §x without them as the game reads it", () => {
  expect(parseMc("§x§f§f§8§8§0§0orange§lbold")).toEqual([
    { text: "orange", style: { colour: "#ff8800" } },
    { text: "bold", style: { colour: "#ff8800", bold: true } },
  ]);
  expect(parseMc("§X§F§F§8§8§0§0x")).toEqual([{ text: "x", style: { colour: "#ff8800" } }]);
  // Short of six: x is no code, and each digit after it a colour of its own.
  expect(parseMc("§x§f§fx")).toEqual([{ text: "x", style: { colour: "f" } }]);
});

test("text without codes is as it was", () => {
  expect(plainMc("Island")).toBe("Island");
  expect(parseMc("Island")).toEqual([{ text: "Island", style: {} }]);
  expect(parseMc("")).toEqual([]);
  expect(plainMc("§6§lDrehmal： §6§lAPOTHEOSIS")).toBe("Drehmal： APOTHEOSIS");
});

// The panel's backgrounds that text sits on, in each theme (app.css).
const backgrounds = {
  dark: ["#0b0c0f", "#111215", "#18191c", "#1e2023"],
  light: ["#f5f6f8", "#ffffff", "#eef0f3", "#e5e8ec"],
};

test("every colour reads on every background of its theme, and keeps the game's order of light to dark", () => {
  const low: string[] = [];
  for (const theme of ["dark", "light"] as const) {
    for (const [code, colour] of Object.entries(shownColours[theme])) {
      for (const bg of backgrounds[theme]) {
        const ratio = contrast(colour, bg);
        if (ratio < 4.5) low.push(`${theme} §${code} ${colour} on ${bg}: ${ratio.toFixed(2)}`);
      }
    }
    // The greys stay in their order: black, dark grey, grey, white.
    const greys = ["0", "8", "7", "f"].map((c) => contrast(shownColours[theme][c], "#000000"));
    expect(greys).toEqual([...greys].sort((a, b) => a - b));
  }
  expect(low).toEqual([]);
  expect(Object.keys(shownColours.dark)).toEqual(Object.keys(gameColours));
  expect(Object.keys(shownColours.light)).toEqual(Object.keys(gameColours));
});

test("a server's own colours are moved to read too, the darkest and lightest included", () => {
  const low: string[] = [];
  const samples = ["#000000", "#ffffff", "#0000ff", "#ff0000", "#00ff00", "#ffff00", "#123456", "#ff8800", "#808080", "#010101", "#fefefe"];
  for (const theme of ["dark", "light"] as const) {
    for (const hex of samples) {
      const shown = readable(hex, theme);
      expect(shown).toMatch(/^#[0-9a-f]{6}$/);
      for (const bg of backgrounds[theme]) {
        const ratio = contrast(shown, bg);
        if (ratio < 4.5) low.push(`${theme} ${hex} as ${shown} on ${bg}: ${ratio.toFixed(2)}`);
      }
    }
  }
  expect(low).toEqual([]);
  // Its hue is kept: red stays red.
  expect(readable("#ff0000", "light")).toMatch(/^#[0-9a-f]{2}0000$/);
});
