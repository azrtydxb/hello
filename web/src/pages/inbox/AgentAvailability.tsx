import { useState } from "react";
import { errorMessage } from "../../api";
import {
  getAgentAvailability,
  setAgentAvailability,
  type AgentAvailability as Availability,
} from "../../api/inboxRouting";
import { Alert, Badge, Button } from "../../design/azrty/components";
import { usePolling } from "../../usePolling";

const REFRESH_MS = 30_000;

/**
 * An agent's login and pause for the text queues. The state is the call
 * queues' own, so pausing here pauses calls too. Nothing shows for someone
 * whose extensions answer no text queue.
 */
export function AgentAvailability() {
  const state = usePolling(getAgentAvailability, REFRESH_MS);
  // What the last change answered, until the next poll reads the state again.
  const [changed, setChanged] = useState<{
    value: Availability;
    at: number;
  } | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  if (state.status === "loading") return null;
  if (state.status === "error" && !state.data) return null;
  const polled = state.status === "ready" ? state.updatedAt.getTime() : 0;
  const a = changed && polled <= changed.at ? changed.value : state.data;
  if (!a?.member) return null;

  async function change(body: { loggedIn?: boolean; paused?: boolean }) {
    setBusy(true);
    setError(null);
    try {
      setChanged({ value: await setAgentAvailability(body), at: Date.now() });
    } catch (err: unknown) {
      setError(errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="inbox-agent az-card" role="group" aria-label="Availability">
      <span>
        Texts for {a.extensions.flatMap((e) => e.queues).join(", ")}:{" "}
        {!a.loggedIn ? (
          <Badge tone="neutral">Logged out</Badge>
        ) : a.paused ? (
          <Badge tone="warn">Paused{a.reason ? `: ${a.reason}` : ""}</Badge>
        ) : (
          <Badge tone="good">Available</Badge>
        )}
      </span>
      {a.needsLogin && (
        <Button
          size="sm"
          variant="secondary"
          disabled={busy}
          onClick={() => void change({ loggedIn: !a.loggedIn })}
        >
          {a.loggedIn ? "Log out" : "Log in"}
        </Button>
      )}
      {a.loggedIn && (
        <Button
          size="sm"
          variant="secondary"
          disabled={busy}
          onClick={() => void change({ paused: !a.paused })}
        >
          {a.paused ? "Resume" : "Pause"}
        </Button>
      )}
      {error && (
        <Alert tone="bad" title="Could not change your availability">
          {error}
        </Alert>
      )}
    </div>
  );
}
