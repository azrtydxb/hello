// Parts shared by the Phones pages (inventory, detail, templates, firmware,
// provisioning settings): the section tabs, a copyable value, and the
// redirect-status badge with its manual step.
import { NavLink } from "react-router";
import { type RedirectStatus, vendorLabel } from "../../api/prov";
import { Badge, Button, type BadgeTone } from "../../design/azrty/components";
import "./phones.css";

const SECTIONS = [
  { label: "Inventory", path: "/phones", end: true },
  { label: "Templates", path: "/phones/templates", end: false },
  { label: "Firmware", path: "/phones/firmware", end: false },
  { label: "Provisioning settings", path: "/phones/settings", end: false },
] as const;

/** The Phones section's sub-navigation, as links (each section has its own URL). */
export function PhonesTabs() {
  return (
    <nav className="az-tabs prov-tabs" aria-label="Phones sections">
      {SECTIONS.map((s) => (
        <NavLink
          key={s.path}
          to={s.path}
          end={s.end}
          className={({ isActive }) =>
            isActive ? "az-tab az-tab--active" : "az-tab"
          }
        >
          {s.label}
        </NavLink>
      ))}
    </nav>
  );
}

/** Copy text to the clipboard; tells `onDone` what happened either way. */
export async function copyText(
  text: string,
  what: string,
  onDone: (message: string) => void,
) {
  try {
    if (!navigator.clipboard?.writeText) throw new Error("no clipboard");
    await navigator.clipboard.writeText(text);
    onDone(`${what} copied.`);
  } catch {
    onDone(
      `Copying is not available here; select the ${what.toLowerCase()} and copy it.`,
    );
  }
}

/** A labelled, read-only, selectable value with a Copy button. */
export function CopyField({
  id,
  label,
  value,
  what,
  onCopied,
  autoFocus,
}: {
  id: string;
  label: string;
  value: string;
  /** What is copied, for the toast: "URL", "Value". */
  what: string;
  onCopied: (message: string) => void;
  autoFocus?: boolean;
}) {
  return (
    <div className="prov-copy">
      <span className="az-field__label" id={`${id}-label`}>
        {label}
      </span>
      <div className="prov-copy__box">
        <span
          className="prov-copy__value"
          aria-labelledby={`${id}-label`}
          role="textbox"
          aria-readonly="true"
        >
          {value}
        </span>
        <Button
          variant="secondary"
          size="sm"
          icon="copy"
          autoFocus={autoFocus}
          aria-label={`Copy ${label}`}
          onClick={() => void copyText(value, what, onCopied)}
        >
          Copy
        </Button>
      </div>
    </div>
  );
}

const REDIRECT_VIEW: Record<string, { tone: BadgeTone; label: string }> = {
  not_configured: { tone: "outline", label: "Not configured" },
  manual: { tone: "warn", label: "Manual step" },
  pending: { tone: "info", label: "Pending" },
  registered: { tone: "good", label: "Registered" },
  failed: { tone: "bad", label: "Failed" },
};

/** A phone's redirect status: "Registered", "Failed: drift", … */
export function RedirectBadge({
  status,
}: {
  status: RedirectStatus | null | undefined;
}) {
  const state = status?.state ?? "not_configured";
  const view = REDIRECT_VIEW[state] ?? {
    tone: "neutral" as const,
    label: state,
  };
  const text =
    state === "failed" && status?.reason
      ? `${view.label}: ${status.reason}`
      : view.label;
  return (
    <Badge tone={view.tone} dot>
      {text}
    </Badge>
  );
}

/**
 * What the administrator does by hand for a vendor whose redirect service
 * Hello cannot drive (spec S-11), or null when there is nothing to do.
 */
export function manualRedirectStep(vendor: string): string | null {
  switch (vendor) {
    case "poly":
      return "Poly has no public redirect API: paste the phone's provisioning URL into the Poly ZT portal for its MAC.";
    case "fanvil":
      return "Fanvil FDPS has no public API: paste the phone's provisioning URL into the FDPS portal for its MAC.";
    case "grandstream":
      return "GDMS adds the device but cannot bind it to a URL: set the GDMS site's provisioning server to the boot URL once; the phone then gets its token through the DHCP boot hand-off.";
    default:
      return null;
  }
}

/** "Yealink T54W" */
export const vendorModel = (p: { vendor: string; model: string }) =>
  `${vendorLabel(p.vendor)} ${p.model}`.trim();

/** "80:5e:c0:12:34:56" from "805ec0123456"; anything else as sent. */
export function formatMac(mac: string): string {
  return /^[0-9a-f]{12}$/i.test(mac)
    ? (mac.toLowerCase().match(/../g) ?? []).join(":")
    : mac;
}

/** A byte count as "1.4 MB". */
export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}
