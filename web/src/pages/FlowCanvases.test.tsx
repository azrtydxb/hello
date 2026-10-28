import { CATALOGUE, GRAPH } from "./flow/fixtures";
import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, mockApi, noContent, renderApp } from "../test/api";

const NOW = "2026-10-25T10:00:00Z";
const canvas = (id: number, name: string, nodeCount = 0, description = "") => ({
  id,
  name,
  description,
  layoutVersion: 0,
  nodeCount,
  createdBy: "admin",
  createdAt: NOW,
  updatedAt: NOW,
});
const LIST = {
  items: [canvas(1, "Main office", 12, "HQ"), canvas(2, "After hours")],
};
const me = (role: string) => ({
  "GET /api/v1/auth/me": () => json({ username: "u", role }),
});

describe("Call flows page", () => {
  it("is in the Call flow section of the navigation", async () => {
    mockApi({ ...me("viewer"), "GET /api/v1/flow/canvases": () => json(LIST) });
    renderApp("/flow");
    await screen.findByRole("heading", { name: "Call flows", level: 1 });
    const nav = screen.getByRole("navigation", { name: "Primary" });
    expect(
      within(nav).getByRole("link", { name: "Call flows" }),
    ).toHaveAttribute("href", "/flow");
  });

  it("lists the canvases with links to them, read-only for a viewer", async () => {
    mockApi({ ...me("viewer"), "GET /api/v1/flow/canvases": () => json(LIST) });
    renderApp("/flow");
    const link = await screen.findByRole("link", { name: "Main office" });
    expect(link).toHaveAttribute("href", "/flow/1");
    expect(screen.getByText(/12 nodes placed/)).toBeInTheDocument();
    expect(screen.getByText(/0 nodes placed/)).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "New canvas" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^Edit canvas/ }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^Delete canvas/ }),
    ).not.toBeInTheDocument();
  });

  it("invites an operator to make the first canvas", async () => {
    const calls = mockApi({
      ...me("operator"),
      "GET /api/v1/flow/canvases": [
        () => json({ items: [] }),
        () => json({ items: [canvas(3, "Sites")] }),
      ] as never,
      "POST /api/v1/flow/canvases": () =>
        json({ ...canvas(3, "Sites"), nodes: [] }, 201),
      "GET /api/v1/flow/canvases/3": () =>
        json({ ...canvas(3, "Sites"), nodes: [] }),
      "GET /api/v1/flow/catalogue": () => json(CATALOGUE),
      "GET /api/v1/flow/graph": () => json(GRAPH),
      "GET /api/v1/flow/revision": () => json({ configRevision: 12 }),
    });
    renderApp("/flow");
    await screen.findByText("No canvases yet");
    fireEvent.click(screen.getAllByRole("button", { name: "New canvas" })[0]!);
    const form = await screen.findByRole("form", { name: "New canvas" });
    fireEvent.change(within(form).getByLabelText("Name"), {
      target: { value: " " },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create canvas" }));
    expect(
      await screen.findByText("Fix the highlighted fields."),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(within(form).getByLabelText("Name"), {
      target: { value: "Sites" },
    });
    fireEvent.change(within(form).getByLabelText("Description"), {
      target: { value: "All sites" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create canvas" }));
    // The new canvas opens, ready to be filled.
    await screen.findByRole("heading", { name: "Sites" });
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      name: "Sites",
      description: "All sites",
    });
  });

  it("shows the server's refusal of a duplicate name in the form", async () => {
    mockApi({
      ...me("operator"),
      "GET /api/v1/flow/canvases": () => json(LIST),
      "POST /api/v1/flow/canvases": () =>
        apiError(409, "conflict", "flow canvas: already exists"),
    });
    renderApp("/flow");
    await screen.findByRole("link", { name: "Main office" });
    fireEvent.click(screen.getByRole("button", { name: "New canvas" }));
    const form = await screen.findByRole("form", { name: "New canvas" });
    fireEvent.change(within(form).getByLabelText("Name"), {
      target: { value: "Main office" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create canvas" }));
    expect(
      await screen.findByText("flow canvas: already exists"),
    ).toBeInTheDocument();
  });

  it("renames a canvas and deletes one after a confirmation", async () => {
    const calls = mockApi({
      ...me("operator"),
      "GET /api/v1/flow/canvases": () => json(LIST),
      "PATCH /api/v1/flow/canvases/2": () => json(canvas(2, "Night")),
      "DELETE /api/v1/flow/canvases/1": () => noContent(),
    });
    renderApp("/flow");
    await screen.findByRole("link", { name: "After hours" });
    fireEvent.click(
      screen.getByRole("button", { name: "Edit canvas After hours" }),
    );
    const form = await screen.findByRole("form", {
      name: "Edit canvas After hours",
    });
    fireEvent.change(within(form).getByLabelText("Name"), {
      target: { value: "Night" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save canvas" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PATCH")).toBe(true),
    );
    expect(calls.find((c) => c.method === "PATCH")?.body).toEqual({
      name: "Night",
      description: "",
    });

    fireEvent.click(
      await screen.findByRole("button", { name: "Delete canvas Main office" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText(/configuration it shows is not changed/),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === "DELETE")).toBe(false);
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Delete canvas" }),
    );
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE")).toBe(true),
    );
  });

  it("shows a load error", async () => {
    mockApi({
      ...me("viewer"),
      "GET /api/v1/flow/canvases": () =>
        apiError(503, "unavailable", "database down"),
    });
    renderApp("/flow");
    expect(await screen.findByText("database down")).toBeInTheDocument();
  });
});
