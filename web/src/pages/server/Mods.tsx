import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Plus } from "lucide-react";
import { useEffect, useState } from "react";
import { AddContentDialog, ContentList, filtersFor, kindsFor, kindsPhrase, newestPack, PackCard, PackFit, packOfVersion, type PickKind } from "../../components/Content";
import { ProjectIcon } from "../../components/ModrinthSearch";
import { Button, Chip, Empty, Field, Input, Modal, Notice, Spinner, Toggle } from "../../components/ui";
import { api, ApiError, atLeast, qs, serverPath, type ContentKind, type ContentRef, type ModrinthVersion, type ResourcePack, type Server } from "../../lib/api";
import { ago } from "../../lib/format";
import { applyServer, keys, useAction, useUpdateServer } from "../../lib/queries";
import { toast } from "../../lib/toast";
import { useGameVersions } from "../../lib/software";
import { useServerContext } from "./ServerPage";

interface VersionDialogProps {
  title: string;
  project: string;
  current?: string;
  filters: { loader?: string; gameVersion?: string };
  onPick: (version: ModrinthVersion | null) => void;
  onClose: () => void;
}

function VersionDialog({ title, project, current, filters, onPick, onClose }: VersionDialogProps) {
  const versions = useQuery({
    queryKey: ["modrinth-versions", project, filters],
    queryFn: () => api.get<ModrinthVersion[]>(`/catalog/modrinth/projects/${encodeURIComponent(project)}/versions?${qs(filters)}`),
    staleTime: 5 * 60_000,
  });
  const row = "flex w-full items-center gap-3 rounded-md px-2 py-2 text-left text-[13px] hover:bg-raised";
  return (
    <Modal open onOpenChange={onClose} title={title} description="The version stays as chosen until you change it here.">
      <ul className="-mx-2 flex flex-col">
        <li>
          <button className={row} onClick={() => onPick(null)}>
            <span className="min-w-0 flex-1">
              <span className="block font-medium">Update to the best fit</span>
              <span className="block text-xs text-muted">The newest release that runs on this server as it is.</span>
            </span>
          </button>
        </li>
        {versions.isPending && (
          <li className="flex justify-center py-6">
            <Spinner />
          </li>
        )}
        {versions.isError && <li className="px-2 py-4 text-[13px] text-danger">{versions.error.message}</li>}
        {versions.data?.length === 0 && <li className="px-2 py-4 text-[13px] text-muted">No published version fits this server's loader and game version.</li>}
        {versions.data?.map((v) => (
          <li key={v.id}>
            <button className={row} onClick={() => onPick(v)}>
              <span className="min-w-0 flex-1">
                <span className="block truncate font-mono">{v.version_number}</span>
                <span className="block truncate text-xs text-muted">
                  {v.game_versions.slice(-3).join(", ")} · {ago(v.date_published)}
                </span>
              </span>
              {v.version_type !== "release" && <Chip>{v.version_type}</Chip>}
              {current === v.id && <Chip className="border-accent/40 text-accent">current</Chip>}
            </button>
          </li>
        ))}
      </ul>
    </Modal>
  );
}

export function Mods() {
  const { server, role } = useServerContext();
  const update = useUpdateServer(server.org, server.name);
  const qc = useQueryClient();
  const [adding, setAdding] = useState<PickKind | null>(null);
  const [versioning, setVersioning] = useState<number | "modpack" | "pack" | null>(null);
  // A pack's version being looked up, before it is saved.
  const [looking, setLooking] = useState(false);
  const canEdit = atLeast(role, "admin");
  const latest = useGameVersions(server.spec.modpack ? null : server.spec.loader).data?.latest;
  const mods = server.spec.mods ?? [];
  const modpack = server.spec.modpack;

  // The response is the server with the upload listed.
  const upload = async (file: File, kind: ContentKind) => {
    try {
      const next = await api.putRaw<Server>(`${serverPath(server.org, server.name)}/content?${qs({ kind, name: file.name })}`, file);
      qc.setQueryData<Server[]>(keys.servers(next.org), (list) => list?.map((s) => (s.name === next.name ? next : s)));
      toast.info(`Uploaded ${file.name}`);
    } catch (err) {
      toast.error(err instanceof Error ? err.message : "Upload failed.");
    }
  };

  const setMod = (index: number, patch: Partial<ContentRef>) => update.mutate({ mods: mods.map((m, i) => (i === index ? { ...m, ...patch } : m)) });
  const versionTarget = typeof versioning === "number" ? mods[versioning] : null;

  return (
    <div className="flex flex-col gap-5">
      {modpack && (
        <div className="flex items-center gap-3 rounded-lg border border-line bg-surface px-4 py-3">
          <ProjectIcon url={modpack.iconUrl} size={40} />
          <div className="min-w-0 flex-1">
            <p className="label-mono">Modpack</p>
            <p className="truncate text-[13px] font-medium">{modpack.title || modpack.project}</p>
          </div>
          {modpack.source === "modrinth" && (
            <Button size="sm" disabled={!canEdit} onClick={() => setVersioning("modpack")}>
              {modpack.versionName || "Newest release"}
            </Button>
          )}
        </div>
      )}

      {server.java.effective > server.java.game && (
        <Notice tone="info">
          This server runs on Java {server.java.effective} instead of the game's usual Java {server.java.game}, because something installed needs it.
        </Notice>
      )}

      <div className="flex items-center justify-between gap-3">
        <p className="text-[13px] text-muted">
          {mods.length === 0 ? `No ${kindsPhrase(server.spec)} added.` : `${mods.length} added${modpack ? " on top of the modpack" : ""}.`}
        </p>
        {canEdit && (
          <Button variant="primary" icon={<Plus className="size-4" />} onClick={() => setAdding(kindsFor(server.spec)[0])}>
            Add
          </Button>
        )}
      </div>

      {mods.length === 0 ? (
        !modpack && (
          <Empty title={server.spec.loader === "vanilla" ? "Vanilla servers take datapacks" : "Nothing installed yet"}>
            {server.spec.loader === "vanilla"
              ? "To run mods or plugins, switch the server software under Settings."
              : "Search Modrinth and add what you want."}
          </Empty>
        )
      ) : (
        <ContentList
          mods={mods}
          gameJava={server.java.game}
          onRemove={canEdit ? (i) => update.mutate({ mods: mods.filter((_, j) => j !== i) }) : undefined}
          extra={(m, i) =>
            m.source === "modrinth" && (
              <Button size="sm" disabled={!canEdit} onClick={() => setVersioning(i)} className="max-w-40">
                <span className="truncate">{m.versionName || "Newest"}</span>
              </Button>
            )
          }
        />
      )}

      <PackSection server={server} gameVersion={server.spec.modpack ? undefined : server.spec.version === "LATEST" ? latest : server.spec.version} looking={looking} canEdit={canEdit} update={update} onChoose={() => setAdding("resourcepack")} onVersion={() => setVersioning("pack")} />

      {adding && (
        <AddContentDialog
          software={server.spec}
          mods={mods}
          busy={update.isPending}
          description="Downloaded when the server next starts."
          onAdd={(ref) => update.mutate({ mods: [...mods, ref] })}
          onUpload={upload}
          start={adding}
          pack={{
            current: server.spec.resourcePack,
            onPick: (pack) => update.mutateAsync({ resourcePack: { ...pack, enforce: !!server.spec.resourcePack?.enforce, prompt: server.spec.resourcePack?.prompt ?? "" } }),
            upload: { path: `${serverPath(server.org, server.name)}/resource-pack`, onDone: (next) => applyServer(qc, next) },
          }}
          onClose={() => setAdding(null)}
        />
      )}
      {versioning === "pack" && server.spec.resourcePack?.project && (
        <VersionDialog
          title={server.spec.resourcePack.project.title || server.spec.resourcePack.project.id}
          project={server.spec.resourcePack.project.id}
          current={server.spec.resourcePack.project.version}
          filters={filtersFor(server.spec, "resourcepack", latest)}
          onClose={() => setVersioning(null)}
          onPick={async (v) => {
            const rp = server.spec.resourcePack;
            const project = rp?.project;
            if (!rp || !project) return;
            setVersioning(null);
            // Nothing else is done to the pack while its newest is looked up,
            // which would otherwise land after, and over, what was done.
            setLooking(true);
            try {
              const next = v ? await packOfVersion(v, project) : await newestPack(project, filtersFor(server.spec, "resourcepack", latest).gameVersion);
              await update.mutateAsync({ resourcePack: { ...next, enforce: !!rp.enforce, prompt: rp.prompt ?? "" } });
            } catch (err) {
              if (!(err instanceof ApiError)) toast.error(err instanceof Error ? err.message : "Something went wrong.");
            } finally {
              setLooking(false);
            }
          }}
        />
      )}
      {versionTarget && typeof versioning === "number" && (
        <VersionDialog
          title={versionTarget.title || versionTarget.project}
          project={versionTarget.project}
          current={versionTarget.version}
          filters={filtersFor(server.spec, versionTarget.kind ?? "mod", latest)}
          onClose={() => setVersioning(null)}
          onPick={(v) => {
            setMod(versioning, { version: v?.id ?? "", versionName: v?.version_number ?? "" });
            setVersioning(null);
          }}
        />
      )}
      {versioning === "modpack" && modpack && (
        <VersionDialog
          title={modpack.title || modpack.project}
          project={modpack.project}
          current={modpack.version}
          filters={{}}
          onClose={() => setVersioning(null)}
          onPick={(v) => {
            update.mutate({ modpack: { ...modpack, version: v?.id ?? "", versionName: v?.version_number ?? "" } });
            setVersioning(null);
          }}
        />
      )}
    </div>
  );
}

/**
 * The resource pack offered to players as they join, beside what is
 * installed: one at most, chosen in the same picker, with whether it is
 * required and what the game asks.
 */
function PackSection({
  server,
  gameVersion,
  looking,
  canEdit,
  update,
  onChoose,
  onVersion,
}: { server: Server; gameVersion?: string; looking: boolean; canEdit: boolean; update: ReturnType<typeof useUpdateServer>; onChoose: () => void; onVersion: () => void }) {
  const qc = useQueryClient();
  const pack = server.spec.resourcePack?.url ? server.spec.resourcePack : null;
  // An uploaded one takes its file with it, which only this does.
  const remove = useAction(() => api.del<Server>(`${serverPath(server.org, server.name)}/resource-pack`));
  const [prompt, setPrompt] = useState(pack?.prompt ?? "");
  useEffect(() => setPrompt(pack?.prompt ?? ""), [pack?.prompt]);
  const change = (patch: Partial<ResourcePack>) => update.mutate({ resourcePack: patch as ResourcePack });
  // One change to it at a time: a prompt saved as its field is left is
  // done before the pack is replaced or removed, so neither undoes the other.
  const busy = update.isPending || remove.isPending || looking;
  return (
    <section className="flex flex-col gap-2.5">
      <div className="flex items-center justify-between gap-3">
        <h2 className="text-[13px] font-medium">Resource pack</h2>
        {canEdit && !pack && (
          <Button size="sm" onClick={onChoose}>
            Choose one
          </Button>
        )}
      </div>
      {!pack ? (
        <p className="text-[13px] text-muted">None. One from Modrinth, uploaded or at a URL is offered to each player's game as they join.</p>
      ) : (
        <>
          <PackCard pack={pack}>
            {pack.project && (
              <Button size="sm" disabled={!canEdit || busy} onClick={onVersion} className="max-w-40">
                <span className="truncate">{pack.project.versionName || "Version"}</span>
              </Button>
            )}
            {canEdit && (
              <>
                <Button size="sm" disabled={busy} onClick={onChoose}>
                  Replace
                </Button>
                <Button
                  size="sm"
                  variant="danger"
                  aria-label="Remove the resource pack"
                  disabled={update.isPending || looking}
                  loading={remove.isPending}
                  onClick={() =>
                    remove.mutate(undefined, {
                      onSuccess: (next) => {
                        applyServer(qc, next);
                        toast.info("Resource pack removed");
                      },
                    })
                  }
                >
                  Remove
                </Button>
              </>
            )}
          </PackCard>
          <PackFit pack={pack} version={gameVersion} />
          <div className="flex flex-col gap-3 px-1">
            <Toggle checked={!!pack.enforce} disabled={!canEdit || busy} onChange={(enforce) => change({ enforce })} label="Required" hint="Players who decline the pack are disconnected." />
            <Field label="Prompt" hint="Shown when the game asks to download the pack.">
              {(id) => (
                <Input
                  id={id}
                  value={prompt}
                  disabled={!canEdit || busy}
                  onChange={(e) => setPrompt(e.target.value)}
                  onBlur={() => prompt !== (pack.prompt ?? "") && change({ prompt })}
                  onKeyDown={(e) => e.key === "Enter" && e.currentTarget.blur()}
                  placeholder="A pack made for this world"
                />
              )}
            </Field>
          </div>
        </>
      )}
    </section>
  );
}
