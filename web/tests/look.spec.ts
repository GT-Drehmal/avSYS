import type { BrowserContext, Page } from "@playwright/test";
import { expect, test } from "./panel";

// How the panel looks: light, dark, or as the system is (Auto), and an
// accent picked from a few. Each person's is kept by their account; before
// signing in, a browser shows the last it had. The panel's administrator
// picks the accent of everyone who has not picked one.
//
// Anything that changes an account's look is done on a fresh panel, so
// that the seeded people the other tests use stay as they are.

const accents = ["amethyst", "gold", "teal", "blue", "rose"] as const;
type Accent = (typeof accents)[number];

/** Each accent as app.css has it, in each theme. */
const accentColours: Record<Accent, { dark: string; light: string }> = {
  amethyst: { dark: "#b98cff", light: "#7340e0" },
  gold: { dark: "#e6c15c", light: "#7a6500" },
  teal: { dark: "#5fe0c6", light: "#087363" },
  blue: { dark: "#7aaaff", light: "#225dcc" },
  rose: { dark: "#f58cc0", light: "#b52b72" },
};

const rgb = (hex: string) => {
  const [r, g, b] = [1, 3, 5].map((i) => parseInt(hex.slice(i, i + 2), 16));
  return `rgb(${r}, ${g}, ${b})`;
};

/** A colour of the page's, as the browser has worked it out: "rgb(…)". */
const colour = (page: Page, token: string) =>
  page.evaluate((name) => {
    const probe = document.createElement("span");
    probe.style.color = `var(--color-${name})`;
    document.body.append(probe);
    const got = getComputedStyle(probe).color;
    probe.remove();
    return got;
  }, token);

const pageBackground = (page: Page) => page.evaluate(() => getComputedStyle(document.documentElement).backgroundColor);
const lightBg = "rgb(245, 246, 248)";
const darkBg = "rgb(11, 12, 15)";

async function expectTheme(page: Page, shown: "light" | "dark") {
  await expect(page.locator("html")).toHaveAttribute("data-theme", shown);
  await expect.poll(() => pageBackground(page)).toBe(shown === "light" ? lightBg : darkBg);
  await expect(page.locator('meta[name="color-scheme"]')).toHaveAttribute("content", shown);
}

async function expectAccent(page: Page, accent: Accent, shown: "light" | "dark") {
  await expect(page.locator("html")).toHaveAttribute("data-accent", accent);
  await expect.poll(() => colour(page, "accent")).toBe(rgb(accentColours[accent][shown]));
}

const password = "a password long enough";

/** The first account on a fresh panel, its administrator, signed in in this page's browser. */
async function founder(page: Page, url: string) {
  const res = await page.request.post(`${url}/api/v1/auth/register`, { headers: { "X-Avsys": "1" }, data: { username: "founder", password } });
  expect(res.ok(), await res.text()).toBe(true);
}

/** Signs a browser in with a password. */
async function signIn(context: BrowserContext, url: string, username: string) {
  const res = await context.request.post(`${url}/api/v1/auth/login`, { headers: { "X-Avsys": "1" }, data: { username, password } });
  expect(res.ok(), await res.text()).toBe(true);
}

const lookOf = async (page: Page, url: string) => (await (await page.request.get(`${url}/api/v1/me`)).json()).look;

test("Auto follows the system, and changes with it", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.goto("/login");
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await expectTheme(page, "light");
  await page.emulateMedia({ colorScheme: "dark" });
  await expectTheme(page, "dark");
  await page.emulateMedia({ colorScheme: "light" });
  await expectTheme(page, "light");
});

/** Opens a page with the app's scripts held back, so that only what index.html does has happened. */
async function firstPaint(page: Page, path: string) {
  let release = () => {};
  const held = new Promise<void>((resolve) => {
    release = resolve;
  });
  await page.route(/\/assets\/.*\.js$/, async (route) => {
    await held;
    await route.continue();
  });
  await page.goto(path, { waitUntil: "commit" });
  return release;
}

test("the first paint is already in the theme to be shown", async ({ page }) => {
  // This browser last had Light, on a system that is dark.
  await page.addInitScript(() => localStorage.setItem("avsys.theme", "light"));
  const release = await firstPaint(page, "/login");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  await expect.poll(() => pageBackground(page)).toBe(lightBg);
  await expect(page.locator("#root")).toBeEmpty();
  release();
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await expectTheme(page, "light");
});

test("the first paint on Auto is the system's", async ({ page }) => {
  await page.emulateMedia({ colorScheme: "light" });
  await page.addInitScript(() => localStorage.setItem("avsys.accent", "teal"));
  const release = await firstPaint(page, "/login");
  await expect(page.locator("html")).toHaveAttribute("data-theme", "light");
  await expect(page.locator("html")).toHaveAttribute("data-accent", "teal");
  await expect.poll(() => pageBackground(page)).toBe(lightBg);
  release();
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  // Before signing in, the browser's own accent stands over the panel's.
  await expectAccent(page, "teal", "light");
});

test("a theme picked is kept by the account, and shown in another browser", async ({ page, browser, freshPanel, pageErrors }) => {
  const url = await freshPanel();
  await founder(page, url);
  await page.goto(`${url}/account`);
  const themes = page.getByRole("radiogroup", { name: "Theme" });
  await expect(themes.getByRole("radio", { name: "Auto" })).toHaveAttribute("aria-checked", "true");
  await expectTheme(page, "dark");

  // Light on a dark system, and it stays when the system changes.
  await themes.getByRole("radio", { name: "Light" }).click();
  await expectTheme(page, "light");
  await page.emulateMedia({ colorScheme: "light" });
  await themes.getByRole("radio", { name: "Dark" }).click();
  await expectTheme(page, "dark");
  await page.emulateMedia({ colorScheme: "dark" });
  await page.emulateMedia({ colorScheme: "light" });
  await expectTheme(page, "dark");
  await expect.poll(() => lookOf(page, url)).toEqual({ theme: "dark", accent: "" });
  await page.reload();
  await expect(themes.getByRole("radio", { name: "Dark" })).toHaveAttribute("aria-checked", "true");
  await expectTheme(page, "dark");

  // Another browser, on a light system, that has never been here: the account's.
  const other = await browser.newContext({ colorScheme: "light" });
  const elsewhere = await other.newPage();
  elsewhere.on("pageerror", (e) => pageErrors.push(e.message));
  await signIn(other, url, "founder");
  await elsewhere.goto(`${url}/account`);
  await expect(elsewhere.getByRole("radiogroup", { name: "Theme" }).getByRole("radio", { name: "Dark" })).toHaveAttribute("aria-checked", "true");
  await expectTheme(elsewhere, "dark");
  // And there, from then on, from the first paint.
  const release = await firstPaint(elsewhere, `${url}/account`);
  await expect(elsewhere.locator("html")).toHaveAttribute("data-theme", "dark");
  release();
  await other.close();

  // Back to Auto: the system's again.
  await themes.getByRole("radio", { name: "Auto" }).click();
  await expectTheme(page, "light");
  await expect.poll(() => lookOf(page, url)).toEqual({ theme: "auto", accent: "" });
});

test("each accent sets its own colours, in both themes", async ({ page, freshPanel }) => {
  const url = await freshPanel();
  await founder(page, url);
  await page.goto(`${url}/account`);
  const picker = page.getByRole("radiogroup", { name: "Accent" });
  await expect(picker.getByRole("radio", { name: "Default" })).toHaveAttribute("aria-checked", "true");
  await expectAccent(page, "amethyst", "dark");
  const defaultWarn = await colour(page, "warn");
  for (const shown of ["dark", "light"] as const) {
    await page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio", { name: shown === "light" ? "Light" : "Dark" }).click();
    for (const accent of accents) {
      await picker.getByRole("radio", { name: accent[0].toUpperCase() + accent.slice(1), exact: true }).click();
      await expectAccent(page, accent, shown);
      // The button that says what is primary is in it.
      await expect.poll(() => colour(page, "accent-ink")).not.toBe(await colour(page, "accent"));
    }
  }
  // Gold moves warnings out of its way.
  await page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio", { name: "Dark" }).click();
  await picker.getByRole("radio", { name: "Gold" }).click();
  await expect.poll(() => colour(page, "warn")).not.toBe(defaultWarn);
  await expect.poll(() => lookOf(page, url)).toEqual({ theme: "dark", accent: "gold" });
  await page.reload();
  await expectAccent(page, "gold", "dark");
});

test("theme and accent change from the account menu", async ({ page, freshPanel }) => {
  const url = await freshPanel();
  await founder(page, url);
  await page.goto(`${url}/account`);
  const menu = page.locator("aside").getByRole("button", { name: /founder/ });

  await menu.click();
  await page.getByRole("menuitem", { name: /Theme/ }).click();
  await page.getByRole("menuitemradio", { name: "Light" }).click();
  await expectTheme(page, "light");

  await menu.click();
  await page.getByRole("menuitem", { name: /Accent/ }).click();
  await expect(page.getByRole("menuitemradio", { name: /Default/ })).toHaveAttribute("aria-checked", "true");
  await page.getByRole("menuitemradio", { name: "Teal" }).click();
  await expectAccent(page, "teal", "light");
  await expect.poll(() => lookOf(page, url)).toEqual({ theme: "light", accent: "teal" });
  // The Account page says the same.
  await expect(page.getByRole("radiogroup", { name: "Theme" }).getByRole("radio", { name: "Light" })).toHaveAttribute("aria-checked", "true");
  await expect(page.getByRole("radiogroup", { name: "Accent" }).getByRole("radio", { name: "Teal" })).toHaveAttribute("aria-checked", "true");

  // The mark on a light page is ink and amethyst, whatever the accent.
  const fills = await page.locator("aside").getByRole("img", { name: "avSYS" }).locator("path").evaluateAll((paths) => paths.slice(0, 2).map((p) => getComputedStyle(p).fill));
  expect(fills).toEqual(["rgb(27, 36, 39)", "rgb(122, 69, 234)"]);
});

test("the panel's accent is that of everyone who has not picked one", async ({ page, browser, freshPanel, pageErrors }) => {
  const url = await freshPanel();
  await founder(page, url);
  await page.goto(`${url}/settings`);
  const panelAccent = page.getByRole("region", { name: "Accent" });
  await expect(panelAccent.getByRole("radio", { name: "Amethyst" })).toHaveAttribute("aria-checked", "true");
  await panelAccent.getByRole("radio", { name: "Gold" }).click();
  // The administrator has not picked one of their own.
  await expectAccent(page, "gold", "dark");

  // Someone invited, who has not picked one.
  const headers = { "X-Avsys": "1" };
  expect((await page.request.post(`${url}/api/v1/orgs`, { headers, data: { name: "Home", slug: "home" } })).ok()).toBe(true);
  const invite = await (await page.request.post(`${url}/api/v1/orgs/home/invites`, { headers, data: { role: "viewer" } })).json();
  const theirs = await browser.newContext();
  const them = await theirs.newPage();
  them.on("pageerror", (e) => pageErrors.push(e.message));
  const joined = await theirs.request.post(`${url}/api/v1/auth/register`, { headers, data: { username: "guest", password, invite: invite.token } });
  expect(joined.ok(), await joined.text()).toBe(true);
  await them.goto(`${url}/account`);
  await expect(them.getByRole("radiogroup", { name: "Accent" }).getByRole("radio", { name: "Default" })).toHaveAttribute("aria-checked", "true");
  await expectAccent(them, "gold", "dark");

  // A browser that has never been here shows it before signing in.
  const stranger = await browser.newContext();
  const outside = await stranger.newPage();
  await outside.goto(`${url}/login`);
  await expect(outside.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await expectAccent(outside, "gold", "dark");
  await stranger.close();

  // One they pick is theirs, whatever the panel's becomes.
  await them.getByRole("radiogroup", { name: "Accent" }).getByRole("radio", { name: "Teal" }).click();
  await expectAccent(them, "teal", "dark");
  await panelAccent.getByRole("radio", { name: "Blue" }).click();
  await expectAccent(page, "blue", "dark");
  await them.reload();
  await expectAccent(them, "teal", "dark");
  await theirs.close();
});

// Every colour of text on what it is put on reads at WCAG AA (4.5:1), in
// each theme with each accent, as the browser works the stylesheet out.
// No status colour is near the accent's hue, so that neither is taken for
// the other.

const luminance = ([r, g, b]: number[]) => {
  const lin = (c: number) => (c / 255 <= 0.04045 ? c / 255 / 12.92 : ((c / 255 + 0.055) / 1.055) ** 2.4);
  return 0.2126 * lin(r) + 0.7152 * lin(g) + 0.0722 * lin(b);
};
const contrast = (a: number[], b: number[]) => {
  const [x, y] = [luminance(a), luminance(b)];
  return (Math.max(x, y) + 0.05) / (Math.min(x, y) + 0.05);
};
/** OKLCH hue, in degrees. */
const hue = ([r, g, b]: number[]) => {
  const lin = (c: number) => (c / 255 <= 0.04045 ? c / 255 / 12.92 : ((c / 255 + 0.055) / 1.055) ** 2.4);
  const [R, G, B] = [lin(r), lin(g), lin(b)];
  const l = Math.cbrt(0.4122214708 * R + 0.5363325363 * G + 0.0514459929 * B);
  const m = Math.cbrt(0.2119034982 * R + 0.6806995451 * G + 0.1073969566 * B);
  const s = Math.cbrt(0.0883024619 * R + 0.2817188376 * G + 0.6299787005 * B);
  const A = 1.9779984951 * l - 2.428592205 * m + 0.4505937099 * s;
  const Bb = 0.0259040371 * l + 0.7827717662 * m - 0.808675766 * s;
  return ((Math.atan2(Bb, A) * 180) / Math.PI + 360) % 360;
};
const apart = (a: number, b: number) => {
  const d = Math.abs(a - b) % 360;
  return Math.min(d, 360 - d);
};

const tokens = ["bg", "surface", "raised", "hover", "fg", "muted", "accent", "accent-strong", "accent-ink", "accent-wash", "warn", "warn-wash", "danger", "danger-wash", "dormant", "dormant-wash"];
/** Text, and what it is put on. */
const pairs: [string, string][] = [
  ...["bg", "surface", "raised", "hover"].map((on) => ["fg", on] as [string, string]),
  ...["bg", "surface", "raised"].map((on) => ["muted", on] as [string, string]),
  ...["bg", "surface", "raised", "hover", "accent-wash"].map((on) => ["accent", on] as [string, string]),
  ["accent-strong", "bg"],
  ["accent-ink", "accent"],
  ["accent-ink", "accent-strong"],
  ...["warn", "danger", "dormant"].flatMap((status) => ["bg", "surface", "raised", `${status}-wash`].map((on) => [status, on] as [string, string])),
];

test("every accent reads in both themes, and is not taken for a status", async ({ page }) => {
  await page.goto("/login");
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  // All at once, so that nothing the page does in between sets them back.
  const all = await page.evaluate(
    ({ accents, tokens }) => {
      const root = document.documentElement;
      const was = { theme: root.dataset.theme, accent: root.dataset.accent };
      const probe = document.createElement("span");
      document.body.append(probe);
      const out: { shown: string; accent: string; got: Record<string, number[]> }[] = [];
      for (const shown of ["dark", "light"]) {
        for (const accent of accents) {
          root.dataset.theme = shown;
          root.dataset.accent = accent;
          const got: Record<string, number[]> = {};
          for (const t of tokens) {
            probe.style.color = `var(--color-${t})`;
            got[t] = (getComputedStyle(probe).color.match(/[\d.]+/g) ?? []).slice(0, 3).map(Number);
          }
          out.push({ shown, accent, got });
        }
      }
      probe.remove();
      Object.assign(root.dataset, was);
      return out;
    },
    { accents: [...accents], tokens },
  );
  expect(all).toHaveLength(accents.length * 2);
  const low: string[] = [];
  for (const { shown, accent: name, got } of all) {
    const accent = name as Accent;
    expect(got.accent, `${accent} ${shown}`).toEqual(rgb(accentColours[accent][shown as "dark" | "light"]).match(/\d+/g)!.map(Number));
    for (const [text, on] of pairs) {
      const ratio = contrast(got[text], got[on]);
      if (ratio < 4.5) low.push(`${accent} ${shown}: ${text} on ${on} is ${ratio.toFixed(2)}`);
    }
    for (const status of ["warn", "danger", "dormant"]) {
      const d = apart(hue(got.accent), hue(got[status]));
      if (d < 35) low.push(`${accent} ${shown}: ${status} is ${d.toFixed(0)}° from the accent`);
    }
    // Nor are the statuses taken for each other.
    for (const [a, b] of [["warn", "danger"], ["danger", "dormant"], ["warn", "dormant"]]) {
      const d = apart(hue(got[a]), hue(got[b]));
      if (d < 30) low.push(`${accent} ${shown}: ${a} is ${d.toFixed(0)}° from ${b}`);
    }
  }
  expect(low).toEqual([]);
});
