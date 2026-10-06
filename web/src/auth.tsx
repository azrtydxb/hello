import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useState,
  type ReactNode,
} from "react";
import { Navigate, useLocation } from "react-router";
import {
  errorMessage,
  fetchMe,
  loginPath,
  logout as apiLogout,
  setUnauthorizedHandler,
} from "./api";
import { Alert, Button, Spinner } from "./design/azrty/components";

/** The session as the UI knows it. */
export type AuthState =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "signedOut" }
  | { status: "signedIn"; username: string };

interface AuthContextValue {
  state: AuthState;
  /** Re-read the session from GET /api/v1/auth/me. */
  refresh: () => Promise<void>;
  /** End the session on the server and locally. */
  signOut: () => Promise<void>;
}

const AuthContext = createContext<AuthContextValue | null>(null);

/** Loads the session from GET /api/v1/auth/me and drops it on any 401. */
export function AuthProvider({ children }: { children: ReactNode }) {
  const [state, setState] = useState<AuthState>({ status: "loading" });

  const refresh = useCallback(async () => {
    try {
      const me = await fetchMe();
      setState(
        me
          ? { status: "signedIn", username: me.username }
          : { status: "signedOut" },
      );
    } catch (err) {
      setState({ status: "error", message: errorMessage(err) });
    }
  }, []);

  const signOut = useCallback(async () => {
    try {
      await apiLogout();
    } finally {
      setState({ status: "signedOut" });
    }
  }, []);

  useEffect(() => {
    let active = true;
    fetchMe()
      .then((me) => {
        if (!active) return;
        setState(
          me
            ? { status: "signedIn", username: me.username }
            : { status: "signedOut" },
        );
      })
      .catch((err: unknown) => {
        if (active) setState({ status: "error", message: errorMessage(err) });
      });
    return () => {
      active = false;
    };
  }, []);

  // Any API call answered with 401 means the session is gone: drop it, and
  // RequireAuth sends the user to /login?next=<where they were>.
  useEffect(
    () =>
      setUnauthorizedHandler(() => {
        setState({ status: "signedOut" });
      }),
    [],
  );

  const value = useMemo(
    () => ({ state, refresh, signOut }),
    [state, refresh, signOut],
  );
  return <AuthContext.Provider value={value}>{children}</AuthContext.Provider>;
}

/** The auth context; throws outside <AuthProvider>. */
export function useAuth(): AuthContextValue {
  const ctx = useContext(AuthContext);
  if (!ctx) throw new Error("useAuth must be used inside <AuthProvider>");
  return ctx;
}

/** Renders children only for a signed-in user; otherwise redirects to sign-in. */
export function RequireAuth({ children }: { children: ReactNode }) {
  const { state, refresh } = useAuth();
  const location = useLocation();

  if (state.status === "loading") {
    return (
      <div className="app-centered">
        <Spinner label="Loading…" />
      </div>
    );
  }
  if (state.status === "error") {
    return (
      <div className="app-centered">
        <Alert tone="bad" title="Could not reach the control plane">
          {state.message}
        </Alert>
        <Button
          variant="secondary"
          icon="rotate-cw"
          onClick={() => void refresh()}
        >
          Retry
        </Button>
      </div>
    );
  }
  if (state.status === "signedOut") {
    return (
      <Navigate to={loginPath(location.pathname + location.search)} replace />
    );
  }
  return <>{children}</>;
}
