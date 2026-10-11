import type { Loader, Server } from "./api";

export const loaderNames: Record<Loader, string> = {
  vanilla: "Vanilla",
  paper: "Paper",
  purpur: "Purpur",
  folia: "Folia",
  fabric: "Fabric",
  quilt: "Quilt",
  forge: "Forge",
  neoforge: "NeoForge",
};

/** What a server runs, in a few words: "Paper 1.21.4" or the modpack's name. */
export function describeSoftware(s: Server): string {
  if (s.spec.modpack) return s.spec.modpack.title || s.spec.modpack.project;
  const version = s.spec.version === "LATEST" ? "latest" : s.spec.version;
  return `${loaderNames[s.spec.loader] ?? s.spec.loader} ${version}`;
}

export const displayName = (s: Server) => s.spec.displayName || s.name;

const units = ["B", "KB", "MB", "GB", "TB"];
export function bytes(n: number): string {
  let i = 0;
  while (n >= 1024 && i < units.length - 1) {
    n /= 1024;
    i++;
  }
  return `${n >= 10 || i === 0 ? Math.round(n) : n.toFixed(1)} ${units[i]}`;
}

/** Kubernetes quantity ("4Gi", "512Mi") to a friendly label. */
export function quantity(q: string | undefined): string {
  if (!q) return "";
  const m = /^(\d+(?:\.\d+)?)(Mi|Gi|Ti|M|G|T)?$/.exec(q);
  if (!m) return q;
  const unit = { Mi: "MB", Gi: "GB", Ti: "TB", M: "MB", G: "GB", T: "TB" }[m[2] ?? ""] ?? "";
  return `${m[1]} ${unit}`.trim();
}

const rtf = new Intl.RelativeTimeFormat("en", { numeric: "auto" });
export function ago(iso: string | null | undefined): string {
  if (!iso) return "never";
  const seconds = (new Date(iso).getTime() - Date.now()) / 1000;
  const steps: [number, Intl.RelativeTimeFormatUnit][] = [
    [60, "second"],
    [60, "minute"],
    [24, "hour"],
    [30, "day"],
    [12, "month"],
    [Infinity, "year"],
  ];
  let value = seconds;
  for (const [size, unit] of steps) {
    if (Math.abs(value) < size) return rtf.format(Math.round(value), unit);
    value /= size;
  }
  return "";
}

export const compact = new Intl.NumberFormat("en", { notation: "compact", maximumFractionDigits: 1 });

/** Removes ANSI escape sequences and Minecraft § colour codes from console text. */
export const plain = (line: string) => line.replace(/\u001b\[[0-9;?]*[ -/]*[@-~]/g, "").replace(/§[0-9a-fk-orx]/gi, "");
