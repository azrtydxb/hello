// Dragging onto the canvas: an object from the "Not on this canvas" list, or
// a kind from the palette of new objects. Written for every browser:
//
// - Firefox starts an HTML5 drag only when dragstart puts data in the
//   DataTransfer, so the payload is always set (a custom type, and the same
//   payload as text/plain with a prefix);
// - Safari and older Firefox may hide custom types during dragover, and
//   Firefox's `types` used to be a DOMStringList without `includes`, so the
//   check reads the list as an array and also accepts the text/plain copy;
// - every browser cancels a drop unless dragover calls preventDefault and the
//   dropEffect is one the source allowed ("copyMove" allows both).

import type { KeyboardEvent } from "react";

export const NODE_DRAG_TYPE = "application/x-hello-flow-node";
export const KIND_DRAG_TYPE = "application/x-hello-flow-kind";
const NODE_PREFIX = "hello-flow-node:";
const KIND_PREFIX = "hello-flow-kind:";

/** What is being dragged. */
export type DragPayload =
  { type: "node"; id: string } | { type: "kind"; kind: string };

/** The part of a DataTransfer the helpers use (a real one, or a test's). */
export interface Transfer {
  types: ArrayLike<string> | Iterable<string>;
  setData(format: string, data: string): void;
  getData(format: string): string;
  effectAllowed: string;
  dropEffect: string;
}

const typesOf = (t: Transfer): string[] =>
  Array.from(t.types as ArrayLike<string>);

/** Fills the DataTransfer in dragstart. */
export function startDrag(t: Transfer, p: DragPayload): void {
  if (p.type === "node") {
    t.setData(NODE_DRAG_TYPE, p.id);
    t.setData("text/plain", NODE_PREFIX + p.id);
  } else {
    t.setData(KIND_DRAG_TYPE, p.kind);
    t.setData("text/plain", KIND_PREFIX + p.kind);
  }
  t.effectAllowed = "copyMove";
}

/**
 * In dragover: whether the canvas takes this drag. When it does, the
 * caller must preventDefault; the dropEffect is set here.
 */
export function acceptDrag(t: Transfer): boolean {
  const types = typesOf(t);
  if (types.includes(NODE_DRAG_TYPE)) {
    t.dropEffect = "move";
    return true;
  }
  if (types.includes(KIND_DRAG_TYPE) || types.includes("text/plain")) {
    t.dropEffect = "copy";
    return true;
  }
  return false;
}

/** In drop: what was dropped, or null when it is not ours. */
export function readDrop(t: Transfer): DragPayload | null {
  const id = t.getData(NODE_DRAG_TYPE);
  if (id) return { type: "node", id };
  const kind = t.getData(KIND_DRAG_TYPE);
  if (kind) return { type: "kind", kind };
  const text = t.getData("text/plain");
  if (text.startsWith(NODE_PREFIX) && text.length > NODE_PREFIX.length) {
    return { type: "node", id: text.slice(NODE_PREFIX.length) };
  }
  if (text.startsWith(KIND_PREFIX) && text.length > KIND_PREFIX.length) {
    return { type: "kind", kind: text.slice(KIND_PREFIX.length) };
  }
  return null;
}

/**
 * Enter and Space activate an element that acts as a button. The labels of
 * draggable items are not <button>s: Firefox never starts a drag from one.
 */
export function pressKeys(action: () => void) {
  return (e: KeyboardEvent<HTMLElement>) => {
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      action();
    }
  };
}
