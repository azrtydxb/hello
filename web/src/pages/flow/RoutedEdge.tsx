// The canvas's connection: an orthogonal line that leaves its source port to
// the right, enters its target port from the left and runs around the node
// boxes (edgeRouter.ts). The boxes come from the page through a context, so
// every edge sees the same, current positions while a node is dragged.

import { BaseEdge, type EdgeProps } from "@xyflow/react";
import { createContext, useContext, useMemo } from "react";
import { midpointOf, pathOf, RouteCache, type Obstacle } from "./edgeRouter";

/** The boxes to route around, and the routes already found. */
export interface RouterState {
  obstacles: readonly Obstacle[];
  cache: RouteCache;
}

export const RouterContext = createContext<RouterState>({
  obstacles: [],
  cache: new RouteCache(),
});

/** The edge data the router reads: which lane of its node a port is. */
export interface RoutedEdgeData extends Record<string, unknown> {
  sourceLane?: number;
  targetLane?: number;
}

/** Draws one connection along its route, with its label halfway. */
export function RoutedEdge({
  id,
  source,
  target,
  sourceX,
  sourceY,
  targetX,
  targetY,
  data,
  label,
  labelStyle,
  labelShowBg,
  labelBgStyle,
  labelBgPadding,
  labelBgBorderRadius,
  style,
  markerEnd,
  markerStart,
  interactionWidth,
}: EdgeProps) {
  const { obstacles, cache } = useContext(RouterContext);
  const lanes = data as RoutedEdgeData | undefined;
  const sourceLane = lanes?.sourceLane;
  const targetLane = lanes?.targetLane;
  const points = useMemo(
    () =>
      cache.route(
        id,
        {
          source: { x: sourceX, y: sourceY },
          target: { x: targetX, y: targetY },
          sourceNode: source,
          targetNode: target,
          sourceLane,
          targetLane,
        },
        obstacles,
      ),
    [
      cache,
      id,
      source,
      target,
      sourceX,
      sourceY,
      targetX,
      targetY,
      sourceLane,
      targetLane,
      obstacles,
    ],
  );
  const path = useMemo(() => pathOf(points), [points]);
  const mid = useMemo(() => midpointOf(points), [points]);
  return (
    <BaseEdge
      id={id}
      path={path}
      label={label}
      labelX={mid.x}
      labelY={mid.y}
      labelStyle={labelStyle}
      labelShowBg={labelShowBg}
      labelBgStyle={labelBgStyle}
      labelBgPadding={labelBgPadding}
      labelBgBorderRadius={labelBgBorderRadius}
      style={style}
      markerEnd={markerEnd}
      markerStart={markerStart}
      interactionWidth={interactionWidth}
    />
  );
}
