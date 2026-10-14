import { useQueryClient } from "@tanstack/react-query";
import { Archive, Check, ChevronsUpDown, LogOut, Plus, Server, Settings, UserRound, Users } from "lucide-react";
import { useEffect, useState } from "react";
import { Link, NavLink, Outlet, useLocation, useMatch, useNavigate } from "react-router";
import { orgIcon, api, atLeast, type Org } from "../lib/api";
import { keys, useAction, useLiveServers, useMe, useMeta } from "../lib/queries";
import { ErrorBoundary } from "./ErrorBoundary";
import { Picture } from "./Picture";
import { Button, cx, Field, Input, Menu, MenuContent, MenuItem, MenuLabel, MenuSeparator, MenuTrigger, Modal } from "./ui";

export function Wordmark({ className }: { className?: string }) {
  return (
    <span className={cx("inline-flex items-center gap-2 font-mono text-[15px] font-medium tracking-tight", className)}>
      <svg viewBox="0 0 20 18" className="h-[15px] w-auto text-accent" fill="currentColor" aria-hidden>
        <path d="M10 0 20 18h-4.5L10 7.6 4.5 18H0Z" />
        <rect x="7" y="14" width="6" height="2.6" />
      </svg>
      <span>
        <span className="text-muted">av</span>SYS
      </span>
    </span>
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
        <Field label="Short name" hint="Used in links and SSH logins. It cannot be changed later.">
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
          "flex h-8 items-center gap-2.5 rounded-md px-2.5 text-[13px] font-medium transition-colors [&>svg]:size-4",
          isActive ? "bg-raised text-fg [&>svg]:text-accent" : "text-muted hover:bg-raised/60 hover:text-fg",
        )
      }
    >
      {icon}
      {children}
    </NavLink>
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
    qc.clear();
    navigate("/login");
  };
  // Outside an organization (the account page), the one last used stays the
  // current one: it is what the switcher shows and where the links lead.
  const navOrg = org ?? me.orgs.find((o) => o.slug === lastOrg()) ?? me.orgs[0];

  return (
    <div className="flex min-h-dvh flex-col md:flex-row">
      <aside className="flex shrink-0 flex-col gap-4 border-b border-line bg-surface p-3 md:sticky md:top-0 md:h-dvh md:w-56 md:border-r md:border-b-0">
        <Link to="/" className="flex h-8 items-center px-1.5">
          <Wordmark />
        </Link>
        <OrgSwitcher current={navOrg} />
        {navOrg && (
          <nav className="flex gap-0.5 md:flex-col">
            <NavItem to={`/o/${navOrg.slug}`} end icon={<Server />}>
              Servers
            </NavItem>
            <NavItem to={`/o/${navOrg.slug}/backups`} icon={<Archive />}>
              Backups
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
        <div className="mx-auto max-w-5xl px-5 py-8 md:px-10">
          {/* An error in a page leaves the navigation standing, and going elsewhere clears it. */}
          <ErrorBoundary key={pathname}>
            <Outlet />
          </ErrorBoundary>
        </div>
      </main>
    </div>
  );
}
