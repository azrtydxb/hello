import { useEffect, useState } from "react";
import { errorMessage, fieldErrors, type Id } from "../../api";
import {
  deleteDoorCamera,
  doorCameraViewUrl,
  getDoorCamera,
  putDoorCamera,
  type CameraKind,
  type DoorCamera,
} from "../../api/doorcamera";
import { Alert, Button, Input, Select } from "../../design/azrty/components";
import { Can } from "../../role";

/** How often a snapshot camera's picture is fetched again, in ms. */
const SNAPSHOT_REFRESH_MS = 2000;

/**
 * The camera of a door station: where its picture comes from, with the
 * camera's credentials (write-only), and a live picture. Hello fetches the
 * picture itself, so the browser needs no route to the camera.
 */
export function DoorCameraField({ id }: { id: Id }) {
  const [camera, setCamera] = useState<DoorCamera | null>(null);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [showing, setShowing] = useState(false);

  useEffect(() => {
    const controller = new AbortController();
    getDoorCamera(id, controller.signal)
      .then(setCamera)
      .catch((err: unknown) => {
        if (!controller.signal.aborted) setLoadError(errorMessage(err));
      });
    return () => controller.abort();
  }, [id]);

  return (
    <fieldset className="dir-section" aria-label="Camera">
      <legend className="az-eyebrow">Camera</legend>
      <p className="dir-muted">
        Where the station&apos;s picture comes from, so the person who answers
        can see who is at the door. Hello fetches it and shows it here; give an
        MJPEG stream or a snapshot address (http://), not rtsp://.
      </p>
      {loadError && <Alert tone="bad">{loadError}</Alert>}
      {camera?.configured && (
        <div>
          <Button
            variant="secondary"
            size="sm"
            onClick={() => setShowing((s) => !s)}
          >
            {showing ? "Hide picture" : "Show picture"}
          </Button>
          {showing && <Picture id={id} kind={camera.kind} />}
        </div>
      )}
      <Can min="admin">
        {camera && (
          <CameraForm
            id={id}
            camera={camera}
            onChange={(c) => {
              setCamera(c);
              if (!c.configured) setShowing(false);
            }}
          />
        )}
      </Can>
    </fieldset>
  );
}

function Picture({ id, kind }: { id: Id; kind: DoorCamera["kind"] }) {
  const [tick, setTick] = useState(1);
  const [failed, setFailed] = useState(false);
  useEffect(() => {
    if (kind !== "snapshot") return;
    const t = setInterval(() => setTick((n) => n + 1), SNAPSHOT_REFRESH_MS);
    return () => clearInterval(t);
  }, [kind]);
  if (failed) {
    return (
      <Alert tone="warn">
        The camera gave no picture. Check its address and password, and that its
        network is in HELLO_DOOR_CAMERA_ALLOW.
      </Alert>
    );
  }
  return (
    <img
      className="dir-camera"
      alt="Door camera"
      src={doorCameraViewUrl(id, kind === "snapshot" ? tick : 0)}
      onError={() => setFailed(true)}
    />
  );
}

function CameraForm({
  id,
  camera,
  onChange,
}: {
  id: Id;
  camera: DoorCamera;
  onChange: (c: DoorCamera) => void;
}) {
  const [kind, setKind] = useState<CameraKind>(camera.kind || "snapshot");
  const [url, setUrl] = useState(camera.url);
  const [username, setUsername] = useState(camera.username);
  const [password, setPassword] = useState("");
  const [errors, setErrors] = useState<Record<string, string>>({});
  const [message, setMessage] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  async function save() {
    setErrors({});
    setMessage(null);
    if (!/^https?:\/\/\S+$/i.test(url.trim())) {
      setErrors({ url: "An absolute http:// or https:// address." });
      return;
    }
    setBusy(true);
    try {
      const next = await putDoorCamera(id, {
        kind,
        url: url.trim(),
        username,
        ...(password !== "" ? { password } : {}),
      });
      onChange(next);
      setPassword("");
      setMessage("Camera saved.");
    } catch (err) {
      const found: Record<string, string> = {};
      for (const f of fieldErrors(err)) found[f.path] = f.message;
      setErrors(
        Object.keys(found).length > 0 ? found : { form: errorMessage(err) },
      );
    } finally {
      setBusy(false);
    }
  }

  async function remove() {
    setBusy(true);
    try {
      await deleteDoorCamera(id);
      onChange({
        configured: false,
        kind: "",
        url: "",
        username: "",
        passwordSet: false,
      });
      setUrl("");
      setUsername("");
      setPassword("");
      setMessage("Camera removed.");
    } catch (err) {
      setErrors({ form: errorMessage(err) });
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="dir-form">
      {errors.form && <Alert tone="bad">{errors.form}</Alert>}
      <Select
        id="camera-kind"
        label="Picture"
        error={errors.kind}
        value={kind}
        options={[
          {
            value: "snapshot",
            label: "Snapshot (a JPEG, refreshed every 2 s)",
          },
          { value: "mjpeg", label: "MJPEG stream" },
        ]}
        onChange={(e) => setKind(e.target.value as CameraKind)}
      />
      <Input
        id="camera-url"
        label="Address"
        mono
        error={errors.url}
        value={url}
        onChange={(e) => setUrl(e.target.value)}
      />
      <div className="dir-grid-2">
        <Input
          id="camera-user"
          label="User name"
          error={errors.username}
          autoComplete="off"
          value={username}
          onChange={(e) => setUsername(e.target.value)}
        />
        <Input
          id="camera-password"
          label="Password"
          type="password"
          autoComplete="off"
          hint={
            camera.passwordSet
              ? "A password is set; leave empty to keep it."
              : undefined
          }
          error={errors.password}
          value={password}
          onChange={(e) => setPassword(e.target.value)}
        />
      </div>
      <div className="dir-actions">
        <Button size="sm" disabled={busy} onClick={() => void save()}>
          Save camera
        </Button>
        {camera.configured && (
          <Button
            variant="ghost"
            size="sm"
            disabled={busy}
            onClick={() => void remove()}
          >
            Remove camera
          </Button>
        )}
      </div>
      {message && <p role="status">{message}</p>}
    </div>
  );
}
