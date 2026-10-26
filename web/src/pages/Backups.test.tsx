import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  apiError,
  json,
  mockApi,
  noContent,
  renderApp,
  type Routes,
} from "../test/api";

const SETTINGS = {
  enabled: true,
  cron: "0 2 * * *",
  kind: "minio",
  bucket: "hello-backups",
  s3Endpoint: "",
  s3Bucket: "",
  s3Region: "",
  s3PathStyle: false,
  s3KeysSet: false,
  keepCount: 14,
  notifyEmail: "",
  updatedAt: "2026-10-26T00:00:00Z",
};

const RUN = {
  id: 7,
  kind: "scheduled",
  status: "succeeded",
  attempt: 1,
  startedAt: "2026-10-26T02:00:00Z",
  finishedAt: "2026-10-26T02:03:00Z",
  dbBytes: 5_000_000,
  objectCount: 12,
  objectBytes: 9_000_000,
  archiveBytes: 5_100_000,
  objectKey: "archives/hello-backup-20261026T020000Z.hbk",
  errorClass: "",
};

const REPORT = {
  dryRun: true,
  helloVersion: "v1",
  schemaVersion: 45,
  createdAt: "2026-10-26T02:00:00Z",
  dbBytes: 5_000_000,
  objects: 12,
  objectBytes: 9_000_000,
  objectsToRestore: 3,
  objectsMissingInBackup: 0,
};

function api(role: string, more: Routes = {}, runs: unknown[] = [RUN]) {
  return mockApi({
    "GET /api/v1/auth/me": () => json({ username: "pat", role }),
    "GET /api/v1/backups/settings": () => json(SETTINGS),
    "GET /api/v1/backups": () => json({ items: runs }),
    ...more,
  });
}

describe("Backups", () => {
  it("shows the schedule and the archives", async () => {
    api("admin");
    renderApp("/settings/backups");
    expect(await screen.findByLabelText("Schedule (cron)")).toHaveValue(
      "0 2 * * *",
    );
    expect(screen.getByLabelText("Archives to keep")).toHaveValue("14");
    expect(screen.getByText("succeeded")).toBeInTheDocument();
    expect(screen.getByText("12")).toBeInTheDocument();
    expect(screen.queryByLabelText("S3 endpoint")).not.toBeInTheDocument();
  });

  it("is for administrators only", async () => {
    api("operator");
    renderApp("/settings/backups");
    expect(await screen.findByText("Administrators only")).toBeInTheDocument();
  });

  it("saves settings and keeps stored S3 credentials unless new ones are typed", async () => {
    const calls = api("admin", {
      "PUT /api/v1/backups/settings": () =>
        json({ ...SETTINGS, kind: "s3", s3KeysSet: true }),
    });
    renderApp("/settings/backups");
    await screen.findByLabelText("Schedule (cron)");
    fireEvent.change(screen.getByLabelText("Destination"), {
      target: { value: "s3" },
    });
    fireEvent.change(screen.getByLabelText("S3 endpoint"), {
      target: { value: "https://s3.example.com" },
    });
    fireEvent.change(screen.getByLabelText("S3 bucket"), {
      target: { value: "mine" },
    });
    fireEvent.change(screen.getByLabelText("Access key"), {
      target: { value: "AK" },
    });
    fireEvent.change(screen.getByLabelText("Secret key"), {
      target: { value: "SK" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    expect(calls.find((c) => c.method === "PUT")?.body).toMatchObject({
      kind: "s3",
      s3Endpoint: "https://s3.example.com",
      s3Bucket: "mine",
      s3AccessKey: "AK",
      s3SecretKey: "SK",
      keepCount: 14,
    });
    // The stored credentials are never shown and are kept by default.
    await screen.findByText(/stored and never shown/);
    expect(screen.getByLabelText("Secret key")).toHaveValue("");
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls.filter((c) => c.method === "PUT")).toHaveLength(2),
    );
    const second = calls.filter((c) => c.method === "PUT")[1]?.body as Record<
      string,
      unknown
    >;
    expect(second).not.toHaveProperty("s3AccessKey");
    expect(second).not.toHaveProperty("s3SecretKey");
  });

  it("shows a field error from the server", async () => {
    api("admin", {
      "PUT /api/v1/backups/settings": () =>
        new Response(
          JSON.stringify({
            error: {
              code: "bad_request",
              message: "invalid configuration: cron: bad",
              fields: [{ path: "cron", message: "cron must have five fields" }],
            },
          }),
          { status: 400, headers: { "Content-Type": "application/json" } },
        ),
    });
    renderApp("/settings/backups");
    fireEvent.change(await screen.findByLabelText("Schedule (cron)"), {
      target: { value: "x" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    expect(
      await screen.findByText("cron must have five fields"),
    ).toBeInTheDocument();
  });

  it("starts a backup now", async () => {
    const calls = api("admin", {
      "POST /api/v1/backups": () =>
        json({ ...RUN, id: 8, status: "running", finishedAt: null }, 202),
    });
    renderApp("/settings/backups");
    fireEvent.click(await screen.findByRole("button", { name: "Back up now" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "POST")).toBe(true),
    );
  });

  it("verifies a backup with counts only and writes nothing", async () => {
    const calls = api("admin", {
      "POST /api/v1/backups/7/restore?dry_run=true": () => json(REPORT),
    });
    renderApp("/settings/backups");
    fireEvent.click(
      await screen.findByRole("button", { name: "Verify backup 7" }),
    );
    expect(await screen.findByText(/12 objects/)).toBeInTheDocument();
    expect(screen.getByText(/3 objects would be restored/)).toBeInTheDocument();
    expect(calls.filter((c) => c.method === "POST").map((c) => c.url)).toEqual([
      "/api/v1/backups/7/restore?dry_run=true",
    ]);
  });

  it("restores only after the confirmation text is typed", async () => {
    const calls = api("admin", {
      "POST /api/v1/backups/7/restore": () =>
        json({ ...REPORT, dryRun: false }),
    });
    renderApp("/settings/backups");
    fireEvent.click(
      await screen.findByRole("button", { name: "Restore backup 7" }),
    );
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore" }));
    expect(
      await within(dialog).findByText(/Type RESTORE 7/),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === "POST")).toBe(false);
    fireEvent.change(
      within(dialog).getByLabelText("Type RESTORE 7 to confirm"),
      {
        target: { value: "RESTORE 7" },
      },
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "POST")?.body).toEqual({
        confirm: "RESTORE 7",
      }),
    );
  });

  it("shows why a restore was refused", async () => {
    api("admin", {
      "POST /api/v1/backups/7/restore": () =>
        apiError(409, "schema_newer", "the backup is from a newer version"),
    });
    renderApp("/settings/backups");
    fireEvent.click(
      await screen.findByRole("button", { name: "Restore backup 7" }),
    );
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(
      within(dialog).getByLabelText("Type RESTORE 7 to confirm"),
      {
        target: { value: "RESTORE 7" },
      },
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Restore" }));
    expect(
      await within(dialog).findByText("the backup is from a newer version"),
    ).toBeInTheDocument();
  });

  it("deletes a backup after confirming", async () => {
    const calls = api("admin", {
      "DELETE /api/v1/backups/7": () => noContent(),
    });
    renderApp("/settings/backups");
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete backup 7" }),
    );
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Delete backup" }),
    );
    await waitFor(() =>
      expect(calls.some((c) => c.method === "DELETE")).toBe(true),
    );
  });

  it("offers no restore for a failed run", async () => {
    api("admin", {}, [{ ...RUN, status: "failed", errorClass: "db_dump" }]);
    renderApp("/settings/backups");
    expect(await screen.findByText("failed: db_dump")).toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: "Restore backup 7" }),
    ).not.toBeInTheDocument();
  });
});
