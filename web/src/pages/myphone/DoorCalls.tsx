import { useState } from "react";
import { errorMessage } from "../../api";
import { listMyDoorCalls, openDoor, type DoorCall } from "../../api/door";
import { Alert, Button } from "../../design/azrty/components";
import { usePolling } from "../../usePolling";

/** How often the user's door calls are looked for. */
const DOOR_REFRESH_MS = 3000;

/**
 * The door calls the signed-in user is on: each has an Open door button that
 * sends the station's door-open code (the same as dialling *50). Nothing shows
 * while no door call is up.
 */
export function DoorCalls() {
  const state = usePolling(listMyDoorCalls, DOOR_REFRESH_MS);
  const [opened, setOpened] = useState<Record<string, boolean>>({});
  const [failed, setFailed] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState<string | null>(null);
  const calls: DoorCall[] = state.status === "ready" ? state.data : [];
  if (calls.length === 0) return null;

  async function open(id: string) {
    setBusy(id);
    setFailed((f) => ({ ...f, [id]: "" }));
    try {
      await openDoor(id);
      setOpened((o) => ({ ...o, [id]: true }));
    } catch (err) {
      setFailed((f) => ({ ...f, [id]: errorMessage(err) }));
    } finally {
      setBusy(null);
    }
  }

  return (
    <section className="az-card" aria-label="Door calls">
      <h2 className="az-eyebrow">Door call</h2>
      {calls.map((c) => (
        <div key={c.id}>
          <span>
            Door station <span className="media-mono">{c.door}</span> is on your
            call.{" "}
          </span>
          <Button
            size="sm"
            disabled={busy === c.id}
            onClick={() => void open(c.id)}
          >
            Open door
          </Button>
          {opened[c.id] && !failed[c.id] && (
            <span role="status"> The door-open code was sent.</span>
          )}
          {failed[c.id] && (
            <Alert tone="bad">Could not open the door: {failed[c.id]}</Alert>
          )}
        </div>
      ))}
    </section>
  );
}
