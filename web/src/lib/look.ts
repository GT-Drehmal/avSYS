import { useSyncExternalStore } from "react";

/**
 * How the panel looks: light or dark, or as the system is, and the colour
 * that picks out what is live or primary. index.html paints this browser's
 * last look before anything is drawn; from then on it is kept here, and
 * matched to the account's once that is known.
 */

export type Theme = "auto" | "light" | "dark";
export type Accent = "amethyst" | "gold" | "teal" | "blue" | "rose";

export const themes: { id: Theme; name: string }[] = [
  { id: "auto", name: "Auto" },
  { id: "light", name: "Light" },
  { id: "dark", name: "Dark" },
];

/** In the order they are offered. app.css has their colours, and the API the same names. */
export const accents: { id: Accent; name: string }[] = [
  { id: "amethyst", name: "Amethyst" },
  { id: "gold", name: "Gold" },
  { id: "teal", name: "Teal" },
  { id: "blue", name: "Blue" },
  { id: "rose", name: "Rose" },
];

export const accentName = (id?: string) => accents.find((a) => a.id === id)?.name ?? accents[0].name;
const isTheme = (v: unknown): v is Theme => themes.some((t) => t.id === v);
const isAccent = (v: unknown): v is Accent => accents.some((a) => a.id === v);

// The same names as index.html reads.
const themeKey = "avsys.theme";
const accentKey = "avsys.accent";

function read(key: string) {
  try {
    return localStorage.getItem(key);
  } catch {
    // Storage turned off or full: the page still works, only unremembered.
    return null;
  }
}

function write(key: string, value: string) {
  try {
    localStorage.setItem(key, value);
  } catch {
    // As above.
  }
}

const light = typeof matchMedia === "function" ? matchMedia("(prefers-color-scheme: light)") : null;

export interface Look {
  /** As picked: "auto" follows the system. */
  theme: Theme;
  /** The one shown. */
  shown: "light" | "dark";
  /** The accent shown. */
  accent: Accent;
}

function settle(theme: Theme, accent: Accent): Look {
  return { theme, accent, shown: theme === "light" || (theme === "auto" && light?.matches) ? "light" : "dark" };
}

const stored = read(themeKey);
const storedAccent = read(accentKey);
let look = settle(isTheme(stored) ? stored : "auto", isAccent(storedAccent) ? storedAccent : "amethyst");
/** Whether this browser has an accent of its own, from before, or the panel's is to be shown until there is one. */
let accentKnown = isAccent(storedAccent);
const listeners = new Set<() => void>();

function paint() {
  const root = document.documentElement;
  root.dataset.theme = look.shown;
  root.dataset.accent = look.accent;
  document.querySelector('meta[name="color-scheme"]')?.setAttribute("content", look.shown);
  for (const l of listeners) l();
}

/** Shows the panel another way, and remembers it in this browser for the next time it is opened. */
export function setLook(next: { theme?: Theme; accent?: Accent }) {
  const theme = next.theme ?? look.theme;
  const accent = next.accent ?? look.accent;
  if (next.accent) accentKnown = true;
  if (theme === look.theme && accent === look.accent && settle(theme, accent).shown === look.shown) return;
  look = settle(theme, accent);
  write(themeKey, theme);
  write(accentKey, accent);
  paint();
}

/** The panel's accent, for a browser that has none of its own yet. */
export function panelAccent(accent: string) {
  if (!accentKnown && isAccent(accent)) setLook({ accent });
}

// The system's theme, followed live while the theme is Auto.
light?.addEventListener("change", () => {
  if (look.theme !== "auto") return;
  look = settle(look.theme, look.accent);
  paint();
});

paint();

const subscribe = (l: () => void) => {
  listeners.add(l);
  return () => listeners.delete(l);
};

/** How the panel looks now, as it changes. */
export const useLook = () => useSyncExternalStore(subscribe, () => look);

/** The look, outside React: for what draws its own colours, as the terminal. */
export const currentLook = () => look;
export const onLook = subscribe;
export { isAccent, isTheme };
