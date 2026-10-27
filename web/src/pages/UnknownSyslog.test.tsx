import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../test/api";

describe("Unknown syslog", () => {
  it("lists the senders and shows one sender's lines", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/syslog/unknown": () =>
        json({ items: ["192.0.2.9", "198.51.100.4"] }),
      "GET /api/v1/syslog/unknown?ip=192.0.2.9&limit=200": () =>
        json({
          items: [
            {
              at: "2026-10-02T10:05:00Z",
              source: "192.0.2.9",
              severity: 4,
              message: "who am i",
            },
          ],
          cap: 1000,
          retentionSeconds: 86400,
        }),
    });
    renderApp("/phones/syslog");
    const list = await screen.findByRole("list", { name: "Unknown senders" });
    expect(within(list).getByText("198.51.100.4")).toBeInTheDocument();
    fireEvent.click(
      within(list).getAllByRole("button", { name: "Show lines" })[0],
    );
    const table = await screen.findByRole("table", {
      name: "Syslog from 192.0.2.9",
    });
    expect(within(table).getByText("who am i")).toBeInTheDocument();
    expect(within(table).getByText("192.0.2.9")).toBeInTheDocument();
    expect(
      calls.some(
        (c) => c.url === "/api/v1/syslog/unknown?ip=192.0.2.9&limit=200",
      ),
    ).toBe(true);
  });

  it("says when there is no unknown sender", async () => {
    mockApi({ ...ME, "GET /api/v1/syslog/unknown": () => json({ items: [] }) });
    renderApp("/phones/syslog");
    expect(await screen.findByText(/No unknown sender/)).toBeInTheDocument();
  });
});
