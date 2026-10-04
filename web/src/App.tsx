import type { ComponentType } from "react";
import {
  Navigate,
  NavLink,
  Outlet,
  Route,
  Routes,
  useNavigate,
} from "react-router";
import { AuthProvider, RequireAuth, useAuth } from "./auth";
import { CURRENT_PHASE, NAV_ITEMS } from "./nav";
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

  function onLogout() {
    // Leave first, so the sign-in page carries no ?next= back into the app.
    navigate("/login", { replace: true });
    void signOut();
  }

  return (
    <div className="shell">
      <a className="skip-link" href="#main">
        Skip to content
      </a>
      <header className="brand">Hello</header>
      <nav className="sidebar" aria-label="Primary">
        <ul>
          {NAV_ITEMS.map((item) => (
            <li key={item.path}>
              {/* NavLink sets aria-current="page" on the active link. */}
              <NavLink to={item.path} end={item.path === "/"}>
                {item.label}
              </NavLink>
            </li>
          ))}
        </ul>
        <div className="session">
          {state.status === "signedIn" && (
            <p className="muted">Signed in as {state.username}</p>
          )}
          <button type="button" onClick={onLogout}>
            Log out
          </button>
        </div>
      </nav>
      <main id="main" className="content" tabIndex={-1}>
        <Outlet />
      </main>
    </div>
  );
}

/** The app: sign-in route plus the authenticated shell and its pages. */
export function App() {
  return (
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
            return <Route key={item.path} path={item.path} element={element} />;
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
  );
}
