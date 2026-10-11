import { QueryClientProvider } from "@tanstack/react-query";
import { lazy, StrictMode, Suspense } from "react";
import { createRoot } from "react-dom/client";
import { BrowserRouter, Navigate, Outlet, Route, Routes, useLocation } from "react-router";
import "./app.css";
import { Shell } from "./components/Shell";
import { Loading, Spinner, Toaster } from "./components/ui";
import { queryClient, useMe } from "./lib/queries";
import { Account } from "./pages/Account";
import { Home } from "./pages/Home";
import { Invite } from "./pages/Invite";
import { Login } from "./pages/Login";
import { Members } from "./pages/Members";
import { NewServer } from "./pages/NewServer";
import { OrgSettings } from "./pages/OrgSettings";
import { ServerPage } from "./pages/server/ServerPage";
import { Servers } from "./pages/Servers";
import { Console } from "./pages/server/Console";
import { Mods } from "./pages/server/Mods";
import { Settings } from "./pages/server/Settings";

// The editor and terminal pull in large libraries, so they load on demand.
const Files = lazy(() => import("./pages/server/Files"));
const Terminal = lazy(() => import("./pages/server/Terminal"));

function RequireAuth() {
  const me = useMe();
  const location = useLocation();
  if (me.isPending) return <div className="flex h-dvh items-center justify-center"><Spinner /></div>;
  if (!me.data) return <Navigate to={`/login?next=${encodeURIComponent(location.pathname + location.search)}`} replace />;
  return <Outlet />;
}

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route path="/invite/:token" element={<Invite />} />
          <Route element={<RequireAuth />}>
            <Route element={<Shell />}>
              <Route index element={<Home />} />
              <Route path="account" element={<Account />} />
              <Route path="o/:org">
                <Route index element={<Servers />} />
                <Route path="new" element={<NewServer />} />
                <Route path="members" element={<Members />} />
                <Route path="settings" element={<OrgSettings />} />
                <Route path="s/:server" element={<ServerPage />}>
                  <Route index element={<Console />} />
                  <Route path="files/*" element={<Suspense fallback={<Loading />}><Files /></Suspense>} />
                  <Route path="terminal" element={<Suspense fallback={<Loading />}><Terminal /></Suspense>} />
                  <Route path="mods" element={<Mods />} />
                  <Route path="settings" element={<Settings />} />
                </Route>
              </Route>
              <Route path="*" element={<Navigate to="/" replace />} />
            </Route>
          </Route>
        </Routes>
      </BrowserRouter>
      <Toaster />
    </QueryClientProvider>
  </StrictMode>,
);
