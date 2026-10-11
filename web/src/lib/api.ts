// Types and a small fetch wrapper for the panel's REST API. Spec and status
// mirror the MinecraftServer resource, so the same shapes work with kubectl.

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}

const base = "/api/v1";

async function request<T>(method: string, path: string, body?: unknown, raw?: BodyInit): Promise<T> {
  const headers: Record<string, string> = { "X-Avsys": "1" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  let res: Response;
  try {
    res = await fetch(base + path, {
      method,
      headers,
      body: raw ?? (body !== undefined ? JSON.stringify(body) : undefined),
    });
  } catch {
    throw new ApiError(0, "network", "Could not reach the panel. Check your connection.");
  }
  if (res.status === 204) return undefined as T;
  const isJSON = res.headers.get("Content-Type")?.includes("application/json");
  if (!res.ok) {
    const err = isJSON ? await res.json() : null;
    throw new ApiError(res.status, err?.code ?? "error", err?.message ?? `Request failed (${res.status}).`);
  }
  return (isJSON ? res.json() : res.text()) as Promise<T>;
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  post: <T>(path: string, body?: unknown) => request<T>("POST", path, body ?? {}),
  patch: <T>(path: string, body: unknown) => request<T>("PATCH", path, body),
  del: <T = void>(path: string) => request<T>("DELETE", path),
  putRaw: <T = void>(path: string, data: BodyInit) => request<T>("PUT", path, undefined, data),
};

export const qs = (params: Record<string, string | number | undefined>) => {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== "") p.set(k, String(v));
  return p.toString();
};

export interface Meta {
  version: string;
  setupRequired: boolean;
  allowSignups: boolean;
  providers: { id: string; name: string }[];
  ssh: { host: string; port: number };
}

export interface User {
  id: string;
  username: string;
  displayName: string;
  email: string;
  avatarUrl: string;
  isAdmin: boolean;
  createdAt: string;
}

export type Role = "viewer" | "operator" | "admin" | "owner";
export const roles: Role[] = ["viewer", "operator", "admin", "owner"];
const rank: Record<Role, number> = { viewer: 1, operator: 2, admin: 3, owner: 4 };
export const atLeast = (role: Role | undefined, min: Role) => !!role && rank[role] >= rank[min];

export const roleSummary: Record<Role, string> = {
  viewer: "See servers, status and console output",
  operator: "Start and stop servers, run commands, open shells and files",
  admin: "Create, configure and delete servers; manage members",
  owner: "Everything, including other owners and deleting the organization",
};

export interface Org {
  id: string;
  slug: string;
  name: string;
  role: Role;
}

export interface Me {
  user: User;
  orgs: Org[];
  identities: { provider: string; login: string }[];
  hasPassword: boolean;
}

export type Phase = "Stopped" | "Stopping" | "Starting" | "Running" | "Sleeping" | "Error";
export type Loader = "vanilla" | "paper" | "purpur" | "folia" | "fabric" | "quilt" | "forge" | "neoforge";
export type ContentKind = "mod" | "plugin" | "datapack";

export interface ContentRef {
  source: "modrinth" | "curseforge" | "url" | "upload";
  kind?: ContentKind;
  /** Modrinth or CurseForge slug, a download URL, or an uploaded file's name. */
  project: string;
  version?: string;
  /** The Java release the pinned file was compiled for, once the panel has read it. */
  java?: number;
  title?: string;
  versionName?: string;
  iconUrl?: string;
}

export interface Modpack {
  source: "modrinth" | "curseforge";
  project: string;
  version?: string;
  /** What the pinned pack version is built for, filled in by the panel. */
  gameVersion?: string;
  loader?: string;
  title?: string;
  versionName?: string;
  iconUrl?: string;
}

export interface ResourcePack {
  url: string;
  sha1?: string;
  enforce?: boolean;
  prompt?: string;
}

export type ExposeType = "Router" | "LoadBalancer" | "NodePort" | "None";

export interface ServerSpec {
  displayName?: string;
  state: "Running" | "Stopped";
  eula: boolean;
  version: string;
  loader: Loader;
  loaderVersion?: string;
  modpack?: Modpack | null;
  mods?: ContentRef[];
  resourcePack?: ResourcePack | null;
  properties?: Record<string, string>;
  ops?: string[];
  whitelist?: string[];
  resources: { memory: string; cpu?: string | null };
  storage: { size: string; storageClassName?: string };
  expose: { type: ExposeType; hostname?: string; loadBalancerIP?: string; nodePort?: number };
  sleep: { enabled?: boolean; idleMinutes: number };
  image?: string;
  env?: { name: string; value?: string }[];
}

export interface ServerStatus {
  phase?: Phase;
  message?: string;
  address?: string;
  version?: string;
  players?: { online: number; max: number; names?: string[] } | null;
  lastActive?: string;
  restarts?: number;
  pendingRestart?: boolean;
}

export interface Server {
  org: string;
  name: string;
  createdAt: string;
  /** Increases with every change; used to drop updates that arrive out of order. */
  revision: string;
  /** Java the game version ships with, and what the server runs on. 0 when unknown. */
  java: { game: number; effective: number };
  spec: ServerSpec;
  status: ServerStatus;
}

/** A partial spec for PATCH: omitted fields are kept, null clears a field. */
export type SpecPatch = { [K in keyof ServerSpec]?: ServerSpec[K] | null };

export interface Member {
  userId: string;
  username: string;
  displayName: string;
  avatarUrl: string;
  role: Role;
  joinedAt: string;
}

export interface Invite {
  token: string;
  role: Role;
  createdBy: string;
  createdAt: string;
  expiresAt: string | null;
  maxUses: number;
  uses: number;
}

export interface SSHKey {
  id: string;
  name: string;
  publicKey: string;
  fingerprint: string;
  createdAt: string;
  lastUsedAt: string | null;
}

export interface APIToken {
  id: string;
  name: string;
  createdAt: string;
  lastUsedAt: string | null;
}

export interface FileEntry {
  name: string;
  size: number;
  dir: boolean;
  link?: boolean;
  modTime: string;
}

export interface ModrinthHit {
  project_id: string;
  slug: string;
  title: string;
  description: string;
  icon_url: string | null;
  downloads: number;
  author: string;
  project_type: string;
  categories: string[];
}

export interface ModrinthVersion {
  id: string;
  name: string;
  version_number: string;
  version_type: "release" | "beta" | "alpha";
  game_versions: string[];
  loaders: string[];
  date_published: string;
}

export interface MinecraftVersions {
  latest: { release: string; snapshot: string };
  versions: { id: string; type: string; releaseTime: string }[];
}

export const serverPath = (org: string, name: string) => `/orgs/${org}/servers/${name}`;
