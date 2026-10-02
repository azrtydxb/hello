import { render, screen, within } from "@testing-library/react";
import { MemoryRouter } from "react-router";
import { describe, expect, it, vi } from "vitest";
import { App } from "./App";
import { NAV_ITEMS } from "./nav";

describe("App navigation", () => {
  it("renders every primary nav item and marks the active one", () => {
    vi.stubGlobal(
      "fetch",
      vi.fn(() => new Promise(() => {})),
    );
    render(
      <MemoryRouter initialEntries={["/trunks"]}>
        <App />
      </MemoryRouter>,
    );

    const nav = screen.getByRole("navigation", { name: "Primary" });
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
});
