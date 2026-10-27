/** Whether every word typed is somewhere in what is said of a thing, whatever the case. */
export function matches(query: string, ...said: (string | null | undefined | false)[]) {
  const words = query.toLowerCase().split(/\s+/).filter(Boolean);
  if (words.length === 0) return true;
  const text = said.filter(Boolean).join(" ").toLowerCase();
  return words.every((w) => text.includes(w));
}
