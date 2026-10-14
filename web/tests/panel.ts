import { test as base, expect, type APIRequestContext, type Page } from "@playwright/test";
import { randomUUID } from "node:crypto";
import { readFileSync } from "node:fs";

/** What the test panel says about itself once it is listening. */
interface Panel {
  url: string;
  password: string;
  tokens: Record<Role | "outsider", string>;
}

export type Role = "owner" | "admin" | "operator" | "viewer";

let panel: Panel | undefined;
const info = () => (panel ??= JSON.parse(readFileSync("tests/.panel.json", "utf8")) as Panel);

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

export const test = base.extend<Fixtures>({
  password: async ({}, use) => use(info().password),

  // Every test gets this whether it asks or not: an error thrown in the page
  // is a failure even when everything the test looked at was fine. A blank
  // page is exactly that.
  pageErrors: [
    async ({ page }, use) => {
      const errors: string[] = [];
      page.on("pageerror", (e) => {
        // WebKit reports a request that was cut short by leaving the page
        // (the live-updates stream, on signing in) as an error of the page.
        // Nothing was thrown by the app.
        if (/due to access control checks/.test(e.message)) return;
        errors.push(e.message);
      });
      await use(errors);
      expect(errors, "errors thrown in the page").toEqual([]);
    },
    { auto: true },
  ],

  as: async ({ context }, use) => {
    await use(async (who) => {
      await context.setExtraHTTPHeaders({ Authorization: `Bearer ${info().tokens[who]}` });
    });
  },

  api: async ({ request }, use) => {
    await use((who) => apiFor(request, info().tokens[who]));
  },

  org: async ({ request }, use, testInfo) => {
    await use(async (servers) => {
      const owner = apiFor(request, info().tokens.owner);
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

/** The main part of the page, without the navigation beside it. */
export const content = (page: Page) => page.locator("main");
