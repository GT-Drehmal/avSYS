// Text the game writes with its formatting codes: § and a letter after it.
// A digit or a to f is one of its 16 colours, k to o a style (k scrambled,
// l bold, m struck through, n underlined, o italic), r back to none. A
// colour ends the styles before it, as in the game. Servers that take
// colours of their own write one as §x and six §-digits: §x§f§f§a§a§0§0.

/** One of the game's 16 colours, by its code, or a colour of a server's own, as #rrggbb. */
export type McColour = string;

export interface McStyle {
  colour?: McColour;
  bold?: boolean;
  italic?: boolean;
  underline?: boolean;
  strike?: boolean;
  /** Scrambled in the game; shown as it is. */
  obfuscated?: boolean;
}

export interface McRun {
  text: string;
  style: McStyle;
}

const colours = "0123456789abcdef";
const styles: Record<string, keyof Omit<McStyle, "colour">> = { k: "obfuscated", l: "bold", m: "strike", n: "underline", o: "italic" };

/** Whether text has any of the game's codes in it. */
export const hasMcCodes = (text: string) => text.includes("§");

/**
 * Text cut where its style changes, with the codes taken out. A § with
 * nothing after it is dropped, and one before a letter that is no code is
 * dropped with the letter, as the game does.
 */
export function parseMc(text: string): McRun[] {
  const runs: McRun[] = [];
  let style: McStyle = {};
  let at = "";
  const flush = () => {
    if (!at) return;
    const last = runs[runs.length - 1];
    if (last && same(last.style, style)) last.text += at;
    else runs.push({ text: at, style });
    at = "";
  };
  for (let i = 0; i < text.length; i++) {
    const c = text[i];
    if (c !== "§") {
      at += c;
      continue;
    }
    if (i + 1 >= text.length) break;
    const code = text[i + 1].toLowerCase();
    i++;
    if (code === "x") {
      const hex = hexAt(text, i + 1);
      if (hex) {
        flush();
        style = { colour: `#${hex}` };
        i += 12;
      }
      continue;
    }
    if (colours.includes(code)) {
      flush();
      style = { colour: code };
    } else if (code === "r") {
      flush();
      style = {};
    } else if (styles[code] && !style[styles[code]]) {
      flush();
      style = { ...style, [styles[code]]: true };
    }
  }
  flush();
  return runs;
}

/** Six § and a hex digit each, from i on: a server's own colour after §x. */
function hexAt(text: string, i: number): string | null {
  let hex = "";
  for (let k = 0; k < 6; k++) {
    const mark = text[i + 2 * k];
    const digit = text[i + 2 * k + 1]?.toLowerCase();
    if (mark !== "§" || !digit || !colours.includes(digit)) return null;
    hex += digit;
  }
  return hex;
}

const same = (a: McStyle, b: McStyle) =>
  a.colour === b.colour && !a.bold === !b.bold && !a.italic === !b.italic && !a.underline === !b.underline && !a.strike === !b.strike && !a.obfuscated === !b.obfuscated;

/** Text without the game's codes. */
export const plainMc = (text: string) => (hasMcCodes(text) ? parseMc(text).map((r) => r.text).join("") : text);

// Colours: each of the game's, moved to be read on the panel's own
// backgrounds in each theme, light and dark, while keeping its hue and its
// order among the others, lightest to darkest. app.css has them as
// --mc-0 to --mc-f; these are the same, for tests and for the colours
// servers pick for themselves.

/** The game's own colours, by code. */
export const gameColours: Record<string, string> = {
  "0": "#000000",
  "1": "#0000aa",
  "2": "#00aa00",
  "3": "#00aaaa",
  "4": "#aa0000",
  "5": "#aa00aa",
  "6": "#ffaa00",
  "7": "#aaaaaa",
  "8": "#555555",
  "9": "#5555ff",
  a: "#55ff55",
  b: "#55ffff",
  c: "#ff5555",
  d: "#ff55ff",
  e: "#ffff55",
  f: "#ffffff",
};

/** The colours as the panel shows them, by theme: as app.css has them. */
export const shownColours: Record<"dark" | "light", Record<string, string>> = {
  dark: {
    "0": "#898989",
    "1": "#568afe",
    "2": "#46d040",
    "3": "#45cccb",
    "4": "#ff6654",
    "5": "#f261ef",
    "6": "#fec579",
    "7": "#c4c4c4",
    "8": "#a2a2a2",
    "9": "#8d9dfd",
    a: "#9bff96",
    b: "#9afffe",
    c: "#ff9a93",
    d: "#ff9afc",
    e: "#fdffa6",
    f: "#ffffff",
  },
  light: {
    "0": "#000000",
    "1": "#001883",
    "2": "#015a01",
    "3": "#025757",
    "4": "#770100",
    "5": "#6f036f",
    "6": "#784e03",
    "7": "#535353",
    "8": "#393939",
    "9": "#3511c8",
    a: "#06700d",
    b: "#026d6e",
    c: "#9c0319",
    d: "#8f0491",
    e: "#696903",
    f: "#676767",
  },
};

const toLinear = (c: number) => (c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4);
const fromLinear = (c: number) => (c <= 0.0031308 ? 12.92 * c : 1.055 * c ** (1 / 2.4) - 0.055);
const channels = (hex: string) => [1, 3, 5].map((i) => toLinear(parseInt(hex.slice(i, i + 2), 16) / 255));
const toHex = (linear: number[]) => `#${linear.map((c) => Math.round(fromLinear(Math.min(1, Math.max(0, c))) * 255).toString(16).padStart(2, "0")).join("")}`;

/** How light a colour is to the eye, 0 to 1: WCAG's relative luminance. */
export const luminance = (hex: string) => {
  const [r, g, b] = channels(hex);
  return 0.2126 * r + 0.7152 * g + 0.0722 * b;
};

/** WCAG's contrast between two colours, 1 to 21. */
export const contrast = (a: string, b: string) => {
  const [x, y] = [luminance(a), luminance(b)];
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
};

/**
 * A server's own colour, moved as the game's are: on dark, a quarter of
 * the way to white in light (as the eye has it, so dark ones lift the
 * most); on light, darkened to at most what reads on the light greys.
 */
export function readable(hex: string, theme: "dark" | "light"): string {
  const c = channels(hex);
  if (theme === "dark") return toHex(c.map((v) => v + 0.25 * (1 - v)));
  const y = luminance(hex);
  if (y === 0) return toHex(c);
  const scale = (0.135 * Math.sqrt(y)) / y;
  return toHex(c.map((v) => v * scale));
}
