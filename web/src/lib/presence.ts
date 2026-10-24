import { useQuery } from "@tanstack/react-query";
import { useEffect } from "react";
import { api } from "./api";

// Which server's pages this tab is on, told to the panel so that the other
// members see who is there. The panel forgets a tab when its event stream
// closes, or when it has not heard from it in a minute.

/** This tab, to the panel. */
export const tab = `${Math.random().toString(36).slice(2, 10)}${Date.now().toString(36)}`;

/** Someone on a server's pages, and the file they have open there. */
export interface Present {
  user: { id: string; name: string; avatarUrl?: string };
  server: string;
  file?: string;
}

export const presenceKey = (org: string) => ["presence", org] as const;

let place: { org: string; server: string; file: string } | null = null;
// One at a time, in order: a server left is not told after the next one.
let told: Promise<unknown> = Promise.resolve();

function tell(org: string, server: string, file: string) {
  told = told.then(() => api.put(`/orgs/${org}/presence`, { tab, server, file }).catch(() => undefined));
}

/** Tells the panel again where this tab is, as after its stream came back. */
export function tellAgain() {
  if (place) tell(place.org, place.server, place.file);
}

/** Says this tab is on a server's pages, and which file it has open there. */
export function useHere(org: string, server: string, file = "") {
  useEffect(() => {
    if (!org || !server) return;
    place = { org, server, file };
    tell(org, server, file);
    const every = setInterval(tellAgain, 25_000);
    return () => {
      clearInterval(every);
      place = null;
      // Left for another page: if that is not one of a server, it is said.
      setTimeout(() => {
        if (!place) tell(org, "", "");
      });
    };
  }, [org, server, file]);
}

/** Who is on an organization's servers, as its event stream says. */
export const usePresence = (org: string) => useQuery({ queryKey: presenceKey(org), queryFn: () => [] as Present[], enabled: false, staleTime: Infinity }).data ?? [];

/** Those other than me on a server, and, with a file, on that file. */
export const others = (people: Present[], me: string | undefined, server: string, file?: string) =>
  people.filter((p) => p.user.id !== me && p.server === server && (file === undefined || p.file === file));
