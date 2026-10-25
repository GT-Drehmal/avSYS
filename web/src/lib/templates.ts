import type { ServerSpec, Template } from "./api";
import { ago, bytes, loaderNames } from "./format";

/** What a server made with these settings runs. */
export function softwareOf(spec: ServerSpec) {
  if (spec.modpack) return `${spec.modpack.title || spec.modpack.project} modpack`;
  const name = loaderNames[spec.loader] ?? spec.loader;
  return spec.version === "LATEST" || !spec.version ? `${name}, latest` : `${name} ${spec.version}`;
}

/** A template in a line: what it runs, how large it is and when it was saved; or that it is being saved. */
export function templateSummary(t: Template) {
  if (t.settingsOnly) return [t.spec && softwareOf(t.spec), "settings only"].filter(Boolean).join(" · ");
  if (!t.current) return null;
  const s = t.current;
  return [[loaderNames[s.loader as keyof typeof loaderNames] ?? s.loader, s.version].filter(Boolean).join(" "), bytes(s.size), `saved ${ago(s.time)}`, t.versions.length > 1 && `${t.versions.length} versions`]
    .filter(Boolean)
    .join(" · ");
}

/** Whether servers can be made from it now: not while its first version is being saved. */
export const ready = (t: Template) => t.settingsOnly || !!t.current;
