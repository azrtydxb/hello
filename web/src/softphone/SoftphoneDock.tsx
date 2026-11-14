// The softphone dock (S-18): a status chip in every page of the console that
// opens into the dialer, the call lines and their controls. An incoming call
// shows its own toast even when the dock is collapsed.
import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link } from "react-router";
import { myContactsApi } from "../api/phonebook";
import { listMyCalls } from "../api/self";
import {
  Alert,
  Badge,
  Button,
  Icon,
  Select,
  Switch,
} from "../design/azrty/components";
import { callWaiting, endLabel, liveCalls, regLabel } from "./phoneState";
import { gradeOf } from "./quality";
import { useSoftphone, type Softphone } from "./SoftphoneProvider";
import type { CallInfo } from "./types";
import "./softphone.css";

const DIALPAD = ["1", "2", "3", "4", "5", "6", "7", "8", "9", "*", "0", "#"];

/** "02:07" from the answered time. */
function useElapsed(since: number): string {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!since) return;
    const t = setInterval(() => setNow(Date.now()), 1000);
    return () => clearInterval(t);
  }, [since]);
  if (!since) return "";
  const s = Math.max(0, Math.floor((now - since) / 1000));
  return `${String(Math.floor(s / 60)).padStart(2, "0")}:${String(s % 60).padStart(2, "0")}`;
}

function who(c: CallInfo): string {
  return c.remoteName && c.remote
    ? `${c.remoteName} (${c.remote})`
    : c.remoteName || c.remote || "Unknown";
}

/** The caller, the dialled extension, the ring tag and the video badge. */
function IncomingToast({
  c,
  sp,
  waiting,
}: {
  c: CallInfo;
  sp: Softphone;
  waiting: boolean;
}) {
  return (
    <div
      className="sp-toast"
      role="alertdialog"
      aria-label={`Incoming call from ${who(c)}`}
    >
      <div className="sp-toast__head">
        <Icon name="phone-incoming" size={18} />
        <strong>{waiting ? "Call waiting" : "Incoming call"}</strong>
        {c.video.offered && (
          <Badge tone="info">
            <Icon name="video" size={12} /> Video
          </Badge>
        )}
        {c.ringTag && <Badge tone="outline">{c.ringTag}</Badge>}
      </div>
      <div className="sp-toast__who">{who(c)}</div>
      {c.called && <div className="sp-toast__sub">Calling {c.called}</div>}
      <div className="sp-toast__actions">
        <Button
          variant="primary"
          icon="phone"
          onClick={() => sp.answer(c.id, false)}
        >
          Answer
        </Button>
        {c.video.offered && (
          <Button
            variant="secondary"
            icon="video"
            onClick={() => sp.answer(c.id, true)}
          >
            Answer with video
          </Button>
        )}
        <Button
          variant="danger"
          icon="phone-off"
          onClick={() => sp.hangup(c.id)}
        >
          Decline
        </Button>
      </div>
    </div>
  );
}

function Dialpad({ onTone }: { onTone: (t: string) => void }) {
  return (
    <div className="sp-pad" role="group" aria-label="Dial pad">
      {DIALPAD.map((k) => (
        <button
          key={k}
          type="button"
          className="sp-pad__key"
          onClick={() => onTone(k)}
          aria-label={`Key ${k}`}
        >
          {k}
        </button>
      ))}
    </div>
  );
}

/** Remote video large with the self-view picture-in-picture. */
function VideoStage({ id, sp }: { id: string; sp: Softphone }) {
  const remote = useRef<HTMLVideoElement>(null);
  const local = useRef<HTMLVideoElement>(null);
  const streams = sp.state.streams[id];
  useEffect(() => {
    if (remote.current) remote.current.srcObject = streams?.remote ?? null;
    if (local.current) local.current.srcObject = streams?.local ?? null;
  }, [streams]);
  const call = sp.state.calls.find((c) => c.id === id);
  if (!call || (!call.video.remote && !call.video.local)) return null;
  const q = sp.quality[id]?.video;
  return (
    <div className="sp-video" aria-label="Video">
      <video
        ref={remote}
        className="sp-video__remote"
        autoPlay
        playsInline
        muted
        aria-label="Remote video"
      />
      {call.video.local && (
        <video
          ref={local}
          className="sp-video__self"
          autoPlay
          playsInline
          muted
          aria-label="Your camera"
        />
      )}
      {q && q.width > 0 && (
        <span className="sp-video__stats">
          {q.width}x{q.height} {Math.round(q.fps)} fps {q.codec}
        </span>
      )}
    </div>
  );
}

function ActiveCall({ c, sp }: { c: CallInfo; sp: Softphone }) {
  const elapsed = useElapsed(c.answeredAt);
  const [pad, setPad] = useState(false);
  const [xfer, setXfer] = useState("");
  const [xferOpen, setXferOpen] = useState(false);
  const others = liveCalls(sp.state).filter(
    (o) => o.id !== c.id && o.phase === "active",
  );
  const q = sp.quality[c.id] ?? null;
  const grade = gradeOf(q);
  const held = c.heldLocal;
  const answered = c.phase === "active" || c.phase === "connecting";
  const canVideo = answered && !held;

  return (
    <div className="sp-call" data-phase={c.phase}>
      <div className="sp-call__head">
        <div>
          <div className="sp-call__who">{who(c)}</div>
          <div className="sp-call__state" aria-live="polite">
            {c.phase === "dialing" || c.phase === "early"
              ? c.phase === "early"
                ? "Ringing…"
                : "Calling…"
              : c.phase === "connecting"
                ? "Connecting…"
                : held
                  ? "On hold"
                  : c.heldRemote
                    ? "They put you on hold"
                    : elapsed}
            {c.transferring && " · transferring…"}
          </div>
        </div>
        {answered && c.recording && c.recording !== "off" && (
          <span
            className="sp-rec"
            title={
              c.recording === "video"
                ? "This call is being recorded, video included"
                : "This call is being recorded"
            }
          >
            <Icon
              name={c.recording === "video" ? "video" : "circle"}
              size={12}
            />
            <span className="visually-hidden">
              {c.recording === "video"
                ? "Recording, including video"
                : "Recording"}
            </span>
            <span aria-hidden="true">REC</span>
          </span>
        )}
        {answered && (
          <span
            className={`sp-quality sp-quality--${grade}`}
            title={
              q
                ? `Loss ${q.lossPct.toFixed(1)}%, jitter ${q.jitterMs.toFixed(0)} ms, RTT ${q.rttMs.toFixed(0)} ms, ${q.localCandidate || "?"} to ${q.remoteCandidate || "?"}`
                : "No statistics yet"
            }
          >
            <Icon name="signal" size={14} />
            <span className="visually-hidden">Call quality: {grade}</span>
            {q && <span aria-hidden="true">{grade}</span>}
          </span>
        )}
      </div>

      <VideoStage id={c.id} sp={sp} />

      <div className="sp-controls">
        <Button
          variant={c.muted ? "accent" : "secondary"}
          size="sm"
          icon={c.muted ? "mic-off" : "mic"}
          aria-pressed={c.muted}
          disabled={!answered}
          onClick={() => sp.mute(c.id, !c.muted)}
        >
          {c.muted ? "Unmute" : "Mute"}
        </Button>
        <Button
          variant={held ? "accent" : "secondary"}
          size="sm"
          icon={held ? "play" : "pause"}
          aria-pressed={held}
          disabled={!answered}
          onClick={() => sp.hold(c.id, !held)}
        >
          {held ? "Resume" : "Hold"}
        </Button>
        <Button
          variant="secondary"
          size="sm"
          icon="grid-3x3"
          aria-pressed={pad}
          disabled={!answered}
          onClick={() => setPad(!pad)}
        >
          Keypad
        </Button>
        <Button
          variant="secondary"
          size="sm"
          icon="phone-forwarded"
          aria-pressed={xferOpen}
          disabled={!answered}
          onClick={() => setXferOpen(!xferOpen)}
        >
          Transfer
        </Button>
        <Button
          variant={c.video.local ? "accent" : "secondary"}
          size="sm"
          icon={c.video.local ? "video-off" : "video"}
          aria-pressed={c.video.local}
          disabled={!canVideo}
          onClick={() => sp.setVideo(c.id, !c.video.local)}
        >
          {c.video.local ? "Stop video" : "Start video"}
        </Button>
        <Button
          variant="danger"
          size="sm"
          icon="phone-off"
          onClick={() => sp.hangup(c.id)}
        >
          {c.phase === "dialing" || c.phase === "early" ? "Cancel" : "Hang up"}
        </Button>
      </div>

      {pad && <Dialpad onTone={(t) => sp.dtmf(c.id, t)} />}

      {xferOpen && (
        <form
          className="sp-xfer"
          onSubmit={(e: FormEvent) => {
            e.preventDefault();
            if (xfer.trim()) {
              sp.transfer(c.id, xfer.trim());
              setXfer("");
              setXferOpen(false);
            }
          }}
        >
          <label className="sp-field">
            <span>Transfer to</span>
            <input
              value={xfer}
              onChange={(e) => setXfer(e.target.value)}
              inputMode="tel"
              autoComplete="off"
            />
          </label>
          <Button type="submit" size="sm" disabled={!xfer.trim()}>
            Blind transfer
          </Button>
          {others.map((o) => (
            <Button
              key={o.id}
              size="sm"
              variant="secondary"
              onClick={() => {
                sp.transferAttended(c.id, o.id);
                setXferOpen(false);
              }}
            >
              Connect to {who(o)}
            </Button>
          ))}
        </form>
      )}
    </div>
  );
}

function EndedCall({
  c,
  sp,
  regTurn,
}: {
  c: CallInfo;
  sp: Softphone;
  regTurn: boolean;
}) {
  return (
    <div className="sp-ended" role="status">
      <Icon name="phone-off" size={14} />
      <span>
        {who(c)}: {endLabel(c)}
        {c.endReason === "media-failed" &&
          !regTurn &&
          ". No relay server is configured; a network that blocks direct media cannot connect."}
      </span>
      <Button size="sm" variant="ghost" onClick={() => sp.dismiss(c.id)}>
        Dismiss
      </Button>
    </div>
  );
}

function DevicePicker({ sp }: { sp: Softphone }) {
  const pick = (kind: "audioinput" | "audiooutput" | "videoinput") =>
    sp.devices
      .filter((d) => d.kind === kind)
      .map((d) => ({ value: d.id, label: d.label }));
  const sel = sp.selection;
  const row = (
    label: string,
    kind: "audioinput" | "audiooutput" | "videoinput",
    field: keyof typeof sel,
  ) => {
    const opts = pick(kind);
    if (opts.length === 0) return null;
    return (
      <Select
        key={field}
        label={label}
        value={sel[field]}
        options={[{ value: "", label: "Default" }, ...opts]}
        onChange={(e) => sp.chooseDevices({ ...sel, [field]: e.target.value })}
      />
    );
  };
  return (
    <details className="sp-devices">
      <summary>Audio and video devices</summary>
      {row("Microphone", "audioinput", "audioInput")}
      {row("Speaker", "audiooutput", "audioOutput")}
      {row("Camera", "videoinput", "videoInput")}
    </details>
  );
}

function Dialer({ sp }: { sp: Softphone }) {
  const [num, setNum] = useState("");
  const [names, setNames] = useState<{ value: string; label: string }[]>([]);
  useEffect(() => {
    const ctl = new AbortController();
    const out: { value: string; label: string }[] = [];
    void Promise.allSettled([
      myContactsApi.list("", ctl.signal),
      listMyCalls(undefined, ctl.signal),
    ]).then(([c, r]) => {
      if (c.status === "fulfilled")
        for (const e of c.value.items)
          for (const n of e.numbers)
            out.push({
              value: n.number,
              label: `${e.firstName} ${e.lastName}`.trim() || e.company,
            });
      if (r.status === "fulfilled")
        for (const call of r.value.items.slice(0, 10)) {
          const n =
            call.direction === "outbound" || call.direction === "internal"
              ? call.destination
              : call.source;
          if (n && !out.some((o) => o.value === n))
            out.push({ value: n, label: "Recent" });
        }
      if (!ctl.signal.aborted) setNames(out);
    });
    return () => ctl.abort();
  }, []);
  const go = (video: boolean) => {
    if (!num.trim()) return;
    if (sp.dial(num.trim(), video)) setNum("");
  };
  return (
    <form
      className="sp-dial"
      onSubmit={(e) => {
        e.preventDefault();
        go(false);
      }}
    >
      <label className="sp-field">
        <span>Number or name</span>
        <input
          list="sp-names"
          value={num}
          onChange={(e) => setNum(e.target.value)}
          inputMode="tel"
          autoComplete="off"
        />
        <datalist id="sp-names">
          {names.map((n) => (
            <option key={`${n.value}-${n.label}`} value={n.value}>
              {n.label}
            </option>
          ))}
        </datalist>
      </label>
      <div className="sp-dial__go">
        <Button type="submit" icon="phone" disabled={!num.trim()}>
          Call
        </Button>
        <Button
          variant="secondary"
          icon="video"
          disabled={!num.trim()}
          onClick={() => go(true)}
        >
          Video call
        </Button>
      </div>
    </form>
  );
}

/** Alt+A answers, Alt+H hangs up, Alt+M mutes (S-18). */
function useShortcuts(sp: Softphone) {
  useEffect(() => {
    const on = (e: KeyboardEvent) => {
      if (!e.altKey || e.ctrlKey || e.metaKey) return;
      const s = sp;
      const ringing = s.state.calls.find((c) => c.phase === "ringing");
      const focus = s.state.calls.find((c) => c.id === s.state.focus);
      const k = e.key.toLowerCase();
      if (k === "a" && ringing) {
        e.preventDefault();
        s.answer(ringing.id);
      } else if (k === "h" && (focus || ringing)) {
        e.preventDefault();
        s.hangup((focus ?? ringing)!.id);
      } else if (k === "m" && focus && focus.phase === "active") {
        e.preventDefault();
        s.mute(focus.id, !focus.muted);
      }
    };
    window.addEventListener("keydown", on);
    return () => window.removeEventListener("keydown", on);
  }, [sp]);
}

export function SoftphoneDock() {
  const sp = useSoftphone();
  if (!sp) return null;
  return <Dock sp={sp} />;
}

function Dock({ sp }: { sp: Softphone }) {
  useShortcuts(sp);
  const { state } = sp;
  const live = liveCalls(state);
  const waiting = callWaiting(state);
  const ringing = state.calls.filter((c) => c.phase === "ringing");
  const lines = state.calls.filter(
    (c) => c.phase !== "ringing" && c.phase !== "ended",
  );
  const ended = state.calls.filter((c) => c.phase === "ended");
  const focus = lines.find((c) => c.id === state.focus) ?? lines[0];
  const inCall = live.some((c) => c.phase !== "ringing");
  const open = sp.dockOpen;

  return (
    <aside className="sp-dock" aria-label="Phone">
      {ringing.map((c) => (
        <IncomingToast
          key={c.id}
          c={c}
          sp={sp}
          waiting={!!waiting && waiting.id === c.id}
        />
      ))}

      {open && (
        <div className="sp-panel" role="region" aria-label="Softphone">
          {!sp.supported && (
            <Alert tone="warn" title="This browser cannot be a phone">
              Calls need WebRTC and a secure (https) page.
            </Alert>
          )}
          {sp.supported && (
            <Switch
              label="Use this browser as a phone"
              checked={sp.enabled}
              disabled={sp.starting}
              onChange={(e) =>
                e.target.checked ? void sp.enable() : sp.disable()
              }
            />
          )}
          {sp.problem && (
            <Alert tone="bad" title="The phone is not ready">
              {sp.problem}
            </Alert>
          )}
          {state.reg === "other-tab" && (
            <div className="sp-note">
              <p>The phone is active in another tab of this browser.</p>
              <Button size="sm" variant="secondary" onClick={sp.takeOver}>
                Use the phone here
              </Button>
            </div>
          )}
          {state.reg === "unregistered" && (
            <Alert tone="warn" title="Connection lost">
              Reconnecting to the phone service. Calls cannot be placed or
              received until it returns.
            </Alert>
          )}
          {sp.enabled && state.reg === "registered" && <Dialer sp={sp} />}
          {ended.map((c) => (
            <EndedCall key={c.id} c={c} sp={sp} regTurn={sp.relay} />
          ))}
          {lines.length > 1 && (
            <div className="sp-lines" role="tablist" aria-label="Calls">
              {lines.map((c) => (
                <button
                  key={c.id}
                  role="tab"
                  type="button"
                  className="sp-lines__tab"
                  aria-selected={c.id === focus?.id}
                  onClick={() => sp.switchTo(c.id)}
                >
                  {who(c)}
                  {c.heldLocal ? " (held)" : ""}
                </button>
              ))}
            </div>
          )}
          {focus && <ActiveCall c={focus} sp={sp} />}
          {sp.enabled && state.reg === "registered" && <DevicePicker sp={sp} />}
          {state.voicemail > 0 && (
            <Link className="sp-vm" to="/me/voicemail">
              <Icon name="voicemail" size={14} /> {state.voicemail} new
              voicemail{state.voicemail === 1 ? "" : "s"}
            </Link>
          )}
        </div>
      )}

      <button
        type="button"
        className={`sp-chip sp-chip--${state.reg}${inCall ? " sp-chip--call" : ""}`}
        aria-expanded={open}
        aria-label={
          inCall
            ? `Phone, in a call. ${regLabel(state)}`
            : `Phone. ${regLabel(state)}`
        }
        onClick={() => sp.setDockOpen(!open)}
      >
        <Icon name={inCall ? "phone-call" : "phone"} size={16} />
        <span className="sp-chip__label">
          {inCall ? "In a call" : regLabel(state)}
        </span>
        {state.voicemail > 0 && (
          <span className="sp-chip__badge">{state.voicemail}</span>
        )}
      </button>
    </aside>
  );
}
