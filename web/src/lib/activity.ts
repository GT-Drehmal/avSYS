import type { Activity } from "./api";

/** What was done, as said after who did it: "restarted it". */
export function didWhat(a: Activity): string {
  const d = a.detail ?? "";
  switch (a.action) {
    case "server.start":
      return "started it";
    case "server.stop":
      return "stopped it";
    case "server.restart":
      return "restarted it";
    case "server.update":
      return "changed its settings";
    case "server.create":
      if (d.startsWith("copy of ")) return `made it as a copy of ${d.slice(8)}`;
      if (d.startsWith("from snapshot")) return "made it from a snapshot";
      return "made it";
    case "server.copy":
      return `copied it to ${d.replace(/^to /, "")}`;
    case "server.move":
      return `moved it to ${d.replace(/^to /, "")}`;
    case "server.import":
      return "brought it in";
    case "server.replace-world":
      return "replaced its world";
    case "server.icon":
      return "changed its picture";
    case "server.resource-pack":
      return d === "removed" ? "removed its resource pack" : "changed its resource pack";
    case "content.upload":
      return `uploaded ${d}`;
    case "snapshot.create":
      return "took a snapshot";
    case "snapshot.restore":
      return "restored a snapshot";
    case "snapshot.delete":
      return "deleted a snapshot";
    case "console.command":
      return `ran /${d}`;
    case "terminal.open":
      return "opened a terminal";
    case "files.write":
      return `saved ${d}`;
    case "files.rename":
      return `renamed ${d.replace(" -> ", " to ")}`;
    case "files.delete":
      return `deleted ${d}`;
    default:
      return a.action;
  }
}

export const whoDid = (a: Activity) => a.user?.name ?? "Someone";
