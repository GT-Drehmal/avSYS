import { useQueryClient } from "@tanstack/react-query";
import { type ReactNode, useEffect, useRef, useState } from "react";
import { AddContentDialog, ContentList, filtersFor, kindNames, kindsFor, newestPack, PackCard, PackFit, packOfVersion, type PickKind } from "../../components/Content";
import { modPortsNote } from "../../components/ExtraPorts";
import { PackContents, PackIcon } from "../../components/Modpack";
import { Button, Field, Input, Notice, Toggle } from "../../components/ui";
import { cancelled, UploadProgress, useUpload } from "../../components/Uploading";
import { NewerWorldConfirm } from "../../components/NewerWorld";
import { api, ApiError, atLeast, qs, serverPath, type ContentKind, type ContentRef, type ModrinthVersion, type ResourcePack, type Server } from "../../lib/api";
import { ArchiveError } from "../../lib/archive";
import { softwareWords } from "../../lib/format";
import { inspectPack } from "../../lib/mrpack";
import { newerThan, pinnedOf } from "../../lib/worlds";
import { applyServer, keys, useAction, useMeta, useUpdateServer } from "../../lib/queries";
import { toast } from "../../lib/toast";
import { useGameVersions } from "../../lib/software";
import { EmptySection, PackFromModpack, PackStack, SectionHead, VersionDialog } from "../../components/ContentParts";
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

  // A newer .mrpack for an uploaded modpack: read here first, so that one
  // that is not a pack is said at once, then sent, in parts where the panel
  // can take them.
  const meta = useMeta().data;
  const { sending, send, sendStaged, cancel } = useUpload();
  const [reading, setReading] = useState(false);
  const packInput = useRef<HTMLInputElement>(null);
  const uploadPack = async (file: File | undefined) => {
    if (!file) return;
    setReading(true);
    try {
      await inspectPack(file);
    } catch (err) {
      toast.error(err instanceof ArchiveError ? err.message : "Could not read this file.");
      return;
    } finally {
      setReading(false);
    }
    const path = `${serverPath(server.org, server.name)}/modpack`;
    try {
      const next = meta?.snapshots
        ? await sendStaged<Server>(
            file.name,
            file,
            (q) => `/orgs/${server.org}/uploads?${qs(q)}`,
            (q) => `/orgs/${server.org}/uploads?${qs(q)}`,
            (upload) => api.put<Server>(`${path}?${qs({ name: file.name, upload, size: file.size })}`, undefined),
          )
        : await send<Server>(file.name, `${path}?${qs({ name: file.name })}`, file);
      applyServer(qc, next);
      toast.info(`${next.spec.modpack?.title || file.name} is the modpack now`);
    } catch (err) {
      if (!cancelled(err)) toast.error(err instanceof Error ? err.message : "Upload failed.");
    }
  };

  const setMod = (index: number, patch: Partial<ContentRef>) => update.mutate({ mods: mods.map((m, i) => (i === index ? { ...m, ...patch } : m)) });
  // A section for each kind the server takes, and for any it has from
  // before its software was changed, so nothing on it goes unseen.
  const takes = kindsFor(server.spec);
  const sections = [...takes, ...(["mod", "plugin", "datapack"] as const).filter((k) => !takes.includes(k) && mods.some((m) => (m.kind ?? "mod") === k))];
  const versionTarget = typeof versioning === "number" ? mods[versioning] : null;

  // Another version of the modpack can be built for an older game than the
  // world was last opened in, which that game cannot open: asked first.
  const order = useGameVersions().data?.ordered ?? [];
  const reported = server.status.version?.split(" ").at(-1);
  const ranOn = reported && order.includes(reported) ? reported : modpack?.gameVersion;
  const [olderPack, setOlderPack] = useState<{ version: ModrinthVersion | null; game: string; name: string } | null>(null);
  const pickPack = (v: ModrinthVersion | null, sure = false) => {
    if (!modpack) return;
    // The best fit is the version the panel pins for none picked, from the
    // list the picker showed.
    const pinned = v ?? pinnedOf(qc.getQueryData<ModrinthVersion[]>(["modrinth-versions", modpack.project, {}]), undefined);
    const game = pinned?.game_versions.at(-1);
    if (pinned && game && !sure && newerThan(order, ranOn, game)) return setOlderPack({ version: v, game, name: pinned.version_number });
    setOlderPack(null);
    const spec = { modpack: { ...modpack, version: v?.id ?? "", versionName: v?.version_number ?? "" } };
    update.mutate(sure ? { spec, newerWorld: true } : spec);
  };

  return (
    <div className="flex flex-col gap-5">
      {modpack && (
        <div className="flex flex-col gap-2 rounded-lg border border-line bg-surface px-4 py-3">
          <div className="flex items-center gap-3">
            <PackIcon modpack={modpack} server={server} size={40} />
            <div className="min-w-0 flex-1">
              <p className="label-mono">Modpack</p>
              <p className="truncate text-[13px] font-medium">{modpack.title || modpack.project}</p>
              {modpack.source === "upload" && (
                <p className="truncate text-xs text-muted">
                  {modpack.version ? [modpack.versionName, softwareWords(modpack.loader, modpack.gameVersion), modpack.project].filter(Boolean).join(" · ") : "The .mrpack file was not uploaded."}
                </p>
              )}
            </div>
            {modpack.source === "modrinth" && (
              <Button size="sm" disabled={!canEdit} onClick={() => setVersioning("modpack")}>
                {modpack.versionName || "Newest release"}
              </Button>
            )}
            {modpack.source === "upload" && (
              <>
                <input
                  ref={packInput}
                  type="file"
                  hidden
                  accept=".mrpack"
                  aria-label="Modpack file"
                  onChange={(e) => {
                    const file = e.target.files?.[0];
                    e.target.value = "";
                    void uploadPack(file);
                  }}
                />
                <Button size="sm" disabled={!canEdit || !!sending} loading={reading} onClick={() => packInput.current?.click()}>
                  {modpack.version ? "Upload newer" : "Upload .mrpack"}
                </Button>
              </>
            )}
          </div>
          {sending && <UploadProgress sending={sending} onCancel={cancel} />}
          {modpack.source === "upload" && modpack.version && <p className="text-xs text-muted">Each start puts the pack's own files back, configs too.</p>}
          <PackContents server={server} />
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
        const onAdd = canEdit && takes.includes(kind) ? () => setAdding(kind) : undefined;
        // On a modpack, mods here are those added to the pack's own, and an
        // empty section is one quiet line.
        const extra = !!modpack && kind === "mod";
        if (modpack && at.length === 0) {
          return (
            <section key={kind} aria-label={kindNames[kind]}>
              <EmptySection text={extra ? "No mods added on top of the pack." : `No ${name}.`} addLabel={`Add ${name}`} onAdd={onAdd} />
            </section>
          );
        }
        return (
          <section key={kind} aria-label={kindNames[kind]} className="flex flex-col gap-2.5">
            <SectionHead title={extra ? "Mods added on top of the pack" : kindNames[kind]} count={at.length} addLabel={`Add ${name}`} onAdd={onAdd} />
            {at.length === 0 ? (
              <p className="text-[13px] text-muted">{`No ${name}.`}</p>
            ) : (
              <ContentList
                mods={at.map((i) => mods[i])}
                gameJava={server.java.game}
                showKind={false}
                note={(m) => modPortsNote(m, server.spec.ports)}
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

      <PackSection quiet={!!modpack} server={server} gameVersion={server.spec.modpack ? server.spec.modpack.gameVersion : server.spec.version === "LATEST" ? latest : server.spec.version} looking={looking} canEdit={canEdit} update={update} onChoose={() => setAdding("resourcepack")}
        onReplace={() => {
          setReplacing(true);
          setAdding("resourcepack");
        }}
        onVersion={() => setVersioning("pack")}
        suggest={canEdit && <PackFromModpack server={server} update={update} disabled={looking || update.isPending} onLooking={setLooking} />}
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
            pickPack(v);
            setVersioning(null);
          }}
        />
      )}
      <NewerWorldConfirm
        open={!!olderPack}
        onOpenChange={(o) => !o && setOlderPack(null)}
        world={ranOn ?? ""}
        version={olderPack?.game ?? ""}
        action={`Change to ${olderPack?.name ?? "it"} anyway`}
        loading={update.isPending}
        onConfirm={() => olderPack && pickPack(olderPack.version, true)}
      />
    </div>
  );
}

/**
 * The resource pack offered to players as they join, beside what is
 * installed, with whether it is required and what the game asks. Several
 * are joined into one, in an order, as in the game's own list of packs.
 */
function PackSection({
  quiet,
  server,
  gameVersion,
  looking,
  canEdit,
  update,
  onChoose,
  onReplace,
  onVersion,
  suggest,
}: { quiet: boolean; server: Server; gameVersion?: string; looking: boolean; canEdit: boolean; update: ReturnType<typeof useUpdateServer>; onChoose: () => void; onReplace: () => void; onVersion: () => void; suggest?: ReactNode }) {
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
  if (quiet && !pack) {
    return (
      <section aria-label="Resource packs" className="flex flex-col gap-2">
        <EmptySection text="No resource packs." addLabel="Add resource pack" onAdd={canEdit ? onChoose : undefined} addDisabled={busy} />
        {suggest}
      </section>
    );
  }
  return (
    <section aria-label="Resource packs" className="flex flex-col gap-2.5">
      {/* Not while the pack is being changed: a version still being looked up would land over it. */}
      <SectionHead title="Resource packs" count={stack?.length} addLabel="Add resource pack" onAdd={canEdit ? onChoose : undefined} addDisabled={busy} />
      {suggest}
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

