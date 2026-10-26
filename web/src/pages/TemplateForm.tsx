import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ChevronLeft } from "lucide-react";
import { useState, type ReactNode } from "react";
import { Link, useNavigate, useParams, useSearchParams } from "react-router";
import { ServerPicker, SnapshotPicker, TemplatePicker } from "../components/StartPicker";
import { Button, cx, Field, Input, Loading, Notice, PageHeader, Section, Textarea } from "../components/ui";
import { api, ApiError, type Server, type Snapshot, type Template } from "../lib/api";
import { displayName } from "../lib/format";
import { keys, useMeta, useServers } from "../lib/queries";
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
 * Making a template, or changing one of settings alone. A new one starts
 * from settings, filled in on the server form, or from a template, a server
 * or a snapshot, whose files and settings it copies.
 */
export function TemplateForm() {
  const { org = "", template: id = "" } = useParams();
  const [search, setSearch] = useSearchParams();
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
  if (id && template.isFetching) return <Loading />;
  if (id && !template.data?.settingsOnly) {
    return (
      <>
        <Back org={org} />
        <PageHeader title="Edit template" />
        <Notice tone="danger">
          {template.error instanceof ApiError ? template.error.message : template.data ? "This template was saved from a server. Give it a new version instead." : "Could not load the template."}
        </Notice>
      </>
    );
  }
  if (id) return <ServerForm key={id} org={org} templating={{ template: template.data ?? null }} taken={none} history={none} />;

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
  return <CopiedTemplate key={from} org={org} from={from} segments={segments} />;
}

function Back({ org }: { org: string }) {
  return (
    <Link to={`/o/${org}/templates`} className="mb-3 inline-flex items-center gap-1 text-xs text-muted hover:text-fg">
      <ChevronLeft className="size-3.5" />
      Templates
    </Link>
  );
}

/**
 * A new template from another's files and settings: a copy of a template's
 * version or of a snapshot, made at once, or a snapshot of a server taken
 * for it, which goes on on the server's page. One of settings alone gives
 * its settings.
 */
function CopiedTemplate({ org, from, segments }: { org: string; from: Exclude<From, "new">; segments: ReactNode }) {
  const qc = useQueryClient();
  const navigate = useNavigate();
  const servers = useServers(org);
  const [template, setTemplate] = useState<Template | null>(null);
  const [server, setServer] = useState<Server | null>(null);
  const [snapshot, setSnapshot] = useState<Snapshot | null>(null);
  const [name, setName] = useState("");
  const [description, setDescription] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const nameOf = (s: Snapshot) => {
    const live = servers.data?.find((x) => x.name === s.server);
    return live ? displayName(live) : s.server;
  };
  const suggested = from === "template" ? (template ? `${template.name} copy`.slice(0, 60) : "") : from === "server" ? (server ? displayName(server) : "") : snapshot ? nameOf(snapshot) : "";
  const picked = from === "template" ? !!template : from === "server" ? !!server : !!snapshot;

  const save = async (e: React.FormEvent) => {
    e.preventDefault();
    if (!picked) return setError(`Pick a ${from}.`);
    setBusy(true);
    setError(null);
    const source = template ? (template.settingsOnly ? { spec: template.spec } : { snapshot: template.current?.id }) : server ? { server: server.name } : { snapshot: snapshot?.id };
    try {
      await api.post(`/orgs/${org}/templates`, { name: name.trim() || suggested, description: description.trim(), ...source });
      await qc.invalidateQueries({ queryKey: keys.templates(org) });
      navigate(`/o/${org}/templates`);
    } catch (err) {
      setBusy(false);
      setError(err instanceof ApiError ? err.message : "Could not make the template.");
    }
  };

  return (
    <form onSubmit={save}>
      <Back org={org} />
      <PageHeader title="New template" />
      <Section title="Starting point">
        {segments}
        {from === "template" ? (
          <TemplatePicker
            org={org}
            picked={template?.id}
            onPick={(t) => {
              setTemplate(t);
              setDescription((was) => was || t.description);
            }}
          />
        ) : from === "server" ? (
          <ServerPicker org={org} picked={server?.name} onPick={setServer} />
        ) : (
          <SnapshotPicker org={org} picked={snapshot?.id} onPick={setSnapshot} />
        )}
      </Section>
      <Section title="Name">
        <Field label="Name" hint="Servers made from it are called this.">
          {(id) => <Input id={id} value={name} onChange={(e) => setName(e.target.value)} placeholder={suggested} maxLength={60} />}
        </Field>
        <Field label="Description">{(id) => <Textarea id={id} rows={2} maxLength={500} value={description} onChange={(e) => setDescription(e.target.value)} />}</Field>
      </Section>
      <Section title="Create">
        <p className="text-[13px] text-muted">{template?.settingsOnly ? "Its settings are copied." : "Its files, world and settings are copied."}</p>
        {error && <Notice tone="danger">{error}</Notice>}
        <div className="flex gap-2">
          <Button type="submit" variant="primary" loading={busy} disabled={!picked}>
            Create template
          </Button>
          <Link to={`/o/${org}/templates`} className="inline-flex h-8 items-center rounded-md px-3 text-[13px] font-medium text-muted hover:bg-raised hover:text-fg">
            Cancel
          </Link>
        </div>
      </Section>
    </form>
  );
}
