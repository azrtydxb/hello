import { render } from "@testing-library/react";
import { MemoryRouter, useLocation } from "react-router";
import { vi } from "vitest";
import { App } from "../App";

/** One recorded fetch call. */
export interface Call {
  method: string;
  url: string;
  body: unknown;
}

type Reply = Response | (() => Response | Promise<Response>);
/** Handlers keyed by "METHOD /path?query"; a list is consumed in order, the last repeating. */
export type Routes = Record<string, Reply | Reply[]>;

/** A JSON response. */
export function json(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

/** An empty 204 response. */
export function noContent(): Response {
  return new Response(null, { status: 204 });
}

/** A response carrying the API error envelope. */
export function apiError(
  status: number,
  code: string,
  message: string,
): Response {
  return json({ error: { code, message } }, status);
}

/** Stub global fetch with a routing table; returns the recorded calls. */
export function mockApi(routes: Routes): Call[] {
  const calls: Call[] = [];
  const used = new Map<string, number>();
  vi.stubGlobal(
    "fetch",
    vi.fn((input: RequestInfo | URL, init?: RequestInit) => {
      const url = String(input);
      const method = (init?.method ?? "GET").toUpperCase();
      // A JSON body is parsed; anything else (e.g. multipart FormData) is
      // recorded as-is so tests can inspect its parts.
      const body =
        typeof init?.body === "string"
          ? (JSON.parse(init.body) as unknown)
          : init?.body;
      calls.push({ method, url, body });
      const key = `${method} ${url}`;
      const route = routes[key];
      if (route === undefined) {
        return Promise.resolve(
          apiError(404, "not_found", `no mock for ${key}`),
        );
      }
      const list = Array.isArray(route) ? route : [route];
      const n = used.get(key) ?? 0;
      used.set(key, n + 1);
      const reply = list[Math.min(n, list.length - 1)];
      if (!reply) throw new Error(`empty mock for ${key}`);
      return Promise.resolve(
        typeof reply === "function" ? reply() : reply.clone(),
      );
    }),
  );
  return calls;
}

export const ME = {
  "GET /api/v1/auth/me": () => json({ username: "admin", role: "admin" }),
};

function LocationProbe() {
  const location = useLocation();
  return (
    <output data-testid="location">
      {location.pathname + location.search}
    </output>
  );
}

/** Render the whole app at `path`, with a probe showing the router location. */
export function renderApp(path: string) {
  return render(
    <MemoryRouter initialEntries={[path]}>
      <App />
      <LocationProbe />
    </MemoryRouter>,
  );
}
