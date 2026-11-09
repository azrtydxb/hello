import { createContext, useContext } from "react";
import type { FlowCatalogue, FlowPortStyle } from "../../api/flow";

/** The catalogue's port types by name; the nodes read their style from it. */
export const PortStylesContext = createContext<
  Record<string, FlowPortStyle | undefined>
>({});

export function portStylesOf(
  catalogue: FlowCatalogue | undefined,
): Record<string, FlowPortStyle | undefined> {
  const out: Record<string, FlowPortStyle | undefined> = {};
  for (const t of catalogue?.portTypes ?? []) out[t.type] = t;
  return out;
}

export const usePortStyles = () => useContext(PortStylesContext);
