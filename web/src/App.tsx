import type { ComponentType } from "react";
import {
  Navigate,
  NavLink,
  Outlet,
  Route,
  Routes,
  useLocation,
  useNavigate,
} from "react-router";
import { AuthProvider, RequireAuth, useAuth } from "./auth";
import { HelloLogo } from "./brand";
import {
  NavItemContent,
  navItemClassName,
  Sidebar,
  SidebarNav,
  SidebarNavGroup,
  ThemeProvider,
  ThemeToggle,
  Topbar,
} from "./design/azrty/components";
import { CURRENT_PHASE, NAV_GROUPS, NAV_ITEMS, navFor } from "./nav";
import { CallDetail } from "./pages/CallDetail";
import { Cluster } from "./pages/Cluster";
import { Calls } from "./pages/Calls";
import { Dashboard } from "./pages/Dashboard";
import { Devices } from "./pages/Devices";
import { Extensions } from "./pages/Extensions";
import { History } from "./pages/History";
import { Login } from "./pages/Login";
import { NotFound } from "./pages/NotFound";
import { Placeholder } from "./pages/Placeholder";
import { Recordings } from "./pages/Recordings";
import { Announcements } from "./pages/Announcements";
import { Registrations } from "./pages/Registrations";
import { RouteTest } from "./pages/RouteTest";
import { RoutesPage } from "./pages/Routes";
import { RingGroups } from "./pages/RingGroups";
import { System } from "./pages/System";
import { Trunks } from "./pages/Trunks";
import { Voicemail } from "./pages/Voicemail";
import { useControlPlane } from "./useControlPlane";

/** Pages that have content; any other nav item renders a placeholder. */
const PAGES: Readonly<Record<string, ComponentType>> = {
  "/extensions": Extensions,
  "/devices": Devices,
  "/registrations": Registrations,
  "/calls": Calls,
  "/history": History,
  "/trunks": Trunks,
  "/routes": RoutesPage,
  "/cluster": Cluster,
  "/voicemail": Voicemail,
  "/ring-groups": RingGroups,
  "/recordings": Recordings,
  "/announcements": Announcements,
  "/system": System,
};

function Shell() {
  const { state, signOut } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const controlPlane = useControlPlane();
  const reachable = controlPlane.status === "reachable";
  const here = navFor(location.pathname);

  function onLogout() {
    // Leave first, so the sign-in page carries no ?next= back into the app.
    navigate("/login", { replace: true });
    void signOut();
  }

  return (
    <div className="app-shell" data-pillar="operate">
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <Sidebar
        brand={<HelloLogo layout="horizontal" size={40} />}
        status={{
          live: reachable,
          label: reachable
            ? "Connected to control plane"
            : controlPlane.status === "checking"
              ? "Checking control plane…"
              : "Control plane unreachable",
        }}
        user={
          state.status === "signedIn"
            ? { name: state.username, role: "Administrator" }
            : undefined
        }
        onSignOut={onLogout}
        signOutLabel="Log out"
        footer={
          <div className="app-shell__theme">
            <ThemeToggle block />
          </div>
        }
      >
        <SidebarNav label="Primary">
          {NAV_GROUPS.map((group) => (
            <SidebarNavGroup
              key={group.label}
              label={group.label}
              className="app-nav-group"
            >
              {group.items.map((item) => (
                // NavLink sets aria-current="page" on the active link.
                <NavLink
                  key={item.path}
                  to={item.path}
                  end={item.path === "/"}
                  className={({ isActive }) =>
                    `${navItemClassName(isActive)} app-nav-item`
                  }
                >
                  <NavItemContent icon={item.icon} label={item.label} />
                </NavLink>
              ))}
            </SidebarNavGroup>
          ))}
        </SidebarNav>
      </Sidebar>
      <div className="app-shell__main-col">
        <Topbar
          crumbs={here ? [here.group.label, here.item.label] : ["Hello"]}
          live={reachable}
        >
          {reachable && (
            <span className="app-shell__version">{controlPlane.version}</span>
          )}
        </Topbar>
        <main id="main" className="app-shell__main" tabIndex={-1}>
          <div className="app-page">
            <Outlet />
          </div>
        </main>
      </div>
    </div>
  );
}

/** The app: sign-in route plus the authenticated shell and its pages. */
export function App() {
  return (
    <ThemeProvider>
      <AuthProvider>
        <Routes>
          <Route path="/login" element={<Login />} />
          <Route
            element={
              <RequireAuth>
                <Shell />
              </RequireAuth>
            }
          >
            <Route index element={<Dashboard />} />
            {NAV_ITEMS.filter((item) => item.path !== "/").map((item) => {
              const Page = PAGES[item.path];
              const element =
                Page && item.phase <= CURRENT_PHASE ? (
                  <Page />
                ) : (
                  <Placeholder title={item.label} phase={item.phase} />
                );
              return (
                <Route key={item.path} path={item.path} element={element} />
              );
            })}
            <Route path="/routes/test" element={<RouteTest />} />
            {/* Dial plans are the structured routes (spec §11). */}
            <Route
              path="/dial-plans"
              element={<Navigate to="/routes" replace />}
            />
            <Route path="/history/:id" element={<CallDetail />} />
            <Route path="*" element={<NotFound />} />
          </Route>
        </Routes>
      </AuthProvider>
    </ThemeProvider>
  );
}
