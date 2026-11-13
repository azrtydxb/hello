import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, mockApi, renderApp, type Routes } from "../../test/api";
import { validateFollowMe } from "./FollowMeEditor";
import type { FollowMe } from "../../api/followme";

const ME = {
  username: "alice",
  role: "user",
  displayName: "Alice",
  email: "alice@example.com",
  authMethod: "local",
  mustChangePassword: false,
  extensions: [{ id: 10, number: "101", name: "Alice" }],
  voicemailEmail: { notify: true, attach: true, after: "none" },
  emailDelivery: true,
};

const EXT = {
  id: 10,
  number: "101",
  name: "Alice",
  dnd: false,
  forwardAlways: "",
  forwardBusy: "",
  forwardNoAnswer: "",
  voicemailEnabled: true,
};

const PROFILE: FollowMe = {
  extensionId: 10,
  number: "101",
  enabled: false,
  strategy: "simultaneous",
  ringOwnDevices: true,
  timeoutSeconds: 30,
  confirmExternal: true,
  dndBlocks: true,
  targets: [],
};

function api(more: Routes = {}) {
  return mockApi({
    "GET /api/v1/auth/me": () => json(ME),
    "GET /api/v1/me/extensions": () => json({ items: [EXT] }),
    "GET /api/v1/me/extensions/10/follow-me": () => json(PROFILE),
    ...more,
  });
}

async function addTarget(kind: string, target: string) {
  fireEvent.click(await screen.findByRole("button", { name: "Add target" }));
  const rows = screen.getAllByRole("listitem");
  const row = rows[rows.length - 1]!;
  fireEvent.change(within(row).getByLabelText(/type$/), {
    target: { value: kind },
  });
  fireEvent.change(within(row).getByLabelText(/^Target \d+$/), {
    target: { value: target },
  });
}

describe("FollowMeEditor", () => {
  it("saves a profile with an external and an extension target", async () => {
    const calls = api({
      "PUT /api/v1/me/extensions/10/follow-me": () =>
        json({
          ...PROFILE,
          enabled: true,
          targets: [
            { kind: "external", target: "+32475123456", delaySeconds: 0 },
          ],
        }),
    });
    renderApp("/me/forwarding");
    await addTarget("external", "+32475123456");
    fireEvent.click(screen.getByRole("switch", { name: /^Follow-me/ }));
    fireEvent.click(screen.getByRole("button", { name: "Save follow-me" }));

    await waitFor(() =>
      expect(calls).toContainEqual({
        method: "PUT",
        url: "/api/v1/me/extensions/10/follow-me",
        body: {
          enabled: true,
          strategy: "simultaneous",
          ringOwnDevices: true,
          timeoutSeconds: 30,
          confirmExternal: true,
          dndBlocks: true,
          targets: [
            { kind: "external", target: "+32475123456", delaySeconds: 0 },
          ],
        },
      }),
    );
    expect(await screen.findByText("Follow-me saved.")).toBeVisible();
  });

  it("sends the delay of a sequential target and hides it otherwise", async () => {
    const calls = api({
      "PUT /api/v1/me/extensions/10/follow-me": () => json(PROFILE),
    });
    renderApp("/me/forwarding");
    await addTarget("extension", "102");
    expect(screen.queryByLabelText(/rings for/)).toBeNull();
    fireEvent.change(screen.getByLabelText("Ring"), {
      target: { value: "sequential" },
    });
    fireEvent.change(screen.getByLabelText(/rings for/), {
      target: { value: "15" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save follow-me" }));
    await waitFor(() => {
      const put = calls.find((c) => c.method === "PUT");
      expect(put?.body).toMatchObject({
        strategy: "sequential",
        targets: [{ kind: "extension", target: "102", delaySeconds: 15 }],
      });
    });
  });

  it("refuses a bad profile before sending", async () => {
    const calls = api();
    renderApp("/me/forwarding");

    // Turned on with no target.
    fireEvent.click(await screen.findByRole("switch", { name: /^Follow-me/ }));
    fireEvent.click(screen.getByRole("button", { name: "Save follow-me" }));
    expect(
      await screen.findByText(/Add at least one target/),
    ).toBeInTheDocument();

    // The extension itself, then a number that is not dialable.
    await addTarget("extension", "101");
    fireEvent.click(screen.getByRole("button", { name: "Save follow-me" }));
    expect(
      await screen.findByText("A target cannot be the extension itself."),
    ).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("Target 1 type"), {
      target: { value: "external" },
    });
    fireEvent.change(screen.getByLabelText("Target 1"), {
      target: { value: "0475 12 34" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save follow-me" }));
    expect(
      await screen.findByText(/2 to 32 digits, optionally starting with \+\./),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("limits the profile to five targets and removes one", async () => {
    api({
      "GET /api/v1/me/extensions/10/follow-me": () =>
        json({
          ...PROFILE,
          targets: [1, 2, 3, 4, 5].map((n) => ({
            kind: "extension",
            target: `20${n}`,
            delaySeconds: 0,
          })),
        }),
    });
    renderApp("/me/forwarding");
    const add = await screen.findByRole("button", { name: "Add target" });
    expect(add).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: "Remove target 5" }));
    expect(screen.getByRole("button", { name: "Add target" })).toBeEnabled();
    expect(screen.queryByLabelText("Target 5")).toBeNull();
  });

  it("shows a server error next to the target it names", async () => {
    api({
      "PUT /api/v1/me/extensions/10/follow-me": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid configuration",
              fields: [
                {
                  path: "targets[0].target",
                  message: "extension 999 does not exist",
                },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/me/forwarding");
    await addTarget("extension", "999");
    fireEvent.click(screen.getByRole("button", { name: "Save follow-me" }));
    expect(
      await screen.findByText("extension 999 does not exist"),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Target 1")).toHaveAttribute(
      "aria-invalid",
      "true",
    );
  });

  it("says when the profile cannot be loaded", async () => {
    api({
      "GET /api/v1/me/extensions/10/follow-me": () =>
        json({ error: { code: "internal", message: "db down" } }, 500),
    });
    renderApp("/me/forwarding");
    expect(
      await screen.findByText(/Could not load follow-me: .*db down/),
    ).toBeInTheDocument();
  });
});

describe("validateFollowMe", () => {
  it("applies the API's rules", () => {
    const base = { ...PROFILE };
    expect(validateFollowMe(base, "101")).toEqual({});
    expect(
      validateFollowMe({ ...base, timeoutSeconds: 4 }, "101").timeoutSeconds,
    ).toBeDefined();
    expect(
      validateFollowMe({ ...base, timeoutSeconds: 121 }, "101").timeoutSeconds,
    ).toBeDefined();
    const withTarget = (kind: "external" | "ring_group", target: string) =>
      validateFollowMe(
        { ...base, targets: [{ kind, target, delaySeconds: 0 }] },
        "101",
      );
    expect(
      withTarget("external", "+3247512")["targets.0.target"],
    ).toBeUndefined();
    expect(withTarget("external", "*72")["targets.0.target"]).toBeDefined();
    expect(withTarget("external", "101")["targets.0.target"]).toBeDefined();
    // A ring group may be named like the extension.
    expect(withTarget("ring_group", "101")["targets.0.target"]).toBeUndefined();
    expect(
      validateFollowMe(
        {
          ...base,
          targets: [{ kind: "external", target: "0475", delaySeconds: 61 }],
        },
        "101",
      )["targets.0.delaySeconds"],
    ).toBeDefined();
  });
});
