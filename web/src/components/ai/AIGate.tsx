import { useEffect, useState, type ReactNode } from "react";
import { errorMessage } from "../../api";
import { getAIStatus, type AIStatus } from "../../api/aiagent";
import { Alert, Spinner } from "../../design/azrty/components";
import { AIOff } from "./AIOff";

/** Renders its children only while AI is enabled; otherwise the reason. */
export function AIGate({ children }: { children: ReactNode }) {
  const [state, setState] = useState<
    { s: "loading" } | { s: "error"; m: string } | { s: "ok"; v: AIStatus }
  >({ s: "loading" });
  useEffect(() => {
    const c = new AbortController();
    getAIStatus(c.signal).then(
      (v) => setState({ s: "ok", v }),
      (e: unknown) => {
        if (!c.signal.aborted) setState({ s: "error", m: errorMessage(e) });
      },
    );
    return () => c.abort();
  }, []);
  if (state.s === "loading") return <Spinner label="Loading…" />;
  if (state.s === "error")
    return (
      <Alert tone="bad" title="Could not load the AI status">
        {state.m}
      </Alert>
    );
  if (!state.v.enabled) return <AIOff reason={state.v.reason} />;
  return <>{children}</>;
}
