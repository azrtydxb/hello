// What the inbox shows of the AI (spec messaging-channels M-30, M-30a): the
// triage labels on a conversation, its summary with an undo, the reply draft
// an agent must click to use, and the auto-reply indicator. The model's text
// is shown as text, never as markup.

import type { Conversation } from "../../api/inbox";
import { Badge, Button } from "../../design/azrty/components";

const URGENCY_TONE = {
  low: "neutral",
  normal: "neutral",
  high: "bad",
} as const;

/** Why the AI handed a conversation to a person, in words. */
export function handoffText(reason: string): string {
  switch (reason) {
    case "handoff_word":
      return "the customer asked for a person";
    case "urgent":
      return "the text is urgent";
    case "negative":
      return "the customer is unhappy";
    case "sensitive":
      return "a sensitive topic";
    case "intent":
      return "the AI may not answer this kind of question";
    case "low_confidence":
      return "the AI is not sure enough";
    case "no_faq":
      return "the number has no FAQ";
    case "not_grounded":
    case "url_not_in_faq":
    case "placeholder":
    case "invalid_reply":
      return "the FAQ does not answer it";
    case "model_handoff":
      return "the AI asked for a person";
    case "reply_cap":
      return "the AI reply limit is reached";
    case "hourly_cap":
      return "the hourly AI limit is reached";
    case "no_triage":
    case "ai_error":
      return "the AI is unavailable";
    default:
      return "a person should answer";
  }
}

/** The AI labels of a conversation in the list: compact, status in words. */
export function AIListChips({ conv }: { conv: Conversation }) {
  const ai = conv.ai;
  if (!ai) return null;
  const t = ai.triage && !ai.triage.reverted ? ai.triage : null;
  return (
    <>
      {ai.mode === "auto" && (
        <Badge tone="info" icon="bot">
          Auto-reply
        </Badge>
      )}
      {ai.handoff && <Badge tone="warn">Needs a person</Badge>}
      {ai.spam && <Badge tone="bad">Spam?</Badge>}
      {t && t.intent !== "other" && <Badge tone="outline">{t.intent}</Badge>}
      {t?.urgency === "high" && <Badge tone="bad">Urgent</Badge>}
    </>
  );
}

/**
 * The triage of the latest text: the model's summary, its labels, what it
 * changed, and the button that undoes that.
 */
export function AITriagePanel({
  conv,
  onRevert,
  busy,
}: {
  conv: Conversation;
  onRevert: () => void;
  busy: boolean;
}) {
  const ai = conv.ai;
  if (!ai || ai.mode === "off") return null;
  const t = ai.triage;
  const changed =
    ai.applied.tags.length > 0 || ai.applied.priority !== "" || ai.spam;
  return (
    <section className="inbox-ai" aria-label="AI triage">
      <div className="inbox-ai__head">
        <span className="az-eyebrow">AI triage</span>
        {ai.mode === "auto" && (
          <Badge tone="info" icon="bot">
            Auto-reply on
          </Badge>
        )}
        {ai.pending && <span className="az-caption">Reading…</span>}
      </div>
      {ai.handoff && (
        <p className="inbox-ai__handoff" role="status">
          The AI handed this conversation to a person:{" "}
          {handoffText(ai.handoffReason)}.
        </p>
      )}
      {t ? (
        <>
          <p className="inbox-ai__summary">{t.summary}</p>
          <div className="inbox-ai__chips">
            <Badge tone="outline">{t.intent}</Badge>
            <Badge tone={URGENCY_TONE[t.urgency]}>urgency {t.urgency}</Badge>
            <Badge tone={t.sentiment === "negative" ? "warn" : "neutral"}>
              {t.sentiment}
            </Badge>
            <Badge tone="neutral">{t.language}</Badge>
            {t.sensitive && <Badge tone="warn">Sensitive</Badge>}
            {t.spam && <Badge tone="bad">Spam?</Badge>}
            {t.suggestedQueue && (
              <Badge tone="neutral">Suggests {t.suggestedQueue}</Badge>
            )}
            {t.reverted && <Badge tone="neutral">Undone</Badge>}
          </div>
          {changed && (
            <div className="inbox-ai__applied">
              <span className="az-caption">
                {[
                  ai.applied.tags.length > 0 &&
                    `Added ${ai.applied.tags.join(", ")}`,
                  ai.applied.priority !== "" && "raised the priority",
                  ai.spam && "flagged as spam",
                ]
                  .filter(Boolean)
                  .join("; ")}
                .
              </span>
              <Button
                size="sm"
                variant="ghost"
                disabled={busy}
                onClick={onRevert}
              >
                Undo
              </Button>
            </div>
          )}
        </>
      ) : (
        !ai.pending && <p className="az-caption">No suggestion.</p>
      )}
    </section>
  );
}

/**
 * The reply the AI drafted, dimmed above the composer. Nothing is sent: a
 * click puts it in the composer, where the agent may change it and sends it.
 */
export function AIDraftChip({
  text,
  onUse,
  onDismiss,
}: {
  text: string;
  onUse: () => void;
  onDismiss: () => void;
}) {
  return (
    <div className="inbox-ai-draft" role="group" aria-label="AI draft">
      <p className="inbox-ai-draft__text">
        <span className="az-eyebrow">AI draft</span> {text}
      </p>
      <div className="inbox-actions">
        <Button type="button" size="sm" variant="secondary" onClick={onUse}>
          Use draft
        </Button>
        <Button type="button" size="sm" variant="ghost" onClick={onDismiss}>
          Dismiss
        </Button>
      </div>
    </div>
  );
}
