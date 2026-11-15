import { useQuery } from "@tanstack/react-query";
import { api, serverPath, type GameLists, type Server } from "../lib/api";
import { keys, useAction } from "../lib/queries";
import { BedrockMark } from "./PlayerLists";
import { Button } from "./ui";
import { lines } from "./ServerFields";

const titles = { whitelist: "the whitelist", ops: "the operators" } as const;

/** A name or UUID as it is compared: in lower case, a UUID without dashes. */
const bare = (s: string) => (/^[0-9a-f-]{32,36}$/i.test(s) ? s.replace(/-/g, "") : s).toLowerCase();

/**
 * What the settings keep a player by: their name, or, for a Bedrock player,
 * their UUID. The image looks a name it does not find on the game's list
 * up with Mojang, which has no Bedrock player.
 */
const kept = (p: { name: string; uuid?: string; bedrock?: boolean }) => (p.bedrock && p.uuid ? p.uuid.toLowerCase() : p.name);

/**
 * Players on one of a server's lists in game that the panel did not put
 * there: added at the console or by an operator. Each can be listed in the
 * settings, or taken off.
 */
export function AddedInGame({ server, list, listed, onKeep }: { server: Server; list: keyof GameLists; listed: string; onKeep: (name: string) => void }) {
  const path = serverPath(server.org, server.name);
  const lists = useQuery({ queryKey: keys.gameLists(server.org, server.name), queryFn: () => api.get<GameLists>(`${path}/lists`), refetchInterval: 30_000 });
  const remove = useAction((name: string) => api.del(`${path}/lists/${list}/${encodeURIComponent(name)}`), [keys.gameLists(server.org, server.name)]);
  // One listed already, saved or not, by name or UUID, is the settings' to keep.
  const named = new Set(lines(listed).map(bare));
  const players = (lists.data?.[list]?.inGame ?? []).filter((p) => !named.has(bare(p.name)) && !(p.uuid && named.has(bare(p.uuid))));
  if (players.length === 0) return null;
  return (
    <div className="flex flex-col gap-1.5">
      <div className="text-xs font-medium text-muted">Added in game</div>
      <ul aria-label={`Added in game to ${titles[list]}`} className="divide-y divide-line rounded-lg border border-line">
        {players.map((p) => {
          // A Bedrock player by their gamertag, without Floodgate's prefix.
          const shown = p.bedrock ? p.name.replace(/^[.*!~+-]/, "") : p.name;
          return (
            <li key={p.name} className="flex items-center justify-between gap-2 py-1 pr-1 pl-3 text-[13px]">
              <span className="flex min-w-0 items-center gap-2">
                <span className="truncate font-mono text-fg">{shown}</span>
                {p.bedrock && <BedrockMark />}
              </span>
              <div className="flex gap-1">
                <Button size="sm" variant="ghost" aria-label={`Keep ${shown} on ${titles[list]}`} onClick={() => onKeep(kept(p))}>
                  Keep
                </Button>
                <Button
                  size="sm"
                  variant="ghost"
                  aria-label={`Remove ${shown} from ${titles[list]}`}
                  loading={remove.isPending && remove.variables === p.name}
                  disabled={remove.isPending}
                  onClick={() => remove.mutate(p.name)}
                >
                  Remove
                </Button>
              </div>
            </li>
          );
        })}
      </ul>
    </div>
  );
}
