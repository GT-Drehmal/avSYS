import { useQuery, useQueryClient } from "@tanstack/react-query";
import { ArrowDown, ArrowUp, ExternalLink, GripVertical, Plus, X } from "lucide-react";
import { useEffect, useLayoutEffect, useRef, useState } from "react";
import { AddContentDialog, ContentList, filtersFor, kindNames, kindsFor, newestPack, PackCard, PackFit, packOfVersion, stackedTitle, type PickKind } from "../../components/Content";
import { ProjectIcon } from "../../components/ModrinthSearch";
import { Button, Chip, cx, Field, Input, Modal, Notice, Spinner, Toggle } from "../../components/ui";
import { api, ApiError, atLeast, qs, serverPath, type ContentKind, type ContentRef, type ModrinthVersion, type ResourcePack, type Server, type StackedPack } from "../../lib/api";
import { ago, bytes } from "../../lib/format";
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
    <Modal open onOpenChange={onClose} title={title} description="Stays on this version until you change it.">
      <ul className="-mx-2 flex flex-col">
        <li>
          <button className={row} onClick={() => onPick(null)}>
            <span className="min-w-0 flex-1">
              <span className="block font-medium">Update to the best fit</span>
              <span className="block text-xs text-muted">The newest release that fits this server.</span>
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
                  return next;
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

/**
 * The packs a server's pack is joined from, the top one first, as in the
 * game: where two change the same thing, the one higher up wins. Dragged, or
 * moved with their buttons, into another order. A move shows at once and is
 * saved behind it; those made while one is saved are saved together next.
 */
function PackStack({ stack, canEdit, busy, onOrder }: { stack: StackedPack[]; canEdit: boolean; busy: boolean; onOrder: (order: string[]) => Promise<Server> }) {
  const key = stack.map((p) => p.sha1).join(",");
  const [order, setOrder] = useState(() => (key ? key.split(",") : []));
  const saving = useRef(false);
  const wanted = useRef<string[] | null>(null);
  // The panel's order, gone back to if one is not saved.
  const panel = useRef(order);
  panel.current = key ? key.split(",") : [];
  useEffect(() => {
    if (!saving.current) setOrder(key ? key.split(",") : []);
  }, [key]);
  const save = async (next: string[]) => {
    wanted.current = next;
    if (saving.current) return;
    saving.current = true;
    try {
      let now: Server | undefined;
      for (let o: string[] | null = next; o; o = wanted.current) {
        wanted.current = null;
        now = await onOrder(o);
      }
      if (now) setOrder((now.spec.resourcePack?.stack ?? []).map((p) => p.sha1));
    } catch {
      wanted.current = null;
      setOrder(panel.current);
    } finally {
      saving.current = false;
    }
  };

  // Rows slide from where they were to where they are now.
  const rows = useRef(new Map<string, HTMLLIElement>());
  const was = useRef<Map<string, number> | null>(null);
  const remember = () => {
    was.current = new Map([...rows.current].map(([sha, el]) => [sha, el.getBoundingClientRect().top]));
  };
  useLayoutEffect(() => {
    const tops = was.current;
    if (!tops) return;
    was.current = null;
    for (const [sha, el] of rows.current) {
      const by = (tops.get(sha) ?? 0) - el.getBoundingClientRect().top;
      if (!tops.has(sha) || Math.abs(by) < 0.5) continue;
      el.style.transition = "none";
      el.style.transform = `translateY(${by}px)`;
      el.getBoundingClientRect();
      el.style.transition = "transform 180ms ease";
      el.style.transform = "";
    }
  });
  const move = (next: string[]) => {
    remember();
    setOrder(next);
    void save(next);
  };
  const moveFrom = (from: number, to: number) => {
    const next = order.slice();
    const [moved] = next.splice(from, 1);
    next.splice(to, 0, moved);
    move(next);
  };

  // Dragged by the pointer: the row follows it, and the others make room.
  type Held = { from: number; y: number; tops: number[]; heights: number[]; dy: number; to: number; moved: boolean };
  const held = useRef<Held | null>(null);
  const [drag, setDrag] = useState<Held | null>(null);
  const canMove = canEdit && !busy;
  const grab = (e: React.PointerEvent<HTMLLIElement>, from: number) => {
    const target = e.target as HTMLElement;
    if (!canMove || e.button !== 0 || target.closest("button, a")) return;
    // A finger scrolls the page, but from the grip.
    if (e.pointerType !== "mouse" && !target.closest("[data-grip]")) return;
    const els = order.map((sha) => rows.current.get(sha));
    if (els.some((el) => !el)) return;
    // Not the start of a text selection, nor of the browser's own drag,
    // either of which takes the pointer away from the row.
    e.preventDefault();
    e.currentTarget.setPointerCapture(e.pointerId);
    held.current = { from, y: e.clientY, tops: els.map((el) => el?.offsetTop ?? 0), heights: els.map((el) => el?.offsetHeight ?? 0), dy: 0, to: from, moved: false };
  };
  const pull = (e: React.PointerEvent) => {
    const h = held.current;
    if (!h) return;
    const last = h.tops.length - 1;
    const dy = Math.min(Math.max(e.clientY - h.y, -h.tops[h.from]), h.tops[last] + h.heights[last] - h.tops[h.from] - h.heights[h.from]);
    if (!h.moved && Math.abs(dy) < 4) return;
    // It passes another once its edge is over that one's middle, so it can
    // reach either end of the list.
    const top = h.tops[h.from] + dy;
    const bottom = top + h.heights[h.from];
    const mids = h.tops.map((t, i) => t + h.heights[i] / 2);
    const to = h.from - mids.filter((m, i) => i < h.from && top < m).length + mids.filter((m, i) => i > h.from && bottom > m).length;
    held.current = { ...h, dy, to, moved: true };
    setDrag(held.current);
  };
  const drop = (keep: boolean) => {
    const h = held.current;
    held.current = null;
    if (!h?.moved) return;
    remember();
    setDrag(null);
    if (keep && h.to !== h.from) moveFrom(h.from, h.to);
  };
  const shift = (i: number) => {
    if (!drag) return undefined;
    if (i === drag.from) return drag.dy;
    if (drag.from < i && i <= drag.to) return -drag.heights[drag.from];
    if (drag.to <= i && i < drag.from) return drag.heights[drag.from];
    return 0;
  };

  const by = new Map(stack.map((p) => [p.sha1, p]));
  const titleOf = (sha?: string) => {
    const p = sha ? by.get(sha) : undefined;
    return p ? stackedTitle(p) : "another pack";
  };
  const shown = order.flatMap((sha) => by.get(sha) ?? []);
  return (
    <>
      <ol aria-label="Packs, the top one first" className="relative overflow-hidden rounded-lg border border-line bg-surface">
        {shown.map((p, i) => {
          const title = stackedTitle(p);
          const line = [p.project ? p.project.versionName : p.size ? bytes(p.size) : "", p.hidden ? `${p.hidden} ${p.hidden === 1 ? "file" : "files"} covered by ${titleOf(p.hiddenBy)}` : ""].filter(Boolean).join(" · ");
          const dy = shift(i);
          const lifted = drag?.from === i;
          return (
            <li
              key={p.sha1}
              ref={(el) => {
                if (el) rows.current.set(p.sha1, el);
                else rows.current.delete(p.sha1);
              }}
              onPointerDown={(e) => grab(e, i)}
              onPointerMove={pull}
              onPointerUp={() => drop(true)}
              onPointerCancel={() => drop(false)}
              // Should the row lose the pointer some other way, it is let go
              // where it is, not left held.
              onLostPointerCapture={() => drop(true)}
              style={dy === undefined ? undefined : { transform: `translateY(${dy}px)`, transition: lifted ? "none" : "transform 150ms ease" }}
              className={cx(
                "relative flex items-center gap-3 border-b border-line bg-surface px-3 py-2.5 last:border-b-0",
                canMove && "select-none",
                lifted && "z-10 cursor-grabbing rounded-md shadow-lg ring-1 ring-line",
              )}
            >
              {canEdit && (
                <span data-grip className={cx("-m-1 touch-none p-1", canMove && !lifted && "cursor-grab")}>
                  <GripVertical aria-hidden className="size-4 shrink-0 text-faint" />
                </span>
              )}
              <ProjectIcon url={p.project?.iconUrl} size={32} kind="resourcepack" name={title} />
              <div className="min-w-0 flex-1">
                <p className="flex items-center gap-2 truncate text-[13px] font-medium">
                  {title}
                  {p.project && (
                    <a href={`https://modrinth.com/resourcepack/${p.project.id}`} target="_blank" rel="noreferrer" className="text-faint hover:text-accent" aria-label="Open on Modrinth">
                      <ExternalLink className="size-3" />
                    </a>
                  )}
                </p>
                {line && <p className="truncate text-xs text-faint">{line}</p>}
              </div>
              {canEdit && (
                <div className="flex shrink-0 items-center">
                  <Button size="sm" variant="ghost" icon={<ArrowUp className="size-4" />} aria-label={`Move ${title} up`} disabled={!canMove || i === 0} onClick={() => moveFrom(i, i - 1)} />
                  <Button size="sm" variant="ghost" icon={<ArrowDown className="size-4" />} aria-label={`Move ${title} down`} disabled={!canMove || i === shown.length - 1} onClick={() => moveFrom(i, i + 1)} />
                  <Button size="sm" variant="ghost" icon={<X className="size-4" />} aria-label={`Remove ${title}`} disabled={!canMove} onClick={() => move(order.filter((sha) => sha !== p.sha1))} />
                </div>
              )}
            </li>
          );
        })}
      </ol>
      <p className="px-1 text-xs text-muted">Joined into one pack. Where two change the same thing, the higher one wins.</p>
    </>
  );
}

function SectionHead({ title, count, addLabel, onAdd, addDisabled }: { title: string; count?: number; addLabel?: string; onAdd?: () => void; addDisabled?: boolean }) {
  return (
    <div className="flex min-h-8 items-center justify-between gap-3">
      <h2 className="text-[13px] font-medium">
        {title}
        {!!count && <span className="ml-1.5 font-normal text-muted">{count}</span>}
      </h2>
      {onAdd && (
        <Button size="sm" icon={<Plus className="size-4" />} onClick={onAdd} disabled={addDisabled} aria-label={addLabel ?? `Add ${title.toLowerCase()}`}>
          Add
        </Button>
      )}
    </div>
  );
}
