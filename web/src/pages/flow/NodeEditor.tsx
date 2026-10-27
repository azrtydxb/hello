// The panel in draft mode (spec S-15, S-16): the object's settings, where
// each of its outputs goes (a picker that offers only what the catalogue
// allows, the keyboard way to wire the canvas), and delete. Every change is
// staged in the draft; nothing is saved here.

import { useEffect, useState } from "react";
import { errorMessage } from "../../api";
import type { FlowEdge, FlowNode } from "../../api/flow";
import {
  Alert,
  Button,
  Input,
  Select,
  Switch,
} from "../../design/azrty/components";
import type { Body } from "./draft";
import {
  connectChoices,
  terminalNode,
  typedTargets,
  type EditCtx,
} from "./edits";
import { kindLabel, nodeTypeOf } from "./model";
import {
  adapterFor,
  canDeleteKind,
  canEditKind,
  OBJECTS,
  portBase,
  portIsEditable,
  type Setting,
} from "./objects";

export interface NodeEditApi {
  ctx: EditCtx;
  /** Changes whenever the draft does, so the panel rereads the object. */
  rev: number;
  errors: string[];
  loadView: (node: FlowNode) => Promise<Body>;
  /** Each returns the reason it could not stage the change. */
  onSetting: (
    node: FlowNode,
    key: string,
    value: unknown,
    label: string,
  ) => Promise<string | null>;
  onConnect: (
    node: FlowNode,
    port: string,
    tgt: FlowNode,
    tgtPort: string,
  ) => Promise<string | null>;
  onDisconnect: (edge: FlowEdge) => Promise<string | null>;
  onDelete: (node: FlowNode) => void;
}

export function NodeEditor({
  node,
  edit,
}: {
  node: FlowNode;
  edit: NodeEditApi;
}) {
  const def = OBJECTS[node.kind];
  const type = nodeTypeOf(edit.ctx.catalogue, node.kind);
  const [view, setView] = useState<Body | null>(null);
  const [loadError, setLoadError] = useState("");
  const [error, setError] = useState("");

  const { loadView, rev } = edit;
  useEffect(() => {
    let live = true;
    if (!canEditKind(node.kind)) return;
    loadView(node)
      .then((b) => {
        if (!live) return;
        setView(b);
        setLoadError("");
      })
      .catch((e: unknown) => live && setLoadError(errorMessage(e)));
    return () => {
      live = false;
    };
  }, [node, loadView, rev]);

  const report = async (p: Promise<string | null>) => {
    setError((await p) ?? "");
  };

  const ports = (type?.ports ?? []).filter(
    (p) => p.direction === "out" && portIsEditable(node.kind, p.id),
  );
  const graph = edit.ctx.graph;
  const byId = new Map(graph.nodes.map((n) => [n.id, n]));

  return (
    <>
      {edit.errors.length > 0 && (
        <Alert tone="bad" title="This change is not accepted">
          {edit.errors.join(" ")}
        </Alert>
      )}
      {error && (
        <Alert tone="bad" title="Not staged">
          {error}
        </Alert>
      )}
      {loadError && (
        <Alert tone="bad" title="Could not read the object">
          {loadError}
        </Alert>
      )}
      {def?.settings && view && (
        <div className="flow-panel__section">
          <h3>Settings</h3>
          {def.settings.map((s) => (
            <SettingField
              key={`${node.id}.${s.key}.${rev}`}
              setting={s}
              value={view[s.key]}
              onCommit={(v) => report(edit.onSetting(node, s.key, v, s.label))}
            />
          ))}
        </div>
      )}
      {ports.length > 0 && (
        <div className="flow-panel__section">
          <h3>Where it goes</h3>
          {ports.map((p) => (
            <PortEditor
              key={p.id}
              node={node}
              portId={p.id}
              label={p.label}
              perKey={Boolean(p.perKey)}
              edit={edit}
              edges={graph.edges.filter(
                (e) =>
                  e.source.node === node.id &&
                  portBase(e.source.port) === p.id &&
                  !e.implicit,
              )}
              byId={byId}
              report={report}
            />
          ))}
        </div>
      )}
      {canDeleteKind(node.kind) && (
        <div className="flow-panel__section">
          <Button
            variant="danger"
            size="sm"
            icon="trash-2"
            onClick={() => edit.onDelete(node)}
          >
            Delete {kindLabel(edit.ctx.catalogue, node.kind).toLowerCase()}
          </Button>
        </div>
      )}
    </>
  );
}

function SettingField({
  setting,
  value,
  onCommit,
}: {
  setting: Setting;
  value: unknown;
  onCommit: (v: unknown) => void;
}) {
  const [text, setText] = useState(
    value === undefined || value === null ? "" : String(value),
  );
  if (setting.type === "bool") {
    return (
      <div className="flow-edit-row">
        <Switch
          label={setting.label}
          checked={value === true}
          onChange={(e) => onCommit(e.target.checked)}
        />
      </div>
    );
  }
  if (setting.type === "select") {
    return (
      <div className="flow-edit-row">
        <Select
          label={setting.label}
          size="sm"
          value={text}
          options={setting.options ?? []}
          onChange={(e) => {
            setText(e.target.value);
            onCommit(e.target.value);
          }}
        />
      </div>
    );
  }
  const commit = () => {
    const raw = text.trim();
    if (setting.type === "number") {
      const n = Number(raw);
      if (raw === "" || !Number.isFinite(n) || n === value) return;
      onCommit(n);
    } else if (raw !== (typeof value === "string" ? value : "")) {
      onCommit(raw);
    }
  };
  return (
    <div className="flow-edit-row">
      <Input
        label={setting.label}
        size="sm"
        type={setting.type === "number" ? "number" : "text"}
        value={text}
        onChange={(e) => setText(e.target.value)}
        onBlur={commit}
        onKeyDown={(e) => {
          if (e.key === "Enter") {
            e.preventDefault();
            commit();
          }
        }}
      />
    </div>
  );
}

function PortEditor({
  node,
  portId,
  label,
  perKey,
  edit,
  edges,
  byId,
  report,
}: {
  node: FlowNode;
  portId: string;
  label: string;
  perKey: boolean;
  edit: NodeEditApi;
  edges: FlowEdge[];
  byId: Map<string, FlowNode>;
  report: (p: Promise<string | null>) => Promise<void>;
}) {
  const [choice, setChoice] = useState("");
  const [key, setKey] = useState("");
  const [typed, setTyped] = useState("");
  const choices = connectChoices(edit.ctx, node.kind, portId, node.id);
  const terminals = typedTargets(edit.ctx.catalogue, node.kind, portId);
  const keyed = perKey;

  const wire = (tgt: FlowNode, tgtPort: string) => {
    const port = keyed ? `${portId}:${key.trim()}` : portId;
    return report(edit.onConnect(node, port, tgt, tgtPort));
  };
  const connectChosen = () => {
    const [id, tgtPort] = choice.split("|");
    const tgt = id ? byId.get(id) : undefined;
    if (!tgt || !tgtPort) return;
    void wire(tgt, tgtPort).then(() => setChoice(""));
  };
  const connectTyped = (kind: "external" | "sipUri") => {
    if (!typed.trim()) return;
    void wire(terminalNode(kind, typed), "call").then(() => setTyped(""));
  };

  return (
    <fieldset className="flow-edit-row" aria-label={label}>
      <legend>{label}</legend>
      {edges.length === 0 && <span>Nothing connected.</span>}
      {edges.map((e) => {
        const tgt = byId.get(e.target.node);
        const can = adapterFor(
          node.kind,
          e.source.port,
          tgt?.kind ?? "",
          e.target.port,
        );
        return (
          <div key={e.id} className="flow-item">
            <span>
              {keyed && e.source.port.includes(":")
                ? `Key ${e.source.port.slice(e.source.port.indexOf(":") + 1)}: `
                : ""}
              {tgt?.label ?? e.target.node}
            </span>
            {can && (
              <Button
                size="sm"
                variant="ghost"
                icon="unlink"
                aria-label={`Disconnect ${label} from ${tgt?.label ?? e.target.node}`}
                onClick={() => void report(edit.onDisconnect(e))}
              >
                Disconnect
              </Button>
            )}
          </div>
        );
      })}
      {keyed && (
        <Input
          label={`${label}: key`}
          size="sm"
          value={key}
          maxLength={1}
          onChange={(e) => setKey(e.target.value)}
          hint="0-9, * or #"
        />
      )}
      <Select
        label={`${label}: connect to`}
        size="sm"
        value={choice}
        onChange={(e) => setChoice(e.target.value)}
        options={[{ value: "", label: "Choose…" }, ...choices]}
      />
      <Button
        size="sm"
        variant="secondary"
        icon="link"
        disabled={!choice || (keyed && !key.trim())}
        onClick={connectChosen}
        aria-label={`Connect ${label}`}
      >
        Connect
      </Button>
      {terminals.map((t) => (
        <div key={t.kind} className="flow-edit-row">
          <Input
            label={`${label}: ${t.label}`}
            size="sm"
            value={typed}
            onChange={(e) => setTyped(e.target.value)}
          />
          <Button
            size="sm"
            variant="secondary"
            icon="phone"
            disabled={!typed.trim() || (keyed && !key.trim())}
            onClick={() => connectTyped(t.kind)}
          >
            Dial {t.label.toLowerCase()}
          </Button>
        </div>
      ))}
    </fieldset>
  );
}
