import type { Loader } from "../lib/api";
import { cx, Field, Input } from "./ui";

/** The kinds of server whose loader comes in builds that can be picked. */
export const hasBuilds = (loader: Loader | null | undefined) => loader === "fabric" || loader === "quilt" || loader === "forge" || loader === "neoforge";

/** Which build of the loader to run; empty for the newest. Shown only for a loader that has builds. */
export function LoaderBuildField({ loader, value, onChange, className }: { loader: Loader | null | undefined; value: string; onChange: (value: string) => void; className?: string }) {
  if (!hasBuilds(loader)) return null;
  return (
    <Field label="Loader build">
      {(id) => <Input id={id} value={value} onChange={(e) => onChange(e.target.value)} placeholder="Newest" className={cx("font-mono", className)} spellCheck={false} autoComplete="off" />}
    </Field>
  );
}
