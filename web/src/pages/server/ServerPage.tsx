import { Camera, ChevronLeft, Code2, Copy, Plug } from "lucide-react";
import { useState } from "react";
import { Link, NavLink, Outlet, useLocation, useNavigate, useOutletContext, useParams, useSearchParams } from "react-router";
import { newFrom } from "../../lib/origin";
import { ActivityLine } from "../../components/ActivityLine";
import { Faces } from "../../components/Faces";
import { Picture } from "../../components/Picture";
import { ServerActions } from "../../components/ServerActions";
import { holdsTheServer, OperationRow } from "../../components/Snapshots";
import { useOrg } from "../../components/Shell";
import { Button, CopyText, cx, Empty, Modal, Notice, Status, Loading } from "../../components/ui";
import { api, atLeast, serverIcon, serverPath, type Operation, type Role, type Server } from "../../lib/api";
import { describeSoftware, displayName, quantity } from "../../lib/format";
import { keys, useAction, useMe, useMeta, useRunning, useServer, useServerAction, useServerSnapshots } from "../../lib/queries";
import { others, useHere, usePresence } from "../../lib/presence";
import { sshCommand, vscodeURL } from "../../lib/ssh";
import { toast } from "../../lib/toast";
import { ErrorBoundary } from "../../components/ErrorBoundary";

export interface ServerContext {
  server: Server;
  role: Role;
}

/** The server and the viewer's role, for the tabs rendered inside ServerPage. */
export const useServerContext = () => useOutletContext<ServerContext>();

function ConnectDialog({ server, open, onOpenChange }: { server: Server; open: boolean; onOpenChange: (o: boolean) => void }) {
  const meta = useMeta().data;
  const me = useMe().data;
  if (!meta) return null;
  const row = "flex flex-col gap-1.5";
  const box = "flex min-h-9 items-center rounded-md border border-line bg-bg px-3 py-1.5";
  return (
    <Modal open={open} onOpenChange={onOpenChange} title="Connect">
      <div className="flex flex-col gap-5">
        <div className={row}>
          {/* Named as the game names it, where it is pasted. */}
          <span className="label-mono">Server address</span>
          <div className={box}>{server.status.address ? <CopyText value={server.status.address} /> : <span className="text-[13px] text-muted">No address yet. Set one under Settings.</span>}</div>
          {server.status.address && <p className="text-xs text-muted">In Minecraft: Multiplayer, then Add Server.</p>}
        </div>
        <div className={row}>
          <span className="label-mono">SSH</span>
          <div className={box}>
            <CopyText value={sshCommand(server, meta)} wrap />
          </div>
          <p className="text-xs leading-relaxed text-muted">
            Uses an SSH key from{" "}
            <Link to="/account" className="text-accent hover:underline">
              your account
            </Link>
            .
          </p>
        </div>
        <div className="flex flex-wrap gap-2">
          <a href={vscodeURL(server, meta)} className="inline-flex h-8 items-center gap-1.5 rounded-md bg-accent px-3 text-[13px] font-medium text-accent-ink hover:bg-accent-strong">
            <Code2 className="size-4" />
            Open in VS Code
          </a>
        </div>
        {me && <p className="text-xs text-faint">Connecting as {me.user.username}</p>}
      </div>
    </Modal>
  );
}

function Tab({ to, end, children }: { to: string; end?: boolean; children: React.ReactNode }) {
  return (
    <NavLink
      to={to}
      end={end}
      className={({ isActive }) =>
        cx("flex h-9 shrink-0 items-center px-0.5 text-[13px] font-medium transition-colors", isActive ? "text-fg shadow-[inset_0_-2px_0_var(--color-accent)]" : "text-muted hover:text-fg")
      }
    >
      {children}
    </NavLink>
  );
}

function Fact({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex min-w-0 flex-col gap-0.5">
      <dt className="label-mono">{label}</dt>
      <dd className="truncate text-[13px]">{children}</dd>
    </div>
  );
}

export function ServerPage() {
  const { pathname } = useLocation();
  const { org = "", server: name = "" } = useParams();
  const [search] = useSearchParams();
  // Here, with the file open in its editor, if one is: on its Files tab,
  // in whichever folder.
  useHere(org, name, pathname.split("/")[5] === "files" ? (search.get("file") ?? "") : "");
  const here = others(usePresence(org), useMe().data?.user.id, name);
  const membership = useOrg();
  const { data: server, isPending } = useServer(org, name);
  const action = useServerAction(org, name);
  const [connecting, setConnecting] = useState(false);
  const navigate = useNavigate();
  const meta = useMeta().data;
  // A server being made from a snapshot, or restored, is held stopped until
  // its files are in, and says so. How far along that is shows on every tab.
  const held = server?.status.phase === "Stopped" && !!server.status.message;
  // Asked for while it is held; what is already known of it, as after a
  // restore was started from its Backups tab, is shown from the start.
  const snapshots = useServerSnapshots(org, name, held);
  const holding = snapshots.data?.operations.find(holdsTheServer);
  // Whether something is being done to its snapshots: as the event stream
  // has said, on any tab, or as its list says where that was loaded.
  const running = useRunning(org, name);
  const busy = !!running || (snapshots.data?.operations.length ?? 0) > 0;
  // Once started, the lists are asked for again and show it as running. The
  // answer is not put into them directly: an operation that failed at once
  // may already have been reported as over, and would be shown as running
  // for ever.
  const snapshot = useAction(() => api.post<Operation>(`${serverPath(org, name)}/snapshots`), [keys.serverSnapshots(org, name), keys.snapshots(org)]);

  if (isPending) return <Loading />;
  if (!server || !membership) {
    return (
      <Empty title="Server not found" action={<Link to={`/o/${org}`} className="text-[13px] text-accent hover:underline">Back to servers</Link>}>
        It may have been deleted, or you may not have access to it.
      </Empty>
    );
  }
  const role = membership.role;
  const canOperate = atLeast(role, "operator");
  const { status, spec } = server;
  const players = status.players;

  return (
    <div className="flex flex-col">
      <Link to={`/o/${org}`} className="mb-3 inline-flex w-fit items-center gap-1 text-xs text-muted hover:text-fg">
        <ChevronLeft className="size-3.5" />
        Servers
      </Link>
      <header className="flex flex-wrap items-center justify-between gap-x-6 gap-y-3">
        <div className="flex min-w-0 items-center gap-4">
          <Picture name={displayName(server)} url={serverIcon(server)} size={36} retryKey={`${status.phase}|${status.message ?? ""}`} />
          {/* What was last done to it, under its name. */}
          <div className="flex min-w-0 flex-col">
            <div className="flex min-w-0 items-center gap-4">
              <h1 className="truncate text-xl font-semibold tracking-tight">{displayName(server)}</h1>
              <Status phase={status.phase} />
              <Faces people={here} />
            </div>
            <ActivityLine org={org} server={name} />
          </div>
        </div>
        {canOperate && (
          <div className="flex flex-wrap items-center gap-2">
            <Button icon={<Plug className="size-3.5" />} onClick={() => setConnecting(true)}>
              Connect
            </Button>
            {/* A copy is made from a snapshot, and making a server is for admins. */}
            {atLeast(role, "admin") && meta?.snapshots && (
              <Button icon={<Copy className="size-3.5" />} onClick={() => navigate(newFrom(server.org, { kind: "server", name: server.name }))}>
                Duplicate
              </Button>
            )}
            {meta?.snapshots && (
              <Button
                icon={<Camera className="size-3.5" />}
                aria-label="Snapshot"
                disabled={busy}
                loading={snapshot.isPending}
                onClick={() => snapshot.mutate(undefined, { onSuccess: () => toast.info(`Taking a snapshot of ${server.name}`) })}
              >
                {/* Its picture alone on a phone, where the row is narrow. */}
                <span className="max-sm:hidden">Snapshot</span>
              </Button>
            )}
            <ServerActions server={server} />
          </div>
        )}
      </header>

      <dl className="mt-5 grid grid-cols-2 gap-x-8 gap-y-4 rounded-lg border border-line bg-surface px-4 py-3.5 sm:grid-cols-4">
        <Fact label="Address">{status.address ? <CopyText value={status.address} /> : <span className="text-muted">Not set</span>}</Fact>
        <Fact label="Software">
          {describeSoftware(server)}
          {server.java.effective > 0 && <span className="ml-2 text-muted">Java {server.java.effective}</span>}
        </Fact>
        <Fact label="Players">
          {status.phase === "Running" && players ? (
            <span title={players.names?.join(", ")}>
              <span className="font-mono tabular-nums">
                {players.online}
                <span className="text-faint"> / {players.max}</span>
              </span>
              {players.names && players.names.length > 0 && <span className="ml-2 text-muted">{players.names.join(", ")}</span>}
            </span>
          ) : (
            <span className="text-faint">–</span>
          )}
        </Fact>
        <Fact label="Memory">
          {quantity(spec.resources.memory)}
          {status.node && <span className="ml-2 text-muted">on {status.node}</span>}
        </Fact>
      </dl>

      <div className="mt-3 flex flex-col gap-2 empty:hidden">
        {status.message && <Notice tone={status.phase === "Error" ? "danger" : "info"}>{status.message}</Notice>}
        {holding && <OperationRow op={holding} />}
        {status.snapshots?.error && (
          <Notice
            tone="danger"
            action={
              <Link to="backups" className="text-xs font-medium underline-offset-2 hover:underline">
                Backups
              </Link>
            }
          >
            The last snapshot or restore failed: {status.snapshots.error}
          </Notice>
        )}
        {status.pendingRestart && (
          <Notice
            action={
              canOperate && (
                <Button size="sm" loading={action.isPending && action.variables === "restart"} onClick={() => action.mutate("restart")}>
                  Restart now
                </Button>
              )
            }
          >
            Restart to apply pending changes.
          </Notice>
        )}
      </div>

      {/* Scrolled sideways where the tabs do not fit, as on a phone. The line
          under them is drawn inside, so that scrolling does not cut the
          line under the tab that is open. */}
      <nav className="mt-6 flex gap-6 overflow-x-auto shadow-[inset_0_-1px_0_var(--color-line)] [scrollbar-width:none] max-sm:gap-4">
        <Tab to="." end>
          Console
        </Tab>
        {canOperate && <Tab to="files">Files</Tab>}
        {canOperate && <Tab to="terminal">Terminal</Tab>}
        <Tab to="content">Content</Tab>
        <Tab to="usage">Usage</Tab>
        <Tab to="backups">Backups</Tab>
        {atLeast(role, "admin") && <Tab to="settings">Settings</Tab>}
      </nav>
      <div className="pt-5">
        {/* An error in one tab leaves the server's name and its other tabs standing. */}
        <ErrorBoundary key={pathname}>
          <Outlet context={{ server, role } satisfies ServerContext} />
        </ErrorBoundary>
      </div>
      <ConnectDialog server={server} open={connecting} onOpenChange={setConnecting} />
    </div>
  );
}
