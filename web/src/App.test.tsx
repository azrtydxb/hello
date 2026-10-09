import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { NAV_GROUPS, NAV_ITEMS } from "./nav";
import { json, ME, mockApi, renderApp } from "./test/api";

/** The AI group shows while AI is enabled. */
const AI_ON = {
  "GET /api/v1/ai/status": () => json({ enabled: true, reason: null }),
};

describe("App navigation", () => {
  it("renders every primary nav item and marks the active one", async () => {
    mockApi({ ...ME, ...AI_ON });
    renderApp("/diagnostics");

    const nav = await screen.findByRole("navigation", { name: "Primary" });
    await within(nav).findByRole("link", { name: "Findings" });
    const links = within(nav).getAllByRole("link");
    expect(links.map((l) => l.textContent)).toEqual(
      NAV_ITEMS.map((i) => i.label),
    );
    expect(
      within(nav).getByRole("link", { name: "Diagnostics" }),
    ).toHaveAttribute("aria-current", "page");
    expect(
      within(nav).getByRole("link", { name: "Ring groups" }),
    ).not.toHaveAttribute("aria-current");
    expect(
      screen.getByRole("heading", { level: 1, name: "Diagnostics" }),
    ).toBeInTheDocument();
  });

  it("badges Cluster and Trunks while they are degraded", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/cluster": () =>
        json({
          members: [
            { id: "hello-sip-1", kind: "sip", state: "READY" },
            { id: "hello-sip-2", kind: "sip", state: "UNHEALTHY" },
          ],
          postgres: { up: true },
          valkey: { up: false, mode: "single", error: "down" },
          configRevision: 1,
        }),
      "GET /api/v1/trunks/status": () =>
        json({
          items: [
            {
              trunkId: 1,
              name: "carrier",
              activeCalls: 0,
              destinations: [
                { destination: "a:5060", up: false, checkedAt: "" },
                { destination: "b:5060", up: true, checkedAt: "" },
              ],
            },
          ],
        }),
    });
    renderApp("/");
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    expect(
      await within(nav).findByRole("link", { name: /^Cluster\s*, 2 issues$/ }),
    ).toHaveAttribute("href", "/cluster");
    expect(
      within(nav).getByRole("link", { name: /^Trunks\s*, 1 issue$/ }),
    ).toHaveAttribute("href", "/trunks");
    expect(within(nav).getByRole("link", { name: "Routes" })).toBeVisible();
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
      within(nav).getByRole("link", { name: "Active calls" }),
    ).toHaveAttribute("href", "/calls");
    expect(
      within(nav).getByRole("link", { name: "Call history" }),
    ).toHaveAttribute("href", "/history");
    expect(
      await screen.findByText("No devices are registered"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Arrives in Phase/)).not.toBeInTheDocument();
  });

  it("groups the nav as the design does and breadcrumbs the page", async () => {
    mockApi({
      ...ME,
      ...AI_ON,
      "GET /api/v1/trunks": () => json({ items: [] }),
    });
    renderApp("/trunks");

    const nav = await screen.findByRole("navigation", { name: "Primary" });
    await within(nav).findByRole("link", { name: "Findings" });
    for (const group of NAV_GROUPS.filter((g) => g.label)) {
      const section = within(nav).getByRole("group", { name: group.label });
      expect(
        within(section)
          .getAllByRole("link")
          .map((l) => l.textContent),
      ).toEqual(group.items.map((i) => i.label));
    }
    expect(NAV_GROUPS.map((g) => g.label)).toEqual([
      "",
      "Directory",
      "Call flow",
      "Media",
      "Activity",
      "AI",
      "Platform",
    ]);
    const crumbs = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(crumbs).toHaveTextContent("Kuvryn Hello");
    expect(within(crumbs).getByText("Trunks")).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("marks Route tester, not Routes, active on /routes/test", async () => {
    mockApi(ME);
    renderApp("/routes/test");

    const nav = await screen.findByRole("navigation", { name: "Primary" });
    expect(
      within(nav).getByRole("link", { name: "Route tester" }),
    ).toHaveAttribute("aria-current", "page");
    expect(
      within(nav).getByRole("link", { name: "Routes" }),
    ).not.toHaveAttribute("aria-current");
    expect(screen.getByRole("link", { name: "Test a number" })).toHaveAttribute(
      "href",
      "/routes/test",
    );
  });

  it("titles a call detail and keeps Call history active", async () => {
    mockApi(ME);
    renderApp("/history/42");

    const crumbs = await screen.findByRole("navigation", {
      name: "Breadcrumb",
    });
    expect(within(crumbs).getByText("Call detail")).toBeInTheDocument();
    const nav = screen.getByRole("navigation", { name: "Primary" });
    expect(within(nav).getByRole("link", { name: "Call history" })).toHaveClass(
      "az-nav__item--active",
    );
  });

  it("shows the host and config revision when the control plane answers", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/version": () =>
        json({ version: "v1.2.3", commit: "abc", configRevision: 42 }),
    });
    renderApp("/diagnostics");

    expect(
      await screen.findByText(`${window.location.host} · rev 42`),
    ).toBeInTheDocument();
    expect(screen.getByText("LIVE")).toBeInTheDocument();
  });

  it("says when the control plane is unreachable and drops LIVE", async () => {
    mockApi(ME);
    renderApp("/diagnostics");

    expect(
      await screen.findByText("Control plane unreachable"),
    ).toBeInTheDocument();
    expect(screen.queryByText("LIVE")).toBeNull();
  });

  it("switches the theme from the top bar", async () => {
    mockApi(ME);
    renderApp("/diagnostics");

    await screen.findByRole("navigation", { name: "Primary" });
    const theme = within(screen.getByRole("banner")).getByRole("radiogroup", {
      name: "Theme",
    });
    fireEvent.click(within(theme).getByRole("radio", { name: "Light" }));
    expect(document.documentElement).toHaveAttribute("data-theme", "light");
    fireEvent.click(within(theme).getByRole("radio", { name: "Dark" }));
    expect(document.documentElement).toHaveAttribute("data-theme", "dark");
  });
});
