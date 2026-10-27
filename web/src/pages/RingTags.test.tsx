import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const tag = (name: string, slot: number, builtin = true, usedBy = 0) => ({
  name,
  builtin,
  bellcoreSlot: slot,
  description: "",
  usedBy,
});
const TAGS = [
  tag("internal", 1),
  tag("external", 2),
  tag("doorbell", 4),
  tag("vip", 5, false, 1),
];

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/ring-tags": () => json({ items: TAGS }),
    "GET /api/v1/ring-rules": () => json({ items: [] }),
    ...extra,
  });
}

describe("RingTags", () => {
  it("lists the tags with their Bellcore slot and marks the built-in ones", async () => {
    setup();
    renderApp("/ringing");
    const door = await screen.findByRole("listitem", {
      name: "Ring tag doorbell",
    });
    expect(door).toHaveTextContent("Bellcore-dr4");
    expect(door).toHaveTextContent("Built in");
    // A built-in tag cannot be deleted; a custom one can.
    expect(
      screen.queryByRole("button", { name: "Delete ring tag doorbell" }),
    ).toBeNull();
    expect(
      screen.getByRole("button", { name: "Delete ring tag vip" }),
    ).toBeVisible();
  });

  it("creates a custom tag", async () => {
    const calls = setup({
      "POST /api/v1/ring-tags": () => json(tag("late-shift", 3, false), 201),
    });
    renderApp("/ringing");
    const create = await screen.findByRole("button", { name: "New ring tag" });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create);
    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "Late Shift" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create ring tag" }));
    expect(
      await screen.findByText(
        "Use 1 to 32 lowercase letters, digits or dashes.",
      ),
    ).toBeVisible();

    fireEvent.change(screen.getByLabelText("Name"), {
      target: { value: "late-shift" },
    });
    fireEvent.change(screen.getByLabelText("Bellcore slot"), {
      target: { value: "3" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create ring tag" }));
    expect(
      await screen.findByText("Ring tag late-shift created."),
    ).toBeVisible();
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/ring-tags",
      body: { name: "late-shift", bellcoreSlot: 3, description: "" },
    });
  });

  it("refuses to delete a tag that is in use and says why", async () => {
    setup({
      "DELETE /api/v1/ring-tags/vip": () =>
        apiError(409, "conflict", "ring tag vip is used by rule 1"),
    });
    renderApp("/ringing");
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete ring tag vip" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Delete ring tag" }));
    expect(
      await screen.findByText(/ring tag vip is used by rule 1/),
    ).toBeVisible();
  });

  it("deletes an unused custom tag", async () => {
    const calls = setup({
      "GET /api/v1/ring-tags": () =>
        json({ items: [tag("internal", 1), tag("spare", 1, false)] }),
      "DELETE /api/v1/ring-tags/spare": () => noContent(),
    });
    renderApp("/ringing");
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete ring tag spare" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Delete ring tag" }));
    expect(await screen.findByText("Ring tag spare deleted.")).toBeVisible();
    expect(calls).toContainEqual({
      method: "DELETE",
      url: "/api/v1/ring-tags/spare",
      body: undefined,
    });
  });

  it("saves the rules in order with their conditions", async () => {
    const calls = setup({
      "PUT /api/v1/ring-rules": () =>
        json({
          items: [
            {
              id: 1,
              position: 1,
              name: "boss",
              match: { callerPattern: "1XX" },
              tag: "vip",
            },
          ],
        }),
    });
    renderApp("/ringing");
    fireEvent.click(await screen.findByRole("tab", { name: "Rules" }));
    const add = await screen.findByRole("button", { name: "Add rule" });
    fireEvent.click(add);
    // A rule with no condition and no tag is refused before the request.
    fireEvent.click(screen.getByRole("button", { name: "Save rules" }));
    expect(
      await screen.findByText("Fix the highlighted fields."),
    ).toBeVisible();
    expect(calls.filter((c) => c.method === "PUT")).toHaveLength(0);

    fireEvent.change(screen.getByLabelText("Rule 1 name"), {
      target: { value: "boss" },
    });
    fireEvent.change(screen.getByLabelText("Rule 1 caller number pattern"), {
      target: { value: "1XX" },
    });
    await waitFor(() =>
      expect(screen.getByLabelText("Rule 1 tag")).toHaveTextContent("vip"),
    );
    fireEvent.change(screen.getByLabelText("Rule 1 tag"), {
      target: { value: "vip" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save rules" }));
    expect(await screen.findByText("Ring tag rules saved.")).toBeVisible();
    expect(calls).toContainEqual({
      method: "PUT",
      url: "/api/v1/ring-rules",
      body: {
        rules: [{ name: "boss", match: { callerPattern: "1XX" }, tag: "vip" }],
      },
    });
  });

  it("moves a rule up and sends the new order", async () => {
    const rule = (id: number, name: string, did: string) => ({
      id,
      position: id,
      name,
      match: { did },
      tag: "vip",
    });
    const calls = setup({
      "GET /api/v1/ring-rules": () =>
        json({ items: [rule(1, "first", "100"), rule(2, "second", "200")] }),
      "PUT /api/v1/ring-rules": () =>
        json({ items: [rule(1, "second", "200"), rule(2, "first", "100")] }),
    });
    renderApp("/ringing");
    fireEvent.click(await screen.findByRole("tab", { name: "Rules" }));
    fireEvent.click(
      await screen.findByRole("button", { name: "Move rule 2 up" }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Save rules" }));
    expect(await screen.findByText("Ring tag rules saved.")).toBeVisible();
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.body).toEqual({
      rules: [
        { name: "second", match: { did: "200" }, tag: "vip" },
        { name: "first", match: { did: "100" }, tag: "vip" },
      ],
    });
  });

  it("shows the tag a described call would get", async () => {
    setup({
      "GET /api/v1/ring-rules/evaluate?kind=alarm&callerNumber=150": () =>
        json({
          tag: "alarm",
          why: "kind:alarm",
          bellcoreSlot: 5,
          alertInfo: "<http://127.0.0.1/Bellcore-dr5>;info=hello-alarm",
        }),
    });
    renderApp("/ringing");
    fireEvent.click(await screen.findByRole("tab", { name: "Test a call" }));
    fireEvent.change(await screen.findByLabelText("Kind of call"), {
      target: { value: "alarm" },
    });
    fireEvent.change(screen.getByLabelText("Caller number"), {
      target: { value: "150" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Test" }));
    expect(await screen.findByText("Rings with alarm")).toBeVisible();
    expect(screen.getByText(/kind:alarm/)).toBeVisible();
  });
});
