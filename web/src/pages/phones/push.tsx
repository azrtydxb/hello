import { useMemo, type ReactNode } from "react";
import type { BulkPush, PhonePush, PushKind, PushStatus } from "../../api/prov";
import {
  Badge,
  ConfirmDialog,
  type BadgeTone,
} from "../../design/azrty/components";
import { formatTime } from "../../format";

/** The words for a push result; "not acknowledged" is the spec's wording for a timeout. */
export const PUSH_LABEL: Record<PushStatus, string> = {
  pending: "Queued",
  sending: "Sent",
  ok: "Acknowledged (200)",
  not_registered: "Not registered",
  timeout: "Push not acknowledged",
  auth_failed: "Phone rejected the request, needs one manual reboot",
  failed: "Failed",
};

const PUSH_TONE: Record<PushStatus, BadgeTone> = {
  pending: "neutral",
  sending: "info",
  ok: "good",
  not_registered: "warn",
  timeout: "warn",
  auth_failed: "bad",
  failed: "bad",
};

/** Whether a push is still on its way, so a page keeps polling. */
export const pushOpen = (p: PhonePush) =>
  p.status === "pending" || p.status === "sending";

/** The result of one push as a badge: "Resync: Acknowledged (200)". */
export function PushBadge({ push }: { push: PhonePush }) {
  const kind = push.kind === "reboot" ? "Reboot" : "Resync";
  return (
    <Badge tone={PUSH_TONE[push.status]}>
      {kind}: {PUSH_LABEL[push.status]}
    </Badge>
  );
}

/** A push with its time and detail, for the phone page. */
export function PushLine({ push }: { push: PhonePush }) {
  return (
    <>
      <PushBadge push={push} />{" "}
      <span className="prov-muted prov-cell-sm">
        {formatTime(push.finishedAt ?? push.requestedAt)}
        {push.source === "auto" ? " (automatic)" : ""}
        {push.contacts > 0 ? `, ${push.contacts} contact(s)` : ""}
        {push.detail && push.status !== "ok" ? `, ${push.detail}` : ""}
      </span>
    </>
  );
}

/** The newest push of each phone, by phone id. */
export function useLatestByPhone(pushes: PhonePush[] | undefined) {
  return useMemo(() => {
    const m = new Map<string, PhonePush>();
    for (const p of pushes ?? []) m.set(String(p.phoneId), p);
    return m;
  }, [pushes]);
}

/** The toast after a bulk push was queued. */
export function bulkMessage(kind: PushKind, r: BulkPush): string {
  const verb = kind === "reboot" ? "reboot" : "resync";
  const skipped = r.skipped > 0 ? `; ${r.skipped} skipped (no device)` : "";
  return `${r.queued} ${r.queued === 1 ? "phone" : "phones"} queued to ${verb}${skipped}.`;
}

/**
 * The confirmation a reboot (single or bulk) asks for: handsets drop their
 * calls while they restart. `run` queues the push and returns the toast.
 */
export function RebootDialog({
  title,
  description,
  run,
  onDone,
  onClose,
}: {
  title: string;
  description: ReactNode;
  run: () => Promise<string>;
  onDone: (message: string) => void;
  onClose: () => void;
}) {
  return (
    <ConfirmDialog
      title={title}
      description={description}
      confirmLabel="Reboot"
      confirmIcon="power"
      errorTitle="Could not queue the reboot"
      onConfirm={async () => {
        onDone(await run());
      }}
      onClose={onClose}
    />
  );
}
