import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import {
  apiError,
  json,
  ME,
  mockApi,
  noContent,
  renderApp,
  type Routes,
} from "../test/api";

// Assembled at runtime so the literal appears nowhere in the source.
const SECRET = ["s3cr", "et-value"].join("");

const BOOT = "http://hello.example.test:8088/boot";

const SETTINGS = {
  publicUrl: "https://hello.example.test/prov",
  bootUrl: BOOT,
  caUrl: "http://hello.example.test:8088/ca.crt",
  caSha256: "AB:CD:EF:01",
  dhcp: [
    { vendor: "yealink", option: "66", value: BOOT },
    { vendor: "poly", option: "160", value: `${BOOT}/poly` },
  ],
  sipServer: "192.168.10.101:5060",
};

const account = (vendor: string, extra: Record<string, unknown> = {}) => ({
  vendor,
  enabled: false,
  hasCredentials: false,
  fromDeployment: false,
  settings: { note: vendor },
  lastCheckAt: null,
  lastCheckResult: null,
  supported: true,
  ...extra,
});

const ACCOUNTS = [
  account("yealink", { fromDeployment: true, hasCredentials: true }),
  account("poly", { supported: false }),
  account("grandstream"),
  account("snom"),
  account("fanvil", { supported: false }),
];

function routes(extra: Routes = {}): Routes {
  return {
    ...ME,
    "GET /api/v1/prov/settings": () => json(SETTINGS),
    "GET /api/v1/prov/redirect": () => json({ items: ACCOUNTS }),
    ...extra,
  };
}

const card = (name: string) => screen.findByRole("listitem", { name });

afterEach(() => vi.unstubAllGlobals());

describe("ProvSettings", () => {
  it("never shows a redirect credential after saving it", async () => {
    const saved = account("snom", { enabled: true, hasCredentials: true });
    const calls = mockApi(
      routes({ "PUT /api/v1/prov/redirect/snom": () => json(saved) }),
    );
    renderApp("/phones/settings");
    const snom = await card("Snom");
    expect(within(snom).getByText(/Credentials: not set/)).toBeInTheDocument();
    expect(within(snom).queryByLabelText("SRAPS access key secret")).toBeNull();

    fireEvent.click(within(snom).getByRole("switch", { name: "Enabled" }));
    fireEvent.click(
      within(snom).getByRole("button", { name: "Set credentials" }),
    );
    const secret = within(snom).getByLabelText("SRAPS access key secret");
    expect(secret).toHaveAttribute("type", "password");
    expect(secret).toHaveAttribute("autocomplete", "new-password");
    expect(secret).toHaveValue("");
    fireEvent.change(within(snom).getByLabelText("SRAPS access key ID"), {
      target: { value: "key-id-1" },
    });
    fireEvent.change(secret, { target: { value: SECRET } });
    fireEvent.click(within(snom).getByRole("button", { name: "Save" }));

    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    const put = calls.find((c) => c.method === "PUT");
    expect(put?.url).toBe("/api/v1/prov/redirect/snom");
    expect(put?.body).toEqual({
      enabled: true,
      settings: { note: "snom" },
      credentials: {
        snomSrapsAccessKeyId: "key-id-1",
        snomSrapsAccessKeySecret: SECRET,
      },
    });

    expect(
      await within(snom).findByText(/Credentials: set \(write-only\)/),
    ).toBeInTheDocument();
    const nowhere = () => {
      expect(document.body.textContent).not.toContain(SECRET);
      expect(document.body.innerHTML).not.toContain(SECRET);
      for (const input of document.querySelectorAll("input")) {
        expect(input.value).not.toBe(SECRET);
      }
    };
    nowhere();
    // Opening the fields again shows them empty, never prefilled.
    fireEvent.click(
      within(snom).getByRole("button", { name: "Change credentials" }),
    );
    expect(within(snom).getByLabelText("SRAPS access key secret")).toHaveValue(
      "",
    );
    nowhere();
  });

  it("sends no credentials when only the switch changed", async () => {
    const calls = mockApi(
      routes({
        "PUT /api/v1/prov/redirect/grandstream": () =>
          json(account("grandstream", { enabled: true })),
      }),
    );
    renderApp("/phones/settings");
    const gs = await card("Grandstream");
    // The GDMS site step is shown alongside the credentials.
    expect(
      within(gs).getByText(/set the GDMS site's provisioning server/),
    ).toBeInTheDocument();
    fireEvent.click(within(gs).getByRole("switch", { name: "Enabled" }));
    fireEvent.click(within(gs).getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls.some((c) => c.method === "PUT")).toBe(true),
    );
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      enabled: true,
      settings: { note: "grandstream" },
    });
  });

  it("keeps deployment credentials read-only", async () => {
    mockApi(routes());
    renderApp("/phones/settings");
    const yl = await card("Yealink");
    expect(within(yl).getByText(/Set by the deployment/)).toBeInTheDocument();
    expect(within(yl).queryAllByRole("textbox")).toHaveLength(0);
    expect(yl.querySelectorAll("input:not([type=checkbox])")).toHaveLength(0);
    expect(
      within(yl).queryByRole("button", { name: /credentials/i }),
    ).toBeNull();
    expect(within(yl).queryByRole("button", { name: "Save" })).toBeNull();
  });

  it("shows the manual step for Poly and no credential fields", async () => {
    mockApi(routes());
    renderApp("/phones/settings");
    const poly = await card("Poly");
    expect(within(poly).getByText(/Poly ZT portal/)).toBeInTheDocument();
    expect(poly.querySelectorAll("input")).toHaveLength(0);
    expect(within(poly).queryByRole("button")).toBeNull();
  });

  it("copies a DHCP value and shows the router snippets", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", {
      value: { writeText },
      configurable: true,
    });
    mockApi(routes());
    renderApp("/phones/settings");
    fireEvent.click(
      await screen.findByRole("button", {
        name: "Copy Poly option 160 value",
      }),
    );
    await waitFor(() => expect(writeText).toHaveBeenCalledWith(`${BOOT}/poly`));
    expect(
      screen.getByText(
        (_, el) =>
          el?.tagName === "PRE" &&
          (el.textContent ?? "").includes(`value="s'${BOOT}'"`) &&
          (el.textContent ?? "").includes("dhcp-option=prov-66"),
      ),
    ).toBeInTheDocument();
    expect(
      screen.getByText(`option tftp-server-name "${BOOT}";`),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("link", { name: "Download CA.crt" }),
    ).toHaveAttribute("href", SETTINGS.caUrl);
  });

  it("Test checks the account and reloads the result", async () => {
    const checked = ACCOUNTS.map((a) =>
      a.vendor === "snom"
        ? { ...a, lastCheckAt: "2026-10-06T10:00:00Z", lastCheckResult: "ok" }
        : a,
    );
    const calls = mockApi(
      routes({
        "GET /api/v1/prov/redirect": [
          json({ items: ACCOUNTS }),
          json({ items: checked }),
        ],
        "POST /api/v1/prov/redirect/snom/check": () => noContent(),
      }),
    );
    renderApp("/phones/settings");
    const snom = await card("Snom");
    expect(within(snom).getByText("Not checked yet")).toBeInTheDocument();
    fireEvent.click(within(snom).getByRole("button", { name: "Test" }));
    expect(await within(snom).findByText(/Last check: ok/)).toBeInTheDocument();
    expect(
      calls.some(
        (c) =>
          c.method === "POST" && c.url === "/api/v1/prov/redirect/snom/check",
      ),
    ).toBe(true);
  });

  it("shows a failed check's message", async () => {
    mockApi(
      routes({
        "POST /api/v1/prov/redirect/snom/check": () =>
          apiError(502, "upstream", "SRAPS rejected the access key"),
      }),
    );
    renderApp("/phones/settings");
    const snom = await card("Snom");
    fireEvent.click(within(snom).getByRole("button", { name: "Test" }));
    expect(
      await within(snom).findByText("SRAPS rejected the access key"),
    ).toBeInTheDocument();
  });

  it("removes credentials after confirming", async () => {
    const calls = mockApi(
      routes({
        "GET /api/v1/prov/redirect": [
          json({
            items: ACCOUNTS.map((a) =>
              a.vendor === "snom" ? { ...a, hasCredentials: true } : a,
            ),
          }),
          json({ items: ACCOUNTS }),
        ],
        "DELETE /api/v1/prov/redirect/snom": () => noContent(),
      }),
    );
    renderApp("/phones/settings");
    const snom = await card("Snom");
    expect(within(snom).getByText(/set \(write-only\)/)).toBeInTheDocument();
    fireEvent.click(
      within(snom).getByRole("button", { name: "Remove credentials" }),
    );
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Remove credentials" }),
    );
    expect(
      await within(await card("Snom")).findByText(/Credentials: not set/),
    ).toBeInTheDocument();
    expect(
      calls.some(
        (c) => c.method === "DELETE" && c.url === "/api/v1/prov/redirect/snom",
      ),
    ).toBe(true);
  });
});
