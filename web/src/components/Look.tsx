import { useQueryClient } from "@tanstack/react-query";
import { Monitor, Moon, Palette, Sun } from "lucide-react";
import { useEffect, type ReactNode } from "react";
import { api, ApiError, type Me } from "../lib/api";
import { accentName, accents, panelAccent, setLook, themes, useLook, type Accent, type Theme } from "../lib/look";
import { keys, useMe, useMeta } from "../lib/queries";
import { toast } from "../lib/toast";
import { cx, MenuRadio, MenuRadioGroup, MenuSub, MenuSubContent, MenuSubTrigger } from "./ui";

const themeIcons: Record<Theme, ReactNode> = { auto: <Monitor />, light: <Sun />, dark: <Moon /> };

/**
 * Shows the panel as the account says, once it is known: until then (and
 * before signing in) as this browser last had it, with the panel's accent
 * where it has none.
 */
export function LookSync() {
  const me = useMe().data;
  const meta = useMeta().data;
  const theme = me?.look?.theme;
  const accent = me?.look?.accent;
  const signedOut = me === null;
  useEffect(() => {
    if (theme) setLook({ theme });
  }, [theme]);
  useEffect(() => {
    if (accent) setLook({ accent });
    else if (accent === "" && meta?.accent) setLook({ accent: meta.accent });
    else if (signedOut && meta?.accent) panelAccent(meta.accent);
  }, [accent, meta?.accent, signedOut]);
  return null;
}

/** Saves a theme or accent to the account, shown at once. An accent of "" is the panel's. */
export function useSetLook() {
  const qc = useQueryClient();
  const meta = useMeta().data;
  return async (change: { theme?: Theme; accent?: Accent | "" }) => {
    setLook({ theme: change.theme, accent: change.accent === "" ? (meta?.accent ?? "amethyst") : change.accent });
    qc.setQueryData<Me | null>(keys.me, (d) => (d ? { ...d, look: { ...d.look, ...change } } : d));
    try {
      await api.patch("/me/look", change);
    } catch (err) {
      toast.error(err instanceof ApiError ? err.message : "Could not save this.");
      await qc.invalidateQueries({ queryKey: keys.me });
    }
  };
}

/** A dot of an accent's colour, in the page's theme. */
export function Swatch({ accent, className }: { accent: Accent; className?: string }) {
  return <span aria-hidden data-accent={accent} className={cx("inline-block size-3 shrink-0 rounded-full bg-accent", className)} />;
}

/** One of a row of choices, of which one is picked. */
export function Choice({ checked, onPick, children }: { checked: boolean; onPick: () => void; children: ReactNode }) {
  return (
    <button
      type="button"
      role="radio"
      aria-checked={checked}
      onClick={onPick}
      className={cx(
        "flex h-8 items-center gap-2 rounded-md border px-3 text-[13px] transition-colors [&>svg]:size-4",
        checked ? "border-accent bg-accent-wash text-fg" : "border-line-strong bg-raised text-muted hover:bg-hover hover:text-fg",
      )}
    >
      {children}
    </button>
  );
}

export function ThemePicker({ value, onChange }: { value: Theme; onChange: (theme: Theme) => void }) {
  return (
    <div role="radiogroup" aria-label="Theme" className="flex flex-wrap gap-2">
      {themes.map((t) => (
        <Choice key={t.id} checked={value === t.id} onPick={() => onChange(t.id)}>
          {themeIcons[t.id]}
          {t.name}
        </Choice>
      ))}
    </div>
  );
}

/**
 * The accents to pick from. With panel, "Default" first: the panel's
 * accent, whichever it is, as value "".
 */
export function AccentPicker<V extends Accent | "">({ value, onChange, panel }: { value: V; onChange: (accent: V) => void; panel?: Accent }) {
  return (
    <div role="radiogroup" aria-label="Accent" className="flex flex-wrap gap-2">
      {panel && (
        <Choice checked={value === ""} onPick={() => onChange("" as V)}>
          <Swatch accent={panel} />
          Default
        </Choice>
      )}
      {accents.map((a) => (
        <Choice key={a.id} checked={value === a.id} onPick={() => onChange(a.id as V)}>
          <Swatch accent={a.id} />
          {a.name}
        </Choice>
      ))}
    </div>
  );
}

/** Theme and accent in the account menu, for a quick change. */
export function LookMenu() {
  const me = useMe().data;
  const meta = useMeta().data;
  const look = useLook();
  const save = useSetLook();
  if (!me) return null;
  const theme = me.look?.theme ?? look.theme;
  const accent = me.look?.accent ?? "";
  const panel = meta?.accent ?? "amethyst";
  return (
    <>
      <MenuSub>
        <MenuSubTrigger icon={themeIcons[look.shown === "light" ? "light" : "dark"]} value={themes.find((t) => t.id === theme)?.name}>
          Theme
        </MenuSubTrigger>
        <MenuSubContent>
          <MenuRadioGroup value={theme} onValueChange={(v) => void save({ theme: v as Theme })}>
            {themes.map((t) => (
              <MenuRadio key={t.id} value={t.id}>
                {t.name}
              </MenuRadio>
            ))}
          </MenuRadioGroup>
        </MenuSubContent>
      </MenuSub>
      <MenuSub>
        <MenuSubTrigger icon={<Palette />} value={<Swatch accent={look.accent} />}>
          Accent
        </MenuSubTrigger>
        <MenuSubContent>
          <MenuRadioGroup value={accent} onValueChange={(v) => void save({ accent: v as Accent | "" })}>
            <MenuRadio value="">
              <Swatch accent={panel} />
              Default
              <span className="text-muted">{accentName(panel)}</span>
            </MenuRadio>
            {accents.map((a) => (
              <MenuRadio key={a.id} value={a.id}>
                <Swatch accent={a.id} />
                {a.name}
              </MenuRadio>
            ))}
          </MenuRadioGroup>
        </MenuSubContent>
      </MenuSub>
    </>
  );
}
