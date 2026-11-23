import { test as base, expect, type APIRequestContext, type BrowserContext, type Page } from "@playwright/test";
import { spawn, type ChildProcess } from "node:child_process";
import { createHmac, randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";

/** What the test panel says about itself once it is listening. */
interface Panel {
  url: string;
  password: string;
  tokens: Record<Role | "outsider", string>;
}

export type Role = "owner" | "admin" | "operator" | "viewer";

/**
 * Where the panel with a metrics API listens (see playwright.config.ts). A
 * test file that needs one says `test.use({ baseURL: withMetrics })`.
 */
export const withMetrics = "http://127.0.0.1:8098";

/**
 * Where the panel that copies snapshots offsite listens, with the older of
 * the first organization's snapshots there already.
 */
export const withOffsite = "http://127.0.0.1:8097";

/** Where the panel installed with a CurseForge key listens: CurseForge is offered there, answered from tests/upstream. */
export const withCurseForge = "http://127.0.0.1:8096";

const panels = new Map<string, Panel>();
/** The panel at baseURL: the plain one unless it is withMetrics or withOffsite. */
const info = (baseURL?: string) => {
  const file =
    baseURL === withMetrics
      ? "tests/.panel-metrics.json"
      : baseURL === withOffsite
        ? "tests/.panel-offsite.json"
        : baseURL === withCurseForge
          ? "tests/.panel-curseforge.json"
          : "tests/.panel.json";
  let panel = panels.get(file);
  if (!panel) {
    panel = JSON.parse(readFileSync(file, "utf8")) as Panel;
    panels.set(file, panel);
  }
  return panel;
};

interface Fixtures {
  /** Makes this page act as one of the seeded people. */
  as: (who: Role | "outsider") => Promise<void>;
  /** Calls the panel's API as someone, for setting a test up and checking what it did. */
  api: (who: Role | "outsider") => Api;
  /**
   * A new organization owned by "owner", with these servers in it, so that
   * a test that changes things has ones nobody else is looking at.
   */
  org: (servers: string[]) => Promise<string>;
  /** A page in another browser, as someone else: for what one sees of another. */
  pageAs: (who: Role | "outsider") => Promise<Page>;
  /**
   * Starts a panel of its own, with nobody in it yet, as one just installed,
   * and gives its address. It is stopped when the test ends. flags are
   * more of cmd/avsys-uitest's.
   */
  freshPanel: (...flags: string[]) => Promise<string>;
  /** Errors a page threw. A test fails on any, unless it empties this itself. */
  pageErrors: string[];
  password: string;
}

export interface Api {
  get: <T = any>(path: string) => Promise<T>;
  post: <T = any>(path: string, body?: unknown) => Promise<T>;
  patch: <T = any>(path: string, body: unknown) => Promise<T>;
}

function apiFor(request: APIRequestContext, token: string): Api {
  const headers = { Authorization: `Bearer ${token}`, "X-Avsys": "1" };
  const call = async (method: string, path: string, body?: unknown) => {
    const res = await request.fetch(`/api/v1${path}`, { method, headers, data: body });
    if (!res.ok()) throw new Error(`${method} ${path}: ${res.status()} ${await res.text()}`);
    return res.status() === 204 ? undefined : res.json();
  };
  return { get: (p) => call("GET", p), post: (p, b) => call("POST", p, b ?? {}), patch: (p, b) => call("PATCH", p, b) };
}

/** Adds each error a page throws to errors. */
export function watchErrors(page: Page, errors: string[]) {
  page.on("pageerror", (e) => {
    // WebKit reports a request that was cut short by leaving the page
    // (the live-updates stream, on signing in) as an error of the page.
    // Nothing was thrown by the app.
    if (/due to access control checks/.test(e.message)) return;
    errors.push(e.message);
  });
}

export const test = base.extend<Fixtures>({
  password: async ({ baseURL }, use) => use(info(baseURL).password),

  // Every test gets this whether it asks or not: an error thrown in the page
  // is a failure even when everything the test looked at was fine. A blank
  // page is exactly that.
  pageErrors: [
    async ({ page }, use) => {
      const errors: string[] = [];
      watchErrors(page, errors);
      await use(errors);
      expect(errors, "errors thrown in the page").toEqual([]);
    },
    { auto: true },
  ],

  // A request a test answers itself, from what the panel answers, has
  // nothing left to answer once the page has gone on, as by reloading: the
  // panel's answer goes with it. That is not the test failing; anything
  // else a handler throws still is.
  page: async ({ page }, use) => {
    const route = page.route.bind(page);
    page.route = ((url, handler, options) =>
      route(
        url,
        async (r, request) => {
          try {
            await handler(r, request);
          } catch (err) {
            if (!/Response has been disposed|has been closed|Route is already handled/.test(String(err))) throw err;
          }
        },
        options,
      )) as Page["route"];
    await use(page);
  },

  as: async ({ context, baseURL }, use) => {
    await use(async (who) => {
      await context.setExtraHTTPHeaders({ Authorization: `Bearer ${info(baseURL).tokens[who]}` });
    });
  },

  pageAs: async ({ browser, baseURL }, use) => {
    const contexts: BrowserContext[] = [];
    await use(async (who) => {
      const context = await browser.newContext({ baseURL, extraHTTPHeaders: { Authorization: `Bearer ${info(baseURL).tokens[who]}` } });
      contexts.push(context);
      return context.newPage();
    });
    for (const c of contexts) await c.close();
  },

  api: async ({ request, baseURL }, use) => {
    await use((who) => apiFor(request, info(baseURL).tokens[who]));
  },

  freshPanel: [
    // It needs no other fixture: Playwright reads that from the empty pattern.
    async ({}, use) => {
      const started: ChildProcess[] = [];
      await use(async (...flags) => {
        // As playwright.config.ts starts the other panels.
        const [command, ...args] = (process.env.AVSYS_UITEST ?? "go run ../cmd/avsys-uitest").split(" ");
        const panel = spawn(command, [...args, "--fresh", "--listen", "127.0.0.1:0", "--web-dir", "dist", "--upstream", "tests/upstream", ...flags], {
          // A process group of its own, so that stopping it stops what `go run` started too.
          detached: true,
          stdio: ["ignore", "pipe", "inherit"],
        });
        started.push(panel);
        return new Promise<string>((resolve, reject) => {
          let out = "";
          panel.stdout?.on("data", (chunk: Buffer) => {
            out += chunk.toString();
            if (out.includes("\n")) resolve((JSON.parse(out.split("\n")[0]) as Panel).url);
          });
          panel.on("exit", (code) => reject(new Error(`the fresh panel exited (${code}) before it was listening`)));
        });
      });
      for (const panel of started) {
        if (panel.pid && panel.exitCode === null) process.kill(-panel.pid, "SIGTERM");
      }
    },
    // Compiling it, where it is not built already, takes a while.
    { timeout: 180_000 },
  ],

  org: async ({ request, baseURL }, use, testInfo) => {
    await use(async (servers) => {
      const owner = apiFor(request, info(baseURL).tokens.owner);
      // Tests run side by side in several processes, so a counter would not keep these apart.
      const slug = `t-${testInfo.project.name.slice(0, 2)}-${randomUUID().slice(0, 12)}`;
      await owner.post("/orgs", { name: `Test ${slug}`, slug });
      for (const name of servers) {
        await owner.post(`/orgs/${slug}/servers`, {
          name,
          spec: { eula: true, loader: "paper", version: "1.21.4", state: "Stopped", resources: { memory: "2Gi" }, storage: { size: "2Gi" } },
        });
      }
      return slug;
    });
  },
});

export { expect };

/** The code an authenticator app shows for a base32 secret (RFC 6238: SHA-1, six digits, 30 seconds). */
export function totp(secret: string, at = Date.now()) {
  const alphabet = "ABCDEFGHIJKLMNOPQRSTUVWXYZ234567";
  let bits = "";
  for (const c of secret.replace(/=+$/, "")) bits += alphabet.indexOf(c).toString(2).padStart(5, "0");
  const key = Buffer.from(bits.match(/.{8}/g)!.map((b) => parseInt(b, 2)));
  const counter = Buffer.alloc(8);
  counter.writeBigUInt64BE(BigInt(Math.floor(at / 1000 / 30)));
  const mac = createHmac("sha1", key).update(counter).digest();
  const offset = mac[mac.length - 1] & 0xf;
  return String((mac.readUInt32BE(offset) & 0x7fffffff) % 1_000_000).padStart(6, "0");
}

/**
 * Sets two-factor up for whoever request is signed in as, with their
 * password ("" for one with none, just signed in), and gives its secret.
 */
export async function turnOnTwoFactor(request: APIRequestContext, url: string, password: string) {
  const headers = { "X-Avsys": "1" };
  const setup = await request.post(`${url}/api/v1/me/two-factor/setup`, { headers, data: { password } });
  expect(setup.ok(), await setup.text()).toBe(true);
  const { secret } = (await setup.json()) as { secret: string };
  const done = await request.post(`${url}/api/v1/me/two-factor`, { headers, data: { code: totp(secret) } });
  expect(done.ok(), await done.text()).toBe(true);
  return secret;
}

/**
 * On a panel just installed, where admins and owners have to use
 * two-factor, has nobody have to, for a test of something else: its
 * administrator, signed in with request, sets it up, says nobody has to,
 * and turns it off.
 */
export async function noTwoFactor(request: APIRequestContext, url: string, password: string) {
  const headers = { "X-Avsys": "1" };
  await turnOnTwoFactor(request, url, password);
  const level = await request.put(`${url}/api/v1/settings/two-factor`, { headers, data: { level: "nobody" } });
  expect(level.ok(), await level.text()).toBe(true);
  const off = await request.post(`${url}/api/v1/me/two-factor/off`, { headers, data: { password } });
  expect(off.ok(), await off.text()).toBe(true);
}

/** The main part of the page, without the navigation beside it. */
export const content = (page: Page) => page.locator("main");
