import type { ServerSpec } from "./api";

/**
 * Whether a server starts with Aikar's flags, and with what else. Before they
 * had their own settings, a server could have them through its environment;
 * that counts until the settings are saved, which then take over.
 */
export function launchOf(spec: ServerSpec): { aikar: boolean; extraFlags: string; env: ServerSpec["env"] } {
  const env = spec.env ?? [];
  if (spec.jvm?.aikarFlags != null || spec.jvm?.extraFlags) {
    return { aikar: !!spec.jvm?.aikarFlags, extraFlags: spec.jvm?.extraFlags ?? "", env };
  }
  const legacy = (name: string) => env.find((e) => e.name === name)?.value;
  return {
    aikar: legacy("USE_AIKAR_FLAGS")?.trim().toLowerCase() === "true",
    extraFlags: (legacy("JVM_OPTS") ?? "").trim().split(/\s+/).filter(Boolean).join("\n"),
    env: env.filter((e) => e.name !== "USE_AIKAR_FLAGS" && e.name !== "JVM_OPTS"),
  };
}
