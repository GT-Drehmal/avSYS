import { Play, RotateCw, Square, Upload } from "lucide-react";
import { useEffect, useState } from "react";
import type { Server } from "../lib/api";
import { useServerAction } from "../lib/queries";
import { useSetups } from "../lib/setups";
import { Button } from "./ui";

/**
 * How long after a restart is asked for it is taken as under way, while the
 * server still says Running: a game saving a large world can take over a
 * minute before it stops answering as ready.
 */
const restartSettles = 120_000;

/**
 * Start/stop/restart for one server, showing whichever apply to its state.
 *
 * Each waits for the one before to take: a game that is loading stops only
 * once it has, which can take a minute, and a button clicked again in the
 * meantime would undo it. Start appears in the place of Stop, so it is only
 * offered once the game has stopped; until then that place says Stopping.
 * Restart is not offered again while one is under way.
 */
export function ServerActions({ server, compact }: { server: Server; compact?: boolean }) {
  const action = useServerAction(server.org, server.name);
  const size = compact ? "sm" : "md";
  const pending = action.isPending ? action.variables : null;
  const phase = server.status.phase;
  const sending = useSetups().some((s) => s.org === server.org && s.server === server.name && s.error === null);
  // Asked for a moment ago: the game is told to stop, and says Running until it has.
  const [restarted, setRestarted] = useState(false);
  useEffect(() => {
    if (!restarted) return;
    const t = setTimeout(() => setRestarted(false), restartSettles);
    return () => clearTimeout(t);
  }, [restarted]);
  useEffect(() => {
    if (phase !== "Running") setRestarted(false);
  }, [phase]);
  const run = (name: "start" | "stop" | "restart") => (e: React.MouseEvent) => {
    // Rows that contain these buttons are links.
    e.preventDefault();
    e.stopPropagation();
    action.mutate(name, { onSuccess: () => name === "restart" && setRestarted(true) });
  };

  // Being given its files from this tab: it is started once they are in.
  if (sending && server.spec.state === "Stopped") {
    return (
      <Button size={size} variant={compact ? "default" : "primary"} icon={<Upload className="size-3.5" />} loading disabled title="It starts once its files are in">
        Sending files
      </Button>
    );
  }
  if (server.spec.state === "Stopped") {
    if (phase !== "Stopped") {
      return (
        <Button size={size} variant={compact ? "default" : "primary"} icon={<Square className="size-3" />} loading disabled title="The server can be started again once it has stopped">
          Stopping
        </Button>
      );
    }
    return (
      <Button size={size} variant={compact ? "default" : "primary"} icon={<Play className="size-3.5" />} loading={pending === "start"} onClick={run("start")}>
        Start
      </Button>
    );
  }
  const asleep = phase === "Sleeping";
  // Starting, stopping to start again, or just asked to: a restart now would begin it over.
  const restarting = phase === "Starting" || phase === "Stopping" || restarted;
  const restartTitle = restarting ? "Already restarting" : "Restart";
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
          <Button size={size} icon={<RotateCw className="size-3.5" />} loading={pending === "restart"} disabled={restarting} onClick={run("restart")} aria-label={`Restart ${server.name}`} title={restartTitle} />
        ) : (
          <Button size={size} icon={<RotateCw className="size-3.5" />} loading={pending === "restart"} disabled={restarting} onClick={run("restart")} title={restarting ? restartTitle : undefined}>
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
