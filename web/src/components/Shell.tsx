import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Archive, Check, ChevronsUpDown, Cpu, Globe, LayoutTemplate, ListChecks, LogOut, Plus, Server, Settings, SlidersHorizontal, UserRound, Users } from "lucide-react";
import { Suspense, useEffect, useRef, useState } from "react";
import { Link, NavLink, Outlet, useLocation, useMatch, useNavigate } from "react-router";
import { orgIcon, api, atLeast, type Meta, type Org } from "../lib/api";
import { keys, useAction, useCluster, useLiveServers, useMe, useMeta } from "../lib/queries";
import { clearSetups } from "../lib/setups";
import { ErrorBoundary } from "./ErrorBoundary";
import { Picture } from "./Picture";
import { Button, cx, Field, Input, Loading, Menu, MenuContent, MenuItem, MenuLabel, MenuSeparator, MenuTrigger, Modal } from "./ui";

/** The mark's pixels: a quartz gate with amethyst light inside, on a 16 by 16 grid of 2-unit cells. */
const gate =
  "M14 0h4v2h-4ZM12 2h8v2h-8ZM10 4h12v2h-12ZM6 8h20v4h-20ZM8 12h4v16h-4ZM20 12h4v16h-4ZM6 28h20v4h-20Z";
const light = "M14 14h4v12h-4Z";
const S = "M9.6 1.2H4.1A2.9 2.9 0 0 0 4.1 7H5.9A2.9 2.9 0 0 1 5.9 12.8H0.4";

/**
 * The avSYS mark and name. Drawn 32 px high (in px: the page's rem is 14)
 * so each of the mark's cells is two whole pixels; the empty margin left of
 * the gate is cut off, so the gate lines up with what is under it.
 */
export function Wordmark({ className }: { className?: string }) {
  return (
    <svg viewBox="6 0 104.571 32" className={cx("h-[32px] w-auto shrink-0", className)} role="img" aria-label="avSYS">
      <g shapeRendering="crispEdges">
        <path className="fill-quartz" d={gate} />
        <path className="fill-amethyst" d={light} />
      </g>
      <g transform="translate(42 8) scale(1.1429)" strokeWidth="2.4">
        <circle className="fill-none stroke-muted" cx="5" cy="9" r="3.8" />
        <path className="fill-none stroke-muted" d="M8.8 4V14" />
        <path className="fill-muted" transform="translate(12.5 0)" d="M0 4H2.7L5 10.13 7.3 4H10L6.25 14H3.75Z" />
        <path className="fill-none stroke-fg" transform="translate(25 0)" d={S} />
        <path className="fill-fg" transform="translate(37.5 0)" d="M0 0H2.8L5 4.63 7.2 0H10L6.2 8V14H3.8V8Z" />
        <path className="fill-none stroke-fg" transform="translate(50 0)" d={S} />
      </g>
    </svg>
  );
}

const lastOrgKey = "avsys.lastOrg";
export const lastOrg = () => {
  try {
    return localStorage.getItem(lastOrgKey);
  } catch {
    return null;
  }
};

/** The organization named in the URL, if the user belongs to it. */
export function useOrg(): Org | undefined {
  const me = useMe();
  const match = useMatch("/o/:org/*");
  return me.data?.orgs.find((o) => o.slug === match?.params.org);
}

function slugify(name: string) {
  return name.toLowerCase().replace(/[^a-z0-9]+/g, "-").replace(/^-+|-+$/g, "").slice(0, 32);
}

export function NewOrgDialog({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const [name, setName] = useState("");
  const [slug, setSlug] = useState<string | null>(null);
  const navigate = useNavigate();
  const create = useAction((body: { name: string; slug: string }) => api.post<Org>("/orgs", body), [keys.me]);
  const effectiveSlug = slug ?? slugify(name);
  return (
    <Modal open={open} onOpenChange={onOpenChange} title="New organization">
      <form
        className="flex flex-col gap-4"
        onSubmit={(e) => {
          e.preventDefault();
          create.mutate(
            { name, slug: effectiveSlug },
            {
              onSuccess: (org) => {
                onOpenChange(false);
                setName("");
                setSlug(null);
                navigate(`/o/${org.slug}`);
              },
            },
          );
        }}
      >
        <Field label="Name">{(id) => <Input id={id} autoFocus required value={name} onChange={(e) => setName(e.target.value)} />}</Field>
        <Field label="Short name" hint="Used in links and SSH logins. Cannot be changed later.">
          {(id) => <Input id={id} required value={effectiveSlug} onChange={(e) => setSlug(e.target.value)} className="font-mono" pattern="[a-z0-9]([a-z0-9\-]{0,30}[a-z0-9])?" />}
        </Field>
        <div className="flex justify-end gap-2">
          <Button onClick={() => onOpenChange(false)}>Cancel</Button>
          <Button type="submit" variant="primary" loading={create.isPending}>
            Create organization
          </Button>
        </div>
      </form>
    </Modal>
  );
}

function OrgSwitcher({ current }: { current?: Org }) {
  const me = useMe().data!;
  const navigate = useNavigate();
  const [creating, setCreating] = useState(false);
  return (
    <>
      <Menu>
        <MenuTrigger asChild>
          <button className="flex h-9 w-full items-center gap-2 rounded-md border border-line-strong bg-raised px-2.5 text-left text-[13px] font-medium hover:bg-hover">
            {current && <Picture name={current.name} url={orgIcon(current)} size={20} />}
            <span className="flex-1 truncate">{current?.name ?? "Select organization"}</span>
            <ChevronsUpDown className="size-3.5 text-faint" />
          </button>
        </MenuTrigger>
        <MenuContent align="start">
          <MenuLabel>Organizations</MenuLabel>
          {me.orgs.map((o) => (
            <MenuItem key={o.id} onSelect={() => navigate(`/o/${o.slug}`)} icon={o.slug === current?.slug ? <Check /> : null}>
              <span className="flex items-center gap-2">
                <Picture name={o.name} url={orgIcon(o)} size={18} />
                {o.name}
              </span>
            </MenuItem>
          ))}
          {me.user.isAdmin && (
            <>
              <MenuSeparator />
              <MenuItem icon={<Plus />} onSelect={() => setCreating(true)}>
                New organization
              </MenuItem>
            </>
          )}
        </MenuContent>
      </Menu>
      <NewOrgDialog open={creating} onOpenChange={setCreating} />
    </>
  );
}

function NavItem({ to, end, icon, children }: { to: string; end?: boolean; icon: React.ReactNode; children: React.ReactNode }) {
  return (
    <NavLink
      to={to}
      end={end}
      className={({ isActive }) =>
        cx(
          "flex h-8 shrink-0 items-center gap-2.5 rounded-md px-2.5 text-[13px] font-medium transition-colors [&>svg]:size-4",
          isActive ? "bg-raised text-fg [&>svg]:text-accent" : "text-muted hover:bg-raised/60 hover:text-fg",
        )
      }
    >
      {icon}
      {children}
    </NavLink>
  );
}

/**
 * Says when the panel has been updated since this page was loaded: what is
 * open is the version before, until it is reloaded.
 */
function UpdateNotice({ loaded }: { loaded?: string }) {
  // The version this page came with, as first told.
  const first = useRef(loaded);
  if (!first.current && loaded) first.current = loaded;
  const now = useQuery({
    queryKey: ["meta", "now"],
    queryFn: () => api.get<Meta>("/meta"),
    enabled: !!first.current,
    refetchInterval: 5 * 60_000,
    refetchOnWindowFocus: "always",
    retry: false,
  });
  const latest = now.data?.version;
  if (!first.current || !latest || latest === first.current) return null;
  return (
    <div role="status" className="flex items-center justify-center gap-3 border-b border-line bg-accent-wash px-4 py-2 text-[13px]">
      <span>
        avSYS was updated to <span className="font-mono">{latest}</span>.
      </span>
      <button type="button" onClick={() => window.location.reload()} className="font-medium text-accent hover:underline">
        Reload
      </button>
    </div>
  );
}

/**
 * Says when the panel cannot reach the cluster: what it shows of servers is
 * what it last heard.
 */
function ClusterNotice() {
  const cluster = useCluster();
  if (!cluster || cluster.reachable) return null;
  const since = cluster.since && new Date(cluster.since).toLocaleTimeString([], { hour: "numeric", minute: "2-digit" });
  return (
    <div role="alert" className="border-b border-warn/30 bg-warn-wash px-4 py-2 text-center text-[13px] text-warn">
      Can't reach the cluster{since && ` since ${since}`}. Servers may not be as shown.
    </div>
  );
}

export function Shell() {
  const { pathname } = useLocation();
  const me = useMe().data!;
  const meta = useMeta().data;
  const org = useOrg();
  const qc = useQueryClient();
  const navigate = useNavigate();
  useLiveServers();

  useEffect(() => {
    if (!org) return;
    try {
      localStorage.setItem(lastOrgKey, org.slug);
    } catch {
      // Remembering the last organization is a convenience only.
    }
  }, [org]);

  const signOut = async () => {
    await api.post("/auth/logout");
    // What was being sent for them stops with them.
    clearSetups();
    qc.clear();
    navigate("/login");
  };
  // Outside an organization (the account page), the one last used stays the
  // current one: it is what the switcher shows and where the links lead.
  const navOrg = org ?? me.orgs.find((o) => o.slug === lastOrg()) ?? me.orgs[0];

  return (
    <div className="flex min-h-dvh flex-col md:flex-row">
      <aside className="flex shrink-0 flex-col gap-4 border-b border-line bg-surface p-3 md:sticky md:top-0 md:h-dvh md:w-56 md:border-r md:border-b-0">
        <Link to="/" className="flex items-center self-start px-1.5">
          <Wordmark />
        </Link>
        <OrgSwitcher current={navOrg} />
        {navOrg && (
          // Scrolled sideways on a phone, where its links do not fit in a row.
          <nav className="-mx-3 flex gap-0.5 overflow-x-auto px-3 [scrollbar-width:none] md:mx-0 md:flex-col md:overflow-visible md:px-0">
            <NavItem to={`/o/${navOrg.slug}`} end icon={<Server />}>
              Servers
            </NavItem>
            <NavItem to={`/o/${navOrg.slug}/backups`} icon={<Archive />}>
              Backups
            </NavItem>
            <NavItem to={`/o/${navOrg.slug}/templates`} icon={<LayoutTemplate />}>
              Templates
            </NavItem>
            <NavItem to={`/o/${navOrg.slug}/worlds`} icon={<Globe />}>
              Worlds
            </NavItem>
            <NavItem to={`/o/${navOrg.slug}/players`} icon={<ListChecks />}>
              Players
            </NavItem>
            <NavItem to={`/o/${navOrg.slug}/members`} icon={<Users />}>
              Members
            </NavItem>
            {atLeast(navOrg.role, "owner") && (
              <NavItem to={`/o/${navOrg.slug}/settings`} icon={<Settings />}>
                Settings
              </NavItem>
            )}
          </nav>
        )}
        <div className="flex flex-col gap-2 md:mt-auto">
          <Menu>
            <MenuTrigger asChild>
              <button className="flex h-10 w-full items-center gap-2.5 rounded-md px-1.5 text-left hover:bg-raised">
                <Picture name={me.user.username} url={me.user.avatarUrl || undefined} shape="round" />
                <span className="min-w-0 flex-1">
                  <span className="block truncate text-[13px] leading-tight font-medium">{me.user.displayName || me.user.username}</span>
                  {me.user.displayName && <span className="block truncate font-mono text-[11px] leading-tight text-faint">{me.user.username}</span>}
                </span>
              </button>
            </MenuTrigger>
            <MenuContent align="start">
              <MenuItem icon={<UserRound />} onSelect={() => navigate("/account")}>
                Account
              </MenuItem>
              {me.user.isAdmin && (
                <>
                  <MenuItem icon={<Cpu />} onSelect={() => navigate("/nodes")}>
                    Nodes
                  </MenuItem>
                  <MenuItem icon={<SlidersHorizontal />} onSelect={() => navigate("/settings")}>
                    Instance settings
                  </MenuItem>
                </>
              )}
              <MenuSeparator />
              <MenuItem icon={<LogOut />} onSelect={signOut}>
                Sign out
              </MenuItem>
            </MenuContent>
          </Menu>
          {meta && <p className="hidden px-1.5 font-mono text-[10px] text-faint md:block">avSYS {meta.version}</p>}
        </div>
      </aside>
      <main className="min-w-0 flex-1">
        <ClusterNotice />
        <UpdateNotice loaded={meta?.version} />
        <div className="mx-auto max-w-5xl px-5 py-8 md:px-10">
          {/* An error in a page leaves the navigation standing, and going elsewhere clears it. */}
          <ErrorBoundary key={pathname}>
            {/* The frame stays while a page is fetched. */}
            <Suspense fallback={<Loading />}>
              <Outlet />
            </Suspense>
          </ErrorBoundary>
        </div>
      </main>
    </div>
  );
}
