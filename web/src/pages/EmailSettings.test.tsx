import { fireEvent, screen, waitFor } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  apiError,
  json,
  mockApi,
  noContent,
  renderApp,
  type Routes,
} from "../test/api";

const SMTP = {
  host: "smtp.example.com",
  port: 587,
  security: "starttls",
  username: "pbx",
  from: "pbx@example.com",
  passwordSet: true,
  source: "database",
  updatedAt: "2026-10-01T00:00:00Z",
};

function api(role: string, more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "pat", role }),
    "GET /api/v1/settings/smtp": () => json(SMTP),
    ...more,
  });
}

describe("Email settings", () => {
  it("shows the stored settings without any password", async () => {
    api("admin");
    renderApp("/settings/email");
    expect(await screen.findByLabelText("SMTP host")).toHaveValue(
      "smtp.example.com",
    );
    expect(screen.getByLabelText("Port")).toHaveValue("587");
    expect(screen.getByLabelText("Username")).toHaveValue("pbx");
    expect(screen.getByLabelText("Password")).toHaveValue("");
    expect(
      screen.getByText(/A password is stored and never shown/),
    ).toBeInTheDocument();
    expect(screen.getByText("Saved here")).toBeInTheDocument();
  });

  it("keeps the stored password when none is typed, and sends a new one when it is", async () => {
    const calls = api("admin", {
      "PUT /api/v1/settings/smtp": () => json(SMTP),
    });
    renderApp("/settings/email");
    await screen.findByLabelText("SMTP host");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      host: "smtp.example.com",
      port: 587,
      security: "starttls",
      username: "pbx",
      from: "pbx@example.com",
    });

    fireEvent.change(screen.getByLabelText("Password"), {
      target: { value: "new-secret" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls.filter((c) => c.method === "PUT")).toHaveLength(2),
    );
    expect(calls.filter((c) => c.method === "PUT")[1]?.body).toMatchObject({
      password: "new-secret",
    });
    // Once saved the field is empty again.
    expect(screen.getByLabelText("Password")).toHaveValue("");
  });

  it("can remove the stored password", async () => {
    const calls = api("admin", {
      "PUT /api/v1/settings/smtp": () => json({ ...SMTP, passwordSet: false }),
    });
    renderApp("/settings/email");
    await screen.findByLabelText("SMTP host");
    fireEvent.click(screen.getByLabelText("Remove the stored password"));
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    expect(calls.find((c) => c.method === "PUT")?.body).toMatchObject({
      password: "",
    });
  });

  it("follows the usual port when the security changes", async () => {
    api("admin");
    renderApp("/settings/email");
    fireEvent.change(await screen.findByLabelText("Security"), {
      target: { value: "tls" },
    });
    expect(screen.getByLabelText("Port")).toHaveValue("465");
  });

  it("shows field errors from the server", async () => {
    api("admin", {
      "PUT /api/v1/settings/smtp": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid configuration",
              fields: [
                { path: "from", message: "from must be an email address" },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/settings/email");
    await screen.findByLabelText("SMTP host");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(
      await screen.findByText("from must be an email address"),
    ).toBeInTheDocument();
  });

  it("sends a test email and reports the result", async () => {
    const calls = api("admin", {
      "POST /api/v1/settings/smtp/test": [
        () =>
          apiError(
            502,
            "smtp_failed",
            "sending failed: 535 authentication failed",
          ),
        () => noContent(),
      ],
    });
    renderApp("/settings/email");
    await screen.findByLabelText("SMTP host");
    const send = screen.getByRole("button", { name: "Send test email" });
    expect(send).toBeDisabled();
    fireEvent.change(screen.getByLabelText("Send the test to"), {
      target: { value: "me@example.com" },
    });
    fireEvent.click(send);
    expect(
      await screen.findByText("sending failed: 535 authentication failed"),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Send test email" }));
    expect(
      await screen.findByText("A test email was sent to me@example.com."),
    ).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST")).toHaveLength(2);
    expect(calls).toContainEqual({
      method: "POST",
      url: "/api/v1/settings/smtp/test",
      body: { to: "me@example.com" },
    });
  });

  it("cannot test when nothing is configured", async () => {
    api("admin", {
      "GET /api/v1/settings/smtp": () =>
        json({
          ...SMTP,
          host: "",
          passwordSet: false,
          source: "none",
          updatedAt: null,
        }),
    });
    renderApp("/settings/email");
    await screen.findByText("Not configured");
    fireEvent.change(screen.getByLabelText("Send the test to"), {
      target: { value: "me@example.com" },
    });
    expect(
      screen.getByRole("button", { name: "Send test email" }),
    ).toBeDisabled();
  });

  it("is for administrators only", async () => {
    const calls = api("operator");
    renderApp("/settings/email");
    expect(
      await screen.findByText("Your role cannot change the email settings."),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.url === "/api/v1/settings/smtp")).toBe(false);
  });
});
