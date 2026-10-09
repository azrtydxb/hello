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
  LinkButton,
  NavItemContent,
  navItemClassName,
  Sidebar,
  SidebarNav,
  SidebarNavGroup,
  ThemeProvider,
  ThemeToggle,
  Topbar,
} from "./design/azrty/components";
import {
  CURRENT_PHASE,
  EXACT_PATHS,
  NAV_ITEMS,
  navGroupsFor,
  pageTitle,
} from "./nav";
import { AIAccess } from "./pages/AIAccess";
import { AIAssistant } from "./pages/AIAssistant";
import { AIFindings } from "./pages/AIFindings";
import { AIProposalDetail } from "./pages/AIProposalDetail";
import { AIProposals } from "./pages/AIProposals";
import { AIStatus } from "./pages/AIStatus";
import { CallDetail } from "./pages/CallDetail";
import { Cluster } from "./pages/Cluster";
import { Calls } from "./pages/Calls";
import { Consent } from "./pages/Consent";
import { Dashboard } from "./pages/Dashboard";
import { Devices } from "./pages/Devices";
import { Diagnostics } from "./pages/Diagnostics";
import { Extensions } from "./pages/Extensions";
import { History } from "./pages/History";
import { Login } from "./pages/Login";
import { NotFound } from "./pages/NotFound";
import { PhoneDetail } from "./pages/PhoneDetail";
import { Phones } from "./pages/Phones";
import { ProvFirmware } from "./pages/ProvFirmware";
import { ProvSettings } from "./pages/ProvSettings";
import { ProvTemplates } from "./pages/ProvTemplates";
import { Placeholder } from "./pages/Placeholder";
import { Users } from "./pages/Users";
import { Can } from "./role";
import { getAIStatus } from "./api/aiagent";
import { usePolling } from "./usePolling";
import { usePlatformBadges } from "./pages/platform/ui";
import { Recordings } from "./pages/Recordings";
import { Announcements } from "./pages/Announcements";
import { Registrations } from "./pages/Registrations";
import { RouteTest } from "./pages/RouteTest";
import { RoutesPage } from "./pages/Routes";
import { RingGroups } from "./pages/RingGroups";
import { System } from "./pages/System";
import { VoiceAgentEdit } from "./pages/VoiceAgentEdit";
import { VoiceAgents } from "./pages/VoiceAgents";
import { VoiceMCPServers } from "./pages/VoiceMCPServers";
import { VoiceRuntime } from "./pages/VoiceRuntime";
import { Trunks } from "./pages/Trunks";
import { Voicemail } from "./pages/Voicemail";
import { useControlPlane } from "./useControlPlane";

/** Pages that have content; any other nav item renders a placeholder. */
const PAGES: Readonly<Record<string, ComponentType>> = {
  "/extensions": Extensions,
  "/devices": Devices,
  "/phones": Phones,
  "/registrations": Registrations,
  "/calls": Calls,
  "/history": History,
  "/trunks": Trunks,
  "/routes": RoutesPage,
  "/cluster": Cluster,
  "/diagnostics": Diagnostics,
  "/voicemail": Voicemail,
  "/ring-groups": RingGroups,
  "/recordings": Recordings,
  "/announcements": Announcements,
  "/system": System,
  "/routes/test": RouteTest,
  "/ai": AIAccess,
  "/ai/assistant": AIAssistant,
  "/ai/findings": AIFindings,
  "/ai/proposals": AIProposals,
  "/ai/status": AIStatus,
  "/users": Users,
  "/voice/agents": VoiceAgents,
  "/voice/mcp-servers": VoiceMCPServers,
  "/voice/runtime": VoiceRuntime,
};

const ROLE_LABELS = {
  viewer: "Viewer",
  operator: "Operator",
  admin: "Administrator",
} as const;

function Shell() {
  const { state, signOut } = useAuth();
  const navigate = useNavigate();
  const location = useLocation();
  const controlPlane = useControlPlane();
  const badges = usePlatformBadges();
  const aiStatus = usePolling(getAIStatus, 60_000);
  const aiEnabled = aiStatus.status !== "loading" && !!aiStatus.data?.enabled;
  const reachable = controlPlane.status === "reachable";
  const title = pageTitle(location.pathname);
  // Call detail belongs to Call history in the nav.
  const historyDetail = /^\/history\/[^/]+$/.test(location.pathname);

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
            ? `${window.location.host} · rev ${controlPlane.configRevision}`
            : controlPlane.status === "checking"
              ? "Checking control plane…"
              : "Control plane unreachable",
        }}
        user={
          state.status === "signedIn"
            ? { name: state.username, role: ROLE_LABELS[state.role] }
            : undefined
        }
        onSignOut={onLogout}
      >
        <SidebarNav label="Primary">
          {navGroupsFor(
            state.status === "signedIn" ? state.role : undefined,
            aiEnabled,
          ).map((group) => (
            <SidebarNavGroup key={group.label} label={group.label}>
              {group.items.map((item) => (
                // NavLink sets aria-current="page" on the active link.
                <NavLink
                  key={item.path}
                  to={item.path}
                  end={EXACT_PATHS.has(item.path)}
                  className={({ isActive }) =>
                    navItemClassName(
                      isActive || (historyDetail && item.path === "/history"),
                    )
                  }
                >
                  <NavItemContent
                    icon={item.icon}
                    label={item.label}
                    badge={badges[item.path]}
                  />
                </NavLink>
              ))}
            </SidebarNavGroup>
          ))}
        </SidebarNav>
      </Sidebar>
      <div className="app-shell__main-col">
        <Topbar
          crumbs={title ? ["Kuvryn Hello", title] : ["Kuvryn Hello"]}
          live={reachable}
        >
          <Can>
            <LinkButton to="/routes/test" size="sm" icon="flask-conical">
              Test a number
            </LinkButton>
          </Can>
          <ThemeToggle />
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
          {/* OAuth consent: full page, outside the shell, after sign-in. */}
          <Route
            path="/oauth/consent"
            element={
              <RequireAuth>
                <Consent />
              </RequireAuth>
            }
          />
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
            {/* Dial plans are the structured routes (spec §11). */}
            <Route
              path="/dial-plans"
              element={<Navigate to="/routes" replace />}
            />
            <Route path="/history/:id" element={<CallDetail />} />
            <Route path="/voice/agents/:id" element={<VoiceAgentEdit />} />
            <Route path="/ai/assistant/:id" element={<AIAssistant />} />
            <Route path="/ai/proposals/:id" element={<AIProposalDetail />} />
            {/* Phones sections; static paths outrank /phones/:id. */}
            <Route path="/phones/templates" element={<ProvTemplates />} />
            <Route path="/phones/firmware" element={<ProvFirmware />} />
            <Route path="/phones/settings" element={<ProvSettings />} />
            <Route path="/phones/:id" element={<PhoneDetail />} />
            <Route path="*" element={<NotFound />} />
          </Route>
        </Routes>
      </AuthProvider>
    </ThemeProvider>
  );
}
