import { fireEvent, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { safeNext } from "../api";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const UNAUTHORIZED = () => apiError(401, "unauthorized", "not signed in");

describe("Login", () => {
  it("redirects to /login with next when an API call answers 401", async () => {
    // The session looks valid at load, then the API rejects it (expired).
    mockApi({ ...ME, "GET /api/v1/extensions": UNAUTHORIZED });
    renderApp("/extensions");

    expect(
      await screen.findByRole("heading", { level: 1, name: "Sign in" }),
    ).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/login?next=%2Fextensions",
    );
    expect(screen.queryByRole("navigation", { name: "Primary" })).toBeNull();
  });

  it("redirects to /login when there is no session at load", async () => {
    mockApi({ "GET /api/v1/auth/me": UNAUTHORIZED });
    renderApp("/devices");

    expect(
      await screen.findByRole("heading", { level: 1, name: "Sign in" }),
    ).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/login?next=%2Fdevices",
    );
  });

  it("signs in and navigates to next", async () => {
    const calls = mockApi({
      "GET /api/v1/auth/me": [UNAUTHORIZED, () => json({ username: "admin" })],
      "POST /api/v1/auth/login": noContent,
      "GET /api/v1/cdrs?limit=50": () => json({ items: [], next: "" }),
    });
    renderApp("/login?next=%2Fhistory");

    fireEvent.change(await screen.findByLabelText("Username"), {
      target: { value: "admin" },
    });
    fireEvent.change(screen.getByLabelText("Password"), {
      target: { value: "s3cret-pass" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));

    expect(
      await screen.findByRole("heading", { level: 1, name: "Call History" }),
    ).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(/^\/history$/);
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/auth/login",
      body: { username: "admin", password: "s3cret-pass" },
    });
    expect(screen.getByText("Signed in as admin")).toBeInTheDocument();
  });

  it("shows an error and stays on the page when the credentials are wrong", async () => {
    mockApi({
      "GET /api/v1/auth/me": UNAUTHORIZED,
      "POST /api/v1/auth/login": () =>
        apiError(401, "unauthorized", "invalid credentials"),
    });
    renderApp("/login?next=%2Fdevices");

    fireEvent.change(await screen.findByLabelText("Username"), {
      target: { value: "admin" },
    });
    fireEvent.change(screen.getByLabelText("Password"), {
      target: { value: "wrong" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Sign in" }));

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveTextContent("Incorrect username or password.");
    expect(screen.getByLabelText("Password")).toHaveAttribute(
      "aria-describedby",
      alert.id,
    );
    expect(screen.getByLabelText("Password")).toHaveValue("");
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/login?next=%2Fdevices",
    );
  });

  it("logs out from the nav", async () => {
    const calls = mockApi({ ...ME, "POST /api/v1/auth/logout": noContent });
    renderApp("/trunks");

    fireEvent.click(await screen.findByRole("button", { name: "Log out" }));

    expect(
      await screen.findByRole("heading", { level: 1, name: "Sign in" }),
    ).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(/^\/login$/);
    expect(calls.map((c) => `${c.method} ${c.url}`)).toContain(
      "POST /api/v1/auth/logout",
    );
  });

  it("only follows same-origin paths after sign-in", () => {
    expect(safeNext("/devices?x=1")).toBe("/devices?x=1");
    expect(safeNext("//evil.example")).toBe("/");
    expect(safeNext("https://evil.example")).toBe("/");
    expect(safeNext("/login?next=/x")).toBe("/");
    expect(safeNext(null)).toBe("/");
  });
});
