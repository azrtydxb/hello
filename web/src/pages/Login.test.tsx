import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { safeNext } from "../api";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const UNAUTHORIZED = () => apiError(401, "unauthorized", "not signed in");
const VERSION = {
  "GET /api/v1/version": () =>
    json({ version: "v0.9.0-test", commit: "abc1234", configRevision: 3 }),
};

function fillAndSubmit(username: string, password: string) {
  fireEvent.change(screen.getByLabelText("Username"), {
    target: { value: username },
  });
  fireEvent.change(screen.getByLabelText("Password"), {
    target: { value: password },
  });
  fireEvent.click(screen.getByRole("button", { name: "Sign in" }));
}

async function signInScreen() {
  return screen.findByRole("heading", { level: 1, name: "Welcome back" });
}

describe("Login", () => {
  it("redirects to /login with next when an API call answers 401", async () => {
    // The session looks valid at load, then the API rejects it (expired).
    const calls = mockApi({ ...ME, "GET /api/v1/extensions": UNAUTHORIZED });
    renderApp("/extensions");

    // Wait on what the 401 causes: the request, then the move to sign-in.
    await waitFor(() =>
      expect(calls.map((c) => c.url)).toContain("/api/v1/extensions"),
    );
    await waitFor(() =>
      expect(screen.getByTestId("location")).toHaveTextContent(
        "/login?next=%2Fextensions",
      ),
    );
    expect(await signInScreen()).toBeInTheDocument();
    expect(screen.queryByRole("navigation", { name: "Primary" })).toBeNull();
  });

  it("redirects to /login when there is no session at load", async () => {
    mockApi({ "GET /api/v1/auth/me": UNAUTHORIZED });
    renderApp("/devices");

    expect(await signInScreen()).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/login?next=%2Fdevices",
    );
  });

  it("signs in and navigates to next", async () => {
    const calls = mockApi({
      "GET /api/v1/auth/me": [
        UNAUTHORIZED,
        () => json({ username: "admin", role: "admin" }),
      ],
      "POST /api/v1/auth/login": noContent,
      "GET /api/v1/cdrs?limit=50": () => json({ items: [], next: "" }),
    });
    renderApp("/login?next=%2Fhistory");

    await signInScreen();
    fillAndSubmit("admin", "s3cret-pass");

    expect(
      await screen.findByRole("heading", { level: 1, name: "Call history" }),
    ).toBeInTheDocument();
    expect(screen.getByTestId("location")).toHaveTextContent(/^\/history$/);
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/auth/login",
      body: { username: "admin", password: "s3cret-pass" },
    });
    // The sidebar's user block names the signed-in user.
    const profile = screen.getByText("Administrator").closest(".az-profile");
    expect(profile).toHaveTextContent("admin");
  });

  it("shows the failure Alert and stays on the page on a 401", async () => {
    mockApi({
      "GET /api/v1/auth/me": UNAUTHORIZED,
      "POST /api/v1/auth/login": () =>
        apiError(401, "unauthorized", "invalid credentials"),
    });
    renderApp("/login?next=%2Fdevices");

    await signInScreen();
    fillAndSubmit("admin", "wrong");

    const alert = await screen.findByRole("alert");
    expect(alert).toHaveClass("az-alert", "az-alert--bad");
    expect(alert).toHaveTextContent("Sign-in failed");
    expect(alert).toHaveTextContent("The username or password is incorrect.");
    const password = screen.getByLabelText("Password");
    expect(password.getAttribute("aria-describedby")?.split(" ")).toContain(
      alert.id,
    );
    expect(password).toHaveAttribute("aria-invalid", "true");
    expect(password).toHaveValue("");
    expect(screen.getByRole("button", { name: "Sign in" })).toBeEnabled();
    expect(screen.getByTestId("location")).toHaveTextContent(
      "/login?next=%2Fdevices",
    );
  });

  it("disables the button and shows progress while the request is in flight", async () => {
    let release: (r: Response) => void = () => {};
    mockApi({
      "GET /api/v1/auth/me": UNAUTHORIZED,
      "POST /api/v1/auth/login": () =>
        new Promise<Response>((resolve) => {
          release = resolve;
        }),
    });
    renderApp("/login");

    await signInScreen();
    fillAndSubmit("admin", "s3cret-pass");

    const busy = await screen.findByRole("button", { name: "Signing in…" });
    expect(busy).toBeDisabled();
    expect(busy.closest("form")).toHaveAttribute("aria-busy", "true");

    release(apiError(401, "unauthorized", "invalid credentials"));
    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(screen.getByRole("button", { name: "Sign in" })).toBeEnabled();
  });

  it("submits on Enter from the password field", async () => {
    const calls = mockApi({
      "GET /api/v1/auth/me": UNAUTHORIZED,
      "POST /api/v1/auth/login": () =>
        apiError(401, "unauthorized", "invalid credentials"),
    });
    renderApp("/login");

    await signInScreen();
    fireEvent.change(screen.getByLabelText("Username"), {
      target: { value: "admin" },
    });
    fireEvent.change(screen.getByLabelText("Password"), {
      target: { value: "x" },
    });
    // Enter in a field submits its form; jsdom models that as a submit event.
    fireEvent.submit(screen.getByLabelText("Password").closest("form")!);

    expect(await screen.findByRole("alert")).toBeInTheDocument();
    expect(calls.map((c) => `${c.method} ${c.url}`)).toContain(
      "POST /api/v1/auth/login",
    );
  });

  it("never prints credentials on the sign-in screen", async () => {
    mockApi({ "GET /api/v1/auth/me": UNAUTHORIZED, ...VERSION });
    renderApp("/login");

    await signInScreen();
    await screen.findByText("Control plane reachable");
    const page = document.body.textContent ?? "";
    // The design's mock printed "Lab sign-in: admin / hello-lab-admin".
    expect(page).not.toMatch(/sign-in:/i);
    expect(page).not.toMatch(/\badmin\s*\//i);
    expect(page).not.toMatch(/hello-lab/i);
    expect(screen.queryByText("admin")).toBeNull();
    expect(screen.getByLabelText("Username")).not.toHaveAttribute(
      "placeholder",
    );
    expect(screen.getByLabelText("Username")).toHaveValue("");
    expect(screen.getByLabelText("Password")).toHaveValue("");
  });

  it("shows the host, the real version and a reachable control plane", async () => {
    mockApi({ "GET /api/v1/auth/me": UNAUTHORIZED, ...VERSION });
    renderApp("/login");

    await signInScreen();
    expect(screen.getByText(window.location.host)).toBeInTheDocument();
    const status = await screen.findByText("Control plane reachable");
    expect(status.querySelector(".az-dot")).toHaveClass("az-dot--pulse");
    expect(screen.getByText("v0.9.0-test")).toBeInTheDocument();
    expect(screen.getByRole("link", { name: "API docs" })).toHaveAttribute(
      "href",
      "/api/v1/openapi.json",
    );
  });

  it("shows an unreachable control plane without a pulse or version", async () => {
    mockApi({
      "GET /api/v1/auth/me": UNAUTHORIZED,
      "GET /api/v1/version": () => new Response("down", { status: 503 }),
    });
    renderApp("/login");

    const status = await screen.findByText("Control plane unreachable");
    expect(status.querySelector(".az-dot")).not.toHaveClass("az-dot--pulse");
    expect(screen.queryByText("Control plane reachable")).toBeNull();
    expect(screen.queryByText(/^v\d/)).toBeNull();
  });

  it("switches the theme and remembers it", async () => {
    mockApi({ "GET /api/v1/auth/me": UNAUTHORIZED });
    renderApp("/login");

    await signInScreen();
    const html = document.documentElement;
    expect(html).toHaveAttribute("data-theme", "dark");
    const theme = screen.getByRole("radiogroup", { name: "Theme" });
    expect(within(theme).getByRole("radio", { name: "Dark" })).toHaveAttribute(
      "aria-checked",
      "true",
    );

    fireEvent.click(within(theme).getByRole("radio", { name: "Light" }));

    expect(html).toHaveAttribute("data-theme", "light");
    expect(window.localStorage.getItem("hello.theme")).toBe("light");
    expect(within(theme).getByRole("radio", { name: "Light" })).toHaveAttribute(
      "aria-checked",
      "true",
    );
  });

  it("starts from the stored theme", async () => {
    window.localStorage.setItem("hello.theme", "light");
    mockApi({ "GET /api/v1/auth/me": UNAUTHORIZED });
    renderApp("/login");

    await signInScreen();
    expect(document.documentElement).toHaveAttribute("data-theme", "light");
  });

  it("logs out from the nav", async () => {
    const calls = mockApi({ ...ME, "POST /api/v1/auth/logout": noContent });
    renderApp("/ring-groups");

    fireEvent.click(await screen.findByRole("button", { name: "Log out" }));

    expect(await signInScreen()).toBeInTheDocument();
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
