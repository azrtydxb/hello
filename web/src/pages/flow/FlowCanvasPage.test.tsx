import {
  act,
  configure,
  fireEvent,
  screen,
  waitFor,
  within,
} from "@testing-library/react";
import { afterAll, beforeAll, describe, expect, it, vi } from "vitest";
import { apiError, json, mockApi, renderApp } from "../../test/api";
import { CANVAS, CATALOGUE, GRAPH } from "./fixtures";
import { REVISION_POLL_MS } from "./FlowCanvasPage";

const me = (role: string) => ({
  "GET /api/v1/auth/me": () => json({ username: "u", role }),
});

function api(
  role: string,
  extra: Record<string, never | (() => Response)> = {},
) {
  return mockApi({
    ...me(role),
    "GET /api/v1/flow/catalogue": () => json(CATALOGUE),
    "GET /api/v1/flow/graph": () => json(GRAPH),
    "GET /api/v1/flow/revision": () => json({ configRevision: 12 }),
    "GET /api/v1/flow/canvases/1": () => json(CANVAS),
    ...extra,
  });
}

// App loads this page with React.lazy, so the first render in the file waits
// for Vite to transform and import @xyflow/react, dagre and their CSS. On a
// loaded CI runner that cold import alone outlasts findBy's 5 s budget (the
// flake seen on several PRs). Warm the module once, under the generous hook
// timeout, so the per-test awaits only cover rendering.
//
// Complement, not the fix: a whole-app render here is the heaviest in the
// suite, so give findBy/waitFor more room than the global 5 s. It only costs
// time when something is actually wrong.
beforeAll(async () => {
  configure({ asyncUtilTimeout: 15000 });
  await import("./FlowCanvasPage");
}, 60000);
afterAll(() => configure({ asyncUtilTimeout: 5000 }));

async function open() {
  renderApp("/flow/1");
  await screen.findByRole("heading", { name: "Main office" });
  await screen.findByText("Main number");
}

const nodeEl = (id: string) =>
  document.querySelector<HTMLElement>(`.react-flow__node[data-id="${id}"]`);

describe("flow canvas page", () => {
  it("draws the nodes of the canvas with ports, warnings and stubs", async () => {
    api("viewer");
    await open();
    expect(screen.getByText("Main number")).toBeInTheDocument();
    expect(screen.getByText("Sales")).toBeInTheDocument();
    expect(screen.getByText("100 Ann")).toBeInTheDocument();
    // The ring group's warning is a badge on the node.
    const sales = nodeEl("ringGroup:1")!;
    expect(within(sales).getByText("Warnings:")).toBeInTheDocument();
    // Ports come from the catalogue: label, colour variable and shape.
    const ring = sales.querySelector<HTMLElement>(
      '[data-handleid="in:members"]',
    )!;
    expect(ring.className).toContain("flow-handle--diamond");
    expect(ring.style.color).toContain("--flow-port-member");
    // Bob feeds the group but is not on this canvas: a stub.
    expect(
      document.querySelector('.react-flow__node[data-id^="stub:in:"]'),
    ).toHaveTextContent("101 Bob");
  });

  it("lists what is not on the canvas, searchable", async () => {
    api("viewer");
    await open();
    const side = screen
      .getByRole("heading", { name: "Not on this canvas" })
      .closest("section")!;
    expect(within(side).getByText("Extension (1)")).toBeInTheDocument();
    expect(within(side).getByText("IVR menu (1)")).toBeInTheDocument();
    fireEvent.change(within(side).getByLabelText("Search"), {
      target: { value: "welc" },
    });
    expect(within(side).queryByText("101 Bob")).not.toBeInTheDocument();
    expect(within(side).getByText("Welcome")).toBeInTheDocument();
  });

  it("shows no edit controls to a viewer", async () => {
    api("viewer");
    await open();
    expect(
      screen.queryByRole("button", { name: "Import" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^Add .* to the canvas$/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Save view" }),
    ).not.toBeInTheDocument();
    expect(nodeEl("ringGroup:1")?.className).not.toContain("draggable");
  });

  it("opens the panel on click: settings, connections, warnings and the page link; Escape closes it", async () => {
    api("viewer");
    await open();
    fireEvent.click(screen.getByText("Sales"));
    const panel = await screen.findByRole("complementary", {
      name: "Selected node",
    });
    expect(
      within(panel).getByRole("heading", { name: "Sales" }),
    ).toBeInTheDocument();
    expect(within(panel).getByText("Strategy")).toBeInTheDocument();
    expect(within(panel).getByText("ring_all")).toBeInTheDocument();
    expect(
      within(panel).getByText("Ring group Sales has no members."),
    ).toBeInTheDocument();
    expect(within(panel).getByText(/View only/)).toBeInTheDocument();
    expect(
      within(panel).getByRole("link", { name: /Open Ring group page/ }),
    ).toHaveAttribute("href", "/ring-groups");
    // A connection can be followed, including to a node that is not on the canvas.
    expect(within(panel).getByText(/101 Bob/)).toBeInTheDocument();
    expect(
      within(panel).getByText(/\(not on this canvas\)/),
    ).toBeInTheDocument();
    expect(
      within(panel).queryByRole("button", { name: "Remove from canvas" }),
    ).not.toBeInTheDocument();
    fireEvent.keyDown(window, { key: "Escape" });
    await waitFor(() =>
      expect(
        screen.queryByRole("complementary", { name: "Selected node" }),
      ).not.toBeInTheDocument(),
    );
  });

  it("moves between nodes with the arrow keys and opens one with Enter", async () => {
    api("viewer");
    await open();
    const route = nodeEl("inboundRoute:1")!;
    route.focus();
    // The stub of Bob, who feeds the group, sits between the two.
    fireEvent.keyDown(route, { key: "ArrowRight" });
    const stub = document.querySelector<HTMLElement>(
      '.react-flow__node[data-id^="stub:in:"]',
    )!;
    expect(stub).toHaveFocus();
    fireEvent.keyDown(stub, { key: "ArrowRight" });
    expect(nodeEl("ringGroup:1")).toHaveFocus();
    fireEvent.keyDown(nodeEl("ringGroup:1")!, { key: "ArrowLeft" });
    expect(stub).toHaveFocus();
    fireEvent.keyDown(stub, { key: "ArrowLeft" });
    expect(route).toHaveFocus();
    fireEvent.keyDown(route, { key: "ArrowDown" });
    expect(nodeEl("extension:1")).toHaveFocus();
    fireEvent.keyDown(nodeEl("extension:1")!, { key: "Enter" });
    expect(
      await screen.findByRole("heading", { name: "100 Ann" }),
    ).toBeInTheDocument();
  });

  it("saves a moved node's place, with the layout version", async () => {
    const calls = api("operator", {
      "PUT /api/v1/flow/canvases/1/layout": () =>
        json({
          ...CANVAS,
          layoutVersion: 4,
          nodes: [
            { node: "extension:1", x: 20, y: 200, collapsed: false },
            ...CANVAS.nodes.slice(0, 2),
          ],
        }),
    });
    await open();
    const ann = nodeEl("extension:1")!;
    ann.focus();
    fireEvent.keyDown(ann, { key: "ArrowRight", altKey: true });
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    const put = calls.find((c) => c.method === "PUT")!;
    expect(put.url).toBe("/api/v1/flow/canvases/1/layout");
    expect(put.body).toEqual({
      layoutVersion: 3,
      nodes: [{ node: "extension:1", x: 20, y: 200, collapsed: false }],
      remove: [],
    });
    await screen.findByText("Layout saved");
    // Saving layout touches nothing else.
    expect(calls.filter((c) => c.method !== "GET")).toHaveLength(1);
  });

  it("merges and sends again when the layout moved under it (409)", async () => {
    let puts = 0;
    const calls = api("operator", {
      "PUT /api/v1/flow/canvases/1/layout": () => {
        puts++;
        return puts === 1
          ? apiError(409, "layout_stale", "the layout changed")
          : json({ ...CANVAS, layoutVersion: 8 });
      },
      "GET /api/v1/flow/canvases/1": [
        () => json(CANVAS),
        () => json({ ...CANVAS, layoutVersion: 7 }),
      ] as never,
    });
    await open();
    const ann = nodeEl("extension:1")!;
    ann.focus();
    fireEvent.keyDown(ann, { key: "ArrowDown", altKey: true });
    await waitFor(() => expect(puts).toBe(2));
    const bodies = calls.filter((c) => c.method === "PUT").map((c) => c.body);
    expect(bodies[0]).toMatchObject({ layoutVersion: 3 });
    expect(bodies[1]).toMatchObject({
      layoutVersion: 7,
      nodes: [{ node: "extension:1", x: 0, y: 220 }],
    });
  });

  it("adds an object from the list and takes one off the canvas", async () => {
    const calls = api("operator", {
      "PUT /api/v1/flow/canvases/1/layout": () =>
        json({
          ...CANVAS,
          layoutVersion: 4,
          nodes: [
            ...CANVAS.nodes,
            { node: "ivr:5", x: 0, y: 0, collapsed: false },
          ],
        }),
    });
    await open();
    fireEvent.click(
      screen.getByRole("button", { name: "Add Welcome to the canvas" }),
    );
    // The panel opens on the node just placed; it is no longer in the list.
    expect(
      await screen.findByRole("heading", { name: "Welcome" }),
    ).toBeInTheDocument();
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    expect(calls.find((c) => c.method === "PUT")?.body).toMatchObject({
      nodes: [{ node: "ivr:5" }],
    });
    fireEvent.click(
      await screen.findByRole("button", { name: "Remove from canvas" }),
    );
    await waitFor(() =>
      expect(calls.filter((c) => c.method === "PUT")).toHaveLength(2),
    );
    expect(calls.filter((c) => c.method === "PUT")[1]?.body).toMatchObject({
      remove: ["ivr:5"],
    });
  });

  it("imports a flow, laying out only what is new", async () => {
    const closure = {
      ...GRAPH,
      nodes: GRAPH.nodes.filter((n) => n.id !== "ivr:5"),
    };
    const calls = api("operator", {
      "GET /api/v1/flow/graph?scope=closure%3AinboundRoute%3A1": () =>
        json(closure),
      "PUT /api/v1/flow/canvases/1/layout": () =>
        json({ ...CANVAS, layoutVersion: 4 }),
    });
    await open();
    fireEvent.change(screen.getByLabelText("Import flow"), {
      target: { value: "inboundRoute:1" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Import" }));
    await screen.findByText(/Placed 1 nodes/);
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    // Only Bob was missing from the closure.
    expect(calls.find((c) => c.method === "PUT")?.body).toMatchObject({
      nodes: [{ node: "extension:2" }],
    });
  });

  it("reloads the graph when the configuration revision moves", async () => {
    let revision = 12;
    let graphReads = 0;
    api("viewer", {
      "GET /api/v1/flow/revision": () => json({ configRevision: revision }),
      "GET /api/v1/flow/graph": () => {
        graphReads++;
        return json(
          graphReads === 1
            ? GRAPH
            : {
                ...GRAPH,
                configRevision: 13,
                nodes: GRAPH.nodes.map((n) =>
                  n.id === "ringGroup:1" ? { ...n, label: "Sales team" } : n,
                ),
              },
        );
      },
    });
    // The poll's timer is driven by the test, not waited out on the wall clock
    // (it used to sit through the real 10 s interval).
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      await open();
      revision = 13;
      await act(() => vi.advanceTimersByTimeAsync(REVISION_POLL_MS));
      await screen.findByText("Sales team");
      expect(graphReads).toBe(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("explains a canvas that does not exist and one that is empty", async () => {
    mockApi({
      ...me("viewer"),
      "GET /api/v1/flow/canvases/1": () =>
        apiError(404, "not_found", "flow canvas: not found"),
      "GET /api/v1/flow/catalogue": () => json(CATALOGUE),
      "GET /api/v1/flow/graph": () => json(GRAPH),
    });
    renderApp("/flow/1");
    expect(
      await screen.findByRole("heading", { name: "Canvas not found" }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Back to the canvas list" }),
    ).toHaveAttribute("href", "/flow");
  });

  it("says so when nothing is placed yet", async () => {
    api("viewer", {
      "GET /api/v1/flow/canvases/1": () =>
        json({ ...CANVAS, nodes: [], nodeCount: 0 }),
    });
    renderApp("/flow/1");
    expect(await screen.findByText("This canvas is empty")).toBeInTheDocument();
    expect(
      screen.getByText("Nothing has been placed on it yet."),
    ).toBeInTheDocument();
  });
});
