import type { FlowCatalogue } from "../../api/flow";
import { KNOWN_SHAPES } from "./model";

/** The port types with their colour and shape, so the canvas can be read. */
export function Legend({
  catalogue,
}: {
  catalogue: FlowCatalogue | undefined;
}) {
  if (!catalogue) return null;
  return (
    <ul className="flow-legend" aria-label="Port types">
      {catalogue.portTypes.map((t) => (
        <li key={t.type}>
          <span
            aria-hidden="true"
            className={`flow-handle react-flow__handle flow-handle--${
              KNOWN_SHAPES.has(t.shape) ? t.shape : "circle"
            }`}
            style={{ color: `var(--flow-port-${t.type}, ${t.colour})` }}
          />
          {t.label}
        </li>
      ))}
    </ul>
  );
}
