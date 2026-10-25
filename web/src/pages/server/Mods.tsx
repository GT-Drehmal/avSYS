import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { AddContentDialog, ContentList, filtersFor, kindNames, kindsFor, newestPack, PackCard, PackFit, packOfVersion, type PickKind } from "../../components/Content";
import { ProjectIcon } from "../../components/ModrinthSearch";
import { Button, Field, Input, Notice, Toggle } from "../../components/ui";
import { api, ApiError, atLeast, qs, serverPath, type ContentKind, type ContentRef, type ResourcePack, type Server } from "../../lib/api";
import { applyServer, keys, useAction, useUpdateServer } from "../../lib/queries";
import { toast } from "../../lib/toast";
import { useGameVersions } from "../../lib/software";
import { PackStack, SectionHead, VersionDialog } from "../../components/ContentParts";
import { useServerContext } from "./ServerPage";

export function Mods() {
  const { server, role } = useServerContext();
  const update = useUpdateServer(server.org, server.name);
  const qc = useQueryClient();
  const [adding, setAdding] = useState<PickKind | null>(null);
  // The pack there is being replaced, rather than another put on top of it.
  const [replacing, setReplacing] = useState(false);
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
  // A section for each kind the server takes, and for any it has from
  // before its software was changed, so nothing on it goes unseen.
  const takes = kindsFor(server.spec);
  const sections = [...takes, ...(["mod", "plugin", "datapack"] as const).filter((k) => !takes.includes(k) && mods.some((m) => (m.kind ?? "mod") === k))];
  const versionTarget = typeof versioning === "number" ? mods[versioning] : null;

  return (
    <div className="flex flex-col gap-5">
      {modpack && (
        <div className="flex items-center gap-3 rounded-lg border border-line bg-surface px-4 py-3">
          <ProjectIcon url={modpack.iconUrl} size={40} kind="modpack" name={modpack.title || modpack.project} />
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
          Uses Java {server.java.effective} instead of {server.java.game} because some content needs it.
        </Notice>
      )}

      {sections.map((kind) => {
        // Where each is in the server's whole list, which is what is saved.
        const at = mods.flatMap((m, i) => ((m.kind ?? "mod") === kind ? [i] : []));
        const name = kindNames[kind].toLowerCase();
        return (
          <section key={kind} aria-label={kindNames[kind]} className="flex flex-col gap-2.5">
            <SectionHead title={kindNames[kind]} count={at.length} onAdd={canEdit && takes.includes(kind) ? () => setAdding(kind) : undefined} />
            {at.length === 0 ? (
              <p className="text-[13px] text-muted">{modpack && kind === "mod" ? "None on top of the modpack." : `No ${name}.`}</p>
            ) : (
              <ContentList
                mods={at.map((i) => mods[i])}
                gameJava={server.java.game}
                showKind={false}
                onRemove={canEdit ? (j) => update.mutate({ mods: mods.filter((_, i) => i !== at[j]) }) : undefined}
                extra={(m, j) =>
                  m.source === "modrinth" && (
                    <Button size="sm" disabled={!canEdit} onClick={() => setVersioning(at[j])} className="max-w-40">
                      <span className="truncate">{m.versionName || "Newest"}</span>
                    </Button>
                  )
                }
              />
            )}
          </section>
        );
      })}

      <PackSection server={server} gameVersion={server.spec.modpack ? server.spec.modpack.gameVersion : server.spec.version === "LATEST" ? latest : server.spec.version} looking={looking} canEdit={canEdit} update={update} onChoose={() => setAdding("resourcepack")}
        onReplace={() => {
          setReplacing(true);
          setAdding("resourcepack");
        }}
        onVersion={() => setVersioning("pack")}
      />

      {adding && (
        <AddContentDialog
          software={server.spec}
          mods={mods}
          busy={update.isPending}
          description="Downloaded when the server next starts."
          onAdd={(ref) => update.mutate({ mods: [...mods, ref] })}
          onUpload={upload}
          start={adding}
          only
          pack={
            // With one there, another goes on top of it; the first is just the pack.
            server.spec.resourcePack?.url && !replacing
              ? {
                  current: server.spec.resourcePack,
                  onTop: true,
                  onPick: async (pack) => applyServer(qc, await api.post<Server>(`${serverPath(server.org, server.name)}/resource-packs/url`, { url: pack.url, sha1: pack.sha1, project: pack.project, formats: pack.formats })),
                  upload: { path: `${serverPath(server.org, server.name)}/resource-packs`, method: "POST", onDone: (next) => applyServer(qc, next) },
                }
              : {
                  current: server.spec.resourcePack,
                  onPick: (pack) => update.mutateAsync({ resourcePack: { ...pack, enforce: !!server.spec.resourcePack?.enforce, prompt: server.spec.resourcePack?.prompt ?? "" } }),
                  upload: { path: `${serverPath(server.org, server.name)}/resource-pack`, onDone: (next) => applyServer(qc, next) },
                }
          }
          onClose={() => {
            setAdding(null);
            setReplacing(false);
          }}
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
 * installed, with whether it is required and what the game asks. Several
 * are joined into one, in an order, as in the game's own list of packs.
 */
function PackSection({
  server,
  gameVersion,
  looking,
  canEdit,
  update,
  onChoose,
  onReplace,
  onVersion,
}: { server: Server; gameVersion?: string; looking: boolean; canEdit: boolean; update: ReturnType<typeof useUpdateServer>; onChoose: () => void; onReplace: () => void; onVersion: () => void }) {
  const qc = useQueryClient();
  const pack = server.spec.resourcePack?.url ? server.spec.resourcePack : null;
  // An uploaded one takes its file with it, which only this does.
  const remove = useAction(() => api.del<Server>(`${serverPath(server.org, server.name)}/resource-pack`));
  const stack = pack?.stack?.length ? pack.stack : null;
  const order = useAction((order: string[]) => api.put<Server>(`${serverPath(server.org, server.name)}/resource-packs`, { order }));
  const [prompt, setPrompt] = useState(pack?.prompt ?? "");
  useEffect(() => setPrompt(pack?.prompt ?? ""), [pack?.prompt]);
  const change = (patch: Partial<ResourcePack>) => update.mutate({ resourcePack: patch as ResourcePack });
  // One change to it at a time: a prompt saved as its field is left is
  // done before the pack is replaced or removed, so neither undoes the other.
  const busy = update.isPending || remove.isPending || order.isPending || looking;
  return (
    <section aria-label="Resource packs" className="flex flex-col gap-2.5">
      {/* Not while the pack is being changed: a version still being looked up would land over it. */}
      <SectionHead title="Resource packs" count={stack?.length} addLabel="Add resource pack" onAdd={canEdit ? onChoose : undefined} addDisabled={busy} />
      {!pack ? (
        <p className="text-[13px] text-muted">None. A pack added here is offered to each player as they join.</p>
      ) : (
        <>
          {stack ? (
            <PackStack
              stack={stack}
              canEdit={canEdit}
              busy={update.isPending || remove.isPending || looking}
              onOrder={(shas) =>
                order.mutateAsync(shas).then((next) => {
                  applyServer(qc, next);
                  return (next.spec.resourcePack?.stack ?? []).map((p) => p.sha1);
                })
              }
            />
          ) : (
          <PackCard pack={pack} showKind={false}>
            {pack.project && (
              <Button size="sm" disabled={!canEdit || busy} onClick={onVersion} className="max-w-40">
                <span className="truncate">{pack.project.versionName || "Version"}</span>
              </Button>
            )}
            {canEdit && (
              <>
                <Button size="sm" disabled={busy} onClick={onReplace}>
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
          )}
          {!stack && <PackFit pack={pack} version={gameVersion} />}
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

