import { fireEvent, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { NAV_GROUPS, NAV_ITEMS } from "./nav";
import { json, ME, mockApi, renderApp } from "./test/api";

/** An administrator who also has an extension: the sidebar shows every group. */
const ME_WITH_EXTENSION = {
  "GET /api/v1/auth/me": () =>
    json({
      username: "admin",
      role: "admin",
      extensions: [{ id: 1, number: "101", name: "Desk" }],
    }),
};

/** The AI group shows while AI is enabled. */
const AI_ON = {
  "GET /api/v1/ai/status": () => json({ enabled: true, reason: null }),
};

describe("App navigation", () => {
  it("renders every primary nav item and marks the active one", async () => {
    mockApi({ ...ME_WITH_EXTENSION, ...AI_ON });
    renderApp("/diagnostics");

    const nav = await screen.findByRole("navigation", { name: "Primary" });
    await within(nav).findByRole("link", { name: "Findings", hidden: true });
    const links = within(nav).getAllByRole("link", { hidden: true });
    expect(links.map((l) => l.textContent)).toEqual(
      NAV_ITEMS.map((i) => i.label),
    );
    expect(
      within(nav).getByRole("link", { name: "Diagnostics" }),
    ).toHaveAttribute("aria-current", "page");
    expect(
      within(nav).getByRole("link", { name: "Ring groups", hidden: true }),
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
      await within(nav).findByRole("link", {
        name: /^Cluster\s*, 2 issues$/,
        hidden: true,
      }),
    ).toHaveAttribute("href", "/cluster");
    expect(
      within(nav).getByRole("link", {
        name: /^Trunks\s*, 1 issue$/,
        hidden: true,
      }),
    ).toHaveAttribute("href", "/trunks");
    // Trunks sits in the collapsed Call flow group, which flags the badge.
    const callFlow = within(nav).getByRole("button", { name: /^Call flow/ });
    expect(callFlow).toHaveAttribute("aria-expanded", "false");
    expect(
      within(callFlow).getByRole("img", { name: "needs attention" }),
    ).toBeInTheDocument();
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
      within(nav).getByRole("link", { name: "Active calls", hidden: true }),
    ).toHaveAttribute("href", "/calls");
    expect(
      within(nav).getByRole("link", { name: "Call history", hidden: true }),
    ).toHaveAttribute("href", "/history");
    expect(
      await screen.findByText("No devices are registered"),
    ).toBeInTheDocument();
    expect(screen.queryByText(/Arrives in Phase/)).not.toBeInTheDocument();
  });

  it("groups the nav as the design does and breadcrumbs the page", async () => {
    mockApi({
      ...ME_WITH_EXTENSION,
      ...AI_ON,
      "GET /api/v1/trunks": () => json({ items: [] }),
    });
    renderApp("/trunks");

    const nav = await screen.findByRole("navigation", { name: "Primary" });
    await within(nav).findByRole("link", { name: "Findings", hidden: true });
    for (const group of NAV_GROUPS.filter((g) => g.label)) {
      const toggle = within(nav).getByRole("button", { name: group.label });
      const section = document.getElementById(
        toggle.getAttribute("aria-controls")!,
      )!;
      expect(
        within(section)
          .getAllByRole("link", { hidden: true })
          .map((l) => l.textContent),
      ).toEqual(group.items.map((i) => i.label));
    }
    expect(NAV_GROUPS.map((g) => g.label)).toEqual([
      "",
      "My phone",
      "Directory",
      "Call flow",
      "Media",
      "Activity",
      "AI",
      "Integrations",
      "Platform",
    ]);
    const crumbs = screen.getByRole("navigation", { name: "Breadcrumb" });
    expect(crumbs).toHaveTextContent("Kuvryn Hello");
    expect(within(crumbs).getByText("Trunks")).toHaveAttribute(
      "aria-current",
      "page",
    );
  });

  it("shows My phone to an administrator only when they have an extension", async () => {
    mockApi({ ...ME, ...AI_ON });
    renderApp("/trunks");
    const nav = await screen.findByRole("navigation", { name: "Primary" });
    await within(nav).findByRole("link", { name: "Findings", hidden: true });
    expect(within(nav).queryByRole("group", { name: "My phone" })).toBeNull();
    expect(
      within(nav).queryByRole("link", { name: "Address book" }),
    ).toBeNull();
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

describe("Collapsible nav groups", () => {
  const key = "hello.nav.expanded.admin";

  async function navFor(path: string) {
    mockApi({ ...ME, ...AI_ON });
    renderApp(path);
    return screen.findByRole("navigation", { name: "Primary" });
  }

  it("expands only the active group by default", async () => {
    const nav = await navFor("/diagnostics");
    const platform = await within(nav).findByRole("button", {
      name: "Platform",
    });
    expect(platform).toHaveAttribute("aria-expanded", "true");
    expect(
      within(nav).getByRole("button", { name: "Directory" }),
    ).toHaveAttribute("aria-expanded", "false");
    expect(
      within(nav).queryByRole("link", { name: "Extensions" }),
    ).not.toBeInTheDocument();
    expect(
      within(nav).getByRole("link", { name: "Diagnostics" }),
    ).toBeInTheDocument();
  });

  it("toggles a group and wires aria-controls", async () => {
    const nav = await navFor("/diagnostics");
    const dir = await within(nav).findByRole("button", { name: "Directory" });
    const panel = document.getElementById(dir.getAttribute("aria-controls")!);
    expect(panel).toHaveAttribute("hidden");
    fireEvent.click(dir);
    expect(dir).toHaveAttribute("aria-expanded", "true");
    expect(panel).not.toHaveAttribute("hidden");
    expect(
      within(nav).getByRole("link", { name: "Extensions" }),
    ).toBeInTheDocument();
    fireEvent.click(dir);
    expect(dir).toHaveAttribute("aria-expanded", "false");
  });

  it("auto-expands the group of the route navigated to", async () => {
    const nav = await navFor("/diagnostics");
    const media = await within(nav).findByRole("button", { name: "Media" });
    expect(media).toHaveAttribute("aria-expanded", "false");
    fireEvent.click(within(nav).getByRole("link", { name: "Diagnostics" }));
    // Navigate via a link in another group: open Activity, follow its link.
    fireEvent.click(within(nav).getByRole("button", { name: "Activity" }));
    fireEvent.click(within(nav).getByRole("link", { name: "Call history" }));
    expect(
      within(nav).getByRole("button", { name: "Activity" }),
    ).toHaveAttribute("aria-expanded", "true");
  });

  it("remembers the state per user", async () => {
    const nav = await navFor("/diagnostics");
    fireEvent.click(await within(nav).findByRole("button", { name: "Media" }));
    expect(JSON.parse(window.localStorage.getItem(key)!)).toEqual(
      expect.arrayContaining(["Platform", "Media"]),
    );
  });

  it("restores the remembered groups on load", async () => {
    window.localStorage.setItem(key, JSON.stringify(["Media"]));
    const nav = await navFor("/diagnostics");
    expect(
      await within(nav).findByRole("button", { name: "Media" }),
    ).toHaveAttribute("aria-expanded", "true");
    expect(
      within(nav).getByRole("button", { name: "Platform" }),
    ).toHaveAttribute("aria-expanded", "true");
  });

  it("degrades when storage is broken", async () => {
    window.localStorage.setItem(key, "{not json");
    const spy = vi
      .spyOn(Storage.prototype, "setItem")
      .mockImplementation(() => {
        throw new Error("quota");
      });
    const nav = await navFor("/diagnostics");
    const dir = await within(nav).findByRole("button", { name: "Directory" });
    fireEvent.click(dir);
    expect(dir).toHaveAttribute("aria-expanded", "true");
    spy.mockRestore();
  });
});
