import { QueryClientProvider } from "@tanstack/react-query";
import { type ComponentType, lazy, StrictMode, Suspense, useEffect } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Navigate, Outlet, Route, Routes, useLocation } from "react-router";
import "./app.css";
import { ErrorBoundary } from "./components/ErrorBoundary";
import { Shell } from "./components/Shell";
import { Setups } from "./components/Setups";
import { Spinner, Toaster } from "./components/ui";
import { queryClient, useMe } from "./lib/queries";

// Each page is fetched as it is needed, so that the first load is the frame
// and the page opened. The rest follow once there is nothing else to do, so
// that going to them is as quick as it was.
const pages = {
  Account: () => import("./pages/Account"),
  Backups: () => import("./pages/Backups"),
  Console: () => import("./pages/server/Console"),
  Home: () => import("./pages/Home"),
  Invite: () => import("./pages/Invite"),
  Login: () => import("./pages/Login"),
  Members: () => import("./pages/Members"),
  Mods: () => import("./pages/server/Mods"),
  NewServer: () => import("./pages/NewServer"),
  Nodes: () => import("./pages/Nodes"),
  OrgSettings: () => import("./pages/OrgSettings"),
  ServerPage: () => import("./pages/server/ServerPage"),
  Servers: () => import("./pages/Servers"),
  Settings: () => import("./pages/server/Settings"),
  Snapshots: () => import("./pages/server/Snapshots"),
  TemplateForm: () => import("./pages/TemplateForm"),
  Templates: () => import("./pages/Templates"),
  Usage: () => import("./pages/server/Usage"),
  Worlds: () => import("./pages/Worlds"),
};
const page = <K extends keyof typeof pages>(name: K) => lazy(() => pages[name]().then((m) => ({ default: (m as unknown as Record<K, ComponentType>)[name] })));

const Account = page("Account");
const Backups = page("Backups");
const Console = page("Console");
const Home = page("Home");
const Invite = page("Invite");
const Login = page("Login");
const Members = page("Members");
const Mods = page("Mods");
const NewServer = page("NewServer");
const Nodes = page("Nodes");
const OrgSettings = page("OrgSettings");
const ServerPage = page("ServerPage");
const Servers = page("Servers");
const Settings = page("Settings");
const Snapshots = page("Snapshots");
const TemplateForm = page("TemplateForm");
const Templates = page("Templates");
const Usage = page("Usage");
const Worlds = page("Worlds");

// The editor and terminal pull in large libraries: not until they are opened.
const Files = lazy(() => import("./pages/server/Files"));
const Terminal = lazy(() => import("./pages/server/Terminal"));

/** Fetches the other pages when the browser is idle, unless asked to save data. */
function fetchPages() {
  if ((navigator as Navigator & { connection?: { saveData?: boolean } }).connection?.saveData) return;
  const fetchAll = () => {
    for (const load of Object.values(pages)) void load().catch(() => undefined);
  };
  if ("requestIdleCallback" in window) requestIdleCallback(fetchAll, { timeout: 5000 });
  else setTimeout(fetchAll, 2000);
}

// A page that could not be fetched is seen to by the error boundary around
// it, which loads the panel again. Nothing else may do that: a page fetched
// ahead fails all the time, as when it is left midway, and that must not
// reload the one open, with whatever has been typed into it.

function RequireAuth() {
  const me = useMe();
  const location = useLocation();
  const signedIn = !!me.data;
  useEffect(() => {
    if (signedIn) fetchPages();
  }, [signedIn]);
  if (me.isPending) return <div className="flex h-dvh items-center justify-center"><Spinner /></div>;
  if (!me.data) return <Navigate to={`/login?next=${encodeURIComponent(location.pathname + location.search)}`} replace />;
  return <Outlet />;
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <ErrorBoundary full>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <Suspense fallback={<div className="flex h-dvh items-center justify-center"><Spinner /></div>}>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route path="/invite/:token" element={<Invite />} />
          <Route element={<RequireAuth />}>
            <Route element={<Shell />}>
              <Route index element={<Home />} />
              <Route path="account" element={<Account />} />
              <Route path="nodes" element={<Nodes />} />
              <Route path="o/:org">
                <Route index element={<Servers />} />
                <Route path="new" element={<NewServer />} />
                <Route path="backups" element={<Backups />} />
                <Route path="templates" element={<Templates />} />
                <Route path="templates/new" element={<TemplateForm />} />
                <Route path="templates/:template/edit" element={<TemplateForm />} />
                <Route path="worlds" element={<Worlds />} />
                <Route path="members" element={<Members />} />
                <Route path="settings" element={<OrgSettings />} />
                <Route path="s/:server" element={<ServerPage />}>
                  <Route index element={<Console />} />
                  <Route path="files/*" element={<Files />} />
                  <Route path="terminal" element={<Terminal />} />
                  <Route path="content" element={<Mods />} />
                  <Route path="usage" element={<Usage />} />
                  {/* Where it was before it was called Content. */}
                  <Route path="mods" element={<Navigate to="../content" replace />} />
                  <Route path="backups" element={<Snapshots />} />
                  <Route path="settings" element={<Settings />} />
                </Route>
              </Route>
              <Route path="*" element={<Navigate to="/" replace />} />
            </Route>
          </Route>
        </Routes>
        </Suspense>
        <Toaster>
          <Setups />
        </Toaster>
      </BrowserRouter>
    </QueryClientProvider>
    </ErrorBoundary>
  </StrictMode>,
);
