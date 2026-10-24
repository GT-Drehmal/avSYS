import { useState } from "react";
import { didWhat, whoDid } from "../lib/activity";
import { ago } from "../lib/format";
import { useActivity } from "../lib/queries";
import { Picture } from "./Picture";
import { Modal } from "./ui";

/** The last thing done to a server, and who did it; pressed, the ones before. */
export function ActivityLine({ org, server }: { org: string; server: string }) {
  const activity = useActivity(org, server).data;
  const [open, setOpen] = useState(false);
  const last = activity?.[0];
  if (!last) return null;
  return (
    <>
      <button type="button" onClick={() => setOpen(true)} className="w-fit max-w-full truncate text-left text-xs text-muted hover:text-fg">
        {whoDid(last)} {didWhat(last)} · {ago(last.at)}
      </button>
      <Modal open={open} onOpenChange={setOpen} title="Activity">
        <ul aria-label="Activity" className="-mx-1 flex max-h-[60dvh] flex-col overflow-y-auto">
          {activity.map((a, i) => (
            <li key={`${a.at}-${i}`} className="flex items-center gap-3 rounded-md px-1 py-2 text-[13px]">
              <Picture name={whoDid(a)} url={a.user?.avatarUrl || undefined} size={24} shape="round" />
              <span className="min-w-0 flex-1 break-words">
                <span className="font-medium">{whoDid(a)}</span> {didWhat(a)}
              </span>
              <span className="shrink-0 text-xs text-faint" title={new Date(a.at).toLocaleString()}>
                {ago(a.at)}
              </span>
            </li>
          ))}
        </ul>
      </Modal>
    </>
  );
}
