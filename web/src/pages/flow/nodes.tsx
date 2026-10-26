// The two node renderers of the canvas. One component draws every kind: its
// ports, their colours and shapes come from the catalogue (spec S-1, S-2).

import { Handle, Position, type NodeProps, type Node } from "@xyflow/react";
import { Badge } from "../../design/azrty/components";
import {
  KNOWN_SHAPES,
  type FlowNodeData,
  type PortView,
  type StubData,
} from "./model";
import { usePortStyles } from "./portStyles";

/** A port's connector dot: colour and shape both say what type it is. */
function PortHandle({ port }: { port: PortView }) {
  const styles = usePortStyles();
  const style = styles[port.type];
  const shape = style && KNOWN_SHAPES.has(style.shape) ? style.shape : "circle";
  return (
    <Handle
      id={`${port.direction}:${port.id}`}
      type={port.direction === "in" ? "target" : "source"}
      position={port.direction === "in" ? Position.Left : Position.Right}
      isConnectable={false}
      className={`flow-handle flow-handle--${shape}`}
      style={{
        color: `var(--flow-port-${port.type}, ${style?.colour ?? "currentColor"})`,
      }}
      title={`${port.label} (${style?.label ?? port.type})`}
    />
  );
}

function PortRow({ port }: { port: PortView }) {
  return (
    <div className={`flow-port flow-port--${port.direction}`}>
      <PortHandle port={port} />
      <span className="flow-port__label">{port.label}</span>
    </div>
  );
}

export function FlowNodeView({
  data,
  selected,
}: NodeProps<Node<FlowNodeData, "flow">>) {
  const { node, typeLabel, inputs, outputs, warnings, summary, pseudo } = data;
  const rows = Math.max(inputs.length, outputs.length, 1);
  return (
    <div
      className={[
        "flow-node",
        `flow-node--${node.kind}`,
        selected ? "flow-node--selected" : "",
        warnings.length > 0 ? "flow-node--warn" : "",
        pseudo ? "flow-node--pseudo" : "",
      ]
        .filter(Boolean)
        .join(" ")}
    >
      <div className="flow-node__head">
        <span className="flow-node__kind">{typeLabel}</span>
        <span className="flow-node__title" title={node.label}>
          {node.label}
        </span>
        {summary && <span className="flow-node__summary">{summary}</span>}
        {warnings.length > 0 && (
          <span
            className="flow-node__warn"
            title={warnings.map((w) => w.message).join("\n")}
          >
            <Badge tone="warn" icon="triangle-alert">
              <span className="flow-sr-only">Warnings: </span>
              {warnings.length}
            </Badge>
          </span>
        )}
      </div>
      <div className="flow-node__ports" style={{ minHeight: rows * 26 }}>
        <div className="flow-node__col">
          {inputs.map((p) => (
            <PortRow key={`in:${p.id}`} port={p} />
          ))}
        </div>
        <div className="flow-node__col flow-node__col--out">
          {outputs.map((p) => (
            <PortRow key={`out:${p.id}`} port={p} />
          ))}
        </div>
      </div>
    </div>
  );
}

/** A grey dashed stand-in for a node that is not on this canvas. */
export function StubNodeView({ data }: NodeProps<Node<StubData, "stub">>) {
  return (
    <div className="flow-stub" title={`Not on this canvas: ${data.label}`}>
      {data.dir === "out" ? (
        <Handle
          id="stub"
          type="target"
          position={Position.Left}
          isConnectable={false}
          className="flow-handle flow-handle--stub"
        />
      ) : (
        <Handle
          id="stub"
          type="source"
          position={Position.Right}
          isConnectable={false}
          className="flow-handle flow-handle--stub"
        />
      )}
      <span className="flow-stub__kind">{data.typeLabel}</span>
      <span className="flow-stub__label">{data.label}</span>
      {data.warnings > 0 && <span className="flow-stub__warn">!</span>}
    </div>
  );
}
