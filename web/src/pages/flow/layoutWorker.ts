// Runs the dagre layout of a large import off the main thread.
import { runDagre, type LayoutInput } from "./autoLayout";

const ctx = self as unknown as {
  onmessage: ((e: MessageEvent<LayoutInput>) => void) | null;
  postMessage(message: unknown): void;
};

ctx.onmessage = (e) => {
  ctx.postMessage(runDagre(e.data));
};
