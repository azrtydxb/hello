import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiError, json, ME, mockApi, noContent, renderApp } from "../test/api";

const SHA = "a".repeat(64);

const FW_OLD = {
  id: 1,
  vendor: "yealink",
  modelGlob: "T5*",
  version: "96.86.0.1",
  filename: "T54W-96.86.0.1.rom",
  size: 30 * 1024 * 1024,
  sha256: SHA,
  uploadedAt: "2026-10-01T10:00:00Z",
  pinned: true,
};

const FW_NEW = {
  ...FW_OLD,
  id: 2,
  version: "96.86.0.2",
  filename: "T54W-96.86.0.2.rom",
  sha256: "b".repeat(64),
  pinned: false,
};

const FW_SNOM = {
  ...FW_OLD,
  id: 3,
  vendor: "snom",
  modelGlob: "D7*",
  version: "10.1.0",
  filename: "snomD7.bin",
  sha256: "c".repeat(64),
  pinned: true,
};

/** A minimal XMLHttpRequest the test drives by hand. */
class FakeXhr {
  static last: FakeXhr | null = null;
  method = "";
  url = "";
  body: unknown = null;
  status = 0;
  responseText = "";
  withCredentials = false;
  upload: {
    onprogress: ((e: Partial<ProgressEvent>) => void) | null;
  } = { onprogress: null };
  onload: (() => void) | null = null;
  onerror: (() => void) | null = null;
  onabort: (() => void) | null = null;
  constructor() {
    FakeXhr.last = this;
  }
  open(method: string, url: string) {
    this.method = method;
    this.url = url;
  }
  setRequestHeader() {}
  send(body: unknown) {
    this.body = body;
  }
  abort() {
    this.onabort?.();
  }
  progress(loaded: number, total: number) {
    this.upload.onprogress?.({ lengthComputable: true, loaded, total });
  }
  respond(status: number, body: unknown) {
    this.status = status;
    this.responseText = JSON.stringify(body);
    this.onload?.();
  }
}

afterEach(() => {
  FakeXhr.last = null;
  vi.unstubAllGlobals();
});

function chooseFile(file: File) {
  const input = screen.getByLabelText("Firmware file") as HTMLInputElement;
  Object.defineProperty(input, "files", { value: [file], configurable: true });
  fireEvent.change(input);
}

describe("ProvFirmware", () => {
  it("uploads with progress and lists the new file", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/prov/firmware": () => json({ items: [FW_OLD] }),
    });
    vi.stubGlobal("XMLHttpRequest", FakeXhr);
    renderApp("/phones/firmware");
    await screen.findByText("T54W-96.86.0.1.rom");

    fireEvent.change(screen.getByLabelText("Model glob"), {
      target: { value: "T5*" },
    });
    fireEvent.change(screen.getByLabelText("Version"), {
      target: { value: "96.86.0.2" },
    });
    chooseFile(new File(["firmware"], "T54W-96.86.0.2.rom"));
    fireEvent.click(screen.getByRole("button", { name: "Upload" }));

    const progress = await screen.findByLabelText("Upload progress");
    const xhr = FakeXhr.last;
    if (!xhr) throw new Error("no upload started");
    expect(xhr.method).toBe("POST");
    expect(xhr.url).toBe("/api/v1/prov/firmware");
    const form = xhr.body as FormData;
    expect(form.get("vendor")).toBe("yealink");
    expect(form.get("modelGlob")).toBe("T5*");
    expect(form.get("version")).toBe("96.86.0.2");
    expect(form.get("file")).toBeInstanceOf(File);

    xhr.progress(50, 100);
    await waitFor(() =>
      expect((progress as HTMLProgressElement).value).toBe(0.5),
    );
    xhr.respond(201, FW_NEW);

    expect(await screen.findByText("T54W-96.86.0.2.rom")).toBeInTheDocument();
    expect(screen.queryByLabelText("Upload progress")).toBeNull();
    // The hash is shortened, with the full value in the title.
    expect(screen.getByTitle("b".repeat(64))).toBeInTheDocument();
  });

  it("refuses a file over the limit without uploading", async () => {
    mockApi({ ...ME, "GET /api/v1/prov/firmware": () => json({ items: [] }) });
    vi.stubGlobal("XMLHttpRequest", FakeXhr);
    renderApp("/phones/firmware");
    await screen.findByText("No firmware yet");
    fireEvent.change(screen.getByLabelText("Version"), {
      target: { value: "1" },
    });
    const big = new File(["x"], "huge.rom");
    Object.defineProperty(big, "size", { value: 600 * 1024 * 1024 });
    chooseFile(big);
    fireEvent.click(screen.getByRole("button", { name: "Upload" }));
    expect(await screen.findByText(/the limit is 512 MB/)).toBeInTheDocument();
    expect(FakeXhr.last).toBeNull();
  });

  it("pinning sends the full set and unpins the same vendor and glob", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/prov/firmware": () =>
        json({ items: [FW_OLD, FW_NEW, FW_SNOM] }),
      "PUT /api/v1/prov/firmware/pins": () => noContent(),
    });
    renderApp("/phones/firmware");
    const pinNew = await screen.findByRole("switch", {
      name: "Pin 96.86.0.2 for Yealink T5*",
    });
    fireEvent.click(pinNew);

    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toBeDefined(),
    );
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.url).toBe("/api/v1/prov/firmware/pins");
    expect(put?.body).toEqual([
      { vendor: "snom", modelGlob: "D7*", firmwareId: 3 },
      { vendor: "yealink", modelGlob: "T5*", firmwareId: 2 },
    ]);
    await waitFor(() => expect(pinNew).toBeChecked());
    expect(
      screen.getByRole("switch", { name: "Pin 96.86.0.1 for Yealink T5*" }),
    ).not.toBeChecked();
    expect(
      screen.getByRole("switch", { name: "Pin 10.1.0 for Snom D7*" }),
    ).toBeChecked();
  });

  it("shows the server's message when a pinned file cannot be deleted", async () => {
    mockApi({
      ...ME,
      "GET /api/v1/prov/firmware": () => json({ items: [FW_OLD] }),
      "DELETE /api/v1/prov/firmware/1": () =>
        apiError(409, "conflict", "firmware is pinned; unpin it first"),
    });
    renderApp("/phones/firmware");
    fireEvent.click(
      await screen.findByRole("button", { name: "Delete T54W-96.86.0.1.rom" }),
    );
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Delete firmware" }),
    );
    expect(
      await within(dialog).findByText("firmware is pinned; unpin it first"),
    ).toBeInTheDocument();
    expect(screen.getByText("T54W-96.86.0.1.rom")).toBeInTheDocument();
  });

  it("TestFirmwareMinimum: refuses a pin below the minimum without asking the server", async () => {
    const calls = mockApi({
      ...ME,
      "GET /api/v1/prov/firmware": () =>
        json({ items: [{ ...FW_NEW, version: "96.70.0.1", pinned: false }] }),
      "GET /api/v1/prov/firmware/minimums": () =>
        json({
          items: [
            {
              vendor: "yealink",
              modelGlob: "T5*",
              minVersion: "96.80.0.1",
              recommended: "96.86.0.70",
            },
          ],
        }),
      "PUT /api/v1/prov/firmware/pins": () => noContent(),
    });
    renderApp("/phones/firmware");
    await screen.findByLabelText("Yealink T5* minimum");
    fireEvent.click(
      await screen.findByRole("switch", {
        name: "Pin 96.70.0.1 for Yealink T5*",
      }),
    );
    expect(
      await screen.findByText(/below the minimum 96\.80\.0\.1/),
    ).toBeInTheDocument();
    expect(calls.some((c) => c.method === "PUT")).toBe(false);
  });

  it("saves edited minimums", async () => {
    const rule = {
      vendor: "grandstream",
      modelGlob: "WP822*",
      minVersion: "1.0.11.22",
      recommended: "1.0.11.78",
    };
    const calls = mockApi({
      ...ME,
      "GET /api/v1/prov/firmware": () => json({ items: [] }),
      "GET /api/v1/prov/firmware/minimums": () => json({ items: [rule] }),
      "PUT /api/v1/prov/firmware/minimums": () => noContent(),
    });
    renderApp("/phones/firmware");
    fireEvent.change(
      await screen.findByLabelText("Grandstream WP822* minimum"),
      { target: { value: "1.0.11.30" } },
    );
    fireEvent.click(screen.getByRole("button", { name: "Save minimums" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual([
        { ...rule, minVersion: "1.0.11.30" },
      ]),
    );
  });

  it("sets the upgrade hour of a pinned Grandstream firmware", async () => {
    const gs = {
      ...FW_OLD,
      id: 9,
      vendor: "grandstream",
      modelGlob: "WP8?6*",
      version: "1.0.3.39",
      filename: "wp8x6fw.bin",
      pinned: true,
    };
    const calls = mockApi({
      ...ME,
      "GET /api/v1/prov/firmware": () => json({ items: [gs] }),
      "GET /api/v1/prov/firmware/minimums": () => json({ items: [] }),
      "PUT /api/v1/prov/firmware/pins": () => noContent(),
    });
    renderApp("/phones/firmware");
    fireEvent.change(
      await screen.findByLabelText("Upgrade hour for 1.0.3.39"),
      {
        target: { value: "3" },
      },
    );
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual([
        {
          vendor: "grandstream",
          modelGlob: "WP8?6*",
          firmwareId: 9,
          upgradeHour: 3,
        },
      ]),
    );
  });

  it("shows the drift badge on Phones only when the firmware drifts", async () => {
    const base = {
      id: 5,
      mac: "805ec0123456",
      serial: "",
      vendor: "yealink",
      model: "T54W",
      label: "",
      deviceId: null,
      extensionId: null,
      extensionNumber: "",
      templateId: null,
      blf: [],
      enabled: true,
      tokenExposed: false,
      uaMismatch: false,
      bootArmed: false,
      bootReclaimed: false,
      redirectStatus: { state: "manual" },
      firstFetchAt: null,
      lastFetchAt: null,
      lastFetchIp: null,
      lastFetchUa: null,
      lastFetchFile: null,
      renderError: false,
      createdAt: "2026-10-01T10:00:00Z",
      updatedAt: "2026-10-01T10:00:00Z",
      firmwarePinned: "96.86.0.70",
      firmwareMinimum: "",
    };
    mockApi({
      ...ME,
      "GET /api/v1/phones": () =>
        json({
          items: [
            { ...base, firmwareSeen: "96.85.0.1", firmwareDrift: true },
            {
              ...base,
              id: 6,
              mac: "805ec0123457",
              firmwareSeen: "96.86.0.70",
              firmwareDrift: false,
            },
          ],
        }),
      "GET /api/v1/extensions": () => json({ items: [] }),
      "GET /api/v1/devices": () => json({ items: [] }),
      "GET /api/v1/prov/templates": () => json({ items: [] }),
    });
    renderApp("/phones");
    await screen.findByText("96.85.0.1");
    expect(screen.getAllByText("Drift")).toHaveLength(1);
  });
});
