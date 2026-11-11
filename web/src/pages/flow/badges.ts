// Badges on a node, derived from the summary the server sends. A new kind
// that needs a badge adds one rule to BADGE_RULES; nothing else changes.

import type { FlowNode } from "../../api/flow";

export interface NodeBadge {
  id: string;
  text: string;
  tone: "info" | "warn" | "neutral";
  title: string;
}

type Summary = FlowNode["summary"];

const BADGE_RULES: ((
  node: FlowNode,
  s: NonNullable<Summary>,
) => NodeBadge | null)[] = [
  // A time condition under a manual override ignores its schedule.
  (node, s) => {
    if (node.kind !== "timeCondition" || s.forced !== true) return null;
    const mode = typeof s.override === "string" ? s.override : "";
    const since =
      typeof s.overrideAt === "string" ? ` since ${s.overrideAt}` : "";
    return {
      id: "override",
      text: mode ? `Override: ${mode}` : "Override",
      tone: "warn",
      title: `A manual override forces this condition${since}; its schedule is ignored.`,
    };
  },
  // An announcement whose audio came from a speech provider.
  (node, s) =>
    node.kind === "announcement" && s.generated === true
      ? {
          id: "generated",
          text: "Generated",
          tone: "info",
          title: "The audio was generated from text (TTS).",
        }
      : null,
];

export function badgesFor(node: FlowNode): NodeBadge[] {
  const s = node.summary;
  if (!s) return [];
  const out: NodeBadge[] = [];
  for (const rule of BADGE_RULES) {
    const b = rule(node, s);
    if (b) out.push(b);
  }
  return out;
}
