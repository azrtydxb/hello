// The "Not on this canvas" list (spec flow-canvas S-8): objects that work and
// are configured but have no place on this canvas, by type and searchable.
// They are added by dragging onto the canvas or with the Add button, which is
// the keyboard route.

import { useState } from "react";
import type { FlowCatalogue, FlowNode } from "../../api/flow";
import { Button, Input } from "../../design/azrty/components";
import { groupByKind } from "./model";

export const DRAG_TYPE = "application/x-hello-flow-node";

export interface OffCanvasListProps {
  nodes: readonly FlowNode[];
  catalogue: FlowCatalogue | undefined;
  /** Absent for a viewer: the list is then only a list. */
  onAdd?: (id: string) => void;
  onSelect: (id: string) => void;
}

export function OffCanvasList({
  nodes,
  catalogue,
  onAdd,
  onSelect,
}: OffCanvasListProps) {
  const [query, setQuery] = useState("");
  const groups = groupByKind(nodes, catalogue, query);
  return (
    <section className="flow-side" aria-labelledby="flow-off-title">
      <h2 className="flow-side__title" id="flow-off-title">
        Not on this canvas
      </h2>
      <Input
        label="Search"
        type="search"
        size="sm"
        icon="search"
        value={query}
        onChange={(e) => setQuery(e.target.value)}
        placeholder="Name or type"
      />
      <div className="flow-side__list">
        {nodes.length === 0 && <p>Everything is on this canvas.</p>}
        {nodes.length > 0 && groups.length === 0 && <p>Nothing matches.</p>}
        {groups.map((g) => (
          <details key={g.kind} className="flow-group" open={query !== ""}>
            <summary>
              {g.label} ({g.nodes.length})
            </summary>
            <ul className="flow-group__items">
              {g.nodes.map((n) => (
                <li
                  key={n.id}
                  className="flow-item"
                  draggable={Boolean(onAdd)}
                  onDragStart={(e) => {
                    e.dataTransfer.setData(DRAG_TYPE, n.id);
                    e.dataTransfer.effectAllowed = "move";
                  }}
                >
                  <button
                    type="button"
                    className="flow-linkbtn flow-item__label"
                    title={n.label}
                    onClick={() => onSelect(n.id)}
                  >
                    {n.label}
                  </button>
                  {onAdd && (
                    <Button
                      size="sm"
                      variant="ghost"
                      icon="plus"
                      aria-label={`Add ${n.label} to the canvas`}
                      onClick={() => onAdd(n.id)}
                    />
                  )}
                </li>
              ))}
            </ul>
          </details>
        ))}
      </div>
    </section>
  );
}
