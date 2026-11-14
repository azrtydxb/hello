import { screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, mockApi, renderApp } from "../test/api";

afterEach(() => {
  vi.unstubAllGlobals();
});

const ALICE = {
  "GET /api/v1/auth/me": () =>
    json({
      username: "alice",
      role: "user",
      extensions: [{ id: 10, number: "101", name: "Alice" }],
    }),
};

describe("ExtensionCard", () => {
  it("links the package and says it is signed when the image carries one", async () => {
    mockApi({
      ...ALICE,
      "GET /downloads/hello-extension.json": () =>
        json({
          version: "1.0.0",
          id: "kengpjalagamnmmkljldjhhgonmbdcph",
          signed: true,
        }),
    });
    renderApp("/dial");
    expect(
      await screen.findByRole("link", { name: "hello-extension.zip" }),
    ).toHaveAttribute("href", "/downloads/hello-extension.zip");
    expect(
      screen.getByRole("link", { name: "hello-extension.crx" }),
    ).toBeTruthy();
    expect(screen.getByText("signed")).toBeTruthy();
    expect(screen.getByText("kengpjalagamnmmkljldjhhgonmbdcph")).toBeTruthy();
  });

  it("says so when the installation has no package", async () => {
    mockApi(ALICE);
    renderApp("/dial");
    expect(
      await screen.findByText("No package in this installation"),
    ).toBeTruthy();
    expect(
      screen.queryByRole("link", { name: "hello-extension.zip" }),
    ).toBeNull();
  });
});
