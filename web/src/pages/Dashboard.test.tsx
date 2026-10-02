import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { Dashboard } from "./Dashboard";

function mockFetch(response: Response) {
  const fn = vi.fn(() => Promise.resolve(response));
  vi.stubGlobal("fetch", fn);
  return fn;
}

describe("Dashboard", () => {
  it("renders the version and commit returned by /api/v1/version", async () => {
    const fetchMock = mockFetch(
      new Response(
        JSON.stringify({
          version: "9.8.7-test",
          commit: "abc1234",
          configRevision: 42,
        }),
        { status: 200, headers: { "Content-Type": "application/json" } },
      ),
    );

    render(<Dashboard />);

    expect(screen.getByRole("status")).toHaveTextContent(/loading/i);
    expect(await screen.findByText("9.8.7-test")).toBeInTheDocument();
    expect(screen.getByText("abc1234")).toBeInTheDocument();
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/v1/version",
      expect.anything(),
    );
  });

  it("shows an error when the request fails", async () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => Promise.reject(new TypeError("Failed to fetch"))),
    );

    render(<Dashboard />);

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent(/could not reach the control plane/i);
    expect(alert).toHaveTextContent("Failed to fetch");
    expect(screen.queryByText("Version")).not.toBeInTheDocument();
  });

  it("shows an error when the server responds with a non-2xx status", async () => {
    mockFetch(new Response("boom", { status: 503 }));

    render(<Dashboard />);

    expect(await screen.findByRole("alert")).toHaveTextContent("HTTP 503");
  });
});
