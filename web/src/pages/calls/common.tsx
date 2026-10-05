/**
 * Pieces shared by the calls pages (Dashboard, Active calls, Call history,
 * Call detail): the page header, the LIVE marker, a ticking clock, and how
 * call states, directions and statuses are labelled.
 */
import { useEffect, useMemo, useState, type ReactNode } from "react";
import { Link } from "react-router";
import {
  listExtensions,
  type ActiveCall,
  type Cdr,
  type TraceStep,
} from "../../api";
import { usePolling } from "../../usePolling";
import { type BadgeTone, Icon } from "../../design/azrty/components";
import "./calls.css";

/** A page's eyebrow, title, subtitle and actions, as in the console design. */
export function PageHeader({
  eyebrow,
  title,
  subtitle,
  actions,
  back,
}: {
  eyebrow?: string;
  title: ReactNode;
  subtitle?: ReactNode;
  actions?: ReactNode;
  back?: ReactNode;
}) {
  return (
    <header className="calls-head">
      <div>
        {back}
        {eyebrow && (
          <span className="az-eyebrow calls-head__eyebrow">{eyebrow}</span>
        )}
        <h1 id="page-title" className="calls-head__title">
          {title}
        </h1>
        {subtitle && <p className="calls-head__sub">{subtitle}</p>}
      </div>
      {actions && <div className="calls-head__actions">{actions}</div>}
    </header>
  );
}

/** A router link drawn as a design-system button (navigation, not an action). */
export function LinkButton({
  to,
  variant = "secondary",
  size = "md",
  icon,
  iconRight,
  className,
  children,
}: {
  to: string;
  variant?: "primary" | "secondary" | "ghost";
  size?: "sm" | "md";
  icon?: string;
  iconRight?: string;
  className?: string;
  children: ReactNode;
}) {
  const is = size === "sm" ? 13 : 15;
  return (
    <Link
      to={to}
      className={[
        "az-btn",
        `az-btn--${variant}`,
        size === "sm" ? "az-btn--sm" : "",
        className ?? "",
      ]
        .filter(Boolean)
        .join(" ")}
    >
      {icon && <Icon name={icon} size={is} />}
      {children}
      {iconRight && <Icon name={iconRight} size={is} />}
    </Link>
  );
}

/** The LIVE · 5 S marker of a view refreshed every five seconds. */
export function LiveMarker({ live }: { live: boolean }) {
  return (
    <span className="az-live">
      <span
        className={live ? "az-dot az-dot--pulse" : "az-dot calls-dot--off"}
      />
      {live ? "LIVE · 5 S" : "PAUSED"}
    </span>
  );
}

/** The current time, updated every `ms` (for running call durations). */
export function useNow(ms = 1000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), ms);
    return () => clearInterval(timer);
  }, [ms]);
  return now;
}

const pad = (n: number) => String(n).padStart(2, "0");

/** A timestamp as local HH:MM:SS; unknown is "—". */
export function clock(
  value: string | undefined | null,
  seconds = true,
): string {
  if (!value) return "—";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "—";
  const hm = `${pad(d.getHours())}:${pad(d.getMinutes())}`;
  return seconds ? `${hm}:${pad(d.getSeconds())}` : hm;
}

/** A timestamp's day: Today, Yesterday, or a short date. */
export function day(
  value: string | undefined | null,
  now = new Date(),
): string {
  if (!value) return "—";
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return "—";
  const start = (x: Date) =>
    new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  const diff = Math.round((start(now) - start(d)) / 86_400_000);
  if (diff === 0) return "Today";
  if (diff === 1) return "Yesterday";
  return d.toLocaleDateString(undefined, {
    day: "numeric",
    month: "short",
    year: d.getFullYear() === now.getFullYear() ? undefined : "numeric",
  });
}

/** Seconds as m:ss or h:mm:ss. */
export function elapsed(seconds: number): string {
  const s = Math.max(0, Math.floor(seconds));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  return h > 0 ? `${h}:${pad(m)}:${pad(s % 60)}` : `${m}:${pad(s % 60)}`;
}

/** How long an active call has run: since answer, or since start while ringing. */
export function callDuration(call: ActiveCall, now: number): string {
  const from = Date.parse(call.answeredAt || call.startedAt);
  return Number.isNaN(from) ? "—" : elapsed((now - from) / 1000);
}

/** An active call's state as a badge label and tone. */
export function callState(state: string): { label: string; tone: BadgeTone } {
  switch (state) {
    case "connected":
    case "answered":
      return { label: "Answered", tone: "good" };
    case "ringing":
      return { label: "Ringing", tone: "neutral" };
    case "held":
    case "hold":
    case "on-hold":
      return { label: "On hold", tone: "warn" };
    default:
      return {
        label: state ? state[0]!.toUpperCase() + state.slice(1) : "—",
        tone: "neutral",
      };
  }
}

/** A media mode in sentence case. */
export function mediaLabel(media: string): string {
  return media ? media[0]!.toUpperCase() + media.slice(1) : "—";
}

/** Direction labels and icons. */
export const DIRECTIONS: Record<
  Cdr["direction"],
  { label: string; icon: string }
> = {
  internal: { label: "Internal", icon: "arrow-left-right" },
  inbound: { label: "Inbound", icon: "phone-incoming" },
  outbound: { label: "Outbound", icon: "phone-outgoing" },
};

/** A CDR direction's label and icon (internal when unknown). */
export function directionOf(cdr: Pick<Cdr, "direction">) {
  return DIRECTIONS[cdr.direction] ?? DIRECTIONS.internal;
}

/** A failed call: no answer, or a final status outside 2xx. */
export function isFailed(
  cdr: Pick<Cdr, "answerTime" | "finalStatus">,
): boolean {
  return !cdr.answerTime || cdr.finalStatus < 200 || cdr.finalStatus >= 300;
}

/** A final status badge tone: 2xx good, 487 (the caller gave up) neutral, else bad. */
export function statusTone(status: number): BadgeTone {
  if (status >= 200 && status < 300) return "good";
  return status === 487 ? "neutral" : "bad";
}

/** A short SIP Call-ID: the first characters and the host, "7b1e…@10.0.0.1". */
export function shortCallId(id: string): string {
  const at = id.indexOf("@");
  const local = at >= 0 ? id.slice(0, at) : id;
  const host = at >= 0 ? id.slice(at) : "";
  return local.length > 6 ? `${local.slice(0, 4)}…${host}` : id;
}

/** How a routing trace step reads: matched, skipped, a warning, the failure, or a fact. */
export type StepKind = "hit" | "miss" | "warn" | "fail" | "info";

const STEP_LOOK: Record<StepKind, { icon: string; className: string }> = {
  hit: { icon: "check", className: "calls-step--hit" },
  miss: { icon: "minus", className: "calls-step--miss" },
  info: { icon: "arrow-right", className: "calls-step--info" },
  warn: { icon: "triangle-alert", className: "calls-step--warn" },
  fail: { icon: "x", className: "calls-step--fail" },
};

/**
 * Classifies a trace step by its text, as the routing engine and SIP nodes
 * write it. Only the icon depends on this; the text says it all.
 */
export function stepKind(
  text: string,
  last: boolean,
  failed: boolean,
): StepKind {
  if (last && failed) return "fail";
  if (/^Call not connected|^No route matched|failed\b/i.test(text))
    return "fail";
  if (
    /-> (?:[3-6]\d\d |no answer)|^Failover permitted|^No failover|: down$|at capacity|cancelled/i.test(
      text,
    )
  )
    return "warn";
  if (/skipped|-> no match|did not match|unchanged/i.test(text)) return "miss";
  if (
    /matched|-> extension |-> 200 OK|^Call established|usable|^Media anchored|identifies trunk|stored/i.test(
      text,
    )
  )
    return "hit";
  return "info";
}

/** A routing trace in step order, one sentence per step, with its icon. */
export function RoutingTrace({
  trace,
  failed,
  labelledBy,
}: {
  trace: readonly TraceStep[];
  failed: boolean;
  labelledBy: string;
}) {
  if (trace.length === 0) {
    return <p className="calls-muted">No trace was recorded.</p>;
  }
  const steps = [...trace].sort((a, b) => a.n - b.n);
  return (
    <ol className="calls-trace" aria-labelledby={labelledBy}>
      {steps.map((s, i) => {
        const look =
          STEP_LOOK[stepKind(s.text, i === steps.length - 1, failed)];
        return (
          <li key={s.n} className="calls-trace__step">
            <span className="calls-trace__n">{s.n}</span>
            <Icon name={look.icon} size={14} className={look.className} />
            <span className="calls-trace__text">{s.text}</span>
          </li>
        );
      })}
    </ol>
  );
}

/** Extension names keyed by number, for the From/To second lines. */
export function useExtensionNames(): Map<string, string> {
  const state = usePolling(listExtensions, 30_000);
  const items = state.status === "loading" ? undefined : state.data;
  return useMemo(
    () => new Map((items ?? []).map((e) => [e.number, e.name])),
    [items],
  );
}

/** A party of a call: the number, with the extension name below when known. */
export function Party({
  number,
  names,
}: {
  number: string;
  names: Map<string, string>;
}) {
  const name = names.get(number);
  return (
    <>
      <span className="calls-mono">{number || "—"}</span>
      {name && <small>{name}</small>}
    </>
  );
}
