import { render, screen } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it } from "vitest";
import type { ProposalAction } from "../../api/aiagent";
import { BatchPreview, canvasLinkOf } from "./BatchPreview";

const action = (canvasId?: number): ProposalAction => ({
  operationId: "applyConfigBatch",
  pathParams: {},
  body: { baseRevision: 3, canvasId, ops: [] },
  preview: {
    baseRevision: 3,
    results: [
      {
        operationId: "updateRingGroup",
        status: "updated",
        node: "ringGroup:2",
        before: {},
        after: {},
        diff: [{ path: "/name", before: "a", after: "b" }],
      },
    ],
    warnings: [
      {
        node: "ivr:4",
        code: "unreachable",
        message: "Nothing reaches IVR Menu.",
      },
    ],
    flowDiff: {
      added: [],
      changed: [],
      removed: [],
      edgesAdded: [],
      edgesRemoved: [],
    },
  },
});

describe("BatchPreview", () => {
  it("lists each operation's dry run and links to the proposal on its canvas", () => {
    render(
      <MemoryRouter>
        <BatchPreview proposalId="p 1" action={action(5)} />
      </MemoryRouter>,
    );
    expect(
      screen.getByText(/updateRingGroup ringGroup:2: \/name/),
    ).toBeInTheDocument();
    expect(screen.getByText("Nothing reaches IVR Menu.")).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Show it on the call-flow canvas" }),
    ).toHaveAttribute("href", "/flow/5?proposal=p%201");
    expect(canvasLinkOf("p", action())).toBe("/flow");
  });
});
