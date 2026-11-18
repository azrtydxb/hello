import { describe, expect, it } from "vitest";
import {
  acceptDrag,
  KIND_DRAG_TYPE,
  NODE_DRAG_TYPE,
  readDrop,
  startDrag,
  type Transfer,
} from "./dnd";

/** A DataTransfer as a browser exposes it; `hide` drops types from `types`. */
function fakeTransfer(hide: string[] = []): Transfer & {
  data: Map<string, string>;
} {
  const data = new Map<string, string>();
  return {
    data,
    get types() {
      return [...data.keys()].filter((t) => !hide.includes(t));
    },
    setData(format, value) {
      data.set(format, value);
    },
    getData(format) {
      return data.get(format) ?? "";
    },
    effectAllowed: "uninitialized",
    dropEffect: "none",
  };
}

describe("drag and drop onto the canvas", () => {
  it("puts the payload in a custom type and as text, allowing copy and move", () => {
    const t = fakeTransfer();
    startDrag(t, { type: "node", id: "ivr:5" });
    // Firefox starts no drag without data: there is always some.
    expect(t.data.get(NODE_DRAG_TYPE)).toBe("ivr:5");
    expect(t.data.get("text/plain")).toBe("hello-flow-node:ivr:5");
    expect(t.effectAllowed).toBe("copyMove");

    const k = fakeTransfer();
    startDrag(k, { type: "kind", kind: "ringGroup" });
    expect(k.data.get(KIND_DRAG_TYPE)).toBe("ringGroup");
    expect(k.data.get("text/plain")).toBe("hello-flow-kind:ringGroup");
  });

  it("accepts our drags in dragover with an allowed drop effect", () => {
    const t = fakeTransfer();
    startDrag(t, { type: "node", id: "ivr:5" });
    expect(acceptDrag(t)).toBe(true);
    expect(t.dropEffect).toBe("move");

    const k = fakeTransfer();
    startDrag(k, { type: "kind", kind: "ringGroup" });
    expect(acceptDrag(k)).toBe(true);
    expect(k.dropEffect).toBe("copy");
  });

  it("accepts a drag whose custom types are hidden (Safari) or are a DOMStringList (older Firefox)", () => {
    const hidden = fakeTransfer([NODE_DRAG_TYPE, KIND_DRAG_TYPE]);
    startDrag(hidden, { type: "node", id: "ivr:5" });
    expect(acceptDrag(hidden)).toBe(true);

    // A DOMStringList has length and indexes but no includes().
    const list = { 0: NODE_DRAG_TYPE, length: 1, item: () => NODE_DRAG_TYPE };
    const legacy: Transfer = {
      types: list,
      setData: () => undefined,
      getData: () => "",
      effectAllowed: "copyMove",
      dropEffect: "none",
    };
    expect(acceptDrag(legacy)).toBe(true);
  });

  it("refuses files and other drags", () => {
    const files: Transfer = {
      types: ["Files"],
      setData: () => undefined,
      getData: () => "",
      effectAllowed: "all",
      dropEffect: "none",
    };
    expect(acceptDrag(files)).toBe(false);
    expect(readDrop(files)).toBeNull();
  });

  it("reads the drop from the custom type, else from the text copy", () => {
    const t = fakeTransfer();
    startDrag(t, { type: "kind", kind: "ivr" });
    expect(readDrop(t)).toEqual({ type: "kind", kind: "ivr" });

    const onlyText = fakeTransfer();
    onlyText.setData("text/plain", "hello-flow-node:extension:1");
    expect(readDrop(onlyText)).toEqual({ type: "node", id: "extension:1" });

    const foreign = fakeTransfer();
    foreign.setData("text/plain", "some text from another page");
    expect(readDrop(foreign)).toBeNull();
    const empty = fakeTransfer();
    empty.setData("text/plain", "hello-flow-kind:");
    expect(readDrop(empty)).toBeNull();
  });
});
