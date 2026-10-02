import { screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { NAV_ITEMS } from "./nav";
import { json, ME, mockApi, renderApp } from "./test/api";

describe("App navigation", () => {
  it("renders every primary nav item and marks the active one", async () => {
    mockApi(ME);
    renderApp("/trunks");

    const nav = await screen.findByRole("navigation", { name: "Primary" });
    const links = within(nav).getAllByRole("link");
    expect(links.map((l) => l.textContent)).toEqual(
      NAV_ITEMS.map((i) => i.label),
    );
    expect(within(nav).getByRole("link", { name: "Trunks" })).toHaveAttribute(
      "aria-current",
      "page",
    );
    expect(
      within(nav).getByRole("link", { name: "Dashboard" }),
    ).not.toHaveAttribute("aria-current");
    expect(
      screen.getByRole("heading", { level: 1, name: "Trunks" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Arrives in Phase 2.")).toBeInTheDocument();
  });

  it("lists Registrations and gives every Phase 1 page real content", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/registrations": () => json({ items: [] }),
    });
    renderApp("/registrations");

    const nav = await screen.findByRole("navigation", { name: "Primary" });
    expect(
      within(nav).getByRole("link", { name: "Registrations" }),
    ).toHaveAttribute("href", "/registrations");
    expect(
      within(nav).getByRole("link", { name: "Active Calls" }),
    ).toHaveAttribute("href", "/calls");
    expect(
      within(nav).getByRole("link", { name: "Call History" }),
    ).toHaveAttribute("href", "/history");
    expect(
      await screen.findByText("No devices are registered."),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Arrives in Phase/)).not.toBeInTheDocument();
  });
});
