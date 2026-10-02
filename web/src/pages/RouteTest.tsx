import { useEffect, useState, type FormEvent } from "react";
import { Link } from "react-router";
import {
  errorMessage,
  fieldErrors,
  listExtensions,
  listTrunks,
  testRoute,
  type Extension,
  type FieldError,
  type RouteTestRequest,
  type RouteTestResult,
  type Trunk,
} from "../api";
import { TraceList } from "../components/TraceList";
import { Field, FormError, mapFieldErrors, type ErrorMap } from "../forms";

const KIND_LABEL: Record<string, string> = {
  internal: "Internal call",
  outbound: "Outbound via trunks",
  inbound: "Inbound",
  reject: "Rejected",
};

/** Route tester: decide a call against the live configuration without placing it. */
export function RouteTest() {
  const [extensions, setExtensions] = useState<Extension[]>([]);
  const [trunks, setTrunks] = useState<Trunk[]>([]);
  const [fromKind, setFromKind] = useState<"extension" | "trunk">("extension");
  const [fromExtension, setFromExtension] = useState("");
  const [fromTrunk, setFromTrunk] = useState("");
  const [number, setNumber] = useState("");
  const [callerId, setCallerId] = useState("");
  const [at, setAt] = useState("");
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<RouteTestResult | null>(null);

  useEffect(() => {
    const controller = new AbortController();
    listExtensions(controller.signal)
      .then(setExtensions)
      .catch(() => {});
    listTrunks(controller.signal)
      .then(setTrunks)
      .catch(() => {});
    return () => controller.abort();
  }, []);

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const found: Record<string, string> = {};
    const from =
      fromKind === "extension"
        ? fromExtension.trim()
        : fromTrunk && `trunk:${fromTrunk}`;
    if (!from) {
      found.from =
        fromKind === "extension"
          ? "Choose or enter an extension."
          : "Choose a trunk.";
    }
    if (number.trim() === "") found.number = "Enter the dialled number.";
    let atIso: string | undefined;
    if (at !== "") {
      const d = new Date(at);
      if (Number.isNaN(d.getTime())) found.at = "Enter a valid date and time.";
      else atIso = d.toISOString();
    }
    setErrors(found);
    setUnmatched([]);
    setFormError(null);
    if (Object.keys(found).length > 0 || !from) return;

    const body: RouteTestRequest = { from, number: number.trim() };
    if (callerId.trim() !== "") body.callerId = callerId.trim();
    if (atIso) body.at = atIso;
    setBusy(true);
    try {
      setResult(await testRoute(body));
    } catch (err) {
      const mapped = mapFieldErrors(fieldErrors(err), [
        "from",
        "number",
        "callerId",
        "at",
      ]);
      setErrors(mapped.byKey);
      setUnmatched(mapped.unmatched);
      setFormError(errorMessage(err));
      setResult(null);
    } finally {
      setBusy(false);
    }
  }

  const decision = result?.decision;
  return (
    <section aria-labelledby="page-title">
      <p>
        <Link to="/routes">← Routes</Link>
      </p>
      <h1 id="page-title">Route tester</h1>
      <p className="muted">
        Shows how a call would be routed now, or at a chosen time. No call is
        placed.
      </p>
      <form
        className="inline-form"
        aria-label="Test a call"
        onSubmit={(e) => void onSubmit(e)}
        noValidate
      >
        <div className="fields">
          <Field id="rt-from-kind" label="From">
            {(p) => (
              <select
                {...p}
                value={fromKind}
                onChange={(e) =>
                  setFromKind(e.target.value as "extension" | "trunk")
                }
              >
                <option value="extension">An extension</option>
                <option value="trunk">A trunk (inbound)</option>
              </select>
            )}
          </Field>
          {fromKind === "extension" ? (
            <Field id="rt-from" label="Extension" error={errors.from}>
              {(p) => (
                <>
                  <input
                    {...p}
                    list="rt-extensions"
                    inputMode="numeric"
                    value={fromExtension}
                    onChange={(e) => setFromExtension(e.target.value)}
                  />
                  <datalist id="rt-extensions">
                    {extensions.map((x) => (
                      <option key={String(x.id)} value={x.number}>
                        {x.name}
                      </option>
                    ))}
                  </datalist>
                </>
              )}
            </Field>
          ) : (
            <Field id="rt-from" label="Trunk" error={errors.from}>
              {(p) => (
                <select
                  {...p}
                  value={fromTrunk}
                  onChange={(e) => setFromTrunk(e.target.value)}
                >
                  <option value="">Choose…</option>
                  {trunks.map((t) => (
                    <option key={String(t.id)} value={String(t.id)}>
                      {t.name}
                    </option>
                  ))}
                </select>
              )}
            </Field>
          )}
          <Field
            id="rt-number"
            label={
              fromKind === "trunk" ? "Called number (DID)" : "Dialled number"
            }
            error={errors.number}
          >
            {(p) => (
              <input
                {...p}
                value={number}
                onChange={(e) => setNumber(e.target.value)}
              />
            )}
          </Field>
          <Field
            id="rt-caller"
            label="Caller ID"
            error={errors.callerId}
            hint="Optional."
          >
            {(p) => (
              <input
                {...p}
                value={callerId}
                onChange={(e) => setCallerId(e.target.value)}
              />
            )}
          </Field>
          <Field
            id="rt-at"
            label="At"
            error={errors.at}
            hint="Optional; your local time. Empty means now."
          >
            {(p) => (
              <input
                {...p}
                type="datetime-local"
                value={at}
                onChange={(e) => setAt(e.target.value)}
              />
            )}
          </Field>
        </div>
        <FormError message={formError} unmatched={unmatched} />
        <button type="submit" className="primary" disabled={busy}>
          {busy ? "Testing…" : "Test"}
        </button>
      </form>

      {result && decision && (
        <section aria-labelledby="rt-result" className="result">
          <h2 id="rt-result">Decision</h2>
          <dl className="facts">
            <dt>Outcome</dt>
            <dd>{KIND_LABEL[decision.kind] ?? decision.kind}</dd>
            {decision.route && (
              <>
                <dt>Route</dt>
                <dd>{decision.route}</dd>
              </>
            )}
            {decision.extension && (
              <>
                <dt>Extension</dt>
                <dd>{decision.extension}</dd>
              </>
            )}
            {decision.sipUri && (
              <>
                <dt>SIP URI</dt>
                <dd>
                  <code>{decision.sipUri}</code>
                </dd>
              </>
            )}
            {decision.number && (
              <>
                <dt>Number sent</dt>
                <dd>{decision.number}</dd>
              </>
            )}
            {decision.callerId && (
              <>
                <dt>Caller ID</dt>
                <dd>{decision.callerId}</dd>
              </>
            )}
            {decision.trunks && decision.trunks.length > 0 && (
              <>
                <dt>Trunks, in try order</dt>
                <dd>
                  <ol className="inline-list">
                    {decision.trunks.map((t) => (
                      <li key={t}>{t}</li>
                    ))}
                  </ol>
                </dd>
              </>
            )}
            {decision.kind === "reject" && (
              <>
                <dt>Rejected with</dt>
                <dd>{decision.rejectCode ?? "—"}</dd>
                <dt>Reason</dt>
                <dd>{decision.reason || "—"}</dd>
              </>
            )}
          </dl>
          <h2 id="rt-trace">Trace</h2>
          <TraceList trace={result.trace} labelledBy="rt-trace" />
        </section>
      )}
    </section>
  );
}
