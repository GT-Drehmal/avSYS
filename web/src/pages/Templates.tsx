import { useQueryClient } from "@tanstack/react-query";
import { Copy, History, MoreHorizontal, Pencil, Plus, RefreshCw, Trash2 } from "lucide-react";
import { useState } from "react";
import { Link, useNavigate, useParams } from "react-router";
import { Picture } from "../components/Picture";
import { SaveTemplate } from "../components/SaveTemplate";
import { useOrg } from "../components/Shell";
import { software } from "../components/Snapshots";
import { Button, Chip, Confirm, cx, Empty, Loading, Menu, MenuContent, MenuItem, MenuTrigger, Modal, Notice, PageHeader, SearchBox, Spinner } from "../components/ui";
import { matches, useSearch } from "../lib/search";
import { api, ApiError, atLeast, qs, snapshotIcon, type Template } from "../lib/api";
import { ago, bytes } from "../lib/format";
import { newFrom } from "../lib/origin";
import { keys, useMeta, useServers, useTemplates } from "../lib/queries";
import { ready, templateSummary } from "../lib/templates";

/** An organization's templates: servers kept to make others from. */
export function Templates() {
  const { org = "" } = useParams();
  const membership = useOrg();
  const meta = useMeta().data;
  const templates = useTemplates(org);
  const servers = useServers(org);
  const navigate = useNavigate();
  const canAdmin = !!membership && atLeast(membership.role, "admin");
  const [updating, setUpdating] = useState<Template | null>(null);
  const [versions, setVersions] = useState<Template | null>(null);
  const [deleting, setDeleting] = useState<Template | null>(null);
  const [query, setQuery] = useSearch();

  const header = (
    <PageHeader
      title="Templates"
      description="Servers kept to make others like them."
      actions={
        canAdmin && (
          <Link to="new" className="inline-flex h-8 items-center gap-1.5 rounded-md bg-accent px-3 text-[13px] font-medium text-accent-ink hover:bg-accent-strong">
            <Plus className="size-4" />
            New template
          </Link>
        )
      }
    />
  );
  if (templates.isPending) return <Loading />;
  if (!templates.data) {
    return (
      <>
        {header}
        <Notice tone="danger">{templates.error instanceof ApiError ? templates.error.message : "Could not load templates."}</Notice>
      </>
    );
  }
  const list = templates.data;
  const shown = list.filter((t) => matches(query, t.name, t.description, templateSummary(t)));
  // Kept fresh on the dialogs, which are opened with the one clicked.
  const latest = (t: Template | null) => (t ? (list.find((x) => x.id === t.id) ?? null) : null);

  return (
    <div className="flex flex-col gap-4">
      {header}
      {meta && !meta.snapshots && <Notice>Saving a server as a template needs snapshots, which are not set up on this panel.</Notice>}
      {list.length === 0 ? (
        <Empty title="No templates yet">Make one from settings, a server or a snapshot.</Empty>
      ) : (
        <>
        <SearchBox value={query} onChange={setQuery} label="Search templates" />
        {shown.length === 0 ? (
          <p className="rounded-lg border border-line bg-surface px-4 py-8 text-center text-[13px] text-muted">No templates match.</p>
        ) : (
        <ul className="flex flex-col divide-y divide-line rounded-lg border border-line bg-surface" aria-label="Templates">
          {shown.map((t) => (
            <li key={t.id} className="flex items-center gap-3 px-4 py-3" aria-label={t.name}>
              <Picture name={t.name} url={t.current ? snapshotIcon(org, t.current.id) : undefined} size={36} />
              <div className="min-w-0 flex-1">
                <p className="truncate text-[13px] font-medium">{t.name}</p>
                {t.description && <p className="line-clamp-2 text-xs text-muted">{t.description}</p>}
                <p className={cx("mt-0.5 text-xs text-faint", !ready(t) && "flex items-center gap-1.5")}>
                  {ready(t) ? (
                    templateSummary(t)
                  ) : (
                    <>
                      <Spinner className="size-3" />
                      Saving
                    </>
                  )}
                </p>
              </div>
              {canAdmin && (
                <>
                  <Button size="sm" disabled={!ready(t)} onClick={() => navigate(newFrom(org, { kind: "template", id: t.id }))}>
                    New server
                  </Button>
                  <Menu>
                    <MenuTrigger asChild>
                      <Button size="sm" variant="ghost" icon={<MoreHorizontal className="size-4" />} aria-label={`Actions for ${t.name}`} />
                    </MenuTrigger>
                    <MenuContent>
                      <MenuItem icon={<Pencil />} disabled={!ready(t)} onSelect={() => navigate(`/o/${org}/templates/${t.id}/edit`)}>
                        Edit
                      </MenuItem>
                      {meta?.snapshots && (
                        <MenuItem icon={<RefreshCw />} onSelect={() => setUpdating(t)}>
                          New version
                        </MenuItem>
                      )}
                      {(t.settingsOnly || meta?.snapshots) && (
                        <MenuItem icon={<Copy />} disabled={!ready(t)} onSelect={() => navigate(`/o/${org}/templates/new?${qs({ from: `template:${t.id}` })}`)}>
                          Duplicate
                        </MenuItem>
                      )}
                      {!t.settingsOnly && (
                        <MenuItem icon={<History />} disabled={!t.versions.length} onSelect={() => setVersions(t)}>
                          Versions
                        </MenuItem>
                      )}
                      <MenuItem icon={<Trash2 />} danger onSelect={() => setDeleting(t)}>
                        Delete
                      </MenuItem>
                    </MenuContent>
                  </Menu>
                </>
              )}
            </li>
          ))}
        </ul>
        )}
        </>
      )}

      {updating && <SaveTemplate key={updating.id} org={org} open onOpenChange={(o) => !o && setUpdating(null)} servers={servers.data} templates={list} into={updating} />}
      {latest(versions) && <Versions org={org} template={latest(versions)!} onClose={() => setVersions(null)} />}
      <DeleteTemplate org={org} template={deleting} onClose={() => setDeleting(null)} />
    </div>
  );
}

function Versions({ org, template, onClose }: { org: string; template: Template; onClose: () => void }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const act = async (id: string, work: () => Promise<unknown>) => {
    setBusy(id);
    setError(null);
    try {
      await work();
      await qc.invalidateQueries({ queryKey: keys.templates(org) });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : "That did not work.");
    } finally {
      setBusy(null);
    }
  };
  return (
    <Modal open onOpenChange={(o) => !o && onClose()} title={`Versions of ${template.name}`} description="New servers are made from the one used.">
      <ul className="flex flex-col divide-y divide-line rounded-md border border-line" aria-label="Versions">
        {template.versions.map((s) => {
          const used = template.current?.id === s.id;
          return (
            <li key={s.id} className="flex items-center gap-3 px-3 py-2" aria-label={new Date(s.time).toLocaleString()}>
              <div className="min-w-0 flex-1">
                <p className="flex items-center gap-2 text-[13px]">
                  {new Date(s.time).toLocaleString(undefined, { dateStyle: "medium", timeStyle: "short" })}
                  {used && <Chip>used</Chip>}
                </p>
                <p className="truncate text-xs text-muted">{[software(s), bytes(s.size), s.by && `by ${s.by}`].filter(Boolean).join(" · ")}</p>
              </div>
              {!used && (
                <Button size="sm" loading={busy === `use:${s.id}`} disabled={!!busy} onClick={() => act(`use:${s.id}`, () => api.patch(`/orgs/${org}/templates/${template.id}`, { current: s.id }))}>
                  Use
                </Button>
              )}
              {template.versions.length > 1 && (
                <Button
                  size="sm"
                  variant="ghost"
                  icon={<Trash2 className="size-4" />}
                  aria-label={`Delete the version from ${ago(s.time)}`}
                  loading={busy === `delete:${s.id}`}
                  disabled={!!busy}
                  onClick={() => act(`delete:${s.id}`, () => api.del(`/orgs/${org}/templates/${template.id}/versions/${s.id}`))}
                />
              )}
            </li>
          );
        })}
      </ul>
      {error && (
        <div className="mt-3">
          <Notice tone="danger">{error}</Notice>
        </div>
      )}
    </Modal>
  );
}

function DeleteTemplate({ org, template, onClose }: { org: string; template: Template | null; onClose: () => void }) {
  const qc = useQueryClient();
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const n = template?.versions.length ?? 0;
  return (
    <Confirm
      open={!!template}
      onOpenChange={(o) => {
        if (!o && !busy) {
          setError(null);
          onClose();
        }
      }}
      title={template ? `Delete ${template.name}?` : ""}
      description={error ?? `${n === 0 ? "" : n === 1 ? "Its version is deleted too. " : `Its ${n} versions are deleted too. `}Servers made from it stay as they are. This cannot be undone.`}
      action="Delete"
      loading={busy}
      onConfirm={async () => {
        if (!template) return;
        setBusy(true);
        try {
          await api.del(`/orgs/${org}/templates/${template.id}`);
          await qc.invalidateQueries({ queryKey: keys.templates(org) });
          setBusy(false);
          onClose();
        } catch (err) {
          setBusy(false);
          setError(err instanceof ApiError ? err.message : "Could not delete it.");
        }
      }}
    />
  );
}
