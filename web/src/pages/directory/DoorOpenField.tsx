import { useEffect, useState } from "react";
import { errorMessage, fieldErrors, type Id } from "../../api";
import { getDoorOpen, putDoorOpen, type DoorOpen } from "../../api/door";
import { Alert, Button, Input, Switch } from "../../design/azrty/components";
import { Can } from "../../role";

const CODE_RE = /^[0-9*#]{1,8}$/;

/**
 * The door-open code of a door station: what Hello sends to the door when the
 * user who answered dials *50 or presses Open door. The code is write-only: it
 * is never shown after it is saved, only whether one is set. Admins only.
 */
export function DoorOpenField({ id }: { id: Id }) {
  return (
    <Can min="admin">
      <DoorOpenForm id={id} />
    </Can>
  );
}

function DoorOpenForm({ id }: { id: Id }) {
  const [state, setState] = useState<DoorOpen | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [code, setCode] = useState("");
  const [hangup, setHangup] = useState(false);
  const [error, setError] = useState<string | undefined>();
  const [message, setMessage] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    getDoorOpen(id, controller.signal)
      .then((d) => {
        setState(d);
        setHangup(d.hangupAfterOpen);
      })
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setLoadError(errorMessage(err));
      });
    return () => controller.abort();
  }, [id]);

  async function save(change: { code?: string; hangupAfterOpen?: boolean }) {
    setError(undefined);
    setMessage(null);
    if (change.code !== undefined && change.code !== "") {
      if (!CODE_RE.test(change.code)) {
        setError("1 to 8 characters of 0-9, * and #.");
        return;
      }
    }
    setBusy(true);
    try {
      const next = await putDoorOpen(id, change);
      setState(next);
      setHangup(next.hangupAfterOpen);
      setCode("");
      setMessage(
        change.code === ""
          ? "Door-open code cleared."
          : "Door-open settings saved.",
      );
    } catch (err) {
      const found = fieldErrors(err).find((f) => f.path === "code");
      setError(found ? found.message : errorMessage(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <fieldset className="dir-section" aria-label="Door-open code">
      <legend className="az-eyebrow">Door-open code</legend>
      <p className="dir-muted">
        The code Hello sends to the door when the user who answered dials *50 or
        presses Open door. Set it to what the unit expects to release its relay.
        It is stored sealed and never shown again.
      </p>
      {loadError && <Alert tone="bad">{loadError}</Alert>}
      {state && (
        <p className="dir-muted" role="status">
          {state.codeSet ? "A code is set." : "No code is set."}
        </p>
      )}
      <Input
        id="ext-door-code"
        label="New code"
        mono
        type="password"
        autoComplete="off"
        hint="1 to 8 characters of 0-9, * and #."
        error={error}
        value={code}
        disabled={busy || !state}
        onChange={(e) => setCode(e.target.value)}
      />
      <Switch
        label="Hang up after opening"
        hint="End the call a few seconds after the code was sent (HELLO_DOOR_HANGUP_SECONDS)."
        labelPosition="end"
        checked={hangup}
        disabled={busy || !state}
        onChange={(e) => setHangup(e.target.checked)}
      />
      <div className="dir-actions">
        <Button
          size="sm"
          disabled={
            busy || !state || (code === "" && hangup === state.hangupAfterOpen)
          }
          onClick={() =>
            void save({
              ...(code !== "" ? { code } : {}),
              ...(hangup !== state?.hangupAfterOpen
                ? { hangupAfterOpen: hangup }
                : {}),
            })
          }
        >
          Save door-open settings
        </Button>
        {state?.codeSet && (
          <Button
            variant="ghost"
            size="sm"
            disabled={busy}
            onClick={() => void save({ code: "" })}
          >
            Clear code
          </Button>
        )}
      </div>
      {message && <p role="status">{message}</p>}
    </fieldset>
  );
}
