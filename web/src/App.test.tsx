import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { NAV_GROUPS, NAV_ITEMS } from "./nav";
import { json, ME, mockApi, renderApp } from "./test/api";

describe("App navigation", () => {
  it("renders every primary nav item and marks the active one", async () => {
    mockApi(ME);
    renderApp("/diagnostics");

    const nav = await screen.findByRole("navigation", { name: "Primary" });
    const links = within(nav).getAllByRole("link");
    expect(links.map((l) => l.textContent)).toEqual(
      NAV_ITEMS.map((i) => i.label),
    );
    expect(
      within(nav).getByRole("link", { name: "Diagnostics" }),
    ).toHaveAttribute("aria-current", "page");
    expect(
      within(nav).getByRole("link", { name: "Ring Groups" }),
    ).not.toHaveAttribute("aria-current");
    expect(
      screen.getByRole("heading", { level: 1, name: "Diagnostics" }),
    ).toBeInTheDocument();
    expect(screen.getByText("Arrives in Phase 3.")).toBeInTheDocument();
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

  it("groups the nav and puts the group and page in the breadcrumb", async () => {
    mockApi({ ...ME, "GET /api/v1/trunks": () => json({ items: [] }) });
    renderApp("/trunks");

    const nav = await screen.findByRole("navigation", { name: "Primary" });
    for (const group of NAV_GROUPS) {
      const section = within(nav).getByRole("group", { name: group.label });
      expect(
        within(section)
          .getAllByRole("link")
          .map((l) => l.textContent),
      ).toEqual(group.items.map((i) => i.label));
    }
    const crumbs = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(crumbs).toHaveTextContent("Routing");
    expect(within(crumbs).getByText("Trunks")).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("shows control-plane reachability and the version in the shell", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/version": () =>
        json({ version: "v1.2.3", commit: "abc", configRevision: 1 }),
    });
    renderApp("/diagnostics");

    expect(
      await screen.findByText("Connected to control plane"),
    ).toBeInTheDocument();
    expect(screen.getByText("LIVE")).toBeInTheDocument();
    expect(screen.getByText("v1.2.3")).toBeInTheDocument();
  });

  it("says when the control plane is unreachable and drops LIVE", async () => {
    mockApi(ME);
    renderApp("/diagnostics");

    expect(
      await screen.findByText("Control plane unreachable"),
    ).toBeInTheDocument();
    expect(screen.queryByText("LIVE")).toBeNull();
  });

  it("switches the theme from the sidebar", async () => {
    mockApi(ME);
    renderApp("/diagnostics");

    const theme = await screen.findByRole("radiogroup", { name: "Theme" });
    fireEvent.click(within(theme).getByRole("radio", { name: "Light" }));
    expect(document.documentElement).toHaveAttribute("data-theme", "light");
    fireEvent.click(within(theme).getByRole("radio", { name: "Dark" }));
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
  });
});
