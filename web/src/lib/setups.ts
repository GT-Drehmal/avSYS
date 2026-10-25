import { useSyncExternalStore } from "react";
import { ApiError, upload, type Progress } from "./api";

/**
 * What a new server is given from this computer once it is made: an archive
 * to unpack, files to upload, then a start. The form hands it over and moves
 * on, and it goes on while the panel is open, shown in a card of its own,
 * whichever page is. Only closing the tab stops it, and the page asks first.
 */
export interface Setup {
  id: number;
  org: string;
  server: string;
  /** The server's name as people know it. */
  title: string;
  /** What it is doing now. */
  stage: string;
  /** How far the file being sent is, while one is. */
  progress: Progress | null;
  /** Whether what it is doing now can be stopped from here. */
  stoppable: boolean;
  /** Why it stopped, once it has. It stays until dismissed. */
  error: string | null;
  /** Where to go to put it right, with the error. */
  fix?: string;
}

/** What a setup's work is given: a way to say what it does, and to send a file. */
export interface SetupWork {
  say: (stage: string, stoppable?: boolean) => void;
  send: <T>(label: string, path: string, file: Blob, method?: "PUT" | "POST") => Promise<T>;
  signal: AbortSignal;
}

/** A setup that could not finish, saying why and where to put it right. */
export class SetupFailed extends Error {
  constructor(
    message: string,
    readonly fix?: string,
  ) {
    super(message);
  }
}

let setups: Setup[] = [];
let nextId = 1;
const stops = new Map<number, AbortController>();
const listeners = new Set<() => void>();
const emit = () => listeners.forEach((l) => l());

const update = (id: number, change: Partial<Setup>) => {
  setups = setups.map((s) => (s.id === id ? { ...s, ...change } : s));
  emit();
};

/** Whether anything is still being sent or waited for. */
export const working = () => setups.some((s) => s.error === null);

// Closing or reloading the tab would cut it off: the browser asks first.
if (typeof window !== "undefined") {
  window.addEventListener("beforeunload", (e) => {
    if (working()) e.preventDefault();
  });
}

/**
 * Runs a new server's setup apart from the page that started it. work
 * resolves once it is done, or throws: a SetupFailed, or what went wrong.
 */
export function startSetup(org: string, server: string, title: string, work: (w: SetupWork) => Promise<void>, onDone?: (ok: boolean) => void) {
  const id = nextId++;
  const ctl = new AbortController();
  stops.set(id, ctl);
  setups = [...setups, { id, org, server, title, stage: "Getting ready", progress: null, stoppable: false, error: null }];
  emit();
  const w: SetupWork = {
    say: (stage, stoppable = false) => update(id, { stage, progress: null, stoppable }),
    send: async <T>(label: string, path: string, file: Blob, method: "PUT" | "POST" = "PUT") => {
      update(id, { stage: label, progress: { sent: 0, total: file.size }, stoppable: true });
      try {
        return await upload<T>(path, file, (progress) => update(id, { progress }), ctl.signal, method);
      } finally {
        update(id, { progress: null, stoppable: false });
      }
    },
    signal: ctl.signal,
  };
  void work(w)
    .then(() => {
      setups = setups.filter((s) => s.id !== id);
      emit();
      return true;
    })
    .catch((err) => {
      const message = err instanceof ApiError && err.code === "aborted" ? "The upload was stopped." : err instanceof Error ? err.message : "Something went wrong.";
      // Cleared meanwhile, as by signing out: nothing is left to say it to.
      if (!stops.has(id)) return false;
      update(id, { error: message, fix: err instanceof SetupFailed ? err.fix : undefined, progress: null, stoppable: false });
      return false;
    })
    .then((ok) => {
      stops.delete(id);
      onDone?.(ok);
    });
}

/** Stops a setup's upload. What was already sent stays. */
export const stopSetup = (id: number) => stops.get(id)?.abort();

/** Stops and forgets every setup, as when signing out. */
export function clearSetups() {
  for (const ctl of stops.values()) ctl.abort();
  stops.clear();
  setups = [];
  emit();
}

export function dismissSetup(id: number) {
  setups = setups.filter((s) => s.id !== id);
  emit();
}

export const useSetups = () =>
  useSyncExternalStore(
    (l) => {
      listeners.add(l);
      return () => listeners.delete(l);
    },
    () => setups,
  );
