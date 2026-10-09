import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../test/api";

const SERVER = {
  id: 9,
  name: "crm",
  url: "https://crm.internal/mcp",
  auth: "bearer",
  timeoutMs: 10000,
  enabled: true,
  lastCheckAt: "2026-10-09T08:00:00Z",
  lastCheckStatus: "ok",
  credentialSet: true,
};

function setup(role: string, extra = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/auth/me": () => json({ username: "pat", role }),
    "GET /api/v1/voice/mcp-servers": () => json({ items: [SERVER] }),
    ...extra,
  });
}

describe("VoiceMCPServers", () => {
  it("lists the servers with their last check status (S-27)", async () => {
    setup("viewer");
    renderApp("/voice/mcp-servers");

    const list = await screen.findByRole("list", {
      name: "Voice MCP servers",
    });
    expect(list).toHaveTextContent("crm");
    expect(list).toHaveTextContent("Bearer token");
    expect(list).toHaveTextContent("credential set");
    expect(list).toHaveTextContent("ok");
  });

  it("never renders a credential value and hides the fields from an operator (S-5, S-27, S-31)", async () => {
    setup("operator");
    renderApp("/voice/mcp-servers");

    await screen.findByRole("list", { name: "Voice MCP servers" });
    // Operators manage attachments, not servers: no form, no credential field.
    expect(screen.queryByText("New MCP server")).toBeNull();
    expect(
      screen.queryByRole("button", { name: "Edit MCP server crm" }),
    ).toBeNull();
    expect(document.querySelector('input[type="password"]')).toBeNull();
  });

  it("offers the credential write-only to an administrator (S-5)", async () => {
    const calls = setup("admin", {
      "PUT /api/v1/voice/mcp-servers/9": () => json(SERVER),
    });
    renderApp("/voice/mcp-servers");

    fireEvent.click(
      await screen.findByRole("button", { name: "Edit MCP server crm" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "MCP server crm",
    });
    const credential = within(dialog).getByLabelText("Credential");
    // Write-only: the stored value never comes back.
    expect(credential).toHaveValue("");
    expect(credential).toHaveAttribute("type", "password");
    expect(dialog).toHaveTextContent(/Stored; leave empty to keep it/);

    fireEvent.change(credential, {
      target: { value: "super-secret-token" },
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Save server" }),
    );
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "PUT" && c.url === "/api/v1/voice/mcp-servers/9",
        ),
      ).toBe(true),
    );
    const put = calls.find(
      (c) => c.method === "PUT" && c.url === "/api/v1/voice/mcp-servers/9",
    );
    expect((put?.body as { credential?: string }).credential).toBe(
      "super-secret-token",
    );
    // The value is not echoed anywhere after the save.
    expect(document.body).not.toHaveTextContent("super-secret-token");
  });

  it("keeps the stored credential when the field stays empty on save", async () => {
    const calls = setup("admin", {
      "PUT /api/v1/voice/mcp-servers/9": () => json(SERVER),
    });
    renderApp("/voice/mcp-servers");

    fireEvent.click(
      await screen.findByRole("button", { name: "Edit MCP server crm" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: "MCP server crm",
    });
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Save server" }),
    );
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    const body = calls.find((c) => c.method === "PUT")?.body as {
      credential?: string;
    };
    expect(body.credential).toBeUndefined();
  });

  it("shows the discovery output as plain text, never HTML (S-7)", async () => {
    setup("operator", {
      "POST /api/v1/voice/mcp-servers/9/discover": () =>
        json({
          tools: [
            {
              name: "page_oncall",
              description:
                'Pages the on-call engineer <img src=x onerror="window.__pwned=1">',
            },
          ],
        }),
    });
    const { container } = renderApp("/voice/mcp-servers");

    fireEvent.click(
      await screen.findByRole("button", { name: "Discover tools" }),
    );
    const text = await screen.findByText(/page_oncall: Pages the on-call/);
    expect(text.closest("pre")).not.toBeNull();
    // The markup stayed text: the pre contains no image element.
    expect(
      within(text.closest("pre") as HTMLElement).queryByRole("img"),
    ).toBeNull();
    expect(text.textContent).toContain(
      '<img src=x onerror="window.__pwned=1">',
    );
    expect((window as { __pwned?: number }).__pwned).toBeUndefined();
    expect(container).toBeDefined();
  });

  it("reports a test connection with its latency (S-9)", async () => {
    setup("operator", {
      "POST /api/v1/voice/mcp-servers/9/test": () =>
        json({ ok: true, latencyMs: 123, result: "2 tools offered" }),
    });
    renderApp("/voice/mcp-servers");

    fireEvent.click(
      await screen.findByRole("button", { name: "Test connection" }),
    );
    expect(await screen.findByText(/123 ms/)).toBeInTheDocument();
    expect(screen.getByText("2 tools offered")).toBeInTheDocument();
  });
});
