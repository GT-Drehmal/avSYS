import type { Present } from "../lib/presence";
import { Picture } from "./Picture";

/** The faces of these people, each once, with their names. ring is the colour of what they are on. */
export function Faces({ people, label = "Also here", size = 22, max = 4, ring = "ring-bg" }: { people: Present[]; label?: string; size?: number; max?: number; ring?: string }) {
  const users = [...new Map(people.map((p) => [p.user.id, p.user])).values()];
  if (users.length === 0) return null;
  const names = users.map((u) => u.name).join(", ");
  const more = users.length - max;
  return (
    <span role="img" aria-label={`${label}: ${names}`} title={`${label}: ${names}`} className="flex shrink-0 items-center">
      {users.slice(0, more > 0 ? max - 1 : max).map((u) => (
        <span key={u.id} className={`-ml-1.5 flex rounded-full ring-2 first:ml-0 ${ring}`}>
          <Picture name={u.name} url={u.avatarUrl || undefined} size={size} shape="round" />
        </span>
      ))}
      {more > 0 && (
        <span className={`-ml-1.5 flex items-center justify-center rounded-full bg-raised text-[10px] font-medium text-muted ring-2 ${ring}`} style={{ width: size, height: size }}>
          +{more + 1}
        </span>
      )}
    </span>
  );
}
