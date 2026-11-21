/** A name or gamertag as it is compared: without case, or Floodgate's prefix. */
const nameKey = (name: string) =>
  name
    .trim()
    .replace(/^[.*!~+-]+/, "")
    .toLowerCase();

/**
 * The names still unchecked after a request: those unchecked before that it
 * did not send, and those it could not check either.
 */
export const stillUnchecked = (was: string[], sent: string[], now: string[]) => {
  const asked = new Set(sent.map(nameKey));
  const out = was.filter((n) => !asked.has(nameKey(n)));
  for (const n of now) if (!out.some((o) => nameKey(o) === nameKey(n))) out.push(n);
  return out;
};
