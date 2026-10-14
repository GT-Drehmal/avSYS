import { difficulties, gameModes } from "../lib/properties";
import { Field, Input, Select } from "./ui";

/** The server.properties entries that have a control of their own here. */
export interface Gameplay {
  difficulty: string;
  gamemode: string;
  maxPlayers: string;
  pvp: string;
}

export const gameplayKeys: Record<keyof Gameplay, string> = { difficulty: "difficulty", gamemode: "gamemode", maxPlayers: "max-players", pvp: "pvp" };

export const gameplayOf = (properties: Record<string, string> | undefined): Gameplay => ({
  difficulty: properties?.["difficulty"] ?? "",
  gamemode: properties?.["gamemode"] ?? "",
  maxPlayers: properties?.["max-players"] ?? "",
  pvp: properties?.["pvp"] ?? "",
});

export const triState = (
  <>
    <option value="">Server default</option>
    <option value="true">On</option>
    <option value="false">Off</option>
  </>
);

/**
 * Difficulty, game mode, player limit and PvP, for a server that exists or
 * is being made of one. Each left on its default leaves server.properties
 * as it is. Meant for a two-column grid.
 */
export function GameplayFields({ value, onChange }: { value: Gameplay; onChange: (key: keyof Gameplay, value: string) => void }) {
  return (
    <>
      <Field label="Difficulty">
        {(id) => (
          <Select id={id} value={value.difficulty} onChange={(e) => onChange("difficulty", e.target.value)}>
            <option value="">Server default</option>
            {difficulties.map(([v, label]) => (
              <option key={v} value={v}>
                {label}
              </option>
            ))}
          </Select>
        )}
      </Field>
      <Field label="Game mode">
        {(id) => (
          <Select id={id} value={value.gamemode} onChange={(e) => onChange("gamemode", e.target.value)}>
            <option value="">Server default</option>
            {gameModes.map(([v, label]) => (
              <option key={v} value={v}>
                {label}
              </option>
            ))}
          </Select>
        )}
      </Field>
      <Field label="Player limit">{(id) => <Input id={id} type="number" min={1} value={value.maxPlayers} onChange={(e) => onChange("maxPlayers", e.target.value)} placeholder="20" />}</Field>
      <Field label="PvP">
        {(id) => (
          <Select id={id} value={value.pvp} onChange={(e) => onChange("pvp", e.target.value)}>
            {triState}
          </Select>
        )}
      </Field>
    </>
  );
}

/** From which role members are operators in game. */
export const autoOpOptions = (
  <>
    <option value="operator">Operators and above</option>
    <option value="admin">Admins and above</option>
    <option value="owner">Owners</option>
    <option value="off">Nobody</option>
  </>
);

/** From which role members are on the whitelist, short of nobody. */
export const memberRoleOptions = (
  <>
    <option value="viewer">Every member</option>
    <option value="operator">Operators and above</option>
    <option value="admin">Admins and above</option>
    <option value="owner">Owners</option>
  </>
);

/** How players reach a server. */
export const exposeOptions = (
  <>
    <option value="Router">Shared address, routed by hostname</option>
    <option value="LoadBalancer">Its own IP address (LoadBalancer)</option>
    <option value="NodePort">A port on every node (NodePort)</option>
    <option value="None">Inside the cluster only</option>
  </>
);
