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

  it("filters by tab, shows the counts, and exports the same filter", async () => {
    const failed = {
      ...cdr(9, "1004"),
      finalStatus: 404,
      direction: "outbound",
      originalDestination: "7777",
      rewrittenDestination: "",
      route: "",
      trunk: "",
    };
    const calls = mockApi({
      ...ME,
      "GET /api/v1/cdrs/counts": () => json({ all: 12, failed: 3 }),
      "GET /api/v1/cdrs?limit=50": () =>
        json({
          items: [
            {
              ...cdr(10, "1001"),
              direction: "outbound",
              originalDestination: "00442071838750",
              rewrittenDestination: "+442071838750",
              route: "International",
              trunk: "carrier-primary",
            },
          ],
          next: "",
        }),
      "GET /api/v1/cdrs?limit=50&failed=true": () =>
        json({ items: [failed], next: "" }),
      "GET /api/v1/cdrs?limit=50&direction=internal": () =>
        json({ items: [], next: "" }),
    });
    renderApp("/history");

    expect(await screen.findByText("00442071838750")).toBeVisible();
    expect(screen.getByText("→ +442071838750")).toBeVisible();
    expect(screen.getByText("International")).toBeVisible();
    expect(
      await screen.findByRole("tab", { name: /^All\s*12$/ }),
    ).toHaveAttribute("aria-selected", "true");
    expect(screen.getByRole("link", { name: /Export CSV/ })).toHaveAttribute(
      "href",
      "/api/v1/cdrs/export",
    );

    fireEvent.click(await screen.findByRole("tab", { name: /^Failed\s*3$/ }));
    expect(await screen.findByText("7777")).toBeVisible();
    expect(screen.queryByText("00442071838750")).toBeNull();
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/history?tab=failed",
    );
    expect(screen.getByText("404")).toHaveClass("az-badge--bad");
    // No route or trunk is shown as unknown, not blank.
    expect(screen.getAllByText("—").length).toBeGreaterThanOrEqual(2);
    expect(screen.getByRole("link", { name: /Export CSV/ })).toHaveAttribute(
      "href",
      "/api/v1/cdrs/export?failed=true",
    );

    fireEvent.click(screen.getByRole("tab", { name: "Internal" }));
    expect(await screen.findByText("No calls recorded")).toBeVisible();
    expect(calls.map((c) => c.url)).toContain(
      "/api/v1/cdrs?limit=50&direction=internal",
    );
  });

  it("opens on the tab named in the URL, and a row opens the call", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/cdrs/counts": () => json({ all: 1, failed: 1 }),
      "GET /api/v1/cdrs?limit=50&failed=true": () =>
        json({ items: [{ ...cdr(5, "1005"), finalStatus: 480 }], next: "" }),
      "GET /api/v1/cdrs/5": () =>
        json({
          ...cdr(5, "1005"),
          finalStatus: 480,
          trace: [],
          explanation: "",
        }),
    });
    renderApp("/history?tab=failed");

    expect(await screen.findByText("1005")).toBeVisible();
    expect(
      await screen.findByRole("tab", { name: /^Failed\s*1$/ }),
    ).toHaveAttribute("aria-selected", "true");
    fireEvent.click(screen.getByText("1005"));
    expect(
      await screen.findByRole("heading", { level: 1, name: /^Call 5/ }),
    ).toBeVisible();
  });

  it("says so when the history cannot load", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/cdrs?limit=50": () =>
        json({ error: { code: "internal", message: "database down" } }, 500),
    });
    renderApp("/history");

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Could not load call history");
    expect(alert).toHaveTextContent("database down");
  });
});
