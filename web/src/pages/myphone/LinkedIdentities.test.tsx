import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { json, mockApi, noContent } from "../../test/api";
import { LinkedIdentities } from "./LinkedIdentities";

const ENTRA = {
  id: 4,
  userId: 2,
  provider: "entra",
  subject: "oid-2",
  tenant: "t1",
  email: "sam@corp.test",
  createdAt: "2026-11-01T00:00:00Z",
  lastLoginAt: null,
};

const PROVIDERS = {
  items: [
    { provider: "google", name: "Google" },
    { provider: "entra", name: "Microsoft" },
  ],
};

describe("LinkedIdentities", () => {
  afterEach(() => {
    window.history.replaceState(null, "", "/");
    vi.unstubAllGlobals();
  });

  it("lists linked accounts, links another and unlinks one", async () => {
    const assign = vi.fn();
    vi.stubGlobal("location", { ...window.location, assign, search: "" });
    const calls = mockApi({
      "GET /api/v1/auth/oidc/providers": () => json(PROVIDERS),
      "GET /api/v1/me/identities": [
        () => json({ items: [ENTRA] }),
        () => json({ items: [] }),
      ],
      "POST /api/v1/me/identities/google/link": () =>
        json({ url: "https://accounts.example/authorize?state=s" }),
      "DELETE /api/v1/me/identities/entra": () => noContent(),
    });
    render(<LinkedIdentities />);
    expect(await screen.findByText("Microsoft")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: "Link Microsoft" })).toBeNull();
    fireEvent.click(screen.getByRole("button", { name: "Unlink Microsoft" }));
    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "DELETE",
        url: "/api/v1/me/identities/entra",
        body: undefined,
      }),
    );
    expect(
      await screen.findByRole("button", { name: "Link Microsoft" }),
    ).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Link Google" }));
    await waitFor(() =>
      expect(assign).toHaveBeenCalledWith(
        "https://accounts.example/authorize?state=s",
      ),
    );
  });

  it("confirms a completed link", async () => {
    window.history.replaceState(null, "", "/me/profile?sso_linked=google");
    mockApi({
      "GET /api/v1/auth/oidc/providers": () => json(PROVIDERS),
      "GET /api/v1/me/identities": () => json({ items: [] }),
    });
    render(<LinkedIdentities />);
    expect(
      await screen.findByText(/You can now sign in with Google/),
    ).toBeInTheDocument();
  });

  it("shows nothing when no provider is enabled", async () => {
    const calls = mockApi({
      "GET /api/v1/auth/oidc/providers": () => json({ items: [] }),
      "GET /api/v1/me/identities": () => json({ items: [] }),
    });
    const { container } = render(<LinkedIdentities />);
    await waitFor(() => expect(calls).toHaveLength(2));
    expect(container).toBeEmptyDOMElement();
  });
});
