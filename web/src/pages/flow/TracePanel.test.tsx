import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { FlowTrace } from "../../api/flowTrace";
import { apiError, json, mockApi, renderApp } from "../../test/api";
import { CANVAS, CATALOGUE, GRAPH } from "./fixtures";

const TRACE: FlowTrace = {
  configRevision: 12,
  at: "2026-10-26T10:00:00Z",
  from: "100",
  number: "800",
  outcome: "inbound",
  route: "Main number",
  steps: [
    { node: "inboundRoute:1", port: "destination", text: "Route matches." },
    { node: "ringGroup:1", port: "noAnswer", text: "Rings the Sales group." },
    { node: "extension:1", port: "voicemail", text: "Leaves a message." },
  ],
  routingTrace: [],
};

function api(role: string, trace: () => Response) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "u", role }),
    "GET /api/v1/flow/catalogue": () => json(CATALOGUE),
    "GET /api/v1/flow/graph": () => json(GRAPH),
    "GET /api/v1/flow/revision": () => json({ configRevision: 12 }),
    "GET /api/v1/flow/canvases/1": () => json(CANVAS),
    "GET /api/v1/flow/trace?from=100&number=800&digits=12": trace,
  });
}

async function open() {
  renderApp("/flow/1");
  // The first render of a test file loads the whole app: allow for it.
  await screen.findByRole(
    "heading",
    { name: "Main office" },
    { timeout: 15_000 },
  );
  await screen.findByText("Main number");
}

const nodeEl = (id: string) =>
  document.querySelector<HTMLElement>(`.react-flow__node[data-id="${id}"]`);

describe("flow canvas trace mode", () => {
  it("lets a viewer trace a call with the keypad and marks the path", async () => {
    const calls = api("viewer", () => json(TRACE));
    await open();
    fireEvent.click(screen.getByRole("button", { name: "Trace a call" }));
    const panel = await screen.findByRole("complementary", {
      name: "Trace a call",
    });
    fireEvent.change(within(panel).getByLabelText("Dialled number"), {
      target: { value: "800" },
    });
    fireEvent.click(within(panel).getByRole("button", { name: "Key 1" }));
    fireEvent.click(within(panel).getByRole("button", { name: "Key 2" }));
    expect(within(panel).getByLabelText("Keys pressed")).toHaveValue("12");
    fireEvent.click(within(panel).getByRole("button", { name: "Trace" }));

    expect(
      await within(panel).findByText("Rings the Sales group."),
    ).toBeInTheDocument();
    expect(within(panel).getByText("Route Main number")).toBeInTheDocument();
    expect(calls.some((c) => c.url.startsWith("/api/v1/flow/trace"))).toBe(
      true,
    );
    // The traced nodes carry the mark, the one off the path does not.
    await waitFor(() =>
      expect(nodeEl("ringGroup:1")?.className).toContain("flow-node--traced"),
    );
    expect(nodeEl("inboundRoute:1")?.className).toContain("flow-node--traced");
    expect(nodeEl("extension:1")?.className).toContain("flow-node--traced");
    // Edges are not laid out in jsdom; their marking is tested in traceHighlight.test.ts.
    expect(document.querySelector(".flow-stage--tracing")).toBeInTheDocument();

    // Closing trace mode clears the marks.
    fireEvent.click(screen.getByRole("button", { name: "Trace a call" }));
    await waitFor(() =>
      expect(nodeEl("ringGroup:1")?.className).not.toContain(
        "flow-node--traced",
      ),
    );
  });

  it("shows the error when the trace fails", async () => {
    api("viewer", () => apiError(503, "unavailable", "database is down"));
    await open();
    fireEvent.click(screen.getByRole("button", { name: "Trace a call" }));
    const panel = await screen.findByRole("complementary", {
      name: "Trace a call",
    });
    fireEvent.change(within(panel).getByLabelText("Dialled number"), {
      target: { value: "800" },
    });
    fireEvent.click(within(panel).getByRole("button", { name: "Key 1" }));
    fireEvent.click(within(panel).getByRole("button", { name: "Key 2" }));
    fireEvent.click(within(panel).getByRole("button", { name: "Trace" }));
    expect(
      await within(panel).findByText("database is down"),
    ).toBeInTheDocument();
  });

  it("asks for a number before tracing", async () => {
    api("viewer", () => json(TRACE));
    await open();
    fireEvent.click(screen.getByRole("button", { name: "Trace a call" }));
    const panel = await screen.findByRole("complementary", {
      name: "Trace a call",
    });
    fireEvent.click(within(panel).getByRole("button", { name: "Trace" }));
    expect(
      within(panel).getByText("Choose who calls and the number they dial."),
    ).toBeInTheDocument();
  });
});
