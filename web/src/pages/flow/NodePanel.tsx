// The right-hand panel in view mode (spec flow-canvas S-15, S-16): the
// object's settings, how it connects, its warnings, and a link to the page
// that edits it. Editing arrives with the draft (P4).

import { useEffect } from "react";
import type { FlowCatalogue, FlowGraph, FlowNode } from "../../api/flow";
import {
  Alert,
  Button,
  Drawer,
  LinkButton,
  PropertyList,
} from "../../design/azrty/components";
import { formPageFor, kindLabel, nodeTypeOf, summaryEntries } from "./model";

export interface NodePanelProps {
  node: FlowNode;
  graph: FlowGraph;
  catalogue: FlowCatalogue | undefined;
  /** Ids of the nodes drawn on this canvas. */
  onCanvas: (id: string) => boolean;
  onSelect: (id: string) => void;
  onClose: () => void;
  /** Takes the node off this canvas; absent for a viewer. */
  onRemove?: () => void;
}

export function NodePanel({
  node,
  graph,
  catalogue,
  onCanvas,
  onSelect,
  onClose,
  onRemove,
}: NodePanelProps) {
  // The inline drawer does not bind Escape (it is not a modal): the panel does.
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if (e.key !== "Escape" || e.defaultPrevented) return;
      // A modal dialog on top (a confirmation) answers Escape itself.
      if (document.querySelector('[aria-modal="true"]')) return;
      onClose();
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, [onClose]);

  const byId = new Map(graph.nodes.map((n) => [n.id, n]));
  const type = nodeTypeOf(catalogue, node.kind);
  const portLabel = (kind: string, id: string) =>
    nodeTypeOf(catalogue, kind)?.ports.find((p) => p.id === id)?.label ?? id;
  const outgoing = graph.edges.filter((e) => e.source.node === node.id);
  const incoming = graph.edges.filter((e) => e.target.node === node.id);
  const warnings = graph.warnings.filter((w) => w.node === node.id);
  const settings = summaryEntries(node.summary);
  const page = formPageFor(node);

  const other = (id: string) => {
    const n = byId.get(id);
    if (!n) return <span>{id}</span>;
    return (
      <>
        <button
          type="button"
          className="flow-linkbtn"
          onClick={() => onSelect(id)}
        >
          {n.label}
        </button>
        <span className="flow-sr-only">, {kindLabel(catalogue, n.kind)}</span>
        {!onCanvas(id) && " (not on this canvas)"}
      </>
    );
  };

  return (
    <aside className="flow-panel" aria-label="Selected node">
      <Drawer
        inline
        open
        title={node.label}
        description={type?.label ?? node.kind}
        onClose={onClose}
        width="100%"
        footer={
          <>
            {onRemove && (
              <Button variant="ghost" icon="eye-off" onClick={onRemove}>
                Remove from canvas
              </Button>
            )}
            {page && (
              <LinkButton to={page} icon="external-link">
                Open {type?.label ?? "object"} page
              </LinkButton>
            )}
          </>
        }
      >
        <Alert tone="info">
          View only. Change this object on its own page.
        </Alert>
        {warnings.length > 0 && (
          <div className="flow-panel__section">
            <h3>Warnings</h3>
            {warnings.map((w, i) => (
              <Alert key={i} tone="warn" title={w.code.replace(/_/g, " ")}>
                {w.message}
              </Alert>
            ))}
          </div>
        )}
        <div className="flow-panel__section">
          <h3>Settings</h3>
          {settings.length > 0 ? (
            <PropertyList
              items={settings.map((s) => ({ label: s.label, value: s.value }))}
            />
          ) : (
            <p>No settings to show.</p>
          )}
        </div>
        <div className="flow-panel__section">
          <h3>Calls and resources it uses</h3>
          {outgoing.length === 0 ? (
            <p>Nothing leaves this node.</p>
          ) : (
            <ul className="flow-panel__links">
              {outgoing.map((e) => (
                <li key={e.id}>
                  {portLabel(node.kind, e.source.port)} to{" "}
                  {other(e.target.node)}
                </li>
              ))}
            </ul>
          )}
        </div>
        <div className="flow-panel__section">
          <h3>Reached from</h3>
          {incoming.length === 0 ? (
            <p>Nothing points at this node.</p>
          ) : (
            <ul className="flow-panel__links">
              {incoming.map((e) => (
                <li key={e.id}>
                  {other(e.source.node)} via{" "}
                  {portLabel(
                    byId.get(e.source.node)?.kind ?? "",
                    e.source.port,
                  )}
                </li>
              ))}
            </ul>
          )}
        </div>
      </Drawer>
    </aside>
  );
}
