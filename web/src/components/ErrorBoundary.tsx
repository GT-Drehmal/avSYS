import { Component, type ErrorInfo, type ReactNode } from "react";
import { Button } from "./ui";

/**
 * Whether an error is a part of the app failing to load. That happens to a
 * tab that was open while the panel was updated: the part it asks for
 * belongs to the version before, and is gone.
 */
export function isLoadFailure(error: unknown) {
  const message = error instanceof Error ? error.message : String(error);
  return /dynamically imported module|Importing a module script failed|module script|Unable to preload/i.test(message);
}

const reloadedAt = "avsys.reloadedAt";

/**
 * Loads the page again to pick up the current version, unless that was just
 * tried: if it did not help the first time, doing it again would only loop.
 */
export function reloadOnce() {
  try {
    const last = Number(sessionStorage.getItem(reloadedAt) ?? 0);
    if (Date.now() - last < 30_000) return false;
    sessionStorage.setItem(reloadedAt, String(Date.now()));
  } catch {
    // With nowhere to note that it was tried, it could not be known the
    // second time round that it had been, and the page would reload for
    // ever. The message with its Reload button is shown instead.
    return false;
  }
  window.location.reload();
  return true;
}

interface Props {
  children: ReactNode;
  /** Fills the window, for the boundary around the whole app. */
  full?: boolean;
}

/**
 * Stands between an error in one part of the page and the rest of it. With
 * no boundary React takes everything down and leaves an empty window.
 */
export class ErrorBoundary extends Component<Props, { failed: boolean; error: unknown }> {
  // Whether something was thrown is kept apart from what was: anything can
  // be thrown, null included.
  state: { failed: boolean; error: unknown } = { failed: false, error: null };

  static getDerivedStateFromError(error: unknown) {
    return { failed: true, error };
  }

  componentDidCatch(error: unknown, info: ErrorInfo) {
    console.error(error, info.componentStack);
    if (isLoadFailure(error)) reloadOnce();
  }

  render() {
    const { failed, error } = this.state;
    if (!failed) return this.props.children;
    const stale = isLoadFailure(error);
    return (
      <div role="alert" className={this.props.full ? "flex min-h-dvh items-center justify-center p-6" : "py-10"}>
        <div className="mx-auto flex max-w-md flex-col gap-3 rounded-lg border border-line bg-surface p-5">
          <h2 className="text-[15px] font-semibold">{stale ? "This page needs reloading" : "Something went wrong on this page"}</h2>
          <p className="text-[13px] leading-relaxed text-muted">
            {stale
              ? "The panel was updated while this tab was open, and the part of it you asked for has changed."
              : "The rest of the panel is fine. Reloading usually clears it; if it comes back, it is a bug worth reporting."}
          </p>
          {!stale && <p className="font-mono text-xs break-words text-faint">{error instanceof Error ? error.message : String(error)}</p>}
          <div className="flex gap-2">
            <Button variant="primary" onClick={() => window.location.reload()}>
              Reload
            </Button>
            {!stale && <Button onClick={() => this.setState({ failed: false, error: null })}>Try again</Button>}
          </div>
        </div>
      </div>
    );
  }
}
