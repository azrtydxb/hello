import { useEffect, useState } from "react";
import { fetchVersion, type VersionInfo } from "../api";

type State =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; info: VersionInfo };

export function Dashboard() {
  const [state, setState] = useState<State>({ status: "loading" });

  useEffect(() => {
    const controller = new AbortController();
    fetchVersion(controller.signal)
      .then((info) => setState({ status: "ready", info }))
      .catch((err: unknown) => {
        if (controller.signal.aborted) return;
        const message = err instanceof Error ? err.message : String(err);
        setState({ status: "error", message });
      });
    return () => controller.abort();
  }, []);

  return (
    <section aria-labelledby="page-title">
      <h1 id="page-title">Dashboard</h1>
      <h2>Control plane</h2>
      {state.status === "loading" && (
        <p role="status" aria-live="polite">
          Loading version…
        </p>
      )}
      {state.status === "error" && (
        <div role="alert" className="error">
          <strong>Could not reach the control plane.</strong>
          <p>{state.message}</p>
        </div>
      )}
      {state.status === "ready" && (
        <dl className="facts">
          <dt>Version</dt>
          <dd data-testid="version">{state.info.version}</dd>
          <dt>Commit</dt>
          <dd>
            <code>{state.info.commit}</code>
          </dd>
          <dt>Configuration revision</dt>
          <dd>{state.info.configRevision}</dd>
        </dl>
      )}
    </section>
  );
}
