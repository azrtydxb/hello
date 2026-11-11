import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { apiError, json, ME, mockApi, renderApp } from "../test/api";

const CA = {
  id: 1,
  subject: "CN=Hello device CA,O=Kuvryn Hello",
  notBefore: "2026-11-01T00:00:00Z",
  notAfter: "2036-11-01T00:00:00Z",
  fingerprintSha256: "AB:CD",
  source: "generated",
  certPem: "-----BEGIN CERTIFICATE-----\nx\n-----END CERTIFICATE-----\n",
  createdAt: "2026-11-01T00:00:00Z",
};

const STATUS = (ca: typeof CA | null) => ({
  serverEnabled: true,
  ca,
  caUrl: "http://prov.lab/p/scep-ca.crt",
  crlUrl: "http://prov.lab/p/scep-ca.crl",
});

const GROUP = {
  id: 7,
  name: "Desk",
  description: "",
  includeExtensions: true,
  includeCompany: true,
  bookIds: [],
};

const SECURITY = {
  groupId: 7,
  scepEnabled: false,
  certValidityDays: 365,
  wiredDot1xProfileId: null,
};

const PROFILE = {
  id: 3,
  name: "Office LAN",
  method: "peap",
  identity: "phone-{mac}",
  anonymousIdentity: "",
  hasPassword: true,
  caCertPem: "",
  serverDomain: "",
  usedByGroups: [],
  updatedAt: "2026-11-01T00:00:00Z",
};

const CERT = {
  id: 11,
  caId: 1,
  phoneId: 5,
  mac: "c074ad000010",
  serial: "abc123",
  subject: "CN=c074ad000010",
  kind: "enrol",
  notBefore: "2026-11-01T00:00:00Z",
  notAfter: "2027-11-01T00:00:00Z",
  issuedAt: "2026-11-01T00:00:00Z",
  revokedAt: null,
  revokeReason: null,
  status: "active",
};

function routes(ca: typeof CA | null) {
  return {
    ...ME,
    "GET /api/v1/scep": () => json(STATUS(ca)),
    "GET /api/v1/dot1x-profiles": () => json({ items: [PROFILE] }),
    "GET /api/v1/scep/certificates": () => json({ items: [CERT] }),
    "GET /api/v1/phone-groups?limit=200": () =>
      json({ items: [GROUP], next: "" }),
    "GET /api/v1/phone-groups/7/security": () => json(SECURITY),
  };
}

afterEach(() => vi.unstubAllGlobals());

describe("PhoneSecurity", () => {
  it("generates the device CA when there is none", async () => {
    const calls = mockApi({
      ...routes(null),
      "PUT /api/v1/scep/ca": () => json(CA),
    });
    renderApp("/phones/security");
    expect(await screen.findByText(/No device CA yet/)).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Generate CA" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Common name"), {
      target: { value: "Lab devices" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Generate" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
        mode: "generate",
        commonName: "Lab devices",
        replace: false,
      }),
    );
  });

  it("shows the CA and its CRL URL, and replacing asks for replace", async () => {
    const calls = mockApi({
      ...routes(CA),
      "PUT /api/v1/scep/ca": () =>
        apiError(400, "bad_request", "keyPem is not the key of certPem"),
    });
    renderApp("/phones/security");
    expect(
      await screen.findByText("http://prov.lab/p/scep-ca.crl"),
    ).toBeInTheDocument();
    expect(screen.getByText("AB:CD")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Import CA" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("CA certificate (PEM)"), {
      target: { value: "CERT" },
    });
    fireEvent.change(
      within(dialog).getByLabelText("CA private key (PEM, RSA)"),
      { target: { value: "KEY" } },
    );
    fireEvent.click(within(dialog).getByRole("button", { name: "Import" }));
    expect(
      await within(dialog).findByText("keyPem is not the key of certPem"),
    ).toBeInTheDocument();
    expect(calls.find((c) => c.method === "PUT")?.body).toEqual({
      mode: "import",
      certPem: "CERT",
      keyPem: "KEY",
      replace: true,
    });
  });

  it("switches SCEP on for a group and names a wired profile", async () => {
    const calls = mockApi({
      ...routes(CA),
      "PUT /api/v1/phone-groups/7/security": [
        () => json({ ...SECURITY, scepEnabled: true }),
        () => json({ ...SECURITY, scepEnabled: true, wiredDot1xProfileId: 3 }),
      ],
    });
    renderApp("/phones/security");
    const sw = await screen.findByRole("switch", { name: "SCEP for Desk" });
    fireEvent.click(sw);
    await waitFor(() => expect(sw).toBeChecked());
    fireEvent.change(screen.getByLabelText("Wired 802.1X profile for Desk"), {
      target: { value: "3" },
    });
    await waitFor(() =>
      expect(
        calls.filter(
          (c) =>
            c.url === "/api/v1/phone-groups/7/security" && c.method === "PUT",
        ),
      ).toHaveLength(2),
    );
    const puts = calls.filter((c) => c.method === "PUT");
    expect(puts[0]?.body).toEqual({
      scepEnabled: true,
      certValidityDays: 365,
      wiredDot1xProfileId: null,
    });
    expect(puts[1]?.body).toEqual({
      scepEnabled: true,
      certValidityDays: 365,
      wiredDot1xProfileId: 3,
    });
  });

  it("edits a profile without sending an empty password", async () => {
    const calls = mockApi({
      ...routes(CA),
      "PUT /api/v1/dot1x-profiles/3": () =>
        json({ ...PROFILE, identity: "{mac}" }),
    });
    renderApp("/phones/security");
    fireEvent.click(
      await screen.findByRole("button", { name: "Edit Office LAN" }),
    );
    const dialog = await screen.findByRole("dialog");
    expect(
      within(dialog).getByText("Leave empty to keep the stored password."),
    ).toBeInTheDocument();
    fireEvent.change(within(dialog).getByLabelText("Identity"), {
      target: { value: "{mac}" },
    });
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "PUT")?.body).toBeDefined(),
    );
    const body = calls.find((c) => c.method === "PUT")?.body as Record<
      string,
      unknown
    >;
    expect(body.identity).toBe("{mac}");
    expect("password" in body).toBe(false);
  });

  it("adds an EAP-TLS profile without a password field", async () => {
    const calls = mockApi({
      ...routes(CA),
      "POST /api/v1/dot1x-profiles": () =>
        json({ ...PROFILE, id: 4, name: "Certs", method: "eap-tls" }, 201),
    });
    renderApp("/phones/security");
    fireEvent.click(await screen.findByRole("button", { name: "Add profile" }));
    const dialog = await screen.findByRole("dialog");
    fireEvent.change(within(dialog).getByLabelText("Name"), {
      target: { value: "Certs" },
    });
    fireEvent.change(within(dialog).getByLabelText("Method"), {
      target: { value: "eap-tls" },
    });
    expect(within(dialog).queryByLabelText("Password")).toBeNull();
    fireEvent.click(within(dialog).getByRole("button", { name: "Save" }));
    await waitFor(() =>
      expect(calls.find((c) => c.method === "POST")?.body).toBeDefined(),
    );
    const body = calls.find((c) => c.method === "POST")?.body as Record<
      string,
      unknown
    >;
    expect(body.method).toBe("eap-tls");
    expect("password" in body).toBe(false);
  });

  it("revokes an active certificate", async () => {
    const calls = mockApi({
      ...routes(CA),
      "POST /api/v1/scep/certificates/11/revoke": () =>
        json({
          ...CERT,
          status: "revoked",
          revokedAt: CERT.issuedAt,
          revokeReason: "unspecified",
        }),
    });
    renderApp("/phones/security");
    fireEvent.click(
      await screen.findByRole("button", { name: "Revoke abc123" }),
    );
    const dialog = await screen.findByRole("dialog");
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Revoke certificate" }),
    );
    expect(await screen.findByText("revoked")).toBeInTheDocument();
    expect(calls.find((c) => c.method === "POST")?.body).toEqual({
      reason: "unspecified",
    });
    expect(screen.queryByRole("button", { name: "Revoke abc123" })).toBeNull();
  });
});
