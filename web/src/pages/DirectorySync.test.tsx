import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { apiError, json, mockApi, noContent, renderApp } from "../test/api";
import { countsText, diffText, sourceText } from "./DirectorySync";
import type { Change, Connector, Run } from "../api/dirsync";

const AT = "2026-11-10T10:00:00Z";

function connector(over: Partial<Connector> = {}): Connector {
  return {
    id: 1,
    kind: "ldap",
    name: "Corp AD",
    enabled: true,
    mode: "authoritative",
    scheduleMinutes: 60,
    config: {
      url: "ldaps://dc.corp.test:636",
      bindDn: "CN=svc,DC=corp",
      baseDn: "OU=People,DC=corp",
      userFilter: "(&(objectCategory=person)(objectClass=user))",
    },
    mappings: {
      username: { sources: ["userPrincipalName"], transforms: [] },
      email: { sources: ["mail"], transforms: [] },
      displayName: { sources: ["displayName"], transforms: [] },
      extension: {
        sources: ["ipPhone"],
        transforms: [{ kind: "digits" }, { kind: "last", n: 4 }],
      },
      active: { attribute: "userAccountControl", mode: "uac" },
    },
    groupRules: { roles: [], ringGroups: [] },
    options: {
      absenceRuns: 2,
      disableDevices: null,
      allowAdminFromDirectory: false,
      runTimeoutMinutes: 0,
      userCap: 0,
    },
    hasCredential: true,
    status: "ok",
    lastRunAt: AT,
    lastSuccessAt: AT,
    lastErrorCode: "",
    lastErrorDetail: "",
    consecutiveFailures: 0,
    linkedUsers: 12,
    createdAt: AT,
    updatedAt: AT,
    ...over,
  };
}

function run(over: Partial<Run> = {}): Run {
  return {
    id: 5,
    connectorId: 1,
    kind: "preview",
    status: "preview",
    requestedBy: "user:admin",
    createdAt: AT,
    startedAt: AT,
    finishedAt: AT,
    replica: "n1",
    counts: { create: 2, adopt: 1, conflict: 1, users: 4 },
    diffHash: "abc",
    errorCode: "",
    errorDetail: "",
    ...over,
  };
}

function change(id: number, over: Partial<Change> = {}): Change {
  return {
    id,
    runId: 5,
    action: "create",
    userId: null,
    externalId: `g${String(id)}`,
    username: `user${String(id)}@corp`,
    fieldDiffs: { displayName: { before: "", after: `User ${String(id)}` } },
    reason: "",
    overridable: false,
    createdAt: AT,
    ...over,
  };
}

function me(role: string) {
  return { "GET /api/v1/auth/me": () => json({ username: "pat", role }) };
}

function setup(
  extra: Parameters<typeof mockApi>[0] = {},
  items = [connector()],
) {
  return mockApi({
    ...me("admin"),
    "GET /api/v1/directory/connectors": () => json({ items }),
    ...extra,
  });
}

describe("Directory (spec integrations P8)", () => {
  it("describes a connector's source and a run's counts", () => {
    expect(sourceText(connector())).toBe("dc.corp.test:636");
    expect(countsText(run())).toBe("2 create, 1 adopt, 1 conflict");
    expect(countsText(run({ counts: { users: 3 } }))).toBe("no changes");
    expect(
      diffText(
        change(1, { fieldDiffs: { email: { before: "a@x", after: "b@x" } } }),
      ),
    ).toBe("email: a@x → b@x");
  });

  it("lists the connectors with their state, users and schedule", async () => {
    setup({}, [
      connector(),
      connector({
        id: 2,
        name: "Lab",
        config: { url: "ldaps://lab.corp.test", bindDn: "x", baseDn: "y" },
        enabled: false,
        status: "failing",
        lastErrorCode: "auth_failed",
        scheduleMinutes: 0,
        lastSuccessAt: null,
        linkedUsers: 0,
      }),
    ]);
    renderApp("/integrations/directory");
    const table = await screen.findByRole("table", {
      name: "Directory connectors",
    });
    expect(within(table).getByText("dc.corp.test:636")).toBeVisible();
    expect(within(table).getByText("on")).toBeVisible();
    expect(within(table).getByText("failing: auth_failed")).toBeVisible();
    expect(within(table).getByText("every 60 min")).toBeVisible();
    expect(within(table).getByText("manual")).toBeVisible();
    expect(within(table).getByText("never")).toBeVisible();
  });

  it("is for administrators only, in the sidebar and on the page", async () => {
    setup({ ...me("operator") });
    renderApp("/integrations/directory");
    expect(await screen.findByText("Administrators only")).toBeVisible();
    const nav = screen.getByRole("navigation", { name: "Primary" });
    expect(within(nav).queryByRole("link", { name: /^Directory/ })).toBeNull();
  });

  it("creates a connector with the bind password and the mapping as typed", async () => {
    const calls = setup({
      "POST /api/v1/directory/connectors": () =>
        json(connector({ id: 3, name: "New AD" }), 201),
    });
    renderApp("/integrations/directory");
    fireEvent.click(
      await screen.findByRole("button", { name: /New connector/ }),
    );
    const form = await screen.findByRole("form", {
      name: "New directory connector",
    });
    fireEvent.change(within(form).getByLabelText("Name"), {
      target: { value: "New AD" },
    });
    fireEvent.change(within(form).getByLabelText("Server URL"), {
      target: { value: "ldaps://dc2.corp.test" },
    });
    fireEvent.change(within(form).getByLabelText("Bind DN"), {
      target: { value: "CN=svc,DC=corp" },
    });
    fireEvent.change(within(form).getByLabelText("Bind password"), {
      target: { value: "s3cret" },
    });
    fireEvent.change(within(form).getByLabelText("Base DN"), {
      target: { value: "DC=corp" },
    });
    fireEvent.change(within(form).getByLabelText("Role rules"), {
      target: { value: "g-ops operator" },
    });
    fireEvent.submit(form);
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" && c.url === "/api/v1/directory/connectors",
        ),
      ).toBe(true),
    );
    const post = calls.find((c) => c.method === "POST");
    const body = post?.body as Record<string, unknown>;
    expect(body.kind).toBe("ldap");
    expect(body.bindPassword).toBe("s3cret");
    expect(body.enabled).toBe(false);
    expect(body.groupRules).toEqual({
      roles: [{ groupId: "g-ops", role: "operator" }],
      ringGroups: [],
    });
    expect((body.mappings as { extension: unknown }).extension).toEqual({
      sources: ["ipPhone", "telephoneNumber"],
      transforms: [{ kind: "digits" }],
    });
    expect((body.config as { url: string }).url).toBe("ldaps://dc2.corp.test");
  });

  it("checks the form before posting", async () => {
    const calls = setup();
    renderApp("/integrations/directory");
    fireEvent.click(
      await screen.findByRole("button", { name: /New connector/ }),
    );
    const form = await screen.findByRole("form", {
      name: "New directory connector",
    });
    fireEvent.change(within(form).getByLabelText("Role rules"), {
      target: { value: "only-one-word" },
    });
    fireEvent.change(within(form).getByLabelText("Schedule (minutes)"), {
      target: { value: "3" },
    });
    fireEvent.submit(form);
    expect(
      await within(form).findByText(
        /One rule per line: <group id> <user\|viewer/,
      ),
    ).toBeVisible();
    expect(
      within(form).getByText(/0 \(manual only\) or 5 to 10080/),
    ).toBeVisible();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
  });

  it("never sends the stored password back and keeps it when left empty", async () => {
    const calls = setup({
      "PATCH /api/v1/directory/connectors/1": () =>
        json(connector({ name: "Renamed" })),
    });
    renderApp("/integrations/directory");
    fireEvent.click(
      await screen.findByRole("button", { name: "Edit Corp AD" }),
    );
    const form = await screen.findByRole("form", { name: "Edit Corp AD" });
    expect(within(form).getByLabelText("Bind password")).toHaveValue("");
    fireEvent.change(within(form).getByLabelText("Name"), {
      target: { value: "Renamed" },
    });
    fireEvent.submit(form);
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PATCH")).toBe(true),
    );
    const patch = calls.find((c) => c.method === "PATCH");
    expect(patch?.body).not.toHaveProperty("bindPassword");
    expect((patch?.body as { name: string }).name).toBe("Renamed");
  });

  it("shows a server field error next to the field, such as the password demanded again", async () => {
    setup({
      "PATCH /api/v1/directory/connectors/1": () =>
        json(
          {
            error: {
              code: "bad_request",
              message: "invalid configuration",
              fields: [
                {
                  path: "bindPassword",
                  message:
                    "enter the bind password again: the host or bind DN changed",
                },
              ],
            },
          },
          400,
        ),
    });
    renderApp("/integrations/directory");
    fireEvent.click(
      await screen.findByRole("button", { name: "Edit Corp AD" }),
    );
    const form = await screen.findByRole("form", { name: "Edit Corp AD" });
    fireEvent.change(within(form).getByLabelText("Server URL"), {
      target: { value: "ldaps://elsewhere.test" },
    });
    fireEvent.submit(form);
    expect(
      await within(form).findByText(/enter the bind password again/),
    ).toBeVisible();
  });

  it("tests the connection and shows the certificate, the sample and the groups", async () => {
    setup({
      "POST /api/v1/directory/connectors/1/test": () =>
        json({
          ok: true,
          errorCode: "",
          errorDetail: "",
          serverCertSubject: "CN=dc.corp.test",
          serverCertExpires: "2027-01-01T00:00:00Z",
          matchedUsers: 42,
          sample: [
            {
              externalId: "g1",
              raw: { mail: ["ann@corp.org"] },
              mapped: {
                username: "ann@corp",
                email: "ann@corp.org",
                displayName: "Ann",
                extension: "2001",
                active: true,
              },
            },
            {
              externalId: "g2",
              raw: {},
              mapped: {
                username: "bob@corp",
                email: "",
                displayName: "Bob",
                extension: "",
                extensionError: "extension_invalid",
                active: false,
              },
            },
          ],
          groups: [{ id: "00000001-0002", name: "sales" }],
        }),
    });
    renderApp("/integrations/directory");
    fireEvent.click(
      await screen.findByRole("button", {
        name: /Test the connection of Corp AD/,
      }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: /Test of Corp AD/,
    });
    expect(within(dialog).getByText(/matched 42 users/)).toBeVisible();
    expect(within(dialog).getByText(/CN=dc.corp.test/)).toBeVisible();
    expect(within(dialog).getByText("ann@corp")).toBeVisible();
    expect(within(dialog).getByText("extension_invalid")).toBeVisible();
    expect(within(dialog).getByText("00000001-0002")).toBeVisible();
  });

  it("shows a failed test with its classified code", async () => {
    setup({
      "POST /api/v1/directory/connectors/1/test": () =>
        json({
          ok: false,
          errorCode: "tls_untrusted",
          errorDetail: "the server certificate is not trusted",
          serverCertSubject: "",
          serverCertExpires: null,
          matchedUsers: 0,
          sample: [],
          groups: [],
        }),
    });
    renderApp("/integrations/directory");
    fireEvent.click(
      await screen.findByRole("button", {
        name: /Test the connection of Corp AD/,
      }),
    );
    expect(
      await screen.findByText(/The test failed: tls_untrusted/),
    ).toBeVisible();
  });

  it("queues a preview and lists the runs of the selected connector", async () => {
    const calls = setup({
      "POST /api/v1/directory/connectors/1/sync": () =>
        json(run({ status: "queued" }), 202),
      "GET /api/v1/directory/connectors/1/runs": () =>
        json({
          items: [
            run(),
            run({
              id: 4,
              status: "needs_confirmation",
              kind: "scheduled",
              errorCode: "needs_confirmation",
              counts: { disable: 9 },
            }),
          ],
        }),
    });
    renderApp("/integrations/directory");
    fireEvent.click(
      await screen.findByRole("button", { name: /Preview a sync of Corp AD/ }),
    );
    await waitFor(() => {
      const post = calls.find(
        (c) => c.method === "POST" && c.url.endsWith("/sync"),
      );
      expect(post?.body).toEqual({ dryRun: true });
    });
    const table = await screen.findByRole("table", { name: "Runs of Corp AD" });
    expect(
      within(table).getByText("2 create, 1 adopt, 1 conflict"),
    ).toBeVisible();
    expect(
      await screen.findByText(/waiting for your confirmation/),
    ).toBeVisible();
  });

  it("applies a preview with the adoptions and overrides the administrator ticked", async () => {
    const calls = setup({
      "GET /api/v1/directory/connectors/1/runs": () => json({ items: [run()] }),
      "GET /api/v1/directory/runs/5": () => json(run()),
      "GET /api/v1/directory/runs/5/changes?limit=500": () =>
        json({
          items: [
            change(1),
            change(2, {
              action: "adopt",
              reason: "adopt_not_confirmed",
              username: "old",
            }),
            change(3, {
              action: "conflict",
              reason: "extension_linked",
              overridable: true,
              username: "carl",
            }),
            change(4, {
              action: "conflict",
              reason: "email_in_use",
              username: "dup",
            }),
          ],
          nextAfter: null,
        }),
      "POST /api/v1/directory/runs/5/apply": () =>
        json(run({ kind: "apply", status: "succeeded" })),
    });
    renderApp("/integrations/directory");
    fireEvent.click(
      await screen.findByRole("button", { name: "Show runs of Corp AD" }),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Open run 5" }));
    const drawer = await screen.findByRole("dialog", {
      name: /Run #5 of Corp AD/,
    });
    expect(await within(drawer).findByText("user1@corp")).toBeVisible();
    // Only adoptions and extension overrides offer a decision, plus the
    // adopt-all box: the plain conflict (email_in_use) offers none.
    expect(
      within(drawer).getAllByRole("checkbox", { name: /^(Adopt|Override) / }),
    ).toHaveLength(3);
    expect(
      within(drawer).queryByRole("checkbox", { name: /Override dup/ }),
    ).toBeNull();
    fireEvent.click(
      within(drawer).getByRole("checkbox", { name: "Adopt old" }),
    );
    fireEvent.click(
      within(drawer).getByRole("checkbox", { name: "Override carl" }),
    );
    fireEvent.click(
      within(drawer).getByRole("button", { name: "Apply this preview" }),
    );
    await waitFor(() => {
      const post = calls.find(
        (c) => c.url === "/api/v1/directory/runs/5/apply",
      );
      expect(post?.body).toEqual({
        adoptAll: false,
        adopt: ["g2"],
        override: ["g3"],
      });
    });
  });

  it("tells the administrator when the preview went stale", async () => {
    setup({
      "GET /api/v1/directory/connectors/1/runs": () => json({ items: [run()] }),
      "GET /api/v1/directory/runs/5": () => json(run()),
      "GET /api/v1/directory/runs/5/changes?limit=500": () =>
        json({ items: [change(1)], nextAfter: null }),
      "POST /api/v1/directory/runs/5/apply": () =>
        apiError(409, "stale_preview", "the preview is out of date"),
    });
    renderApp("/integrations/directory");
    fireEvent.click(
      await screen.findByRole("button", { name: "Show runs of Corp AD" }),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Open run 5" }));
    const drawer = await screen.findByRole("dialog", { name: /Run #5/ });
    fireEvent.click(
      await within(drawer).findByRole("button", { name: "Apply this preview" }),
    );
    expect(
      await within(drawer).findByText(/A fresh preview was made/),
    ).toBeVisible();
  });

  it("confirms a halted run after a confirmation dialog", async () => {
    const halted = run({
      status: "needs_confirmation",
      errorCode: "needs_confirmation",
      errorDetail: "a run would disable 9 of 12 linked users",
      counts: { disable: 9 },
    });
    const calls = setup({
      "GET /api/v1/directory/connectors/1/runs": () =>
        json({ items: [halted] }),
      "GET /api/v1/directory/runs/5": () => json(halted),
      "GET /api/v1/directory/runs/5/changes?limit=500": () =>
        json({
          items: [
            change(1, { action: "disable", reason: "absent_from_source" }),
          ],
          nextAfter: null,
        }),
      "POST /api/v1/directory/runs/5/confirm": () =>
        json(run({ status: "succeeded", kind: "apply" })),
    });
    renderApp("/integrations/directory");
    fireEvent.click(
      await screen.findByRole("button", { name: "Show runs of Corp AD" }),
    );
    fireEvent.click(await screen.findByRole("button", { name: "Open run 5" }));
    const drawer = await screen.findByRole("dialog", { name: /Run #5/ });
    expect(
      await within(drawer).findByText(/Halted: nothing was applied/),
    ).toBeVisible();
    fireEvent.click(
      within(drawer).getByRole("button", { name: "Confirm and apply" }),
    );
    const confirm = await screen.findByRole("dialog", {
      name: /Apply the halted run\?/,
    });
    fireEvent.click(
      within(confirm).getByRole("button", { name: "Confirm and apply" }),
    );
    await waitFor(() =>
      expect(
        calls.some(
          (c) =>
            c.method === "POST" && c.url === "/api/v1/directory/runs/5/confirm",
        ),
      ).toBe(true),
    );
  });

  it("deletes a connector, releasing its users when asked", async () => {
    const calls = setup({
      "DELETE /api/v1/directory/connectors/1?release=true": () => noContent(),
    });
    renderApp("/integrations/directory");
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete Corp AD" }),
    );
    const dialog = await screen.findByRole("dialog", {
      name: /Delete Corp AD\?/,
    });
    expect(within(dialog).getByText(/12 users are managed/)).toBeVisible();
    fireEvent.click(
      within(dialog).getByRole("checkbox", { name: /Release the users/ }),
    );
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Delete connector" }),
    );
    await waitFor(() =>
      expect(
        calls.some(
          (c) => c.method === "DELETE" && c.url.endsWith("?release=true"),
        ),
      ).toBe(true),
    );
  });
});
