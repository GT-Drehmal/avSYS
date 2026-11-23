import { useQueryClient } from "@tanstack/react-query";
import { useEffect, useState } from "react";
import { AccentPicker } from "../components/Look";
import { ProviderIcon } from "../components/ProviderIcon";
import { Button, Chip, Confirm, CopyText, Empty, Field, Input, Loading, Notice, PageHeader, Section, SelectMenu, Spinner, Toggle, type Choice } from "../components/ui";
import { api, ApiError, type Meta, type PanelInfo, type PanelState, type ReleaseUpdate, type TwoFactorLevel } from "../lib/api";
import type { Accent } from "../lib/look";
import { keys, useAction, useMe, useMeta, usePanel } from "../lib/queries";
import { toast } from "../lib/toast";

/**
 * Waits for the panel to answer as the version given, while it restarts on
 * it, then loads the page again so that what the browser runs is new too.
 */
function useBackOn(version?: string) {
  const [slow, setSlow] = useState(false);
  const [back, setBack] = useState(false);
  useEffect(() => {
    if (!version) return;
    setSlow(false);
    setBack(false);
    const started = Date.now();
    let timer: ReturnType<typeof setTimeout>;
    let stopped = false;
    const look = async () => {
      try {
        const res = await fetch("/api/v1/meta", { cache: "no-store" });
        if (res.ok && ((await res.json()) as { version?: string }).version === version) {
          if (stopped) return;
          setBack(true);
          timer = setTimeout(() => window.location.reload(), 1_000);
          return;
        }
      } catch {
        // Not answering while it restarts.
      }
      if (stopped) return;
      if (Date.now() - started > 10 * 60_000) setSlow(true);
      timer = setTimeout(look, 2_000);
    };
    timer = setTimeout(look, 2_000);
    return () => {
      stopped = true;
      clearTimeout(timer);
    };
  }, [version]);
  return { slow, back };
}

const box = "rounded-md border border-line-strong bg-raised px-3 py-2.5 text-[13px]";

/**
 * The panel moving to a newer release: until it answers as that version,
 * then while the page loads again. Kept by the page, not by the notice of
 * the release, which the panel stops sending once it runs that release.
 */
function Updating({ version }: { version: string }) {
  const { slow, back } = useBackOn(version);
  return (
    <section aria-label="New release" className={box}>
      <p role="status" className="flex items-center gap-2">
        {back ? (
          <>Updated to {version}. Loading it…</>
        ) : (
          <>
            <Spinner className="size-3.5" />
            Updating to {version}…{slow && " This is taking longer than usual."}
          </>
        )}
      </p>
    </section>
  );
}

/**
 * A newer release of avSYS than the one running: an Update button where the
 * panel can move itself to it, and otherwise the command that upgrades.
 */
function NewRelease({ update, panel, onUpdating }: { update: ReleaseUpdate; panel: PanelInfo; onUpdating: (version: string) => void }) {
  const [asking, setAsking] = useState(false);
  const [pressing, setPressing] = useState(false);
  const press = async () => {
    setPressing(true);
    try {
      const now = await api.post<PanelInfo>("/panel/update", { version: update.version });
      setAsking(false);
      onUpdating(now.updating ?? update.version);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not update.");
    } finally {
      setPressing(false);
    }
  };

  return (
    <section aria-label="New release" className={box}>
      <div className="flex flex-wrap items-center justify-between gap-2">
        <p>
          avSYS <span className="font-mono">{update.version}</span> is out.{" "}
          <a href={update.notes} target="_blank" rel="noreferrer" className="font-medium text-accent hover:underline">
            What's new
          </a>
        </p>
        {panel.canUpdate && (
          <Button variant="primary" size="sm" onClick={() => setAsking(true)}>
            Update
          </Button>
        )}
      </div>
      {panel.canUpdate ? (
        <Confirm
          open={asking}
          onOpenChange={setAsking}
          title={`Update to ${update.version}?`}
          description="avSYS restarts. Players are disconnected for a moment."
          action="Update"
          onConfirm={press}
          loading={pressing}
        />
      ) : (
        update.command && (
          <>
            <p className="mt-1.5 text-muted">
              {update.manifest ? (
                <>
                  Copy your changes into the new{" "}
                  <a href={update.manifest} className="font-medium text-accent hover:underline">
                    install.yaml
                  </a>
                  , then run:
                </>
              ) : (
                "To upgrade, run:"
              )}
            </p>
            <div className="mt-1 rounded-md border border-line bg-bg px-3 py-2">
              <CopyText value={update.command} className="text-xs" wrap />
            </div>
          </>
        )
      )}
    </section>
  );
}

/** The version running, a newer one where there is, and whether fixes are installed by themselves. */
function Updates({ state }: { state: PanelState }) {
  const qc = useQueryClient();
  const { panel, update } = state;
  // The release the panel is moving to, once pressed or as the panel says:
  // held on to after the panel, restarted on it, no longer says so.
  const [updating, setUpdating] = useState<string>();
  useEffect(() => {
    if (panel.updating) setUpdating((was) => was ?? panel.updating);
  }, [panel.updating]);
  const setAuto = async (on: boolean) => {
    qc.setQueryData<PanelState>(keys.panel, (d) => (d ? { ...d, panel: { ...d.panel, autoUpdate: on } } : d));
    try {
      await api.patch("/panel", { autoUpdate: on });
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not change this.");
    }
    await qc.invalidateQueries({ queryKey: keys.panel });
  };
  return (
    <Section title="Updates" region description={`This is avSYS ${panel.version}.`}>
      {updating ? <Updating version={updating} /> : update ? <NewRelease update={update} panel={panel} onUpdating={setUpdating} /> : null}
      {panel.canUpdate && <Toggle checked={panel.autoUpdate} onChange={setAuto} label="Install fixes by itself" hint="Fixes are installed while no one is playing." />}
    </Section>
  );
}

const twoFactorChoices: Choice<TwoFactorLevel>[] = [
  { value: "nobody", label: "Nobody", description: "Up to each person." },
  { value: "admins", label: "Admins and owners", description: "Of any organization." },
  { value: "operators", label: "Operators and up", description: "Operators, admins and owners." },
  { value: "everyone", label: "Everyone", description: "Every account." },
];

/** How people sign in, as the install has it, and who has to use two-factor. */
function SignIn({ meta }: { meta: Meta }) {
  const me = useMe().data!;
  const setLevel = useAction((level: TwoFactorLevel) => api.put("/settings/two-factor", { level }), [keys.me]);
  return (
    <Section title="Sign-in" region description="Set when avSYS is installed.">
      <dl className="grid grid-cols-[auto_minmax(0,1fr)] gap-x-6 gap-y-1.5 text-[13px]">
        <dt className="text-muted">Passwords</dt>
        <dd>{meta.passwords ? "On" : "Off"}</dd>
        <dt className="text-muted">Other ways</dt>
        <dd>
          {meta.providers.length > 0 ? (
            <ul className="flex flex-wrap gap-x-4 gap-y-1.5">
              {meta.providers.map((p) => (
                <li key={p.id} className="flex items-center gap-1.5">
                  <ProviderIcon id={p.id} />
                  {p.name}
                </li>
              ))}
            </ul>
          ) : (
            "None"
          )}
        </dd>
        <dt className="text-muted">New accounts</dt>
        <dd>{meta.allowSignups ? "Anyone can sign up" : "By invite"}</dd>
      </dl>
      <Field label="Require two-factor for" hint="By highest role in any organization. Includes you, unless Nobody.">
        {(id) => (
          <SelectMenu
            id={id}
            value={setLevel.isPending && setLevel.variables ? setLevel.variables : me.twoFactorLevel}
            choices={twoFactorChoices}
            onChange={(level) => setLevel.mutate(level)}
            className="w-48"
            aria-label="Require two-factor for"
          />
        )}
      </Field>
    </Section>
  );
}

/**
 * The panel's CurseForge API key, which searching and installing from
 * CurseForge needs. It is kept by the panel and never shown again: only
 * whether there is one, to replace or remove.
 */
function CurseForge({ panel }: { panel: PanelInfo }) {
  const qc = useQueryClient();
  const [editing, setEditing] = useState(false);
  const [key, setKey] = useState("");
  const [busy, setBusy] = useState(false);
  const [removing, setRemoving] = useState(false);
  const from = panel.curseforge ?? "";
  const save = async (value: string) => {
    setBusy(true);
    try {
      await api.patch("/panel", { curseforgeKey: value });
      toast.info(value ? "CurseForge key saved" : "CurseForge key removed");
      setEditing(false);
      setKey("");
      setRemoving(false);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not change this.");
    } finally {
      setBusy(false);
      await Promise.all([qc.invalidateQueries({ queryKey: keys.panel }), qc.invalidateQueries({ queryKey: keys.meta })]);
    }
  };
  const asking = editing || from === "";
  return (
    <Section
      title="CurseForge"
      region
      description={
        from === "" ? (
          <>
            Add an API key to find and install mods and modpacks from CurseForge. Get one at{" "}
            <a href="https://console.curseforge.com" target="_blank" rel="noreferrer" className="text-accent hover:underline">
              console.curseforge.com
            </a>
            .
          </>
        ) : from === "install" ? (
          "Mods and modpacks can be found and installed from CurseForge, with the key avSYS was installed with."
        ) : (
          "Mods and modpacks can be found and installed from CurseForge."
        )
      }
    >
      {from !== "" && (
        <div className="flex flex-wrap items-center gap-2">
          <Chip className="border-accent/40 text-accent">key set</Chip>
          <span className="flex-1" />
          {!editing && (
            <Button size="sm" onClick={() => setEditing(true)}>
              Replace
            </Button>
          )}
          {from === "panel" && !editing && (
            <Button size="sm" variant="danger" onClick={() => setRemoving(true)}>
              Remove
            </Button>
          )}
        </div>
      )}
      {asking && (
        <form
          className="flex flex-wrap items-end gap-2"
          onSubmit={(e) => {
            e.preventDefault();
            if (key.trim()) void save(key.trim());
          }}
        >
          <Field label="API key" className="min-w-0 flex-1">
            {(id) => <Input id={id} type="password" autoComplete="off" spellCheck={false} required value={key} onChange={(e) => setKey(e.target.value)} className="font-mono" />}
          </Field>
          <Button type="submit" variant="primary" loading={busy}>
            Save
          </Button>
          {editing && (
            <Button
              onClick={() => {
                setEditing(false);
                setKey("");
              }}
            >
              Cancel
            </Button>
          )}
        </form>
      )}
      <Confirm
        open={removing}
        onOpenChange={setRemoving}
        title="Remove the CurseForge key?"
        description="CurseForge can no longer be searched. Servers keep what they have."
        action="Remove"
        loading={busy}
        onConfirm={() => save("")}
      />
    </Section>
  );
}

/** The accent of anyone who has not picked their own, and of the sign-in pages. */
function PanelAccent({ panel }: { panel: PanelInfo }) {
  const qc = useQueryClient();
  const save = async (accent: Accent) => {
    qc.setQueryData<PanelState>(keys.panel, (d) => (d ? { ...d, panel: { ...d.panel, accent } } : d));
    qc.setQueryData<Meta>(keys.meta, (d) => (d ? { ...d, accent } : d));
    try {
      await api.patch("/panel", { accent });
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not change this.");
    }
    await Promise.all([qc.invalidateQueries({ queryKey: keys.panel }), qc.invalidateQueries({ queryKey: keys.meta })]);
  };
  return (
    <Section title="Accent" region description="For everyone who has not picked their own.">
      <AccentPicker value={panel.accent ?? "amethyst"} onChange={(accent) => void save(accent)} />
    </Section>
  );
}

/** How many days a deleted snapshot may be kept, as offered. */
const keptDays = [0, 1, 3, 7, 14, 30, 90];
const daysWord = (n: number) => (n === 0 ? "Not kept" : n === 1 ? "1 day" : `${n} days`);

/**
 * How long a deleted snapshot waits in Recently deleted before it is
 * removed for good: for every organization, as only the panel's
 * administrator can empty it sooner.
 */
function DeletedSnapshots({ panel }: { panel: PanelInfo }) {
  const qc = useQueryClient();
  const save = async (days: number) => {
    qc.setQueryData<PanelState>(keys.panel, (d) => (d ? { ...d, panel: { ...d.panel, binDays: days } } : d));
    try {
      await api.patch("/panel", { binDays: days });
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not change this.");
    }
    // Pages that say how long a deleted snapshot is kept ask again.
    await Promise.all([qc.invalidateQueries({ queryKey: keys.panel }), qc.invalidateQueries({ queryKey: keys.meta }), qc.invalidateQueries({ queryKey: ["snapshots"] })]);
  };
  // One set through the API to a number not offered is shown too.
  const offered = keptDays.includes(panel.binDays) ? keptDays : [...keptDays, panel.binDays].sort((a, b) => a - b);
  const choices: Choice<string>[] = offered.map((n) => ({ value: String(n), label: daysWord(n), description: n === 0 ? "Deleted at once, for good." : `Can be put back for ${daysWord(n)}.` }));
  return (
    <Section title="Deleted snapshots" region description="Kept in Recently deleted, then removed for good. Only you can remove them sooner.">
      <Field label="Keep for">
        {(id) => <SelectMenu id={id} value={String(panel.binDays)} choices={choices} onChange={(v) => void save(Number(v))} className="w-48" aria-label="Keep deleted snapshots for" />}
      </Field>
    </Section>
  );
}

/** What is set for the whole panel, by its administrator: updates, sign-in, deleted snapshots, CurseForge and its accent. */
export function InstanceSettings() {
  const me = useMe().data;
  const meta = useMeta().data;
  const state = usePanel(!!me?.user.isAdmin);
  if (me && !me.user.isAdmin) return <Empty title="Admins only">Only the panel's administrator can change these.</Empty>;
  if (state.isPending) return <Loading />;
  if (!state.data) return <Notice tone="danger">{state.error instanceof ApiError ? state.error.message : "Could not load the settings."}</Notice>;
  return (
    <>
      <PageHeader title="Instance settings" description="For the whole panel." />
      <Updates state={state.data} />
      {meta && <SignIn meta={meta} />}
      {meta?.snapshots && <DeletedSnapshots panel={state.data.panel} />}
      <CurseForge panel={state.data.panel} />
      <PanelAccent panel={state.data.panel} />
    </>
  );
}
