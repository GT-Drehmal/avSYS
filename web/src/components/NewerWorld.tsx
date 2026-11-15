import { newerWorldQuestion, newerWorldWarning } from "../lib/worlds";
import { Confirm } from "./ui";

/**
 * Asked before a server is given a world last opened in a newer game version
 * than it runs, or is put on an older version than its world: the same
 * question everywhere, with what goes ahead anyway as its action.
 */
export function NewerWorldConfirm({
  open,
  onOpenChange,
  world,
  version,
  action,
  more,
  loading,
  onConfirm,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** The version the world was last opened in. */
  world: string;
  /** The version the server is to run. */
  version: string;
  action: string;
  /** What else there is to say where it is asked. */
  more?: string;
  loading?: boolean;
  onConfirm: () => void;
}) {
  return (
    <Confirm
      open={open}
      onOpenChange={onOpenChange}
      title={newerWorldQuestion(world, version)}
      description={more ? `${newerWorldWarning(world, version)} ${more}` : newerWorldWarning(world, version)}
      action={action}
      loading={loading}
      onConfirm={onConfirm}
    />
  );
}
