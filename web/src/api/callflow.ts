/**
 * API client additions for the call-flow pages (trunks, routes, route
 * tester, ring groups). The shared client lives in ../api.ts; this file only
 * adds what those pages need beyond it.
 */
import {
  testRoute,
  type RouteDecision,
  type RouteTestRequest,
  type TraceStep,
} from "../api";

/** The route tester's decision, with the emergency flag of the matched route. */
export interface CallflowDecision extends RouteDecision {
  /** True when the matched outbound route is an emergency route. */
  emergency?: boolean;
}

/** POST /api/v1/routing/test response, typed with `emergency`. */
export interface CallflowTestResult {
  decision: CallflowDecision;
  trace: TraceStep[];
}

/** POST /api/v1/routing/test: decide a call without placing it. */
export const testCall = (input: RouteTestRequest) =>
  testRoute(input) as Promise<CallflowTestResult>;
