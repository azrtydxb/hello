import { fireEvent, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, mockApi, renderApp } from "../test/api";
import { parsePrefixes } from "./ClickToCallPolicy";

const ADMIN = {
  "GET /api/v1/auth/me": () => json({ username: "root", role: "admin" }),
  "GET /api/v1/service-accounts": () => json({ items: [] }),
  "GET /api/v1/tokens": () => json({ items: [] }),
};

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("parsePrefixes", () => {
  it("splits on commas and whitespace", () => {
    expect(parsePrefixes("+971, 00971  +44")).toEqual(["+971", "00971", "+44"]);
    expect(parsePrefixes("")).toEqual([]);
  });
});

describe("ClickToCallPolicy", () => {
  it("shows the policy and saves a restriction", async () => {
    const calls = mockApi({
      ...ADMIN,
      "GET /api/v1/integrations/click-to-call": () =>
        json({
          destinations: "inherit",
          countryPrefixes: [],
          autoAnswerAllowed: false,
        }),
      "PUT /api/v1/integrations/click-to-call": () =>
        json({
          destinations: "restricted",
          countryPrefixes: ["+971"],
          autoAnswerAllowed: true,
        }),
    });
    renderApp("/integrations");
    const select = await screen.findByLabelText("Destinations");
    expect(screen.queryByLabelText("Country prefixes")).toBeNull();
    fireEvent.change(select, { target: { value: "restricted" } });
    fireEvent.change(await screen.findByLabelText("Country prefixes"), {
      target: { value: "+971" },
    });
    fireEvent.click(
      screen.getByRole("switch", {
        name: /Allow a request to ask the phone to answer/,
      }),
    );
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
        destinations: "restricted",
        countryPrefixes: ["+971"],
        autoAnswerAllowed: true,
      }),
    );
  });
});
