// A flow canvas (spec flow-canvas P2): the call flow drawn from the live
// configuration, with positions saved per canvas. Read-only for the
// configuration: moving a node saves layout, nothing else.

import "@xyflow/react/dist/style.css";
import "./flow.css";
import {
  applyNodeChanges,
  Background,
  Controls,
  MiniMap,
  ReactFlow,
  ReactFlowProvider,
  useReactFlow,
  type NodeChange,
  type NodeTypes,
} from "@xyflow/react";
import {
  useCallback,
  useEffect,
  useMemo,
  useReducer,
  useRef,
  useState,
  type DragEvent,
  type KeyboardEvent,
} from "react";
import { Link, useParams } from "react-router";
import { ApiError, errorMessage } from "../../api";
import type { FlowTrace } from "../../api/flowTrace";
import {
  getFlowCanvas,
  getFlowCatalogue,
  getFlowGraph,
  getFlowRevision,
  putFlowLayout,
  updateFlowCanvas,
  type FlowCanvasDetail,
  type FlowCatalogue,
  type FlowGraph,
} from "../../api/flow";
import {
  Alert,
  Button,
  EmptyState,
  PageHeader,
  Select,
  Spinner,
  useTheme,
} from "../../design/azrty/components";
import { useCan } from "../../role";
import { usePolling } from "../../usePolling";
import { computeLayout, layoutInput } from "./autoLayout";
import { Legend } from "./Legend";
import { emptyLayout, layoutReducer, pendingWrite } from "./layoutState";
import { buildView, kindLabel, neighbour, type FlowRFNode } from "./model";
import { NodePanel } from "./NodePanel";
import { TracePanel } from "./TracePanel";
import {
  applyHighlight,
  highlightOf,
  missingFromCanvas,
} from "./traceHighlight";
import { FlowNodeView, StubNodeView } from "./nodes";
import { DRAG_TYPE, OffCanvasList } from "./OffCanvasList";
import { PortStylesContext, portStylesOf } from "./portStyles";

const nodeTypes: NodeTypes = { flow: FlowNodeView, stub: StubNodeView };

/** How often the canvas asks whether the configuration changed (spec S-9). */
export const REVISION_POLL_MS = 10_000;
const SAVE_DELAY_MS = 600;
const KEY_STEP = 20;
const ENTRY_KINDS = ["trunk", "anyTrunk", "inboundRoute", "featureCode"];

type SaveState = "idle" | "saving" | "saved" | "error";

export default function FlowCanvasPage() {
  const { canvasId = "" } = useParams();
  return (
    <ReactFlowProvider>
      {/* Keyed: another canvas starts from a clean load. */}
      <Canvas key={canvasId} canvasId={canvasId} />
    </ReactFlowProvider>
  );
}

interface Loaded {
  canvas: FlowCanvasDetail;
  catalogue: FlowCatalogue;
  graph: FlowGraph;
}

function Canvas({ canvasId }: { canvasId: string }) {
  const [boot, setBoot] = useState<
    | { status: "loading" }
    | { status: "error"; message: string; notFound: boolean }
    | ({ status: "ready" } & Loaded)
  >({ status: "loading" });

  useEffect(() => {
    const ctl = new AbortController();
    Promise.all([
      getFlowCanvas(canvasId, ctl.signal),
      getFlowCatalogue(ctl.signal),
      getFlowGraph(undefined, ctl.signal),
    ])
      .then(([canvas, catalogue, graph]) =>
        setBoot({ status: "ready", canvas, catalogue, graph }),
      )
      .catch((err: unknown) => {
        if (ctl.signal.aborted) return;
        setBoot({
          status: "error",
          message: errorMessage(err),
          notFound: err instanceof ApiError && err.status === 404,
        });
      });
    return () => ctl.abort();
  }, [canvasId]);

  if (boot.status === "loading") {
    return (
      <section aria-labelledby="page-title">
        <PageHeader eyebrow="Call flows" title="Loading canvas" />
        <Spinner label="Loading the call flow" />
      </section>
    );
  }
  if (boot.status === "error") {
    return (
      <section aria-labelledby="page-title">
        <PageHeader
          eyebrow="Call flows"
          title={boot.notFound ? "Canvas not found" : "Canvas unavailable"}
        />
        <Alert tone="bad" title="Could not open the canvas">
          {boot.message}
        </Alert>
        <p>
          <Link to="/flow">Back to the canvas list</Link>
        </p>
      </section>
    );
  }
  return (
    <CanvasView
      key={String(boot.canvas.id)}
      canvas={boot.canvas}
      catalogue={boot.catalogue}
      initialGraph={boot.graph}
    />
  );
}

function CanvasView({
  canvas,
  catalogue,
  initialGraph,
}: {
  canvas: FlowCanvasDetail;
  catalogue: FlowCatalogue;
  initialGraph: FlowGraph;
}) {
  const canEdit = useCan("operator");
  const { theme } = useTheme();
  const rf = useReactFlow();
  const stageRef = useRef<HTMLDivElement>(null);

  const [graph, setGraph] = useState(initialGraph);
  const [layout, dispatch] = useReducer(layoutReducer, emptyLayout, () =>
    layoutReducer(emptyLayout, {
      type: "load",
      version: canvas.layoutVersion,
      nodes: canvas.nodes,
    }),
  );
  const [selected, setSelected] = useState<string | null>(null);
  const [save, setSave] = useState<{ state: SaveState; message?: string }>({
    state: "idle",
  });
  const [note, setNote] = useState("");
  const [tick, setTick] = useState(0);
  const saving = useRef(false);

  // --- the configuration changed elsewhere: reload the graph silently -----
  const revision = usePolling(getFlowRevision, REVISION_POLL_MS);
  const latest = revision.status === "ready" ? revision.data : null;
  useEffect(() => {
    if (latest === null || latest === graph.configRevision) return;
    const ctl = new AbortController();
    getFlowGraph(undefined, ctl.signal)
      .then((g) => {
        if (!ctl.signal.aborted) setGraph(g);
      })
      .catch(() => undefined); // the next poll tries again
    return () => ctl.abort();
  }, [latest, graph.configRevision]);

  // --- the drawn nodes ------------------------------------------------------
  const baseView = useMemo(
    () =>
      buildView({ graph, catalogue, positions: layout.positions, selected }),
    [graph, catalogue, layout.positions, selected],
  );
  // Trace mode (P5): the path of a traced call is marked on the view.
  const [tracing, setTracing] = useState(false);
  const [trace, setTrace] = useState<FlowTrace | null>(null);
  const highlight = useMemo(
    () => (trace ? highlightOf(trace, graph) : null),
    [trace, graph],
  );
  const view = useMemo(
    () => applyHighlight(baseView, highlight),
    [baseView, highlight],
  );
  const traceOffCanvas = highlight
    ? missingFromCanvas(highlight, (id) => layout.positions[id] !== undefined)
    : 0;
  const closeTrace = useCallback(() => {
    setTracing(false);
    setTrace(null);
  }, []);
  const focusTraced = useCallback(
    (id: string) => {
      if (rf.getNode(id)) {
        void rf.fitView({ nodes: [{ id }], duration: 200, maxZoom: 1 });
      }
    },
    [rf],
  );
  // The library's own copy of the nodes follows the view (React's pattern
  // for state derived from a changed value: set it while rendering).
  const [nodes, setNodes] = useState<FlowRFNode[]>(view.nodes);
  const [seen, setSeen] = useState(view.nodes);
  if (seen !== view.nodes) {
    setSeen(view.nodes);
    setNodes(view.nodes);
  }
  const onNodesChange = useCallback((changes: NodeChange<FlowRFNode>[]) => {
    // Selection is ours (it opens the panel); the rest is the library's.
    const rest = changes.filter((c) => c.type !== "select");
    if (rest.length > 0) setNodes((ns) => applyNodeChanges(rest, ns));
  }, []);

  // --- saving layout ---------------------------------------------------------
  const pending = useMemo(() => pendingWrite(layout), [layout]);
  useEffect(() => {
    if (!canEdit || !pending || saving.current) return;
    const timer = setTimeout(() => {
      saving.current = true;
      setSave({ state: "saving" });
      void (async () => {
        let again = true;
        try {
          const res = await putFlowLayout(canvas.id, pending);
          dispatch({
            type: "synced",
            version: res.layoutVersion,
            nodes: res.nodes,
            sent: { nodes: pending.nodes, remove: pending.remove },
          });
          setSave({ state: "saved" });
        } catch (err) {
          if (err instanceof ApiError && err.status === 409) {
            // Someone else saved first: merge per node and send again.
            try {
              const fresh = await getFlowCanvas(canvas.id);
              dispatch({
                type: "synced",
                version: fresh.layoutVersion,
                nodes: fresh.nodes,
              });
            } catch (e2) {
              again = false;
              setSave({ state: "error", message: errorMessage(e2) });
            }
          } else {
            again = false;
            setSave({ state: "error", message: errorMessage(err) });
          }
        } finally {
          saving.current = false;
          if (again) setTick((t) => t + 1);
        }
      })();
    }, SAVE_DELAY_MS);
    return () => clearTimeout(timer);
  }, [pending, canEdit, canvas.id, tick]);

  // --- adding, moving, removing ---------------------------------------------
  const place = useCallback((id: string, x: number, y: number) => {
    dispatch({
      type: "place",
      nodes: [{ node: id, x: Math.round(x), y: Math.round(y) }],
    });
    setSelected(id);
  }, []);

  const addFromList = useCallback(
    (id: string) => {
      const rect = stageRef.current?.getBoundingClientRect();
      const centre = rf.screenToFlowPosition({
        x: (rect?.left ?? 0) + (rect?.width ?? 0) / 2,
        y: (rect?.top ?? 0) + (rect?.height ?? 0) / 2,
      });
      const n = Object.keys(layout.positions).length % 6;
      place(id, centre.x - 124 + n * 24, centre.y - 60 + n * 24);
    },
    [rf, layout.positions, place],
  );

  const onDrop = (e: DragEvent) => {
    const id = e.dataTransfer.getData(DRAG_TYPE);
    if (!canEdit || !id) return;
    e.preventDefault();
    const p = rf.screenToFlowPosition({ x: e.clientX, y: e.clientY });
    place(id, p.x - 124, p.y - 20);
  };

  const onDragOver = (e: DragEvent) => {
    if (canEdit && e.dataTransfer.types.includes(DRAG_TYPE)) {
      e.preventDefault();
      e.dataTransfer.dropEffect = "move";
    }
  };

  // --- import ---------------------------------------------------------------
  const entries = useMemo(
    () =>
      graph.nodes
        .filter((n) => ENTRY_KINDS.includes(n.kind))
        .sort((a, b) =>
          `${a.kind} ${a.label}`.localeCompare(
            `${b.kind} ${b.label}`,
            undefined,
            { numeric: true },
          ),
        ),
    [graph.nodes],
  );
  const [entry, setEntry] = useState("");
  const [importing, setImporting] = useState<AbortController | null>(null);
  const importFlow = async () => {
    if (!entry) return;
    const ctl = new AbortController();
    setImporting(ctl);
    setNote("");
    try {
      const g =
        entry === "all"
          ? graph
          : await getFlowGraph(`closure:${entry}`, ctl.signal);
      const input = layoutInput(g.nodes, g.edges, catalogue, layout.positions);
      if (input.nodes.length === 0) {
        setNote("Everything in that flow is already on this canvas.");
        return;
      }
      const placed = await computeLayout(input, ctl.signal);
      dispatch({
        type: "place",
        nodes: placed.map((p) => ({ node: p.node, x: p.x, y: p.y })),
      });
      setNote(`Placed ${placed.length} nodes.`);
      requestAnimationFrame(() => void rf.fitView({ duration: 200 }));
    } catch (err) {
      setNote(
        err instanceof Error && err.name === "AbortError"
          ? "Import cancelled."
          : `Import failed: ${errorMessage(err)}`,
      );
    } finally {
      setImporting(null);
    }
  };

  // --- selection and the keyboard -------------------------------------------
  const selectedNode = selected
    ? graph.nodes.find((n) => n.id === selected)
    : undefined;
  const closePanel = useCallback(() => {
    const id = selected;
    setSelected(null);
    // Focus goes back to the node the panel was opened from.
    requestAnimationFrame(() => {
      if (id) {
        document
          .querySelector<HTMLElement>(
            `.react-flow__node[data-id="${CSS.escape(id)}"]`,
          )
          ?.focus();
      }
    });
  }, [selected]);

  const activate = (
    id: string,
    type: string | undefined,
    x: number,
    y: number,
  ) => {
    if (type !== "stub") {
      setSelected(id);
      return;
    }
    const target = nodes.find((n) => n.id === id)?.data;
    const targetId =
      target && "target" in target ? String(target.target) : null;
    if (!targetId) return;
    if (canEdit) place(targetId, x, y);
    else setSelected(targetId);
  };

  const onKeyDown = (e: KeyboardEvent<HTMLDivElement>) => {
    const el = (e.target as HTMLElement).closest<HTMLElement>(
      ".react-flow__node",
    );
    const id = el?.dataset.id;
    if (!el || !id || e.target !== el) return;
    const dirs: Record<string, "left" | "right" | "up" | "down"> = {
      ArrowLeft: "left",
      ArrowRight: "right",
      ArrowUp: "up",
      ArrowDown: "down",
    };
    const dir = dirs[e.key];
    const node = nodes.find((n) => n.id === id);
    if (dir && e.altKey) {
      // Alt+arrow nudges a node: the keyboard way to arrange the canvas.
      e.preventDefault();
      if (canEdit && node?.type === "flow") {
        dispatch({
          type: "move",
          node: id,
          x:
            node.position.x +
            (dir === "left" ? -KEY_STEP : dir === "right" ? KEY_STEP : 0),
          y:
            node.position.y +
            (dir === "up" ? -KEY_STEP : dir === "down" ? KEY_STEP : 0),
        });
      }
    } else if (dir) {
      e.preventDefault();
      const next = neighbour(
        nodes.map((n) => ({ id: n.id, x: n.position.x, y: n.position.y })),
        id,
        dir,
      );
      if (next) {
        document
          .querySelector<HTMLElement>(
            `.react-flow__node[data-id="${CSS.escape(next)}"]`,
          )
          ?.focus();
      }
    } else if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      activate(id, node?.type, node?.position.x ?? 0, node?.position.y ?? 0);
    }
  };

  const saveView = async () => {
    try {
      await updateFlowCanvas(canvas.id, { viewport: rf.getViewport() });
      setNote("View saved.");
    } catch (err) {
      setNote(`Could not save the view: ${errorMessage(err)}`);
    }
  };

  const styles = useMemo(() => portStylesOf(catalogue), [catalogue]);
  const empty = Object.keys(layout.positions).length === 0;
  const status =
    save.state === "saving"
      ? "Saving layout…"
      : save.state === "saved"
        ? "Layout saved"
        : save.state === "error"
          ? `Layout not saved: ${save.message ?? "error"}`
          : pending && canEdit
            ? "Unsaved moves"
            : "";

  return (
    <section aria-labelledby="page-title" className="flow-page">
      <PageHeader
        back={
          <Link to="/flow" className="az-eyebrow">
            Call flows
          </Link>
        }
        title={canvas.name}
        description={
          canvas.description ||
          `Configuration revision ${graph.configRevision}. ${
            canEdit
              ? "Moving a node saves its place on this canvas."
              : "View only."
          }`
        }
        actions={
          <div className="flow-toolbar">
            <Button
              variant={tracing ? "primary" : "secondary"}
              size="sm"
              icon="route"
              aria-pressed={tracing}
              onClick={() => (tracing ? closeTrace() : setTracing(true))}
            >
              Trace a call
            </Button>
            {canEdit && (
              <>
                <Select
                  label="Import flow"
                  size="sm"
                  value={entry}
                  onChange={(e) => setEntry(e.target.value)}
                  options={[
                    { value: "", label: "Choose where it starts" },
                    ...entries.map((n) => ({
                      value: n.id,
                      label: `${kindLabel(catalogue, n.kind)}: ${n.label}`,
                    })),
                    { value: "all", label: "Everything" },
                  ]}
                />
                {importing ? (
                  <Button
                    variant="secondary"
                    size="sm"
                    icon="x"
                    onClick={() => importing.abort()}
                  >
                    Cancel layout
                  </Button>
                ) : (
                  <Button
                    size="sm"
                    icon="workflow"
                    disabled={!entry}
                    onClick={() => void importFlow()}
                  >
                    Import
                  </Button>
                )}
                <Button
                  variant="secondary"
                  size="sm"
                  icon="save"
                  onClick={() => void saveView()}
                >
                  Save view
                </Button>
              </>
            )}
          </div>
        }
      />
      <div className="flow-toolbar" aria-label="Canvas status">
        <Legend catalogue={catalogue} />
        <span className="flow-toolbar__status" role="status" aria-live="polite">
          {[status, importing ? "Laying out the flow…" : "", note]
            .filter(Boolean)
            .join(" ")}
        </span>
      </div>
      {trace && traceOffCanvas > 0 && (
        <Alert tone="info">
          {traceOffCanvas} steps of the traced call are on objects that are not
          on this canvas. Add them from the list to see the whole path.
        </Alert>
      )}
      {view.hiddenStubs > 0 && (
        <Alert tone="info">
          {view.hiddenStubs} more connections go to objects that are not on this
          canvas. Add them from the list to see them.
        </Alert>
      )}
      <div className="flow-shell">
        <OffCanvasList
          nodes={view.offCanvas}
          catalogue={catalogue}
          onAdd={canEdit ? addFromList : undefined}
          onSelect={setSelected}
        />
        <PortStylesContext.Provider value={styles}>
          <div
            ref={stageRef}
            className={`flow-stage${trace ? " flow-stage--tracing" : ""}`}
            role="region"
            aria-label="Flow canvas. Arrow keys move between nodes, Enter opens details, Alt and an arrow key moves a node."
            onKeyDown={onKeyDown}
            onDrop={onDrop}
            onDragOver={onDragOver}
          >
            <ReactFlow<FlowRFNode>
              nodes={nodes}
              edges={view.edges}
              nodeTypes={nodeTypes}
              onNodesChange={onNodesChange}
              colorMode={theme}
              nodesConnectable={false}
              nodesDraggable={canEdit}
              edgesFocusable={false}
              disableKeyboardA11y
              minZoom={0.1}
              maxZoom={1.75}
              fitView={!canvas.viewport}
              defaultViewport={canvas.viewport}
              onlyRenderVisibleElements={nodes.length > 200}
              onNodeClick={(_, n) =>
                activate(n.id, n.type, n.position.x, n.position.y)
              }
              onPaneClick={() => setSelected(null)}
              onNodeDragStop={(_, n) =>
                dispatch({
                  type: "move",
                  node: n.id,
                  x: Math.round(n.position.x),
                  y: Math.round(n.position.y),
                })
              }
              onSelectionDragStop={(_, ns) =>
                ns.forEach((n) =>
                  dispatch({
                    type: "move",
                    node: n.id,
                    x: Math.round(n.position.x),
                    y: Math.round(n.position.y),
                  }),
                )
              }
            >
              <Background gap={24} />
              <Controls showInteractive={false} />
              <MiniMap pannable zoomable ariaLabel="Overview of the canvas" />
            </ReactFlow>
            {empty && (
              <div className="flow-empty">
                <EmptyState
                  icon="workflow"
                  title="This canvas is empty"
                  description={
                    canEdit
                      ? "Import a flow from a trunk, a number or a feature code, or add objects from the list."
                      : "Nothing has been placed on it yet."
                  }
                />
              </div>
            )}
          </div>
        </PortStylesContext.Provider>
        {tracing && (
          <TracePanel
            graph={graph}
            trace={trace}
            onTrace={setTrace}
            onFocusNode={focusTraced}
            onClose={closeTrace}
          />
        )}
        {selectedNode && !tracing && (
          <NodePanel
            node={selectedNode}
            graph={graph}
            catalogue={catalogue}
            onCanvas={(id) => layout.positions[id] !== undefined}
            onSelect={setSelected}
            onClose={closePanel}
            onRemove={
              canEdit && layout.positions[selectedNode.id]
                ? () => {
                    dispatch({ type: "remove", node: selectedNode.id });
                    setSelected(null);
                  }
                : undefined
            }
          />
        )}
      </div>
    </section>
  );
}
