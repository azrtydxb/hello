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
  type Connection,
  type NodeChange,
  type NodeTypes,
} from "@xyflow/react";
import {
  lazy,
  Suspense,
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
import { ApiError, errorMessage, request } from "../../api";
import {
  applyConfigBatch,
  conflictsOf,
  isStale,
  problemsOf,
  type BatchResponse,
} from "../../api/configBatch";
import {
  getFlowCanvas,
  getFlowCatalogue,
  getFlowGraph,
  getFlowRevision,
  putFlowLayout,
  updateFlowCanvas,
  type FlowCanvasDetail,
  type FlowCatalogue,
  type FlowEdge,
  type FlowGraph,
  type FlowNode,
} from "../../api/flow";
import {
  Alert,
  Button,
  ConfirmDialog,
  EmptyState,
  Input,
  Modal,
  PageHeader,
  Select,
  Spinner,
  useTheme,
} from "../../design/azrty/components";
import { useAuth } from "../../auth";
import { useCan } from "../../role";
import { usePolling } from "../../usePolling";
import { computeLayout, layoutInput } from "./autoLayout";
import { CreateDialog } from "./CreateDialog";
import {
  compile,
  compiledOrder,
  draftKey,
  draftMarks,
  draftReducer,
  emptyDraft,
  isPending,
  loadDraft,
  project,
  rebase,
  saveDraft,
  type Body,
  type DraftPositions,
  type DraftState,
} from "./draft";
import {
  connect,
  connectionValid,
  createNode,
  deleteNode,
  disconnect,
  referrersOf,
  setSetting,
  stageBody,
  type EditCtx,
  type EditResult,
  type FormValues,
} from "./edits";
import { HistoryDialog } from "./HistoryDialog";
import { Legend } from "./Legend";
import { emptyLayout, layoutReducer, pendingWrite } from "./layoutState";
import { buildView, kindLabel, neighbour, type FlowRFNode } from "./model";
import { NodePanel } from "./NodePanel";
import type { NodeEditApi } from "./NodeEditor";
import { OBJECTS, idOf } from "./objects";
import { ReviewDialog } from "./ReviewDialog";
import { FlowNodeView, StubNodeView } from "./nodes";
import { DRAG_TYPE, OffCanvasList } from "./OffCanvasList";
import { PortStylesContext, portStylesOf } from "./portStyles";

// The shared forms are big and only needed when one is opened.
const FormHost = lazy(() =>
  import("./FormHost").then((m) => ({ default: m.FormHost })),
);
const PromptHost = lazy(() =>
  import("./PromptHost").then((m) => ({ default: m.PromptHost })),
);

const nodeTypes: NodeTypes = { flow: FlowNodeView, stub: StubNodeView };

/** How often the canvas asks whether the configuration changed (spec S-9). */
export const REVISION_POLL_MS = 10_000;
const SAVE_DELAY_MS = 600;
const KEY_STEP = 20;
const ENTRY_KINDS = ["trunk", "anyTrunk", "inboundRoute", "featureCode"];

const CHECK_DELAY_MS = 500;

type SaveState = "idle" | "saving" | "saved" | "error";

/** What the last dry run of the draft said. */
type Check =
  | { state: "idle" }
  | { state: "checking" }
  | { state: "ok"; res: BatchResponse }
  | {
      state: "invalid";
      message: string;
      problems: ReturnType<typeof problemsOf>;
    }
  | { state: "stale"; message: string }
  | { state: "error"; message: string };

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
  // --- the draft (spec S-12): staged changes, kept in the browser ------------
  const auth = useAuth();
  const user = auth.state.status === "signedIn" ? auth.state.username : "";
  const storageKey = draftKey(canvas.id, user);
  const [started] = useState(() => {
    const loaded = canEdit ? loadDraft(storageKey) : null;
    if (!loaded) {
      return {
        draft: emptyDraft(initialGraph.configRevision),
        dropped: [] as string[],
        positions: {} as DraftPositions,
      };
    }
    const { positions, ...saved } = loaded;
    if (saved.baseRevision === initialGraph.configRevision) {
      return { draft: saved, dropped: [] as string[], positions };
    }
    const r = rebase(saved, initialGraph);
    return {
      draft: {
        ...saved,
        baseRevision: initialGraph.configRevision,
        ops: r.ops,
        past: [],
        future: [],
      },
      dropped: r.dropped.map((o) => o.summary || o.operationId),
      positions,
    };
  });
  const [layout, dispatch] = useReducer(layoutReducer, emptyLayout, () => {
    const loaded = layoutReducer(emptyLayout, {
      type: "load",
      version: canvas.layoutVersion,
      nodes: canvas.nodes,
    });
    // The new nodes of a draft kept in the browser keep their places.
    const mine = started.draft.ops
      .filter((o) => o.kind === "create")
      .flatMap((o) => {
        const p = started.positions[o.node];
        return p ? [{ node: o.node, x: p.x, y: p.y }] : [];
      });
    return mine.length > 0
      ? layoutReducer(loaded, { type: "place", nodes: mine })
      : loaded;
  });
  const [selected, setSelected] = useState<string | null>(null);
  const [save, setSave] = useState<{ state: SaveState; message?: string }>({
    state: "idle",
  });
  const [note, setNote] = useState("");
  const [tick, setTick] = useState(0);
  const saving = useRef(false);

  const [draft, dispatchDraft] = useReducer(draftReducer, started.draft);
  const [dropped, setDropped] = useState<string[]>(started.dropped);
  const [checked, setChecked] = useState<{
    ops: DraftState["ops"];
    result: Check;
  } | null>(null);
  const [dialog, setDialog] = useState<
    "create" | "review" | "history" | "discard" | null
  >(null);
  const [deleting, setDeleting] = useState<FlowNode | null>(null);
  const [formNode, setFormNode] = useState<FlowNode | null>(null);
  // A prompt being written: an announcement node, or null for a new one.
  const [promptFor, setPromptFor] = useState<FlowNode | null | undefined>(
    undefined,
  );
  const [keyPrompt, setKeyPrompt] = useState<{
    src: FlowNode;
    port: string;
    tgt: FlowNode;
    tgtPort: string;
  } | null>(null);
  useEffect(() => {
    if (!canEdit) return;
    const positions: DraftPositions = {};
    for (const o of draft.ops) {
      const p = o.kind === "create" ? layout.positions[o.node] : undefined;
      if (p) positions[o.node] = { x: p.x, y: p.y };
    }
    saveDraft(storageKey, draft, positions);
  }, [canEdit, storageKey, draft, layout.positions]);

  // A newer configuration: keep the staged changes nothing else touched.
  const draftRef = useRef(draft);
  useEffect(() => {
    draftRef.current = draft;
  }, [draft]);
  const acceptGraph = useCallback((g: FlowGraph) => {
    setGraph(g);
    const d = draftRef.current;
    if (d.baseRevision === g.configRevision) return;
    if (d.ops.length === 0) {
      dispatchDraft({ type: "clear", baseRevision: g.configRevision });
      return;
    }
    const r = rebase(d, g);
    dispatchDraft({
      type: "rebase",
      ops: r.ops,
      baseRevision: g.configRevision,
    });
    if (r.dropped.length > 0) {
      setDropped(r.dropped.map((o) => o.summary || o.operationId));
      dispatch({
        type: "forget",
        nodes: r.dropped.filter((o) => isPending(o.node)).map((o) => o.node),
      });
    }
  }, []);

  // --- the configuration changed elsewhere: reload the graph silently -----
  const revision = usePolling(getFlowRevision, REVISION_POLL_MS);
  const latest = revision.status === "ready" ? revision.data : null;
  useEffect(() => {
    if (latest === null || latest === graph.configRevision) return;
    const ctl = new AbortController();
    getFlowGraph(undefined, ctl.signal)
      .then((g) => {
        if (!ctl.signal.aborted) acceptGraph(g);
      })
      .catch(() => undefined); // the next poll tries again
    return () => ctl.abort();
  }, [latest, graph.configRevision, acceptGraph]);

  // The panel rereads the object whenever the draft changes.
  const [rev, setRev] = useState(0);
  const [seenOps, setSeenOps] = useState(draft.ops);
  if (seenOps !== draft.ops) {
    setSeenOps(draft.ops);
    setRev((r) => r + 1);
  }

  // The graph with the draft applied is what is drawn and edited.
  const shown = useMemo(() => project(graph, draft), [graph, draft]);

  // The stored objects the edits start from, read once per revision.
  const bases = useRef({ rev: -1, map: new Map<string, Promise<Body>>() });
  const load = useCallback(
    (node: FlowNode): Promise<Body> => {
      const cache = bases.current;
      if (cache.rev !== graph.configRevision) {
        cache.rev = graph.configRevision;
        cache.map.clear();
      }
      const def = OBJECTS[node.kind];
      const slot = node.kind === "featureCode" ? "featureCode:*" : node.id;
      let p = cache.map.get(slot);
      if (!p) {
        if (!def?.read) return Promise.reject(new Error("Cannot read it"));
        p = request<Body>("GET", def.read(idOf(node.id))).then((got) =>
          def.pick ? def.pick(got) : { ...got },
        );
        cache.map.set(slot, p);
        p.catch(() => cache.map.delete(slot));
      }
      return p;
    },
    [graph.configRevision],
  );
  const ctx: EditCtx = useMemo(
    () => ({ graph: shown, catalogue, draft, load }),
    [shown, catalogue, draft, load],
  );

  // --- checking the draft: a dry run, shortly after the last change ------------
  const check: Check = useMemo(
    () =>
      !canEdit || draft.ops.length === 0
        ? { state: "idle" }
        : checked?.ops === draft.ops
          ? checked.result
          : { state: "checking" },
    [canEdit, draft.ops, checked],
  );
  useEffect(() => {
    if (!canEdit || draft.ops.length === 0) return;
    const ctl = new AbortController();
    const ops = draft.ops;
    const setCheck = (result: Check) => setChecked({ ops, result });
    const timer = setTimeout(() => {
      applyConfigBatch(
        compile(draft, { dryRun: true, canvasId: canvas.id }),
        ctl.signal,
      )
        .then((res) => setCheck({ state: "ok", res }))
        .catch((err: unknown) => {
          if (ctl.signal.aborted) return;
          if (isStale(err)) {
            setCheck({ state: "stale", message: errorMessage(err) });
            void getFlowGraph().then(acceptGraph, () => undefined);
          } else if (err instanceof ApiError && err.status === 422) {
            setCheck({
              state: "invalid",
              message: errorMessage(err),
              problems: problemsOf(err),
            });
          } else {
            setCheck({ state: "error", message: errorMessage(err) });
          }
        });
    }, CHECK_DELAY_MS);
    return () => {
      clearTimeout(timer);
      ctl.abort();
    };
  }, [canEdit, canvas.id, draft, acceptGraph]);

  // Problems of the check, on the node they belong to.
  const nodeErrors = useMemo(() => {
    const out: Record<string, string[]> = {};
    if (check.state !== "invalid") return out;
    const order = compiledOrder(draft);
    for (const p of check.problems) {
      const id = p.node || order[p.opIndex]?.node;
      if (id) out[id] = [...(out[id] ?? []), p.message];
    }
    return out;
  }, [check, draft]);
  const generalProblems =
    check.state === "invalid"
      ? check.problems.filter((p) => {
          const order = compiledOrder(draft);
          return !(p.node || order[p.opIndex]?.node);
        })
      : [];

  // --- the drawn nodes ------------------------------------------------------
  const view = useMemo(
    () =>
      buildView({
        graph: shown,
        catalogue,
        positions: layout.positions,
        selected,
        edit: canEdit,
        marks: draftMarks(draft),
        errors: nodeErrors,
      }),
    [shown, catalogue, layout.positions, selected, canEdit, draft, nodeErrors],
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

  // --- editing: every gesture stages operations in the draft -------------------
  const gestures = useRef(0);
  const run = useCallback(
    async (p: Promise<EditResult>): Promise<string | null> => {
      const r = await p;
      if (!r.ok) {
        setNote(r.error);
        return r.error;
      }
      const group = `g${Date.now().toString(36)}-${++gestures.current}`;
      dispatchDraft({
        type: "stage",
        ops: r.ops.map((o) => ({ ...o, groups: [group] })),
      });
      setNote("");
      return null;
    },
    [],
  );
  const nodeById = useCallback(
    (id: string) => shown.nodes.find((n) => n.id === id),
    [shown.nodes],
  );

  const connectGesture = useCallback(
    (c: Connection) => {
      const sp = c.sourceHandle?.startsWith("out:")
        ? c.sourceHandle.slice(4)
        : "";
      const tp = c.targetHandle?.startsWith("in:")
        ? c.targetHandle.slice(3)
        : "";
      const src = nodeById(c.source);
      const tgt = nodeById(c.target);
      if (!sp || !tp || !src || !tgt) return;
      const perKey = catalogue.nodes
        .find((n) => n.kind === src.kind)
        ?.ports.find((p) => p.id === sp)?.perKey;
      if (perKey) {
        setKeyPrompt({ src, port: sp, tgt, tgtPort: tp });
        return;
      }
      void run(connect(ctx, src, sp, tgt, tp));
    },
    [catalogue.nodes, ctx, nodeById, run],
  );

  const isValidConnection = useCallback(
    (c: {
      source: string;
      target: string;
      sourceHandle?: string | null;
      targetHandle?: string | null;
    }) => connectionValid(shown, catalogue, c),
    [shown, catalogue],
  );

  const editApi: NodeEditApi | undefined = canEdit
    ? {
        ctx,
        rev,
        errors: selected ? (nodeErrors[selected] ?? []) : [],
        loadView: async (node) => {
          const base = isPending(node.id) ? {} : await load(node);
          const op = draft.ops.find((o) =>
            isPending(node.id)
              ? o.node === node.id
              : o.kind === "update" && o.node === node.id,
          );
          return { ...base, ...op?.body };
        },
        onSetting: (node, key, value, label) =>
          run(setSetting(ctx, node, key, value, label)),
        onConnect: (node, port, tgt, tgtPort) =>
          run(connect(ctx, node, port, tgt, tgtPort)),
        onDisconnect: (edge: FlowEdge) => run(disconnect(ctx, edge)),
        onDelete: setDeleting,
        onOpenForm: setFormNode,
        onPrompt: (node) => setPromptFor(node),
      }
    : undefined;

  const createFromDialog = async (
    kind: string,
    values: FormValues,
  ): Promise<string | null> => {
    const r = await createNode(ctx, kind, values);
    if (!r.ok) return r.error;
    const group = `g${Date.now().toString(36)}-${++gestures.current}`;
    dispatchDraft({
      type: "stage",
      ops: r.ops.map((o) => ({ ...o, groups: [group] })),
    });
    if (r.node) {
      const rect = stageRef.current?.getBoundingClientRect();
      const centre = rf.screenToFlowPosition({
        x: (rect?.left ?? 0) + (rect?.width ?? 0) / 2,
        y: (rect?.top ?? 0) + (rect?.height ?? 0) / 2,
      });
      place(r.node, centre.x - 124, centre.y - 60);
    }
    return null;
  };

  const pendingIds = (d: DraftState) =>
    d.ops.filter((o) => o.kind === "create").map((o) => o.node);

  const reload = async () => {
    const [g, c] = await Promise.all([
      getFlowGraph(),
      getFlowCanvas(canvas.id),
    ]);
    acceptGraph(g);
    dispatch({ type: "synced", version: c.layoutVersion, nodes: c.nodes });
  };

  const applyDraft = async () => {
    const positions: Record<string, { x: number; y: number }> = {};
    for (const [id, p] of Object.entries(layout.positions)) {
      if (isPending(id)) positions[id] = { x: p.x, y: p.y };
    }
    const count = draft.ops.length;
    try {
      const res = await applyConfigBatch(
        compile(draft, { dryRun: false, canvasId: canvas.id, positions }),
      );
      dispatch({ type: "forget", nodes: pendingIds(draft) });
      dispatchDraft({ type: "clear", baseRevision: res.revision });
      setDialog(null);
      setNote(`Applied ${count} ${count === 1 ? "change" : "changes"}.`);
      await reload().catch(() => undefined);
    } catch (err) {
      if (isStale(err)) {
        // Someone changed what the draft touches: nothing was applied. The
        // reload rebases the draft and reports what had to be dropped.
        const n = conflictsOf(err).length;
        setDialog(null);
        setNote(
          `Nothing was applied: the configuration changed${n > 0 ? ` under ${n} of your changes` : ""}. Review what is left.`,
        );
        await reload().catch(() => undefined);
        return;
      }
      const detail = problemsOf(err)
        .map((p) => p.message)
        .join(" ");
      throw new Error(
        detail ? `${errorMessage(err)} ${detail}` : errorMessage(err),
        { cause: err },
      );
    }
  };

  const discardDraft = () => {
    dispatch({ type: "forget", nodes: pendingIds(draft) });
    dispatchDraft({ type: "clear", baseRevision: graph.configRevision });
    setSelected(null);
    setDialog(null);
  };

  // Ctrl/Cmd+Z and Shift+Ctrl/Cmd+Z, unless the user is typing.
  useEffect(() => {
    if (!canEdit) return;
    const onKey = (e: globalThis.KeyboardEvent) => {
      if (!(e.metaKey || e.ctrlKey) || e.key.toLowerCase() !== "z") return;
      const el = e.target as HTMLElement | null;
      if (el && /^(INPUT|TEXTAREA|SELECT)$/.test(el.tagName)) return;
      if (document.querySelector('[aria-modal="true"]')) return;
      e.preventDefault();
      dispatchDraft({ type: e.shiftKey ? "redo" : "undo" });
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [canEdit]);

  const checkText =
    draft.ops.length === 0
      ? "No staged changes."
      : `${draft.ops.length} staged ${draft.ops.length === 1 ? "change" : "changes"}. ` +
        (check.state === "checking"
          ? "Checking…"
          : check.state === "ok"
            ? check.res.warnings.length > 0
              ? `Accepted, with ${check.res.warnings.length} ${check.res.warnings.length === 1 ? "warning" : "warnings"}.`
              : "Checked: accepted."
            : check.state === "invalid"
              ? "Not accepted: see the marked objects."
              : check.state === "stale"
                ? "The configuration changed. Reloading…"
                : check.state === "error"
                  ? `Could not check: ${check.message}`
                  : "");

  // --- import ---------------------------------------------------------------
  const entries = useMemo(
    () =>
      shown.nodes
        .filter((n) => ENTRY_KINDS.includes(n.kind) && !isPending(n.id))
        .sort((a, b) =>
          `${a.kind} ${a.label}`.localeCompare(
            `${b.kind} ${b.label}`,
            undefined,
            { numeric: true },
          ),
        ),
    [shown.nodes],
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
          ? shown
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
    ? shown.nodes.find((n) => n.id === selected)
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
      {canEdit && (
        <div className="flow-draftbar" role="group" aria-label="Draft">
          <Button
            size="sm"
            variant="secondary"
            icon="plus"
            onClick={() => setDialog("create")}
          >
            Add object
          </Button>
          <Button
            size="sm"
            variant="secondary"
            icon="wand-sparkles"
            onClick={() => setPromptFor(null)}
          >
            New prompt from text
          </Button>
          <Button
            size="sm"
            variant="ghost"
            icon="undo-2"
            disabled={draft.past.length === 0}
            onClick={() => dispatchDraft({ type: "undo" })}
          >
            Undo
          </Button>
          <Button
            size="sm"
            variant="ghost"
            icon="redo-2"
            disabled={draft.future.length === 0}
            onClick={() => dispatchDraft({ type: "redo" })}
          >
            Redo
          </Button>
          <span
            className="flow-draftbar__status"
            role="status"
            aria-live="polite"
          >
            {checkText}
          </span>
          <Button
            size="sm"
            variant="ghost"
            icon="history"
            onClick={() => setDialog("history")}
          >
            History
          </Button>
          <Button
            size="sm"
            variant="secondary"
            icon="x"
            disabled={draft.ops.length === 0}
            onClick={() => setDialog("discard")}
          >
            Discard draft
          </Button>
          <Button
            size="sm"
            icon="check"
            disabled={check.state !== "ok"}
            onClick={() => setDialog("review")}
          >
            Review and apply
          </Button>
        </div>
      )}
      {dropped.length > 0 && (
        <Alert tone="warn" title="Some staged changes were dropped">
          Someone changed these objects since you staged your changes, so they
          were not kept: {dropped.join("; ")}.{" "}
          <button
            type="button"
            className="flow-linkbtn"
            onClick={() => setDropped([])}
          >
            Dismiss
          </button>
        </Alert>
      )}
      {generalProblems.length > 0 && (
        <Alert tone="bad" title="The draft is not accepted">
          {generalProblems.map((p) => p.message).join(" ")}
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
            className="flow-stage"
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
              nodesConnectable={canEdit}
              nodesDraggable={canEdit}
              edgesFocusable={canEdit}
              isValidConnection={isValidConnection}
              onConnect={connectGesture}
              deleteKeyCode={canEdit ? ["Backspace", "Delete"] : null}
              // Only connections are deleted with the key; objects have a
              // confirmation with the list of what points at them.
              onBeforeDelete={({ edges }) =>
                Promise.resolve({ nodes: [], edges })
              }
              onEdgesDelete={(edges) =>
                edges.forEach((e) => {
                  const edge = shown.edges.find((x) => x.id === e.id);
                  if (edge) void run(disconnect(ctx, edge));
                })
              }
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
        {selectedNode && (
          <NodePanel
            node={selectedNode}
            graph={shown}
            catalogue={catalogue}
            edit={editApi}
            onCanvas={(id) => layout.positions[id] !== undefined}
            onSelect={setSelected}
            onClose={closePanel}
            onRemove={
              canEdit && layout.positions[selectedNode.id]
                ? () => {
                    dispatch(
                      isPending(selectedNode.id)
                        ? { type: "forget", nodes: [selectedNode.id] }
                        : { type: "remove", node: selectedNode.id },
                    );
                    setSelected(null);
                  }
                : undefined
            }
          />
        )}
      </div>
      {dialog === "create" && (
        <CreateDialog
          ctx={ctx}
          onCreate={createFromDialog}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog === "history" && (
        <HistoryDialog
          canRevert={canEdit}
          onClose={() => setDialog(null)}
          onReverted={() => {
            setNote("Reverted.");
            void reload().catch(() => undefined);
          }}
        />
      )}
      {dialog === "discard" && (
        <ConfirmDialog
          title="Discard the draft?"
          description={`All ${draft.ops.length} staged changes are dropped. Nothing was applied, so nothing else changes.`}
          confirmLabel="Discard draft"
          errorTitle="Could not discard"
          onConfirm={() => {
            discardDraft();
            return Promise.resolve();
          }}
          onClose={() => setDialog(null)}
        />
      )}
      {dialog === "review" && check.state === "ok" && (
        <ReviewDialog
          title="Apply these changes?"
          description="They are applied together: all of them, or none."
          items={compiledOrder(draft).map((op, i) => ({
            title: op.summary || op.operationId,
            result: check.res.results[i],
          }))}
          warnings={check.res.warnings}
          confirmLabel={`Apply ${draft.ops.length} ${draft.ops.length === 1 ? "change" : "changes"}`}
          onConfirm={applyDraft}
          onClose={() => setDialog(null)}
        />
      )}
      {formNode && (
        <Suspense fallback={null}>
          <FormHost
            node={formNode}
            draftBody={
              draft.ops.find(
                (o) => o.kind === "update" && o.node === formNode.id,
              )?.body
            }
            onClose={() => setFormNode(null)}
            onCommit={async (body) => {
              const err = await run(stageBody(ctx, formNode, body));
              if (err) throw new Error(err);
            }}
          />
        </Suspense>
      )}
      {promptFor !== undefined && (
        <Suspense fallback={null}>
          <PromptHost
            node={promptFor}
            onClose={() => setPromptFor(undefined)}
            onDone={async (id, message) => {
              setPromptFor(undefined);
              setNote(message);
              await reload().catch(() => undefined);
              addFromList(`announcement:${String(id)}`);
            }}
          />
        </Suspense>
      )}
      {deleting && (
        <DeleteDialog
          node={deleting}
          ctx={ctx}
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            const err = await run(deleteNode(ctx, deleting));
            if (err) throw new Error(err);
            setDeleting(null);
            setSelected(null);
          }}
        />
      )}
      {keyPrompt && (
        <KeyDialog
          label={`${keyPrompt.src.label} to ${keyPrompt.tgt.label}`}
          onClose={() => setKeyPrompt(null)}
          onKey={async (key) => {
            const err = await run(
              connect(
                ctx,
                keyPrompt.src,
                `${keyPrompt.port}:${key}`,
                keyPrompt.tgt,
                keyPrompt.tgtPort,
              ),
            );
            if (err) throw new Error(err);
            setKeyPrompt(null);
          }}
        />
      )}
    </section>
  );
}

/** The confirmation before an object is deleted: what points at it first. */
function DeleteDialog({
  node,
  ctx,
  onConfirm,
  onClose,
}: {
  node: FlowNode;
  ctx: EditCtx;
  onConfirm: () => Promise<void>;
  onClose: () => void;
}) {
  const refs = referrersOf(ctx.graph, node);
  return (
    <ConfirmDialog
      title={`Delete ${node.label}?`}
      description="It is removed when you apply the draft, not before."
      confirmLabel="Delete"
      errorTitle="Cannot delete it"
      onConfirm={onConfirm}
      onClose={onClose}
    >
      {refs.length > 0 ? (
        <>
          <p>These point at it and are changed in the same step:</p>
          <ul aria-label="Used by">
            {refs.map((r) => (
              <li key={r.edge.id}>
                {r.owner.label}
                {r.blocked ? ` (cannot be changed here: ${r.blocked})` : ""}
              </li>
            ))}
          </ul>
        </>
      ) : (
        <p>Nothing points at it.</p>
      )}
    </ConfirmDialog>
  );
}

/** Asks for the key of a new IVR option. */
function KeyDialog({
  label,
  onKey,
  onClose,
}: {
  label: string;
  onKey: (key: string) => Promise<void>;
  onClose: () => void;
}) {
  const [key, setKey] = useState("");
  const [error, setError] = useState("");
  return (
    <Modal
      title="Which key?"
      description={label}
      onClose={onClose}
      actions={
        <>
          <Button variant="secondary" size="sm" onClick={onClose}>
            Cancel
          </Button>
          <Button
            size="sm"
            disabled={!key}
            onClick={() =>
              void onKey(key).catch((e: unknown) => setError(errorMessage(e)))
            }
          >
            Connect
          </Button>
        </>
      }
    >
      <Input
        label="Key"
        autoFocus
        maxLength={1}
        value={key}
        onChange={(e) => setKey(e.target.value)}
        hint="0-9, * or #"
      />
      {error && <Alert tone="bad">{error}</Alert>}
    </Modal>
  );
}
