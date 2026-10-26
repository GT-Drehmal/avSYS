import { useQuery } from "@tanstack/react-query";
import { ChevronLeft } from "lucide-react";
import { Link } from "react-router";
import { Loading, Notice, PageHeader } from "../components/ui";
import { api, ApiError, type SnapshotDetail, type Template } from "../lib/api";
import type { Origin } from "../lib/origin";
import { keys, useOrgSnapshots, useServers } from "../lib/queries";
import { ServerForm, slugify, type Source } from "./ServerForm";

/**
 * Making a server out of one that exists, a snapshot or a template: the server form,
 * filled in with what the original has, once that has been read.
 */
export function NewFromSource({ org, origin, templating }: { org: string; origin: Origin; templating?: { template: Template | null } }) {
  const servers = useServers(org);
  // Names that snapshots are under go with them: a new server cannot take one.
  const snapshots = useOrgSnapshots(org);
  const snapshot = useQuery({
    queryKey: ["snapshot", org, origin.kind === "snapshot" ? origin.id : ""],
    queryFn: () => api.get<SnapshotDetail>(`/orgs/${org}/snapshots/${origin.kind === "snapshot" ? origin.id : ""}`),
    enabled: origin.kind === "snapshot",
    retry: false,
  });
  const template = useQuery({
    // Under the list's key: whatever changes the list asks for it again too.
    queryKey: keys.template(org, origin.kind === "template" ? origin.id : ""),
    queryFn: () => api.get<Template>(`/orgs/${org}/templates/${origin.kind === "template" ? origin.id : ""}`),
    enabled: origin.kind === "template",
    retry: false,
    // The form is filled in once: from the version used now, not one
    // remembered from before another was picked.
    refetchOnMount: "always",
  });
  const back = (
    <Link to={templating ? `/o/${org}/templates` : `/o/${org}`} className="mb-3 inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
      <ChevronLeft className="size-3.5" />
      {templating ? "Templates" : "Servers"}
    </Link>
  );
  if (servers.isPending || snapshots.isPending || (origin.kind === "snapshot" && snapshot.isPending) || (origin.kind === "template" && template.isFetching)) return <Loading />;
  // Without the list of servers it cannot be said which names are taken, or
  // whether the original still exists: nothing is guessed from its absence.
  if (!servers.data) {
    return (
      <>
        {back}
        <PageHeader title={templating ? "New template" : "New server"} />
        <Notice tone="danger">{servers.error instanceof ApiError ? servers.error.message : "Could not load servers."}</Notice>
      </>
    );
  }
  const taken = new Set(servers.data.map((s) => s.name));
  // Without the snapshots (backups switched off, say) none are known of: the
  // panel says so if a name turns out to have some.
  const history = new Set((snapshots.data?.snapshots ?? []).map((s) => s.server));
  // A template of settings alone has no files: the server is a new one, with them.
  const settings = origin.kind === "template" ? template.data : undefined;
  if (settings?.settingsOnly && settings.spec) {
    return <ServerForm key={`settings:${settings.id}`} org={org} preset={{ id: settings.id, name: settings.name, spec: settings.spec }} templating={templating} taken={taken} history={history} />;
  }
  const live = origin.kind === "server" ? servers.data?.find((s) => s.name === origin.name) : undefined;
  // A template's version, named as the template is: the server made from it
  // is called after it.
  const version = template.data?.current;
  const source: Source | null =
    origin.kind === "server"
      ? live
        ? { origin, name: live.name, spec: live.spec, size: 0, live }
        : null
      : origin.kind === "template"
        ? template.data?.spec && version
          ? { origin, name: slugify(template.data.name) || "server", spec: { ...template.data.spec, displayName: template.data.name }, size: version.size, taken: version.time, snapshot: version.id, world: template.data.world }
          : null
        : snapshot.data
          ? { origin, name: snapshot.data.server, spec: snapshot.data.spec, size: snapshot.data.size, taken: snapshot.data.time, live: servers.data?.find((s) => s.name === snapshot.data.server) }
          : null;
  if (!source) {
    return (
      <>
        {back}
        <PageHeader title={templating ? "New template" : "New server"} />
        <Notice tone="danger">
          {origin.kind === "server"
            ? `There is no server named ${origin.name} to copy.`
            : origin.kind === "template"
              ? template.error instanceof ApiError
                ? template.error.message
                : template.data
                  ? "This template has no version yet. Wait for its first to be saved."
                  : "Could not load the template."
              : snapshot.error instanceof ApiError
              ? snapshot.error.message
              : "Could not load the snapshot."}
        </Notice>
      </>
    );
  }
  return <ServerForm key={`${origin.kind}:${source.name}`} org={org} source={source} templating={templating} taken={taken} history={history} />;
}
