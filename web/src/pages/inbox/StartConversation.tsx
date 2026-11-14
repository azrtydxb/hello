import { useState, type FormEvent } from "react";
import { errorMessage, fieldErrors } from "../../api";
import {
  startConversation,
  type Conversation,
  type InboxSender,
} from "../../api/inbox";
import {
  Alert,
  Button,
  Checkbox,
  Input,
  Modal,
  Select,
  useRestoreFocus,
} from "../../design/azrty/components";

/**
 * Starts a conversation with a customer who has not texted first: one
 * number, one message, one attestation. There is no list or import form.
 */
export function StartConversation({
  senders,
  onClose,
  onStarted,
}: {
  senders: InboxSender[];
  onClose: () => void;
  onStarted: (c: Conversation) => void;
}) {
  useRestoreFocus();
  const [from, setFrom] = useState(String(senders[0]?.addressId ?? ""));
  const [to, setTo] = useState("");
  const [body, setBody] = useState("");
  const [attested, setAttested] = useState(false);
  const [note, setNote] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [fields, setFields] = useState<Record<string, string>>({});
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError(null);
    setFields({});
    try {
      onStarted(
        await startConversation({
          addressId: Number(from),
          to: to.trim(),
          body: body.trim(),
          consentAttestation: attested,
          consentNote: note.trim(),
        }),
      );
    } catch (err: unknown) {
      setFields(
        Object.fromEntries(fieldErrors(err).map((f) => [f.path, f.message])),
      );
      setError(errorMessage(err));
      setBusy(false);
    }
  }

  return (
    <Modal
      title="Text a customer"
      description="Starts a conversation with a customer who has not texted you. One customer and one message at a time."
      onClose={onClose}
      actions={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button type="submit" form="start-form" disabled={busy || !attested}>
            Send text
          </Button>
        </>
      }
    >
      {error && (
        <Alert tone="bad" title="Could not send the text">
          {error}
        </Alert>
      )}
      <form id="start-form" className="cf-stack" onSubmit={submit}>
        <Select
          label="From"
          value={from}
          error={fields.addressId}
          options={senders.map((s) => ({
            value: String(s.addressId),
            label: `${s.address}${s.label ? ` (${s.label})` : ""}, ${s.channel}`,
          }))}
          onChange={(e) => setFrom(e.target.value)}
        />
        <Input
          label="Customer's number"
          hint="In international format, such as +32475123456."
          value={to}
          error={fields.to}
          autoFocus
          onChange={(e) => setTo(e.target.value)}
        />
        <Input
          label="Message"
          value={body}
          error={fields.body}
          onChange={(e) => setBody(e.target.value)}
        />
        <Checkbox
          label="The customer agreed to be texted"
          hint="Your confirmation is stored with the conversation and recorded in the audit log."
          checked={attested}
          onChange={(e) => setAttested(e.target.checked)}
        />
        {fields.consentAttestation && (
          <Alert tone="bad" title="Confirm consent">
            {fields.consentAttestation}
          </Alert>
        )}
        <Input
          label="How did they agree?"
          hint="For example: asked for a callback by text on the website form."
          value={note}
          error={fields.consentNote}
          onChange={(e) => setNote(e.target.value)}
        />
      </form>
    </Modal>
  );
}
