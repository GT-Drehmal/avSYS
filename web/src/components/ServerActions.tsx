import { Play, RotateCw, Square } from "lucide-react";
import type { Server } from "../lib/api";
import { useServerAction } from "../lib/queries";
import { Button } from "./ui";

/** Start/stop/restart for one server, showing whichever apply to its state. */
export function ServerActions({ server, compact }: { server: Server; compact?: boolean }) {
  const action = useServerAction(server.org, server.name);
  const size = compact ? "sm" : "md";
  const pending = action.isPending ? action.variables : null;
  const run = (name: "start" | "stop" | "restart") => (e: React.MouseEvent) => {
    // Rows that contain these buttons are links.
    e.preventDefault();
    e.stopPropagation();
    action.mutate(name);
  };

  if (server.spec.state === "Stopped") {
    return (
      <Button size={size} variant={compact ? "default" : "primary"} icon={<Play className="size-3.5" />} loading={pending === "start"} onClick={run("start")}>
        Start
      </Button>
    );
  }
  const asleep = server.status.phase === "Sleeping";
  return (
    <div className="flex items-center gap-2">
      {asleep && (
        <Button size={size} variant={compact ? "default" : "primary"} icon={<Play className="size-3.5" />} loading={pending === "start"} onClick={run("start")}>
          Wake
        </Button>
      )}
      {!asleep &&
        (compact ? (
          // In a list there is room for the icon alone.
          <Button size={size} icon={<RotateCw className="size-3.5" />} loading={pending === "restart"} onClick={run("restart")} aria-label={`Restart ${server.name}`} title="Restart" />
        ) : (
          <Button size={size} icon={<RotateCw className="size-3.5" />} loading={pending === "restart"} onClick={run("restart")}>
            Restart
          </Button>
        ))}
      {!(compact && asleep) && (
        <Button size={size} icon={<Square className="size-3" />} loading={pending === "stop"} onClick={run("stop")}>
          Stop
        </Button>
      )}
    </div>
  );
}
