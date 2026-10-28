import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, mockApi, renderApp, type Routes } from "../test/api";

const STAMP = "2026-10-01T10:00:00Z";

const optOut = (over: Record<string, unknown> = {}) => ({
  id: 4,
  addressId: 5,
  address: "+3270123456",
  channel: "Main",
  peer: "+32475123456",
  state: "out",
  source: "keyword",
  note: "",
  at: STAMP,
  ...over,
});

function api(more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "a", role: "admin" }),
    "GET /api/v1/messaging/optouts": () => json({ items: [optOut()] }),
    "GET /api/v1/messaging/addresses": () =>
      json({ items: [{ id: 5, address: "+3270123456", label: "Support" }] }),
    ...more,
  });
}

describe("Messaging opt-outs", () => {
  it("lists who opted out and how", async () => {
    api();
    renderApp("/messaging/optouts");
    const table = await screen.findByRole("table", { name: "Opt-outs" });
    expect(within(table).getByText("+32475123456")).toBeInTheDocument();
    expect(within(table).getByText("Texted a stop word")).toBeInTheDocument();
    expect(within(table).getByText("Opted out")).toBeInTheDocument();
  });

  it("searches by the end of the number and can include those who opted back in", async () => {
    const calls = api({
      "GET /api/v1/messaging/optouts?q=3456&all=true": () =>
        json({ items: [] }),
    });
    renderApp("/messaging/optouts");
    await screen.findByRole("table", { name: "Opt-outs" });
    fireEvent.change(screen.getByLabelText("Search by the end of the number"), {
      target: { value: "3456" },
    });
    fireEvent.click(
      screen.getByLabelText("Include customers who opted back in"),
    );
    fireEvent.click(screen.getByRole("button", { name: "Search" }));
    expect(await screen.findByText("Nobody has opted out")).toBeInTheDocument();
    expect(
      calls.some((c) => c.url === "/api/v1/messaging/optouts?q=3456&all=true"),
    ).toBe(true);
  });

  it("will not record consent without saying how", async () => {
    const calls = api({
      "POST /api/v1/messaging/optouts/4/reconsent": () =>
        json(optOut({ state: "in" })),
    });
    renderApp("/messaging/optouts");
    fireEvent.click(
      await screen.findByRole("button", {
        name: /Record consent for \+32475123456/,
      }),
    );
    const submit = await screen.findByRole("button", {
      name: "Record consent",
    });
    expect(submit).toBeDisabled();
    fireEvent.change(screen.getByLabelText("How the customer consented"), {
      target: { value: "verbal, 26 October" },
    });
    expect(submit).toBeEnabled();
    fireEvent.click(submit);
    await waitFor(() =>
      expect(calls.find((c) => c.url.endsWith("/reconsent"))?.body).toEqual({
        note: "verbal, 26 October",
      }),
    );
  });

  it("records an opt-out by hand", async () => {
    const calls = api({
      "POST /api/v1/messaging/optouts": () =>
        json(optOut({ id: 9, source: "admin" }), 201),
    });
    renderApp("/messaging/optouts");
    const record = await screen.findByRole("button", {
      name: "Record an opt-out",
    });
    await waitFor(() => expect(record).toBeEnabled());
    fireEvent.click(record);
    fireEvent.change(await screen.findByLabelText("Customer's number"), {
      target: { value: "+32475000001" },
    });
    fireEvent.change(screen.getByLabelText("Note"), {
      target: { value: "asked by phone" },
    });
    fireEvent.click(
      screen.getAllByRole("button", { name: "Record opt-out" }).at(-1)!,
    );
    await waitFor(() =>
      expect(calls.find((c) => c.method === "POST")?.body).toEqual({
        addressId: 5,
        peer: "+32475000001",
        note: "asked by phone",
      }),
    );
  });
});
