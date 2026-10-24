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

/** Sends a request, throwing the panel's error for one refused. */
async function send(method: string, path: string, init: { headers?: Record<string, string>; body?: BodyInit } = {}): Promise<Response> {
  let res: Response;
  try {
    res = await fetch(base + path, { method, headers: { "X-Avsys": "1", ...init.headers }, body: init.body });
  } catch {
    throw new ApiError(0, "network", "Could not reach the panel. Check your connection.");
  }
  if (!res.ok) {
    const err = res.headers.get("Content-Type")?.includes("application/json") ? await res.json() : null;
    throw new ApiError(res.status, err?.code ?? "error", err?.message ?? `Request failed (${res.status}).`);
  }
  return res;
}

async function request<T>(method: string, path: string, body?: unknown, raw?: BodyInit): Promise<T> {
  const res = await send(method, path, body !== undefined ? { headers: { "Content-Type": "application/json" }, body: JSON.stringify(body) } : { body: raw });
  if (res.status === 204) return undefined as T;
  return (res.headers.get("Content-Type")?.includes("application/json") ? res.json() : res.text()) as Promise<T>;
}

/** A file to edit: its text, and the tag a save sends back. */
export interface EditedFile {
  text: string;
  tag: string | null;
}

export async function readFile(path: string): Promise<EditedFile> {
  const res = await send("GET", path);
  return { text: await res.text(), tag: res.headers.get("ETag") };
}

/**
 * Saves a file, only over the one of this tag, if one is given: the panel
 * refuses, with "changed" or "removed", one changed since. Says the tag of
 * what was saved.
 */
export async function saveFile(path: string, text: string, tag: string | null): Promise<string | null> {
  const res = await send("PUT", path, { headers: tag ? { "If-Match": tag } : {}, body: text });
  return res.headers.get("ETag");
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  post: <T>(path: string, body?: unknown) => request<T>("POST", path, body ?? {}),
  patch: <T>(path: string, body: unknown) => request<T>("PATCH", path, body),
  put: <T>(path: string, body: unknown) => request<T>("PUT", path, body),
  del: <T = void>(path: string, body?: unknown) => request<T>("DELETE", path, body),
  putRaw: <T = void>(path: string, data: BodyInit) => request<T>("PUT", path, undefined, data),
};

/** How much of an upload has been sent. */
export interface Progress {
  sent: number;
  total: number;
}

/**
 * PUT a file, as putRaw does, saying as it goes how much has been sent and
 * stopping when the signal aborts. fetch cannot say how far an upload is.
 */
export function upload<T = void>(path: string, file: Blob, onProgress: (p: Progress) => void, signal?: AbortSignal, method: "PUT" | "POST" = "PUT"): Promise<T> {
  return new Promise<T>((resolve, reject) => {
    const xhr = new XMLHttpRequest();
    xhr.open(method, base + path);
    xhr.setRequestHeader("X-Avsys", "1");
    xhr.upload.onprogress = (e) => onProgress({ sent: e.loaded, total: e.lengthComputable ? e.total : file.size });
    // All of it is sent: the rest is the panel's.
    xhr.upload.onload = () => onProgress({ sent: file.size, total: file.size });
    xhr.onerror = () => reject(new ApiError(0, "network", "Could not reach the panel. Check your connection."));
    xhr.onabort = () => reject(new ApiError(0, "aborted", "Upload cancelled."));
    xhr.onload = () => {
      const isJSON = xhr.getResponseHeader("Content-Type")?.includes("application/json");
      let data: unknown = xhr.responseText;
      if (isJSON) {
        try {
          data = JSON.parse(xhr.responseText);
        } catch {
          data = null;
        }
      }
      if (xhr.status >= 200 && xhr.status < 300) return resolve((xhr.status === 204 ? undefined : data) as T);
      const err = isJSON ? (data as { code?: string; message?: string } | null) : null;
      reject(new ApiError(xhr.status, err?.code ?? "error", err?.message ?? `Request failed (${xhr.status}).`));
    };
    if (signal?.aborted) return reject(new ApiError(0, "aborted", "Upload cancelled."));
    signal?.addEventListener("abort", () => xhr.abort(), { once: true });
    xhr.send(file);
  });
}

export const qs = (params: Record<string, string | number | undefined>) => {
  const p = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) if (v !== undefined && v !== "") p.set(k, String(v));
  return p.toString();
};

/** Whether the panel can reach the cluster; since when not, if not. */
export interface Cluster {
  reachable: boolean;
  since?: string;
}

export interface Meta {
  version: string;
  setupRequired: boolean;
  allowSignups: boolean;
  providers: { id: string; name: string }[];
  ssh: { host: string; port: number };
  /** Whether this panel can take snapshots at all. */
  snapshots: boolean;
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
  /** Whether it has a picture of its own, at /orgs/{slug}/icon. */
  hasIcon: boolean;
}

/** Where an organization's picture is, if it has one. */
export const orgIcon = (o: Pick<Org, "slug" | "hasIcon">) => (o.hasIcon ? `/api/v1/orgs/${o.slug}/icon` : undefined);
/** Where a server's icon is. Whether it has one is only known by asking. */
export const serverIcon = (s: Pick<Server, "org" | "name">) => `/api/v1/orgs/${s.org}/servers/${s.name}/icon`;
/** The picture among a snapshot's files: what a server made from it will have. */
export const snapshotIcon = (org: string, id: string) => `/api/v1/orgs/${org}/snapshots/${id}/icon`;

export interface Me {
  user: User;
  orgs: Org[];
  identities: { provider: string; login: string }[];
  hasPassword: boolean;
  /** Their main Minecraft account, if they have linked any. */
  minecraft?: MinecraftAccount;
  /** Every Minecraft account they have linked, the main one first. */
  minecraftAccounts?: MinecraftAccount[];
}

/** A Minecraft account a person has linked. */
export interface MinecraftAccount {
  uuid: string;
  name: string;
  linkedAt: string;
  /** The one shown as their name. */
  main: boolean;
  /** Proven to be theirs, by logging in as it. */
  verified: boolean;
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
  /** Set for a pack uploaded to the panel, which keeps it and serves it at url. */
  uploaded?: { name?: string; size: number; time: string } | null;
  /** Set for a pack picked from Modrinth: url and sha1 are of the version's file. */
  project?: PackProject | null;
  /** The pack formats it is made for, where known. */
  formats?: PackFormats | null;
  /** Set for a pack the panel joined from several, the top one first. */
  stack?: StackedPack[] | null;
}

/** One of the packs a server's resource pack is joined from. */
export interface StackedPack {
  sha1: string;
  size: number;
  name?: string;
  project?: PackProject | null;
  formats?: PackFormats | null;
  /** How many of its files a pack higher up has too, and the one that hides most of them. */
  hidden?: number;
  hiddenBy?: string;
}

/** A range of resource pack formats, by their major number. */
export interface PackFormats {
  min: number;
  max: number;
}

export interface PackProject {
  source: "modrinth";
  /** Its slug. */
  id: string;
  version?: string;
  versionName?: string;
  title?: string;
  iconUrl?: string;
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
  /** How its Java is started. A server made before these were offered has neither. */
  jvm?: { aikarFlags?: boolean | null; extraFlags?: string };
  storage: { size: string; storageClassName?: string };
  expose: { type: ExposeType; hostname?: string; loadBalancerIP?: string; nodePort?: number };
  /** What mods and plugins listen on beside the game, opened to players. */
  ports?: ExtraPort[] | null;
  sleep: { enabled?: boolean; idleMinutes: number };
  /** From which role members are operators in game: "" is operator, "off" is nobody. */
  autoOp?: "" | "operator" | "admin" | "owner" | "off";
  /** From which role members are on the whitelist: "viewer" is every member; "" and "off" are nobody. */
  autoWhitelist?: "" | "viewer" | "operator" | "admin" | "owner" | "off";
  /** Labels of the nodes it is kept to, set outside the panel. */
  nodeSelector?: Record<string, string>;
  /** Members put on, or left off, the operators and the whitelist whatever their role, by user ID. */
  opMembers?: { also?: string[]; except?: string[] } | null;
  whitelistMembers?: { also?: string[]; except?: string[] } | null;
  /** Where the server's scheduled snapshots depart from its organization's. Absent, it follows them. */
  backups?: BackupOverride | null;
  image?: string;
  env?: { name: string; value?: string }[];
}

/** A port a mod or plugin listens on in the server. */
export interface ExtraPort {
  name: string;
  port: number;
  protocol?: "TCP" | "UDP";
}

/** Where an extra port is reached from outside. */
export interface PortStatus {
  name: string;
  port: number;
  protocol: "TCP" | "UDP";
  publicPort?: number;
  address?: string;
  /** Why it is not open, when it cannot be. */
  message?: string;
}

export interface ServerStatus {
  phase?: Phase;
  message?: string;
  address?: string;
  ports?: PortStatus[];
  version?: string;
  players?: { online: number; max: number; names?: string[] } | null;
  lastActive?: string;
  restarts?: number;
  pendingRestart?: boolean;
  /** The cluster node the server runs on. */
  node?: string;
  /** Written by the panel after each snapshot or restore. */
  snapshots?: { lastTime?: string; lastID?: string; error?: string; /** When the daily snapshot last completed, whether or not anything had changed. */ lastRun?: string } | null;
}

export type SnapshotTrigger = "manual" | "scheduled" | "pre-restore" | "pre-replace" | "final" | "copy" | "move";

/** One saved state of a server. */
export interface Snapshot {
  id: string;
  time: string;
  server: string;
  trigger: SnapshotTrigger;
  by?: string;
  version?: string;
  loader?: string;
  /** How large the server's files were. */
  size: number;
  /** What this snapshot added to the repository. */
  added: number;
  files: number;
}

/** A snapshot with the settings its server had when it was taken. */
export interface SnapshotDetail extends Snapshot {
  /** Without the image and extra environment for those who could not make a server of it. */
  spec: ServerSpec;
  retention: {
    /** Whether its server's schedule looks after it. Anything else stays until it is deleted by hand. */
    scheduled: boolean;
    /** Which rules of the schedule keep it. Absent for one the schedule removes at its next run. */
    kept?: ("daily" | "weekly" | "monthly" | "yearly")[];
    /** The organization's daily snapshots are off: the rules only apply when they are run by hand or switched back on. */
    paused?: boolean;
  };
}

/** A snapshot or restore under way on a server, or a new server receiving its files. */
export interface Operation {
  kind: "snapshot" | "restore" | "create" | "delete-server" | "move" | "import";
  org: string;
  server: string;
  stage: string;
  percent: number;
  startedAt: string;
}

export interface OperationDone {
  kind: "snapshot" | "restore" | "create" | "delete" | "delete-server" | "move" | "import";
  org: string;
  server: string;
  error?: string;
  /** One of the daily snapshots, which nobody is waiting to hear about. */
  scheduled?: boolean;
  /** The operation was stopped on purpose; its error is not news. */
  calledOff?: boolean;
  snapshot?: Snapshot;
}

/** What a server sets for itself in place of its organization's schedule. A part left out, or null, follows the organization. */
export interface BackupOverride {
  enabled?: boolean | null;
  minute?: number | null;
  keep?: BackupPolicy["keep"] | null;
}

/** An organization's daily snapshots and how long they are kept. */
export interface BackupPolicy {
  enabled: boolean;
  /** Minute of the day, UTC, at which they begin. */
  minute: number;
  keep: { daily: number; weekly: number; monthly: number; yearly: number };
  /** When they next begin; absent when they are off. */
  next?: string;
}

/** How an organization's offsite copy of its snapshots stands. */
export interface Offsite {
  /** False when the install copies snapshots nowhere else; the rest is then absent. */
  configured: boolean;
  lastAttempt?: string;
  /** When the last copy that went through began: what was here by then is there. */
  lastSuccess?: string;
  /** Why the last attempt failed. */
  error?: string;
  /** How many snapshots are offsite. */
  snapshots?: number;
  /** Something here has yet to be copied. */
  pending: boolean;
  running: boolean;
}

export interface SnapshotList {
  /** False when the panel has no restic and cannot take snapshots at all. */
  available: boolean;
  snapshots: Snapshot[];
  operations: Operation[];
  usage?: { bytes: number; snapshots: number };
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
  /** Why the game may not be run on its files, when a copy, restore or import of them did not finish. */
  unfinished?: string;
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
  /** The player they say they are in game: their main account. */
  minecraftName?: string;
  /** Every account they have linked, the main one first. */
  minecraftAccounts?: MinecraftAccount[];
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
  /** The categories without the loaders, which Modrinth counts among them. */
  display_categories?: string[];
  follows?: number;
  date_modified?: string;
  /** An SPDX identifier, or a name where there is none. */
  license?: string;
  client_side?: string;
  server_side?: string;
}

/** A page of search results. */
export interface ModrinthResults {
  hits: ModrinthHit[];
  offset: number;
  limit: number;
  total_hits: number;
}

/** A project as far as naming it goes. */
export interface ModrinthProject {
  id: string;
  slug: string;
  title: string;
  icon_url: string | null;
  project_type: string;
}

export interface ModrinthVersion {
  id: string;
  name: string;
  version_number: string;
  version_type: "release" | "beta" | "alpha";
  game_versions: string[];
  loaders: string[];
  date_published: string;
  files?: { url: string; filename: string; primary: boolean; size: number; hashes: { sha1?: string } }[];
  /** What it needs or clashes with. One names a project, or only a version of one. */
  dependencies?: { project_id: string | null; version_id?: string | null; dependency_type: "required" | "optional" | "incompatible" | "embedded" }[];
}

export interface MinecraftVersions {
  latest: { release: string; snapshot: string };
  /** java is the Java a version ships with. */
  versions: { id: string; type: string; releaseTime: string; java?: number }[];
}

export const serverPath = (org: string, name: string) => `/orgs/${org}/servers/${name}`;

/** A node of the cluster, as one is chosen for a server. */
/** Where a server runs, and what moving it involves. */
export interface Placement {
  node?: string;
  /** Its files are on that node's own disk, and go with it in a snapshot. */
  local: boolean;
  /** Node labels it is kept to, set outside the panel. */
  labels?: Record<string, string>;
}

export interface NodeInfo {
  name: string;
  /** Up and taking pods. */
  ready: boolean;
  /** New servers may be placed on it without asking. */
  automatic: boolean;
  /** It is set to take new servers, whether or not it is up. */
  hosts: boolean;
  /** Measured single-core speed; 0 when not measured. */
  score: number;
  memory: number;
  free: number;
  /** CPU it has for pods, in thousandths of a core. */
  cores: number;
  /** What it is using in all, where the cluster says. */
  inUse?: { cpu: number; memory: number };
  /** Players on the servers it runs, for the panel's administrator. */
  players?: number;
  /** What runs on it, for the panel's administrator. */
  servers?: { org: string; name: string; memory: string; phase?: string; players?: number }[];
}

/** The cluster's nodes, and for the panel's administrator how measuring their speed went. */
export interface NodeList {
  nodes: NodeInfo[];
  /** The node automatic placement would choose for the memory asked about. */
  automatic?: string;
  measuring?: boolean;
  measured?: { at: string; failed?: string[]; error?: string };
}
