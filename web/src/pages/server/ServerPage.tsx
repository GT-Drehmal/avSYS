import { ChevronLeft, Code2, Copy, Plug } from "lucide-react";
import { useState } from "react";
import { Link, NavLink, Outlet, useOutletContext, useParams } from "react-router";
import { DuplicateServer } from "../../components/NewFromSnapshot";
import { ServerActions } from "../../components/ServerActions";
import { useOrg } from "../../components/Shell";
import { Button, CopyText, cx, Empty, Modal, Notice, Status, Loading } from "../../components/ui";
import { atLeast, type Meta, type Role, type Server } from "../../lib/api";
import { describeSoftware, displayName, quantity } from "../../lib/format";
import { useMe, useMeta, useServer, useServerAction } from "../../lib/queries";

export interface ServerContext {
  server: Server;
  role: Role;
}

/** The server and the viewer's role, for the tabs rendered inside ServerPage. */
export const useServerContext = () => useOutletContext<ServerContext>();

const sshTarget = (server: Server) => `${server.name}.${server.org}`;

function sshCommand(server: Server, meta: Meta) {
  const port = meta.ssh.port === 22 ? "" : ` -p ${meta.ssh.port}`;
  return `ssh ${sshTarget(server)}@${meta.ssh.host}${port}`;
}

/**
 * A link that opens the server directory in VS Code over Remote SSH. The host
 * is passed as hex-encoded JSON, the form Remote SSH accepts for hosts that
 * need a user and port.
 */
function vscodeURL(server: Server, meta: Meta) {
  const host = JSON.stringify({ hostName: meta.ssh.host, user: sshTarget(server), port: meta.ssh.port });
  const hex = Array.from(new TextEncoder().encode(host), (b) => b.toString(16).padStart(2, "0")).join("");
  return `vscode://vscode-remote/ssh-remote+${hex}/data`;
}

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
          <span className="label-mono">Players join at</span>
          <div className={box}>{server.status.address ? <CopyText value={server.status.address} /> : <span className="text-[13px] text-muted">No address yet. Set one under Settings.</span>}</div>
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
        cx("-mb-px flex h-9 items-center border-b-2 px-0.5 text-[13px] font-medium transition-colors", isActive ? "border-accent text-fg" : "border-transparent text-muted hover:text-fg")
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
  const { org = "", server: name = "" } = useParams();
  const membership = useOrg();
  const { data: server, isPending } = useServer(org, name);
  const action = useServerAction(org, name);
  const [connecting, setConnecting] = useState(false);
  const [duplicating, setDuplicating] = useState(false);
  const meta = useMeta().data;

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
          <h1 className="truncate text-xl font-semibold tracking-tight">{displayName(server)}</h1>
          <Status phase={status.phase} />
        </div>
        {canOperate && (
          <div className="flex items-center gap-2">
            <Button icon={<Plug className="size-3.5" />} onClick={() => setConnecting(true)}>
              Connect
            </Button>
            {/* A copy is made from a snapshot, and making a server is for admins. */}
            {atLeast(role, "admin") && meta?.snapshots && (
              <Button icon={<Copy className="size-3.5" />} onClick={() => setDuplicating(true)}>
                Duplicate
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

      <nav className="mt-6 flex gap-6 border-b border-line">
        <Tab to="." end>
          Console
        </Tab>
        {canOperate && <Tab to="files">Files</Tab>}
        {canOperate && <Tab to="terminal">Terminal</Tab>}
        <Tab to="mods">Mods</Tab>
        <Tab to="backups">Backups</Tab>
        {atLeast(role, "admin") && <Tab to="settings">Settings</Tab>}
      </nav>
      <div className="pt-5">
        <Outlet context={{ server, role } satisfies ServerContext} />
      </div>
      <ConnectDialog server={server} open={connecting} onOpenChange={setConnecting} />
      <DuplicateServer server={server} open={duplicating} onClose={() => setDuplicating(false)} />
    </div>
  );
}
