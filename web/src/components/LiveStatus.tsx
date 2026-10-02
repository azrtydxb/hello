import type { PollState } from "../usePolling";

/** Loading / error / last-updated line for a page refreshed by usePolling. */
export function LiveStatus<T>({
  state,
  what,
}: {
  state: PollState<T>;
  what: string;
}) {
  if (state.status === "loading") {
    return (
      <p role="status" aria-live="polite">
        Loading {what}…
      </p>
    );
  }
  if (state.status === "error") {
    return (
      <div role="alert" className="error">
        <strong>Could not load {what}.</strong>
        <p>{state.message}</p>
      </div>
    );
  }
  return (
    <p className="muted">
      Refreshed every 5 seconds; last at {state.updatedAt.toLocaleTimeString()}.
    </p>
  );
}
