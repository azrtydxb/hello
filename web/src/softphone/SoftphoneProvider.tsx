// The softphone's React side (S-18): owns the engine, the one-tab lock, the
// device credentials and the call state, and hands the dock and the "Call"
// buttons of other pages one small API. The browser phone is off until the
// user switches it on for this browser profile.
import {
  createContext,
  useCallback,
  useContext,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { ApiError } from "../api";
import {
  createMyDevice,
  getIceServers,
  postCallQuality,
  refreshMyDevice,
  type DeviceCredentials,
} from "../api/clients";
import { backoffDelay } from "./backoff";
import { JsSipEngine } from "./jssipEngine";
import { createPhoneLock, type PhoneLock } from "./lock";
import {
  busy,
  ENDED_LINGER_MS,
  initialPhoneState,
  phoneReducer,
  type PhoneState,
} from "./phoneState";
import { browserPlatform } from "./platform";
import { foldReport, summarizeStats, type QualitySample } from "./quality";
import { createRinger, type Ringer } from "./ringtone";
import type {
  CallInfo,
  DeviceChoice,
  DeviceSelection,
  PhoneEngine,
  Platform,
  RegisterConfig,
} from "./types";

const STORE_KEY = "hello.softphone";
/** Refresh the credentials this long before they expire. */
const REFRESH_AHEAD_MS = 5 * 60_000;
const QUALITY_POLL_MS = 5000;

interface Stored {
  enabled: boolean;
  deviceId?: string | number;
  instanceId: string;
  selection: DeviceSelection;
}

function load(): Stored {
  try {
    const raw = window.localStorage.getItem(STORE_KEY);
    if (raw) {
      const s = JSON.parse(raw) as Partial<Stored>;
      return {
        enabled: !!s.enabled,
        deviceId: s.deviceId,
        instanceId: s.instanceId || newInstanceId(),
        selection: {
          audioInput: "",
          audioOutput: "",
          videoInput: "",
          ...s.selection,
        },
      };
    }
  } catch {
    /* unreadable: start over */
  }
  return {
    enabled: false,
    instanceId: newInstanceId(),
    selection: { audioInput: "", audioOutput: "", videoInput: "" },
  };
}

function newInstanceId(): string {
  return `<urn:uuid:${crypto.randomUUID()}>`;
}

function save(s: Stored) {
  try {
    window.localStorage.setItem(STORE_KEY, JSON.stringify(s));
  } catch {
    /* private mode: the phone just forgets this browser next time */
  }
}

/** "Chrome on macOS", for the device's name and platform columns. */
export function browserLabel(ua: string): string {
  const b = /Edg\//.test(ua)
    ? "Edge"
    : /Firefox\//.test(ua)
      ? "Firefox"
      : /Chrome\//.test(ua)
        ? "Chrome"
        : /Safari\//.test(ua)
          ? "Safari"
          : "Browser";
  const o = /Windows/.test(ua)
    ? "Windows"
    : /Mac OS X/.test(ua)
      ? "macOS"
      : /Android/.test(ua)
        ? "Android"
        : /iPhone|iPad/.test(ua)
          ? "iOS"
          : /Linux/.test(ua)
            ? "Linux"
            : "";
  return o ? `${b} on ${o}` : b;
}

export interface Softphone {
  state: PhoneState;
  /** The switch: this browser is a phone. */
  enabled: boolean;
  /** WebRTC and secure-context support; false shows the reason instead of the switch. */
  supported: boolean;
  /** A setup failure the dock explains (no microphone, no WSS configured). */
  problem: string;
  starting: boolean;
  devices: DeviceChoice[];
  selection: DeviceSelection;
  quality: Record<string, QualitySample | null>;
  /** Whether the ICE servers include a TURN relay (the media-failed banner says so when not). */
  relay: boolean;
  dockOpen: boolean;
  setDockOpen(open: boolean): void;
  enable(): Promise<void>;
  disable(): void;
  /** Ask the other tab for the phone. */
  takeOver(): void;
  /** Place a call; false when the phone is not on (the dock opens to ask). */
  dial(target: string, video?: boolean): boolean;
  answer(id: string, video?: boolean): void;
  hangup(id: string): void;
  hold(id: string, on: boolean): void;
  mute(id: string, on: boolean): void;
  dtmf(id: string, tones: string): void;
  transfer(id: string, target: string): void;
  transferAttended(id: string, otherId: string): void;
  setVideo(id: string, on: boolean): void;
  focus(id: string): void;
  dismiss(id: string): void;
  chooseDevices(sel: DeviceSelection): void;
  /** The call to put on hold before taking another, for call waiting. */
  switchTo(id: string): void;
}

const Ctx = createContext<Softphone | null>(null);

/** The softphone, or null outside the provider (pages render without it). */
export function useSoftphone(): Softphone | null {
  return useContext(Ctx);
}

export interface ProviderProps {
  children: ReactNode;
  /** The display name the phone registers with. */
  displayName?: string;
  engineFactory?: (p: Platform) => PhoneEngine;
  platform?: Platform;
  lock?: PhoneLock;
  ringer?: Ringer;
}

export function SoftphoneProvider({
  children,
  displayName = "",
  engineFactory = (p) => new JsSipEngine(p),
  platform = browserPlatform,
  lock: lockProp,
  ringer: ringerProp,
}: ProviderProps) {
  const [state, dispatch] = useReducer(phoneReducer, initialPhoneState);
  const stored = useRef<Stored>(load());
  const [enabled, setEnabled] = useState(stored.current.enabled);
  const [problem, setProblem] = useState("");
  const [starting, setStarting] = useState(false);
  const [dockOpen, setDockOpen] = useState(false);
  const [relay, setRelay] = useState(false);
  const [devices, setDevices] = useState<DeviceChoice[]>([]);
  const [selection, setSelection] = useState(stored.current.selection);
  const [quality, setQuality] = useState<Record<string, QualitySample | null>>(
    {},
  );
  const engineRef = useRef<PhoneEngine | null>(null);
  const lockRef = useRef<PhoneLock>(lockProp ?? createPhoneLock());
  const ringerRef = useRef<Ringer>(ringerProp ?? createRinger());
  const stateRef = useRef(state);
  stateRef.current = state;
  const refreshTimer = useRef<ReturnType<typeof setTimeout> | null>(null);
  const retries = useRef(0);
  const credRetried = useRef(false);
  const samples = useRef<Record<string, QualitySample[]>>({});
  const deviceId = useRef<string | number | undefined>(stored.current.deviceId);
  const configRef = useRef<{ cfg: RegisterConfig; expiresAt: number } | null>(
    null,
  );

  const supported =
    typeof RTCPeerConnection !== "undefined" &&
    typeof WebSocket !== "undefined" &&
    (typeof window === "undefined" || window.isSecureContext !== false);

  const persist = useCallback((patch: Partial<Stored>) => {
    stored.current = { ...stored.current, ...patch };
    save(stored.current);
  }, []);

  const engine = useCallback((): PhoneEngine => {
    if (!engineRef.current) {
      const e = engineFactory(platform);
      e.on((ev) => dispatch(ev));
      e.setDevices(stored.current.selection);
      engineRef.current = e;
    }
    return engineRef.current;
    // The factory and platform are fixed for the life of the provider.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  const loadDevices = useCallback(async () => {
    try {
      const all = await platform.enumerateDevices();
      setDevices(
        all
          .filter(
            (d) =>
              d.kind === "audioinput" ||
              d.kind === "audiooutput" ||
              d.kind === "videoinput",
          )
          .map((d, i) => ({
            id: d.deviceId,
            kind: d.kind as DeviceChoice["kind"],
            label: d.label || `${d.kind} ${i + 1}`,
          })),
      );
    } catch {
      setDevices([]);
    }
  }, [platform]);

  /** Credentials of this browser's device: refresh the stored one, or enrol. */
  const fetchConfig = useCallback(async (): Promise<{
    cfg: RegisterConfig;
    expiresAt: number;
  }> => {
    let creds: DeviceCredentials | undefined;
    if (deviceId.current !== undefined) {
      try {
        creds = await refreshMyDevice(deviceId.current);
      } catch (err) {
        // A revoked or unknown device is enrolled again; anything else is a failure.
        if (!(
          err instanceof ApiError &&
          (err.status === 404 || err.status === 403)
        ))
          throw err;
        deviceId.current = undefined;
      }
    }
    if (!creds) {
      const ua = navigator.userAgent;
      const enrolled = await createMyDevice({
        label: browserLabel(ua),
        platform: browserLabel(ua),
      });
      creds = enrolled;
      deviceId.current = enrolled.device.id;
      persist({ deviceId: enrolled.device.id });
    }
    if (!creds.wssUrl)
      throw new Error("The server has no WebSocket address for phones yet.");
    let ice: RTCIceServer[] = [];
    try {
      ice = (await getIceServers(deviceId.current)).iceServers;
    } catch {
      /* LAN-only without STUN/TURN: host candidates still connect */
    }
    return {
      cfg: {
        wssUrl: creds.wssUrl,
        sipUsername: creds.sipUsername,
        sipPassword: creds.sipPassword,
        realm: creds.realm,
        displayName,
        instanceId: stored.current.instanceId,
        iceServers: ice,
      },
      expiresAt: Date.parse(creds.expiresAt) || Date.now() + 3600_000,
    };
  }, [displayName, persist]);

  const scheduleRefresh = useCallback(
    (expiresAt: number) => {
      if (refreshTimer.current) clearTimeout(refreshTimer.current);
      const wait = Math.max(30_000, expiresAt - Date.now() - REFRESH_AHEAD_MS);
      refreshTimer.current = setTimeout(function tick() {
        // Never swap credentials under a call: the username carries the expiry.
        if (busy(stateRef.current)) {
          refreshTimer.current = setTimeout(tick, 15_000);
          return;
        }
        void fetchConfig()
          .then((c) => {
            configRef.current = c;
            engine().updateCredentials(c.cfg);
            scheduleRefresh(c.expiresAt);
          })
          .catch(() => {
            refreshTimer.current = setTimeout(
              tick,
              backoffDelay(retries.current++),
            );
          });
      }, wait);
    },
    [engine, fetchConfig],
  );

  const start = useCallback(async () => {
    setProblem("");
    setStarting(true);
    try {
      const have = await lockRef.current.acquire();
      if (!have) {
        dispatch({ type: "registration", state: "other-tab" });
        return;
      }
      const c = await fetchConfig();
      configRef.current = c;
      setRelay(
        c.cfg.iceServers.some((i) =>
          (Array.isArray(i.urls) ? i.urls : [i.urls]).some((u) =>
            /^turns?:/i.test(u),
          ),
        ),
      );
      credRetried.current = false;
      engine().register(c.cfg);
      scheduleRefresh(c.expiresAt);
      void loadDevices();
    } catch (err) {
      setProblem(
        err instanceof Error ? err.message : "The phone could not start.",
      );
      dispatch({
        type: "registration",
        state: "failed",
        reason: "Could not start",
      });
    } finally {
      setStarting(false);
    }
  }, [engine, fetchConfig, loadDevices, scheduleRefresh]);

  const enable = useCallback(async () => {
    setProblem("");
    // The microphone first, with its own explanation: a phone without one is
    // no phone, and the permission prompt belongs to a click.
    try {
      const s = await platform.getUserMedia({ audio: true });
      s.getTracks().forEach((t) => t.stop());
    } catch (err) {
      const denied =
        err instanceof DOMException && err.name === "NotAllowedError";
      setProblem(
        denied
          ? "The browser blocked the microphone. Allow it for this site in the address bar, then switch the phone on again."
          : "No microphone was found. Connect one and try again.",
      );
      return;
    }
    persist({ enabled: true });
    setEnabled(true);
    await start();
  }, [persist, platform, start]);

  const disable = useCallback(() => {
    persist({ enabled: false });
    setEnabled(false);
    if (refreshTimer.current) clearTimeout(refreshTimer.current);
    engineRef.current?.unregister();
    lockRef.current.release();
    ringerRef.current.stop();
    dispatch({ type: "reset" });
  }, [persist]);

  // A browser that had the phone on starts it again on load.
  useEffect(() => {
    if (stored.current.enabled && supported) void start();
    const lock = lockRef.current;
    const ringer = ringerRef.current;
    return () => {
      if (refreshTimer.current) clearTimeout(refreshTimer.current);
      engineRef.current?.unregister();
      lock.release();
      ringer.stop();
    };
    // Once per mount.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, []);

  // Registration refused: refresh the credentials once, then say so.
  useEffect(() => {
    if (state.reg !== "failed" || !enabled || problem) return;
    if (credRetried.current) return;
    credRetried.current = true;
    void fetchConfig()
      .then((c) => {
        configRef.current = c;
        engine().register(c.cfg);
        scheduleRefresh(c.expiresAt);
      })
      .catch((err: unknown) =>
        setProblem(
          err instanceof Error ? err.message : "Registration was refused.",
        ),
      );
  }, [state.reg, enabled, problem, engine, fetchConfig, scheduleRefresh]);

  // Ringing: the tone, and a notification when the tab is hidden.
  const ringing = state.calls.find(
    (c) => c.direction === "in" && c.phase === "ringing",
  );
  const ringingId = ringing?.id ?? "";
  useEffect(() => {
    if (!ringingId) return;
    const ringer = ringerRef.current;
    ringer.start();
    let note: Notification | null = null;
    const c = stateRef.current.calls.find((x) => x.id === ringingId);
    if (
      document.hidden &&
      typeof Notification !== "undefined" &&
      Notification.permission === "granted" &&
      c
    ) {
      note = new Notification(c.remoteName || c.remote || "Incoming call", {
        body: c.called ? `Calling ${c.called}` : "Incoming call",
        tag: "hello-call",
        requireInteraction: true,
      });
      note.onclick = () => window.focus();
    }
    return () => {
      ringer.stop();
      note?.close();
    };
  }, [ringingId]);

  // Ended lines linger briefly, then go; the quality report goes out once.
  const ended = state.calls
    .filter((c) => c.phase === "ended")
    .map((c) => c.id)
    .join(",");
  useEffect(() => {
    const timers: ReturnType<typeof setTimeout>[] = [];
    for (const id of ended ? ended.split(",") : []) {
      const c = stateRef.current.calls.find((x) => x.id === id);
      if (c) void report(c);
      timers.push(
        setTimeout(() => dispatch({ type: "dismiss", id }), ENDED_LINGER_MS),
      );
    }
    return () => timers.forEach(clearTimeout);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [ended]);

  const report = async (c: CallInfo) => {
    const list = samples.current[c.id];
    delete samples.current[c.id];
    if (!list || !c.sipCallId || !c.answeredAt) return;
    const r = foldReport(list, platform.now() - c.answeredAt);
    if (!r) return;
    try {
      await postCallQuality(c.sipCallId, { ...r, deviceId: deviceId.current });
    } catch {
      /* a lost report is not worth a banner */
    }
  };

  // Quality while a call is active.
  const activeIds = state.calls
    .filter((c) => c.phase === "active")
    .map((c) => c.id)
    .join(",");
  useEffect(() => {
    if (!activeIds) return;
    const tick = async () => {
      for (const id of activeIds.split(",")) {
        const rep = await engineRef.current?.stats(id).catch(() => null);
        if (!rep) continue;
        const s = summarizeStats(rep);
        (samples.current[id] ??= []).push(s);
        setQuality((q) => ({ ...q, [id]: s }));
      }
    };
    void tick();
    const t = setInterval(() => void tick(), QUALITY_POLL_MS);
    return () => clearInterval(t);
  }, [activeIds]);

  const api = useMemo<Softphone>(
    () => ({
      state,
      enabled,
      supported,
      problem,
      starting,
      devices,
      selection,
      quality,
      relay,
      dockOpen,
      setDockOpen,
      enable,
      disable,
      takeOver() {
        lockRef.current.request(() => void start());
      },
      dial(target, video = false) {
        if (!enabled || state.reg !== "registered") {
          setDockOpen(true);
          return false;
        }
        const e = engine();
        // A second call puts the first on hold.
        const cur = state.calls.find(
          (c) => c.id === state.focus && c.phase === "active",
        );
        if (cur && !cur.heldLocal) e.hold(cur.id, true);
        try {
          e.call(target, { video });
        } catch (err) {
          setProblem(
            err instanceof Error
              ? err.message
              : "The call could not be placed.",
          );
        }
        return true;
      },
      answer(id, video = false) {
        const cur = state.calls.find(
          (c) => c.id !== id && c.phase === "active" && !c.heldLocal,
        );
        if (cur) engine().hold(cur.id, true);
        engine().answer(id, { video });
      },
      hangup: (id) => engine().hangup(id),
      hold: (id, on) => engine().hold(id, on),
      mute: (id, on) => engine().mute(id, on),
      dtmf: (id, t) => engine().dtmf(id, t),
      transfer: (id, t) => engine().transfer(id, t),
      transferAttended: (id, o) => engine().transferAttended(id, o),
      setVideo: (id, on) => engine().setVideo(id, on),
      focus: (id) => dispatch({ type: "focus", id }),
      dismiss: (id) => dispatch({ type: "dismiss", id }),
      chooseDevices(sel) {
        setSelection(sel);
        persist({ selection: sel });
        engineRef.current?.setDevices(sel);
      },
      switchTo(id) {
        const cur = state.calls.find(
          (c) => c.id === state.focus && c.phase === "active" && !c.heldLocal,
        );
        if (cur && cur.id !== id) engine().hold(cur.id, true);
        const target = state.calls.find((c) => c.id === id);
        if (target?.heldLocal) engine().hold(id, false);
        dispatch({ type: "focus", id });
      },
    }),
    [
      state,
      enabled,
      supported,
      problem,
      starting,
      devices,
      selection,
      quality,
      relay,
      dockOpen,
      enable,
      disable,
      start,
      engine,
      persist,
    ],
  );

  return <Ctx.Provider value={api}>{children}</Ctx.Provider>;
}
