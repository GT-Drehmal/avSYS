import { useParams, useSearchParams } from "react-router";
import { originOf } from "../lib/origin";
import type { StartKind } from "../components/StartPicker";
import { useOrgSnapshots, useServers } from "../lib/queries";
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
  return origin ? <NewFromSource org={org} origin={origin} /> : <NewFromNothing key={pick ?? ""} org={org} pick={pick} />;
}

function NewFromNothing({ org, pick }: { org: string; pick?: StartKind }) {
  // Said under its name before it is sent: a name a server has, or that
  // snapshots of an earlier server are under, is not free.
  const servers = useServers(org);
  const snapshots = useOrgSnapshots(org);
  const taken = new Set(servers.data?.map((s) => s.name));
  const history = new Set(snapshots.data?.snapshots.map((s) => s.server));
  return <ServerForm org={org} pick={pick} taken={taken} history={history} />;
}
