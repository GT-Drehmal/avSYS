import { X } from "lucide-react";
import { Link } from "react-router";
import { useMe } from "../lib/queries";
import { dismissSetup, stopSetup, useSetups } from "../lib/setups";
import { cx, Spinner } from "./ui";
import { UploadProgress } from "./Uploading";

/** New servers being given their files from here, wherever in the panel one is. */
export function Setups() {
  const setups = useSetups();
  // Only for whoever is signed in: signing out clears them.
  const me = useMe();
  if (!me.data) return null;
  return setups.map((s) => (
    <section
      key={s.id}
      aria-label={`Setting up ${s.title}`}
      className={cx(
        "pointer-events-auto flex animate-rise flex-col gap-1.5 rounded-md border bg-surface px-3 py-2.5 text-[13px] shadow-xl shadow-black/50",
        s.error ? "border-danger/40" : "border-line-strong",
      )}
    >
      <div className="flex items-center gap-2">
        <Link to={`/o/${s.org}/s/${s.server}`} className="min-w-0 flex-1 truncate font-medium hover:underline">
          {s.title}
        </Link>
        {s.error && (
          <button onClick={() => dismissSetup(s.id)} className="shrink-0 text-faint hover:text-fg" aria-label="Dismiss">
            <X className="size-3.5" />
          </button>
        )}
      </div>
      {s.error ? (
        <>
          <p className="text-muted wrap-break-word">{s.error}</p>
          {s.fix && (
            <Link to={s.fix} onClick={() => dismissSetup(s.id)} className="w-fit text-xs text-accent hover:underline">
              Open it
            </Link>
          )}
        </>
      ) : s.progress ? (
        <>
          <p className="truncate text-xs">{s.stage}</p>
          <UploadProgress sending={{ label: "", progress: s.progress }} onCancel={() => stopSetup(s.id)} leaving="" />
        </>
      ) : (
        <p className="flex items-center gap-2 text-xs text-muted" role="status">
          <Spinner className="size-3.5 shrink-0" />
          <span className="min-w-0 flex-1 truncate">{s.stage}</span>
          {s.stoppable && (
            <button type="button" className="shrink-0 text-accent hover:underline" onClick={() => stopSetup(s.id)}>
              Cancel
            </button>
          )}
        </p>
      )}
      {!s.error && <p className="text-[11px] text-faint">Closing this tab stops it.</p>}
    </section>
  ));
}
