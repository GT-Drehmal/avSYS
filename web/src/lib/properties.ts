// server.properties entries that the panel gives their own controls.

export const difficulties = [
  ["peaceful", "Peaceful"],
  ["easy", "Easy"],
  ["normal", "Normal"],
  ["hard", "Hard"],
] as const;

export const gameModes = [
  ["survival", "Survival"],
  ["creative", "Creative"],
  ["adventure", "Adventure"],
  ["spectator", "Spectator"],
] as const;

// The spellings every game version reads; newer ones map them to world presets.
export const levelTypes = [
  ["flat", "Flat"],
  ["largebiomes", "Large biomes"],
  ["amplified", "Amplified"],
] as const;

/** Entries the game only reads while it generates a world. */
export const worldKeys = ["level-type", "level-seed", "generate-structures", "hardcore"];

/** key=value lines, as typed into an "other properties" box. */
export function parseProperties(text: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const i = line.indexOf("=");
    if (i > 0) out[line.slice(0, i).trim()] = line.slice(i + 1).trim();
  }
  return out;
}
