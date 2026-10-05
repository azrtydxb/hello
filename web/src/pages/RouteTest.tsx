import { useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useSearchParams } from "react-router";
import {
  errorMessage,
  fieldErrors,
  listExtensions,
  listTrunks,
  type Extension,
  type FieldError,
  type RouteTestRequest,
  type TraceStep,
  type Trunk,
} from "../api";
import {
  testCall,
  type CallflowDecision,
  type CallflowTestResult,
} from "../api/callflow";
import {
  Alert,
  Badge,
  Button,
  EmptyState,
  Icon,
  Input,
  PropertyList,
  SegmentedControl,
  Select,
  type BadgeTone,
  type Property,
} from "../design/azrty/components";
import { mapFieldErrors, type ErrorMap } from "../forms";
import { orUnknown, sipStatus, UNKNOWN } from "./callflow/format";
import { FormAlert, Loading, PageHeader } from "./callflow/ui";

type FromKind = "extension" | "trunk";

type Load<T> =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: T[] };

interface Presented {
  tone: "good" | "bad" | "info" | "warn";
  icon: string;
  title: string;
  kindLabel: string;
  chain: { k: string; v: string }[];
  facts: Property[];
}

/** The design's decision card, from the tester's answer. */
function present(d: CallflowDecision, from: string): Presented {
  const trunks = d.trunks ?? [];
  const callerId = {
    label: "Caller ID",
    value: orUnknown(d.callerId),
    mono: true,
  };
  switch (d.kind) {
    case "internal":
      return {
        tone: "good",
        icon: "arrow-left-right",
        title: `Internal call to ${orUnknown(d.extension)}`,
        kindLabel: "Internal call",
        chain: [
          { k: "From", v: from },
          { k: "Extension", v: orUnknown(d.extension) },
        ],
        facts: [
          { label: "Outcome", value: "Internal call" },
          { label: "Extension", value: orUnknown(d.extension), mono: true },
          { label: "SIP URI", value: orUnknown(d.sipUri), mono: true },
        ],
      };
    case "outbound": {
      const first = trunks[0] ?? UNKNOWN;
      return {
        tone: d.emergency ? "bad" : "good",
        icon: d.emergency ? "siren" : "phone-outgoing",
        title: d.emergency
          ? `Emergency call via ${first}`
          : `Outbound via ${first}`,
        kindLabel: d.emergency ? "Emergency" : "Outbound via trunks",
        chain: [
          { k: "From", v: from },
          { k: "Route", v: orUnknown(d.route) },
          { k: "Number sent", v: orUnknown(d.number) },
          { k: "Trunk", v: first },
        ],
        facts: [
          { label: "Outcome", value: "Outbound via trunks" },
          { label: "Route", value: orUnknown(d.route) },
          { label: "Number sent", value: orUnknown(d.number), mono: true },
          callerId,
          {
            label: "Trunks, in try order",
            value:
              trunks.length > 0 ? (
                <ol
                  className="cf-chips cf-plain"
                  aria-label="Trunks, in try order"
                >
                  {trunks.map((t, i) => (
                    <li key={t} className="cf-inline">
                      {i > 0 && <Icon name="chevron-right" size={12} />}
                      <span className="cf-chip">{t}</span>
                    </li>
                  ))}
                </ol>
              ) : (
                UNKNOWN
              ),
          },
        ],
      };
    }
    case "inbound": {
      const target = d.extension || d.sipUri || d.number || "";
      return {
        tone: "good",
        icon: "phone-incoming",
        title: `Inbound to ${orUnknown(target)}`,
        kindLabel: "Inbound",
        chain: [
          { k: "From", v: from },
          { k: "Route", v: orUnknown(d.route) },
          { k: "Destination", v: orUnknown(target) },
        ],
        facts: [
          { label: "Outcome", value: "Inbound" },
          { label: "Route", value: orUnknown(d.route) },
          d.extension
            ? { label: "Extension", value: d.extension, mono: true }
            : d.sipUri
              ? { label: "SIP URI", value: d.sipUri, mono: true }
              : {
                  label: "Number sent",
                  value: orUnknown(d.number),
                  mono: true,
                },
          callerId,
        ],
      };
    }
    case "reject":
      return {
        tone: "bad",
        icon: "ban",
        title: d.rejectCode
          ? `Rejected · ${sipStatus(d.rejectCode)}`
          : "Rejected",
        kindLabel: "Rejected",
        chain: [],
        facts: [
          { label: "Outcome", value: "Rejected" },
          {
            label: "Rejected with",
            value: d.rejectCode ? String(d.rejectCode) : UNKNOWN,
            mono: true,
          },
          { label: "Reason", value: orUnknown(d.reason) },
        ],
      };
    default:
      return {
        tone: "info",
        icon: "info",
        title: String(d.kind),
        kindLabel: String(d.kind),
        chain: [],
        facts: [{ label: "Outcome", value: String(d.kind) }],
      };
  }
}

/** Reads ?from= (an extension number or trunk:<id>) and ?number=. */
function initialFrom(params: URLSearchParams) {
  const from = params.get("from") ?? "";
  const trunk = /^trunk:(.+)$/.exec(from);
  return {
    kind: (trunk ? "trunk" : "extension") as FromKind,
    extension: trunk ? "" : from,
    trunk: trunk?.[1] ?? "",
    number: params.get("number") ?? "",
  };
}

/** Route tester: decide a call against the live configuration without placing it. */
export function RouteTest() {
  const [params] = useSearchParams();
  const [initial] = useState(() => initialFrom(params));
  const [extensions, setExtensions] = useState<Load<Extension>>({
    status: "loading",
  });
  const [trunks, setTrunks] = useState<Load<Trunk>>({ status: "loading" });
  const [fromKind, setFromKind] = useState<FromKind>(initial.kind);
  const [fromExtension, setFromExtension] = useState(initial.extension);
  const [fromTrunk, setFromTrunk] = useState(initial.trunk);
  const [number, setNumber] = useState(initial.number);
  const [callerId, setCallerId] = useState("");
  const [at, setAt] = useState("");
  const [errors, setErrors] = useState<ErrorMap>({});
  const [unmatched, setUnmatched] = useState<FieldError[]>([]);
  const [formError, setFormError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<{
    data: CallflowTestResult;
    from: string;
  } | null>(null);
  const autorun = useRef(
    initial.number !== "" && (initial.extension !== "" || initial.trunk !== ""),
  );
  const formRef = useRef<HTMLFormElement>(null);

  useEffect(() => {
    const controller = new AbortController();
    const fail = (set: (s: Load<never>) => void) => (err: unknown) => {
      if (!controller.signal.aborted) {
        set({ status: "error", message: errorMessage(err) });
      }
    };
    listExtensions(controller.signal)
      .then((items) => setExtensions({ status: "ready", items }))
      .catch(fail(setExtensions));
    listTrunks(controller.signal)
      .then((items) => setTrunks({ status: "ready", items }))
      .catch(fail(setTrunks));
    return () => controller.abort();
  }, []);

  // Opened with ?from=…&number=… (e.g. "Test a route" on a trunk): test at once.
  useEffect(() => {
    if (autorun.current) {
      autorun.current = false;
      formRef.current?.requestSubmit();
    }
  }, []);

  const trunkName = (id: string) =>
    trunks.status === "ready"
      ? (trunks.items.find((t) => String(t.id) === id)?.name ?? `trunk ${id}`)
      : `trunk ${id}`;

  async function onSubmit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const found: Record<string, string> = {};
    const from =
      fromKind === "extension"
        ? fromExtension.trim()
        : fromTrunk && `trunk:${fromTrunk}`;
    if (!from) {
      found.from =
        fromKind === "extension" ? "Choose an extension." : "Choose a trunk.";
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
      const data = await testCall(body);
      setResult({
        data,
        from:
          fromKind === "extension"
            ? fromExtension.trim()
            : trunkName(fromTrunk),
      });
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

  const extensionOptions =
    extensions.status === "ready"
      ? [
          { value: "", label: "Choose…" },
          ...extensions.items.map((x) => ({
            value: x.number,
            label: x.name ? `${x.number} · ${x.name}` : x.number,
          })),
          // Keep a number from the link even if it is not in the list.
          ...(fromExtension &&
          !extensions.items.some((x) => x.number === fromExtension)
            ? [{ value: fromExtension, label: fromExtension }]
            : []),
        ]
      : [];
  const trunkOptions = [
    { value: "", label: "Choose…" },
    ...(trunks.status === "ready"
      ? trunks.items.map((t) => ({ value: String(t.id), label: t.name }))
      : []),
    ...(fromTrunk &&
    !(
      trunks.status === "ready" &&
      trunks.items.some((t) => String(t.id) === fromTrunk)
    )
      ? [{ value: fromTrunk, label: `Trunk ${fromTrunk}` }]
      : []),
  ];

  const view = result ? present(result.data.decision, result.from) : null;

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        title="Route tester"
        description="Shows how a call would be routed now, or at a chosen time, against the live configuration. No call is placed."
        back={
          <Link
            to="/routes"
            className="az-btn az-btn--ghost az-btn--sm cf-back"
          >
            <Icon name="arrow-left" size={14} />
            Routes
          </Link>
        }
      />
      <div className="cf-tester">
        <form
          ref={formRef}
          className="az-card cf-card cf-card--form"
          aria-labelledby="rt-call"
          onSubmit={(e) => void onSubmit(e)}
          noValidate
        >
          <h2 className="cf-card__title" id="rt-call">
            Call
          </h2>
          <div className="cf-field-group">
            <span className="az-field__label" id="rt-from-label">
              From
            </span>
            <SegmentedControl<FromKind>
              aria-label="From"
              block
              value={fromKind}
              onChange={setFromKind}
              options={[
                { value: "extension", label: "Extension" },
                { value: "trunk", label: "Trunk (inbound)" },
              ]}
            />
          </div>
          {fromKind === "extension" ? (
            extensions.status === "ready" ? (
              <Select
                id="rt-from"
                label="Extension"
                value={fromExtension}
                options={extensionOptions}
                error={errors.from}
                onChange={(e) => setFromExtension(e.target.value)}
              />
            ) : (
              <Input
                id="rt-from"
                label="Extension"
                mono
                inputMode="numeric"
                value={fromExtension}
                error={errors.from}
                hint={
                  extensions.status === "error"
                    ? `The extension list did not load (${extensions.message}); type the number.`
                    : "Loading extensions…"
                }
                onChange={(e) => setFromExtension(e.target.value)}
              />
            )
          ) : (
            <Select
              id="rt-from"
              label="Trunk"
              value={fromTrunk}
              options={trunkOptions}
              error={
                errors.from ??
                (trunks.status === "error"
                  ? `The trunk list did not load: ${trunks.message}`
                  : undefined)
              }
              onChange={(e) => setFromTrunk(e.target.value)}
            />
          )}
          <Input
            id="rt-number"
            label={
              fromKind === "trunk" ? "Called number (DID)" : "Dialled number"
            }
            mono
            value={number}
            error={errors.number}
            hint={
              fromKind === "trunk"
                ? "The number the carrier sent."
                : "An extension, or an external number as dialled."
            }
            onChange={(e) => setNumber(e.target.value)}
          />
          <div className="cf-two">
            <Input
              id="rt-caller"
              label="Caller ID"
              mono
              placeholder="Optional"
              value={callerId}
              error={errors.callerId}
              onChange={(e) => setCallerId(e.target.value)}
            />
            <Input
              id="rt-at"
              label="At"
              type="datetime-local"
              value={at}
              error={errors.at}
              hint="Empty means now"
              onChange={(e) => setAt(e.target.value)}
            />
          </div>
          <FormAlert message={formError} unmatched={unmatched} />
          <Button type="submit" icon="play" block disabled={busy}>
            {busy ? "Testing…" : "Test"}
          </Button>
        </form>

        <div className="cf-tester__result">
          {!view && busy && (
            <div className="az-card cf-card">
              <Loading what="the decision" />
            </div>
          )}
          {!view && !busy && (
            <div className="az-card">
              <EmptyState
                icon="flask-conical"
                title="No test yet"
                description="Choose who calls and the number, then select Test. Nothing is dialled."
              />
            </div>
          )}
          {view && result && (
            <>
              <section
                className="az-card cf-card cf-card--form"
                aria-labelledby="rt-decision"
                aria-busy={busy}
              >
                <div className="cf-card__head">
                  <span className={`cf-tile cf-tile--${view.tone}`}>
                    <Icon name={view.icon} size={18} />
                  </span>
                  <div style={{ minWidth: 0 }}>
                    <span className="az-eyebrow">Decision</span>
                    <h2 className="cf-decision__title" id="rt-decision">
                      {view.title}
                    </h2>
                  </div>
                  <Badge tone={view.tone as BadgeTone}>{view.kindLabel}</Badge>
                </div>
                {view.chain.length > 0 && (
                  <p className="cf-chain">
                    {view.chain.map((c, i) => (
                      <ChainStep key={c.k} k={c.k} v={c.v} later={i > 0} />
                    ))}
                  </p>
                )}
                <PropertyList items={view.facts} />
              </section>
              <section className="az-card cf-card" aria-labelledby="rt-trace">
                <h2 className="cf-card__title" id="rt-trace">
                  Trace
                </h2>
                <Trace
                  trace={result.data.trace}
                  tone={view.tone === "bad" ? "bad" : "good"}
                />
              </section>
            </>
          )}
          {trunks.status === "error" && fromKind === "extension" && (
            <Alert tone="warn" title="Trunk names unavailable">
              {trunks.message}
            </Alert>
          )}
        </div>
      </div>
    </section>
  );
}

function ChainStep({ k, v, later }: { k: string; v: string; later: boolean }) {
  return (
    <>
      {later && <Icon name="arrow-right" size={13} />}
      <span className="cf-chain__step">
        <span className="cf-chain__k">{k}</span>
        <span className="cf-chain__v">{v}</span>
      </span>
    </>
  );
}

/** The routing trace, in step order; the last step carries the outcome. */
function Trace({
  trace,
  tone,
}: {
  trace: readonly TraceStep[];
  tone: "good" | "bad";
}) {
  if (trace.length === 0) {
    return <p className="cf-form__note">No trace was recorded.</p>;
  }
  const steps = [...trace].sort((a, b) => a.n - b.n);
  return (
    <ol className="cf-trace" aria-labelledby="rt-trace">
      {steps.map((s, i) => {
        const last = i === steps.length - 1;
        return (
          <li
            key={s.n}
            value={s.n}
            className={
              last ? `cf-trace__step cf-trace__step--${tone}` : "cf-trace__step"
            }
          >
            <span className="cf-trace__n" aria-hidden="true">
              {s.n}
            </span>
            <Icon
              name={
                last
                  ? tone === "bad"
                    ? "circle-x"
                    : "circle-check"
                  : "chevron-right"
              }
              size={14}
            />
            <span className="cf-trace__text">{s.text}</span>
          </li>
        );
      })}
    </ol>
  );
}
