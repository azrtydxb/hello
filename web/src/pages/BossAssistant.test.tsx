import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, mockApi, renderApp, type Routes } from "../test/api";

const EXTENSIONS = [
  { id: 1, number: "200", name: "Boss", externalNumber: "" },
  { id: 2, number: "300", name: "Assistant", externalNumber: "" },
  { id: 3, number: "210", name: "Other", externalNumber: "" },
];

const LINK = {
  id: 7,
  bossExtensionId: 1,
  bossNumber: "200",
  bossName: "Boss",
  assistantExtensionId: 2,
  assistantNumber: "300",
  assistantName: "Assistant",
  ringMode: "both",
  screening: false,
  enabled: true,
  divertOn: false,
  outboundAsBoss: false,
};

function api(role: string, links: unknown[], more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "pat", role }),
    "GET /api/v1/boss-links": () => json({ items: links }),
    "GET /api/v1/extensions": () => json({ items: EXTENSIONS }),
    ...more,
  });
}

describe("Boss and assistant", () => {
  it("shows the stored links", async () => {
    api("operator", [LINK]);
    renderApp("/boss");
    const row = await screen.findByRole("group", { name: "Link 1" });
    expect(within(row).getByLabelText("Boss")).toHaveValue("1");
    expect(within(row).getByLabelText("Assistant")).toHaveValue("2");
    expect(within(row).getByLabelText("Ring mode")).toHaveValue("both");
    expect(
      within(row).getByRole("switch", { name: /^Screening/ }),
    ).not.toBeChecked();
    expect(within(row).getByRole("switch", { name: /^Enabled/ })).toBeChecked();
  });

  it("saves the whole list with the changes", async () => {
    const calls = api("operator", [LINK], {
      "PUT /api/v1/boss-links": () =>
        json({
          items: [{ ...LINK, ringMode: "assistant_first", screening: true }],
        }),
    });
    renderApp("/boss");
    const row = await screen.findByRole("group", { name: "Link 1" });
    fireEvent.change(within(row).getByLabelText("Ring mode"), {
      target: { value: "assistant_first" },
    });
    fireEvent.click(within(row).getByRole("switch", { name: /^Screening/ }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      items: [
        {
          bossExtensionId: 1,
          assistantExtensionId: 2,
          ringMode: "assistant_first",
          screening: true,
          enabled: true,
          divertOn: false,
          outboundAsBoss: false,
        },
      ],
    });
    expect(await screen.findByText("Boss links saved.")).toBeInTheDocument();
  });

  it("refuses a boss with two assistants and a link to the boss itself before saving", async () => {
    const calls = api("operator", [LINK]);
    renderApp("/boss");
    await screen.findByRole("group", { name: "Link 1" });
    fireEvent.click(screen.getByRole("button", { name: "Add link" }));
    const second = await screen.findByRole("group", { name: "Link 2" });
    fireEvent.change(within(second).getByLabelText("Boss"), {
      target: { value: "1" },
    });
    fireEvent.change(within(second).getByLabelText("Assistant"), {
      target: { value: "3" },
    });
    expect(
      await within(second).findByText("A boss has one assistant."),
    ).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Save" })).toBeDisabled();
    fireEvent.change(within(second).getByLabelText("Boss"), {
      target: { value: "3" },
    });
    expect(
      await within(second).findByText(
        "The assistant must be a different extension.",
      ),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("removes a link from the list that is sent", async () => {
    const calls = api("operator", [LINK], {
      "PUT /api/v1/boss-links": () => json({ items: [] }),
    });
    renderApp("/boss");
    await screen.findByRole("group", { name: "Link 1" });
    fireEvent.click(screen.getByRole("button", { name: "Remove link 1" }));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
        items: [],
      }),
    );
    expect(await screen.findByText("No boss links")).toBeInTheDocument();
  });

  it("is read-only for a viewer", async () => {
    api("viewer", [LINK]);
    renderApp("/boss");
    const row = await screen.findByRole("group", { name: "Link 1" });
    expect(within(row).getByLabelText("Boss")).toBeDisabled();
    expect(
      screen.queryByRole("button", { name: "Save" }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Add link" }),
    ).not.toBeInTheDocument();
  });
});
