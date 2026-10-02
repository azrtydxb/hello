import { fireEvent, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../test/api";

function cdr(id: number, source: string) {
  return {
    id,
    correlationId: `corr-${id}`,
    sipCallId: `call-${id}`,
    source,
    destination: "102",
    startTime: "2026-10-01T10:00:00Z",
    endTime: "2026-10-01T10:01:05Z",
    durationMs: 65000,
    billableMs: 60000,
    sipNode: "hello-sip-1",
    mediaMode: "direct",
    finalStatus: 200,
    terminationSide: "caller",
    failureReason: "",
  };
}

describe("Call History", () => {
  it("pages to older calls with next, and back to newer", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/cdrs?limit=50": () =>
        json({ items: [cdr(42, "first-page")], next: "42" }),
      "GET /api/v1/cdrs?before=42&limit=50": () =>
        json({ items: [cdr(41, "second-page")], next: "" }),
    });
    renderApp("/history");

    expect(await screen.findByText("first-page")).toBeVisible();
    expect(screen.getByText("1:05")).toBeVisible();
    expect(screen.getByRole("button", { name: "Newer" })).toBeDisabled();

    fireEvent.click(screen.getByRole("button", { name: "Older" }));
    expect(await screen.findByText("second-page")).toBeVisible();
    expect(screen.queryByText("first-page")).toBeNull();
    // An empty next means there is nothing older.
    expect(screen.getByRole("button", { name: "Older" })).toBeDisabled();
    expect(calls.map((c) => c.url)).toContain(
      "/api/v1/cdrs?before=42&limit=50",
    );

    fireEvent.click(screen.getByRole("button", { name: "Newer" }));
    expect(await screen.findByText("first-page")).toBeVisible();
  });
});
