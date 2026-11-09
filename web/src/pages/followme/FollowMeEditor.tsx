import { useEffect, useState } from "react";
import { errorMessage, fieldErrors, type Id } from "../../api";
import {
  FOLLOW_ME_MAX_TARGETS,
  type FollowMe,
  type FollowMeChange,
  type FollowMeKind,
  type FollowMeStrategy,
  type FollowMeTarget,
} from "../../api/followme";
import {
  Alert,
  Button,
  Input,
  Select,
  Spinner,
  Switch,
} from "../../design/azrty/components";

/** An external follow-me target: digits with an optional leading +. */
export const EXTERNAL_TARGET = /^\+?[0-9]{2,32}$/;

const KIND_OPTIONS = [
  { value: "external", label: "External number" },
  { value: "extension", label: "Extension" },
  { value: "ring_group", label: "Ring group" },
];

const STRATEGY_OPTIONS = [
  { value: "simultaneous", label: "All at once" },
  { value: "sequential", label: "One after another" },
];

const TARGET_HINT: Record<FollowMeKind, string> = {
  external: "2 to 32 digits, optionally starting with +",
  extension: "An extension number",
  ring_group: "The name of a ring group",
};

/** Errors by field key: a target's are keyed `targets.<index>.<field>`. */
export type FollowMeErrors = Record<string, string>;

/** Client-side checks that mirror the API's rules. */
export function validateFollowMe(
  d: FollowMe,
  ownNumber: string,
): FollowMeErrors {
  const e: FollowMeErrors = {};
  if (
    !Number.isInteger(d.timeoutSeconds) ||
    d.timeoutSeconds < 5 ||
    d.timeoutSeconds > 120
  ) {
    e.timeoutSeconds = "Between 5 and 120 seconds.";
  }
  if (d.enabled && d.targets.length === 0) {
    e.enabled = "Add at least one target before turning follow-me on.";
  }
  d.targets.forEach((t, i) => {
    const target = t.target.trim();
    if (target === "") {
      e[`targets.${i}.target`] = "Enter a target.";
    } else if (t.kind === "external" && !EXTERNAL_TARGET.test(target)) {
      e[`targets.${i}.target`] = TARGET_HINT.external + ".";
    } else if (t.kind !== "ring_group" && target === ownNumber) {
      e[`targets.${i}.target`] = "A target cannot be the extension itself.";
    }
    if (
      !Number.isInteger(t.delaySeconds) ||
      t.delaySeconds < 0 ||
      t.delaySeconds > 60
    ) {
      e[`targets.${i}.delaySeconds`] = "Between 0 and 60 seconds.";
    }
  });
  return e;
}

/** Server field errors (`targets[1].target`) as editor keys. */
function serverErrors(err: unknown): { byKey: FollowMeErrors; rest: string[] } {
  const byKey: FollowMeErrors = {};
  const rest: string[] = [];
  const known = [
    "enabled",
    "strategy",
    "ringOwnDevices",
    "timeoutSeconds",
    "confirmExternal",
    "dndBlocks",
    "targets",
  ];
  for (const f of fieldErrors(err)) {
    const m = /^targets\[(\d+)\]\.(\w+)$/.exec(f.path);
    if (m) {
      byKey[`targets.${m[1]}.${m[2]}`] = f.message;
    } else if (known.includes(f.path)) {
      byKey[f.path] = f.message;
    } else {
      rest.push(f.message);
    }
  }
  return { byKey, rest };
}

/**
 * The follow-me profile of one extension: who else rings when a call reaches
 * it. It has its own Save, since it is saved apart from the rest of the
 * extension, and renders no form of its own so a form can contain it.
 */
export function FollowMeEditor({
  id,
  ownNumber,
  load,
  save,
}: {
  id: Id;
  /** The extension's number, which no target may be. */
  ownNumber: string;
  load: (id: Id, signal?: AbortSignal) => Promise<FollowMe>;
  save: (id: Id, change: FollowMeChange) => Promise<FollowMe>;
}) {
  // What was loaded, with the extension it is for: a change of extension
  // reads as loading until its own profile arrives.
  const key = String(id);
  const [loaded, setLoaded] = useState<{ key: string; draft: FollowMe } | null>(
    null,
  );
  const [failed, setFailed] = useState<{ key: string; message: string } | null>(
    null,
  );
  const draft = loaded?.key === key ? loaded.draft : null;
  const loadError = failed?.key === key ? failed.message : null;
  const [errors, setErrors] = useState<FollowMeErrors>({});
  const [formError, setFormError] = useState<string | null>(null);
  const [saved, setSaved] = useState(false);
  const [saving, setSaving] = useState(false);

  useEffect(() => {
    const ctl = new AbortController();
    load(id, ctl.signal)
      .then((d) => setLoaded({ key, draft: d }))
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) setFailed({ key, message: errorMessage(err) });
      });
    return () => ctl.abort();
  }, [id, key, load]);

  if (loadError) {
    return <Alert tone="bad">Could not load follow-me: {loadError}</Alert>;
  }
  if (!draft) return <Spinner label="Loading follow-me" />;

  const change = (patch: Partial<FollowMe>) => {
    setSaved(false);
    setLoaded((l) => (l ? { ...l, draft: { ...l.draft, ...patch } } : l));
  };
  const setTarget = (i: number, patch: Partial<FollowMeTarget>) =>
    change({
      targets: draft.targets.map((t, j) => (j === i ? { ...t, ...patch } : t)),
    });

  async function onSave() {
    if (!draft || saving) return;
    setFormError(null);
    setSaved(false);
    const found = validateFollowMe(draft, ownNumber);
    setErrors(found);
    if (Object.keys(found).length > 0) return;
    setSaving(true);
    try {
      const next = await save(id, {
        enabled: draft.enabled,
        strategy: draft.strategy,
        ringOwnDevices: draft.ringOwnDevices,
        timeoutSeconds: draft.timeoutSeconds,
        confirmExternal: draft.confirmExternal,
        dndBlocks: draft.dndBlocks,
        targets: draft.targets.map((t) => ({
          kind: t.kind,
          target: t.target.trim(),
          delaySeconds: draft.strategy === "sequential" ? t.delaySeconds : 0,
        })),
      });
      setLoaded({ key, draft: next });
      setSaved(true);
    } catch (err) {
      const { byKey, rest } = serverErrors(err);
      setErrors(byKey);
      setFormError(
        rest.length > 0 || Object.keys(byKey).length === 0
          ? `Could not save follow-me: ${errorMessage(err)}`
          : null,
      );
    } finally {
      setSaving(false);
    }
  }

  const sequential = draft.strategy === "sequential";
  return (
    <div className="dir-section" role="group" aria-labelledby="follow-me-label">
      <span id="follow-me-label" className="az-eyebrow">
        Follow-me
      </span>
      {formError && <Alert tone="bad">{formError}</Alert>}
      {saved && <Alert tone="good">Follow-me saved.</Alert>}
      <Switch
        label="Follow-me"
        hint="Also ring the targets below when a call reaches this extension. *76 turns it on and *77 off from the phone."
        labelPosition="end"
        checked={draft.enabled}
        onChange={(e) => change({ enabled: e.target.checked })}
      />
      {errors.enabled && <Alert tone="bad">{errors.enabled}</Alert>}
      <Select
        id="fm-strategy"
        label="Ring"
        options={STRATEGY_OPTIONS}
        value={draft.strategy}
        error={errors.strategy}
        onChange={(e) =>
          change({ strategy: e.target.value as FollowMeStrategy })
        }
      />
      <Switch
        label="Ring this extension's own phones"
        labelPosition="end"
        checked={draft.ringOwnDevices}
        onChange={(e) => change({ ringOwnDevices: e.target.checked })}
      />
      <Input
        id="fm-timeout"
        label="Give up after (seconds)"
        mono
        size="sm"
        inputMode="numeric"
        hint="Then the no-answer forward or voicemail applies."
        error={errors.timeoutSeconds}
        value={String(draft.timeoutSeconds)}
        onChange={(e) => change({ timeoutSeconds: Number(e.target.value) })}
      />
      <Switch
        label="Ask an external number to press 1"
        hint="Keeps a mobile's voicemail from taking the call."
        labelPosition="end"
        checked={draft.confirmExternal}
        onChange={(e) => change({ confirmExternal: e.target.checked })}
      />
      <Switch
        label="Stop follow-me while on DND"
        labelPosition="end"
        checked={draft.dndBlocks}
        onChange={(e) => change({ dndBlocks: e.target.checked })}
      />
      <ol className="dir-device-list" aria-label="Follow-me targets">
        {draft.targets.map((t, i) => (
          <li key={i} className="dir-stack">
            <Select
              id={`fm-kind-${i}`}
              label={`Target ${i + 1} type`}
              options={KIND_OPTIONS}
              value={t.kind}
              onChange={(e) =>
                setTarget(i, { kind: e.target.value as FollowMeKind })
              }
            />
            <Input
              id={`fm-target-${i}`}
              label={`Target ${i + 1}`}
              mono
              hint={TARGET_HINT[t.kind]}
              error={errors[`targets.${i}.target`]}
              value={t.target}
              onChange={(e) => setTarget(i, { target: e.target.value })}
            />
            {sequential && (
              <Input
                id={`fm-delay-${i}`}
                label={`Target ${i + 1} rings for (seconds)`}
                mono
                size="sm"
                inputMode="numeric"
                hint="0 on the last target rings it to the end."
                error={errors[`targets.${i}.delaySeconds`]}
                value={String(t.delaySeconds)}
                onChange={(e) =>
                  setTarget(i, { delaySeconds: Number(e.target.value) })
                }
              />
            )}
            <Button
              variant="ghost"
              size="sm"
              onClick={() =>
                change({ targets: draft.targets.filter((_, j) => j !== i) })
              }
            >
              Remove target {i + 1}
            </Button>
          </li>
        ))}
      </ol>
      <div>
        <Button
          variant="secondary"
          size="sm"
          disabled={draft.targets.length >= FOLLOW_ME_MAX_TARGETS}
          onClick={() =>
            change({
              targets: [
                ...draft.targets,
                { kind: "external", target: "", delaySeconds: 0 },
              ],
            })
          }
        >
          Add target
        </Button>{" "}
        <Button size="sm" disabled={saving} onClick={() => void onSave()}>
          Save follow-me
        </Button>
      </div>
    </div>
  );
}
