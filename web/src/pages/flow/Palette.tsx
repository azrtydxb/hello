// The palette of new objects: every kind the canvas can create (derived from
// objects.ts through creatableKinds, never a list of its own). Dragging a
// kind onto the canvas opens "Add an object" for that kind and puts the new
// node where it was dropped; clicking it, or Enter, opens the same form.

import type { FlowCatalogue } from "../../api/flow";
import { creatableKinds } from "./CreateDialog";
import { pressKeys, startDrag } from "./dnd";
import { kindLabel } from "./model";

/** The "New object" section of the side column. */
export function Palette({
  catalogue,
  onPick,
}: {
  catalogue: FlowCatalogue;
  onPick: (kind: string) => void;
}) {
  const kinds = creatableKinds(catalogue);
  if (kinds.length === 0) return null;
  return (
    <section
      className="flow-side flow-palette"
      aria-labelledby="flow-new-title"
    >
      <h2 className="flow-side__title" id="flow-new-title">
        New object
      </h2>
      <p className="flow-side__hint">Drag onto the canvas, or click.</p>
      <ul className="flow-palette__items">
        {kinds.map((kind) => {
          const label = kindLabel(catalogue, kind);
          return (
            <li
              key={kind}
              className="flow-item flow-palette__item"
              draggable
              data-kind={kind}
              onDragStart={(e) =>
                startDrag(e.dataTransfer, { type: "kind", kind })
              }
            >
              <span
                role="button"
                tabIndex={0}
                className="flow-palette__label"
                aria-label={`New ${label}`}
                onClick={() => onPick(kind)}
                onKeyDown={pressKeys(() => onPick(kind))}
              >
                <span className="flow-palette__grip" aria-hidden="true">
                  ⋮⋮
                </span>
                {label}
              </span>
            </li>
          );
        })}
      </ul>
    </section>
  );
}
