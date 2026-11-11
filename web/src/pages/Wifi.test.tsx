import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, mockApi, noContent, renderApp } from "../test/api";

const PROFILE = {
  id: 4,
  name: "Office",
  ssid: "corp-wifi",
  security: "wpa2-psk",
  pskSet: true,
  eapMethod: "",
  identity: "",
  anonymousIdentity: "",
  passwordSet: false,
  caCert: "system",
  priority: 5,
  hidden: false,
  preferAccount: 0,
  verified: false,
  groups: 1,
  phones: 0,
  updatedAt: "2026-11-09T10:00:00Z",
};

const SCOPE = {
  profileIds: [4],
  inheritedProfileIds: [],
  pushing: false,
  radio: { threshold: -60 },
};

const as = (role: string) => ({
  "GET /api/v1/auth/me": () => json({ username: "u", role }),
});

function setup(role = "admin", extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...as(role),
    "GET /api/v1/wifi-profiles": () => json({ items: [PROFILE] }),
    "GET /api/v1/phone-groups?limit=200": () =>
      json({ items: [{ id: 7, name: "Warehouse" }], nextCursor: "" }),
    "GET /api/v1/phones": () =>
      json({ items: [{ id: 9, mac: "805ec0000009", label: "Dock" }] }),
    "GET /api/v1/phone-groups/7/wifi": () => json(SCOPE),
    ...extra,
  });
}

describe("Wi-Fi profiles", () => {
  it("lists profiles without a secret and keeps a stored key out of the edit form", async () => {
    setup();
    renderApp("/phones/wifi");
    const table = await screen.findByRole("table", { name: "Wi-Fi profiles" });
    expect(within(table).getByText("corp-wifi")).toBeTruthy();
    expect(within(table).getByText("Not verified")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Edit Office" }));
    const key = (await screen.findByLabelText(
      "Pre-shared key",
    )) as HTMLInputElement;
    expect(key.value).toBe("");
    expect(key.placeholder).toContain("stored");
  });

  it("sends the typed key once and shows a refused field at its input", async () => {
    const calls = setup("admin", {
      "POST /api/v1/wifi-profiles": [
        () =>
          json(
            {
              error: {
                code: "bad_request",
                message: "invalid",
                fields: [
                  {
                    path: "psk",
                    message: "must be 8 to 63 printable ASCII characters",
                  },
                ],
              },
            },
            400,
          ),
        () => json({ ...PROFILE, id: 5, name: "Guest", ssid: "guest" }, 201),
      ],
    });
    renderApp("/phones/wifi");
    fireEvent.click(await screen.findByRole("button", { name: "Add profile" }));
    fireEvent.change(await screen.findByLabelText("Name"), {
      target: { value: "Guest" },
    });
    fireEvent.change(screen.getByLabelText("SSID"), {
      target: { value: "guest" },
    });
    fireEvent.change(screen.getByLabelText("Pre-shared key"), {
      target: { value: "short" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create profile" }));
    expect(await screen.findByText(/must be 8 to 63/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Pre-shared key"), {
      target: { value: "longenough1" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create profile" }));
    await waitFor(() =>
      expect(calls.filter((c) => c.method === "POST")).toHaveLength(2),
    );
    const sent = calls.filter((c) => c.method === "POST")[1]?.body as Record<
      string,
      unknown
    >;
    expect(sent.psk).toBe("longenough1");
    expect(sent.security).toBe("wpa2-psk");
    // The key does not stay on screen after the save.
    await waitFor(() =>
      expect(screen.queryByLabelText("Pre-shared key")).toBeNull(),
    );
    expect(document.body.textContent).not.toContain("longenough1");
  });

  it("asks before deleting and shows the refusal of a profile in use", async () => {
    setup("admin", {
      "DELETE /api/v1/wifi-profiles/4": () =>
        apiError(
          409,
          "conflict",
          "the Wi-Fi profile is assigned to 1 phone groups and 0 phones; remove it from them first",
        ),
    });
    renderApp("/phones/wifi");
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete Office" }),
    );
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete profile" }),
    );
    expect(await screen.findByText(/assigned to 1 phone groups/)).toBeTruthy();
  });

  it("shows the gate and saves a group's profiles and roaming", async () => {
    const calls = setup("admin", {
      "PUT /api/v1/phone-groups/7/wifi": () =>
        json({ ...SCOPE, pushing: true }),
    });
    renderApp("/phones/wifi");
    await screen.findByRole("option", { name: "Phone group Warehouse" });
    fireEvent.change(screen.getByLabelText("Applies to"), {
      target: { value: "g:7" },
    });
    expect(await screen.findByText("Not pushing")).toBeTruthy();
    expect(
      (screen.getByLabelText("Roaming threshold (dBm)") as HTMLInputElement)
        .value,
    ).toBe("-60");
    fireEvent.change(screen.getByLabelText("Band"), { target: { value: "5" } });
    fireEvent.click(screen.getByRole("button", { name: "Save Wi-Fi setup" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    const put = calls.find((c) => c.method === "PUT")?.body as {
      profileIds: number[];
      radio: Record<string, unknown>;
    };
    expect(put.profileIds).toEqual([4]);
    expect(put.radio).toEqual({ band: "5", threshold: -60 });
    expect(await screen.findByText("Pushing")).toBeTruthy();
  });

  it("assigns profiles to a single phone without the roaming block", async () => {
    const calls = setup("admin", {
      "GET /api/v1/phones/9/wifi": () =>
        json({ profileIds: [], inheritedProfileIds: [4], pushing: false }),
      "PUT /api/v1/phones/9/wifi": () =>
        json({ profileIds: [4], inheritedProfileIds: [4], pushing: false }),
    });
    renderApp("/phones/wifi");
    await screen.findByRole("option", { name: "Phone Dock" });
    fireEvent.change(screen.getByLabelText("Applies to"), {
      target: { value: "p:9" },
    });
    expect(await screen.findByText(/From its group: Office/)).toBeTruthy();
    expect(screen.queryByLabelText("Band")).toBeNull();
    fireEvent.click(screen.getByLabelText(/Office \(corp-wifi\)/));
    fireEvent.click(screen.getByRole("button", { name: "Save Wi-Fi setup" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    expect(
      (calls.find((c) => c.method === "PUT")?.body as { profileIds: number[] })
        .profileIds,
    ).toEqual([4]);
  });

  it("gives a viewer no write control", async () => {
    setup("viewer");
    renderApp("/phones/wifi");
    await screen.findByRole("table", { name: "Wi-Fi profiles" });
    expect(screen.queryByRole("button", { name: "Add profile" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Edit Office" })).toBeNull();
    expect(screen.queryByRole("button", { name: "Delete Office" })).toBeNull();
    await screen.findByRole("option", { name: "Phone group Warehouse" });
    fireEvent.change(screen.getByLabelText("Applies to"), {
      target: { value: "g:7" },
    });
    await screen.findByText("Not pushing");
    expect(
      screen.queryByRole("button", { name: "Save Wi-Fi setup" }),
    ).toBeNull();
    expect(
      (screen.getByLabelText(/Office \(corp-wifi\)/) as HTMLInputElement)
        .disabled,
    ).toBe(true);
    void noContent;
  });
});
