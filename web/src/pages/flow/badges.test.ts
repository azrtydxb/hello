import { describe, expect, it } from "vitest";
import { badgesFor } from "./badges";
import { buildView } from "./model";
import { CATALOGUE, GRAPH } from "./fixtures";
import type { FlowNode } from "../../api/flow";

const node = (kind: string, summary: FlowNode["summary"]): FlowNode =>
  ({ id: `${kind}:1`, kind, label: "x", summary }) as FlowNode;

describe("badgesFor", () => {
  it("badges a forced time condition with its mode", () => {
    const b = badgesFor(
      node("timeCondition", { forced: true, override: "closed" }),
    );
    expect(b.map((x) => x.text)).toEqual(["Override: closed"]);
  });
  it("does not badge an automatic time condition", () => {
    expect(
      badgesFor(node("timeCondition", { forced: false, override: "auto" })),
    ).toEqual([]);
  });
  it("badges a generated announcement only", () => {
    expect(badgesFor(node("announcement", { generated: true }))).toHaveLength(
      1,
    );
    expect(badgesFor(node("announcement", { generated: false }))).toEqual([]);
    expect(badgesFor(node("ivr", { generated: true }))).toEqual([]);
  });
  it("reaches the drawn node data", () => {
    const g = {
      ...GRAPH,
      nodes: GRAPH.nodes.map((x) =>
        x.id === "ivr:5"
          ? {
              ...x,
              kind: "timeCondition",
              summary: { forced: true, override: "open" },
            }
          : x,
      ),
    };
    const v = buildView({
      graph: g,
      catalogue: CATALOGUE,
      positions: { "ivr:5": { x: 0, y: 0, collapsed: false } },
    });
    const d = v.nodes.find((x) => x.id === "ivr:5")?.data as {
      badges: { id: string }[];
    };
    expect(d.badges.map((b) => b.id)).toEqual(["override"]);
  });
});
