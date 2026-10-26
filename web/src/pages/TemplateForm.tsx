import { useQuery } from "@tanstack/react-query";
import { ChevronLeft } from "lucide-react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { ServerPicker, SnapshotPicker, TemplatePicker } from "../components/StartPicker";
import { cx, Loading, Notice, PageHeader, Section } from "../components/ui";
import { api, ApiError, qs, type Template } from "../lib/api";
import { originOf, type Origin } from "../lib/origin";
import { keys, useMeta } from "../lib/queries";
import { NewFromSource } from "./NewFromSource";
import { ServerForm } from "./ServerForm";

const none = new Set<string>();

/** What a new template starts from: settings alone, or another's files. */
type From = "new" | "template" | "server" | "snapshot";

const froms: [From, string][] = [
  ["new", "New"],
  ["template", "Template"],
  ["server", "Server"],
  ["snapshot", "Snapshot"],
];

/**
 * Making a template, or changing one. A new one starts from settings, or
 * from a template, a server or a snapshot picked here, whose files it has:
 * either way it is the server form, filled in from what it starts from, to
 * change before it is made. One with files is changed the same way.
 */
export function TemplateForm() {
  const { org = "", template: id = "" } = useParams();
  const [search, setSearch] = useSearchParams();
  const navigate = useNavigate();
  const meta = useMeta().data;
  const template = useQuery({
    queryKey: keys.template(org, id),
    queryFn: () => api.get<Template>(`/orgs/${org}/templates/${id}`),
    enabled: !!id,
    retry: false,
    // Filled in once, from what it has now.
    refetchOnMount: "always",
  });
  const asked = search.get("start");
  const from: From = froms.find(([f]) => f === asked)?.[0] ?? "new";
  const origin = originOf(search.get("from"));
  // Once: the form filled in from it asks for it again as it opens, and
  // would otherwise be taken away for that and opened again, without end.
  if (id && !template.isFetchedAfterMount) return <Loading />;
  if (id && !template.data) {
    return (
      <>
        <Back org={org} />
        <PageHeader title="Edit template" />
        <Notice tone="danger">{template.error instanceof ApiError ? template.error.message : "Could not load the template."}</Notice>
      </>
    );
  }
  if (id && template.data?.settingsOnly) return <ServerForm key={id} org={org} templating={{ template: template.data }} taken={none} history={none} />;
  // Its files, as the version used now has them, with what was changed.
  if (id) return <NewFromSource key={id} org={org} origin={{ kind: "template", id }} templating={{ template: template.data ?? null }} />;
  if (origin) return <NewFromSource key={search.get("from")} org={org} origin={origin} templating={{ template: null }} />;

  // Copying files needs snapshots: without them, settings are all there is.
  const choices = froms.filter(([f]) => f === "new" || f === "template" || meta?.snapshots);
  const choose = (next: From) => setSearch(next === "new" ? {} : { start: next }, { replace: true });
  const segment = (on: boolean) => cx("h-7 rounded-sm px-3 text-xs font-medium", on ? "bg-hover text-fg" : "text-muted hover:text-fg");
  const segments = (
    <div role="group" aria-label="Start from" className="flex w-fit flex-wrap gap-1 rounded-md border border-line bg-bg p-0.5">
      {choices.map(([f, label]) => (
        <button key={f} type="button" aria-pressed={f === from} className={segment(f === from)} onClick={() => choose(f)}>
          {label}
        </button>
      ))}
    </div>
  );
  if (from === "new") return <ServerForm key="new" org={org} templating={{ template: null }} taken={none} history={none} lead={<Section title="Starting point">{segments}</Section>} />;
  // Picked, it is the form filled in from it, under an address of its own.
  const open = (picked: Origin) => navigate(`/o/${org}/templates/new?${qs({ from: `${picked.kind}:${picked.kind === "server" ? picked.name : picked.id}` })}`);
  return (
    <>
      <Back org={org} />
      <PageHeader title="New template" />
      <Section title="Starting point">
        {segments}
        {from === "template" ? (
          <TemplatePicker org={org} onPick={(t) => open({ kind: "template", id: t.id })} />
        ) : from === "server" ? (
          <ServerPicker org={org} onPick={(s) => open({ kind: "server", name: s.name })} />
        ) : (
          <SnapshotPicker org={org} onPick={(s) => open({ kind: "snapshot", id: s.id })} />
        )}
      </Section>
    </>
  );
}

function Back({ org }: { org: string }) {
  return (
    <Link to={`/o/${org}/templates`} className="mb-3 inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
      <ChevronLeft className="size-3.5" />
      Templates
    </Link>
  );
}
