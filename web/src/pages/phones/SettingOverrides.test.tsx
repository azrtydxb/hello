import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../../test/api";
import { rowProblem } from "./SettingOverrides";

const STAMP = "2026-10-01T10:00:00Z";
const PHONE = {
  id: 5,
  mac: "805ec0123456",
  serial: "",
  vendor: "grandstream",
  model: "WP836",
  label: "Front desk",
  deviceId: 3,
  extensionId: 7,
  extensionNumber: "101",
  templateId: null,
  groupId: null,
  blf: [],
  enabled: true,
  tokenExposed: false,
  uaMismatch: false,
  bootArmed: false,
  bootReclaimed: false,
  redirectStatus: { state: "manual" },
  firstFetchAt: null,
  lastFetchAt: null,
  lastFetchIp: null,
  lastFetchUa: null,
  lastFetchFile: null,
  firmwareSeen: null,
  renderError: false,
  createdAt: STAMP,
  updatedAt: STAMP,
};

describe("rowProblem", () => {
  const row = (key: string, value = "1") => ({
    key,
    value,
    secret: false,
    stored: false,
  });
  it("accepts Grandstream and Yealink keys", () => {
    expect(rowProblem([row("P71"), row("features.text_message.enable")])).toBe(
      null,
    );
  });
  it("refuses other keys, duplicates and multi-line values", () => {
    expect(rowProblem([row("")])).toMatch(/not a Grandstream/);
    expect(rowProblem([row("p71")])).toMatch(/not a Grandstream/);
    expect(rowProblem([row("P71"), row("P71")])).toMatch(/twice/);
    expect(rowProblem([row("P71", "a\nb")])).toMatch(/one line/);
  });
});

describe("phone setting overrides", () => {
  it("lists, keeps a masked secret and saves an added setting", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/phones/5": () => json(PHONE),
      "GET /api/v1/phones/5/settings": () =>
        json({
          items: [
            { key: "P71", value: "100", secret: false, updatedAt: STAMP },
            { key: "P9999", value: "********", secret: true, updatedAt: STAMP },
          ],
        }),
      "PUT /api/v1/phones/5/settings": () =>
        json({
          items: [
            { key: "P71", value: "200", secret: false, updatedAt: STAMP },
          ],
        }),
    });
    renderApp("/phones/5");
    const key = await screen.findByDisplayValue("P71");
    expect(key).toBeTruthy();
    // A secret's value is never shown.
    expect(screen.queryByDisplayValue("hunter2")).toBeNull();
    fireEvent.change(screen.getByDisplayValue("100"), {
      target: { value: "200" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save settings" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.body).toEqual({
      overrides: [
        { key: "P71", secret: false, value: "200" },
        // The masked secret is sent back as "keep".
        { key: "P9999", secret: true, value: null },
      ],
    });
  });

  it("blocks a bad key before it reaches the server", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/phones/5": () => json(PHONE),
      "GET /api/v1/phones/5/settings": () => json({ items: [] }),
    });
    renderApp("/phones/5");
    fireEvent.click(
      await screen.findByRole("button", { name: "Add a setting" }),
    );
    fireEvent.change(screen.getByLabelText("Key"), {
      target: { value: "not a key" },
    });
    expect(
      await screen.findByText(/is not a Grandstream P-value/),
    ).toBeTruthy();
    expect(
      (
        screen.getByRole("button", {
          name: "Save settings",
        }) as HTMLButtonElement
      ).disabled,
    ).toBe(true);
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
  });
});
