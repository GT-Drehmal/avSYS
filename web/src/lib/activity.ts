import type { Activity } from "./api";

/** What was done, as said after who did it: "restarted the server". */
export function didWhat(a: Activity): string {
  const d = a.detail ?? "";
  switch (a.action) {
    case "server.start":
      return "started the server";
    case "server.stop":
      return "stopped the server";
    case "server.restart":
      return "restarted the server";
    case "server.update":
      return "changed the settings";
    case "server.create":
      if (d.startsWith("copy of ")) return `created the server as a copy of ${d.slice(8)}`;
      if (d.startsWith("from snapshot")) return "created the server from a snapshot";
      return "created the server";
    case "server.copy":
      return `copied the server to ${d.replace(/^to /, "")}`;
    case "server.move":
      return `moved the server to ${d.replace(/^to /, "")}`;
    case "server.import":
      return "imported the server";
    case "server.replace-world":
      return "replaced the world";
    case "server.icon":
      return "changed the picture";
    case "server.resource-pack":
      return d === "removed" ? "removed the resource pack" : "changed the resource pack";
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
