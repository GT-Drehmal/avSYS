import { useParams, useSearchParams } from "react-router";
import { originOf } from "../lib/origin";
import type { StartKind } from "../components/StartPicker";
import { Loading } from "../components/ui";
import { useOrgSnapshots, useServers, useWorlds } from "../lib/queries";
import { NewFromSource } from "./NewFromSource";
import { ServerForm } from "./ServerForm";

/**
 * The page for making a server: from nothing, or, when its address names
 * one, out of a template, a server or a snapshot. All are the same form.
 */
export function NewServer() {
  const { org = "" } = useParams();
  const [search] = useSearchParams();
  const origin = originOf(search.get("from"));
  const start = search.get("start");
  const pick = start === "template" || start === "server" || start === "snapshot" ? start : undefined;
  const world = search.get("world") ?? undefined;
  return origin ? <NewFromSource org={org} origin={origin} /> : <NewFromNothing key={`${pick ?? ""}:${world ?? ""}`} org={org} pick={pick} world={world} />;
}

function NewFromNothing({ org, pick, world }: { org: string; pick?: StartKind; world?: string }) {
  // Said under its name before it is sent: a name a server has, or that
  // snapshots of an earlier server are under, is not free.
  const servers = useServers(org);
  const snapshots = useOrgSnapshots(org);
  // A world from the library to start with, as its address says.
  const worlds = useWorlds(org, !!world);
  if (world && worlds.isPending) return <Loading />;
  const given = worlds.data?.find((w) => w.id === world && w.ready);
  const taken = new Set(servers.data?.map((s) => s.name));
  const history = new Set(snapshots.data?.snapshots.map((s) => s.server));
  return <ServerForm org={org} pick={given ? undefined : pick} world={given} taken={taken} history={history} />;
}
