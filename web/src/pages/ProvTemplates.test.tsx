import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import {
  json,
  ME,
  mockApi,
  noContent,
  renderApp,
  type Routes,
} from "../test/api";

const BUILTIN = {
  id: "builtin:yealink-t5x",
  vendor: "yealink",
  modelGlob: "T5*",
  priority: 10,
  name: "Yealink T5x",
  files: [
    {
      pattern: "{mac}.cfg",
      contentType: "text/plain",
      body: "#!version:1.0.0.1\naccount.1.user_name = {{.Line.Username}}",
    },
  ],
  builtin: true,
  builtinRef: "yealink-t5x",
  version: 1,
  updatedAt: "2026-10-01T10:00:00Z",
};

const COPY = {
  id: 42,
  vendor: "yealink",
  modelGlob: "T5*",
  priority: 20,
  name: "Yealink T5x (copy)",
  files: [
    {
      pattern: "{mac}.cfg",
      contentType: "text/plain",
      body: "#!version:1.0.0.1\naccount.1.enable = 1\naccount.1.label = {{.Foo}}\n",
    },
    {
      pattern: "y000000000{model}.cfg",
      contentType: "text/plain",
      body: "static.auto_provision.enable = 1",
    },
  ],
  builtin: false,
  builtinRef: "yealink-t5x",
  version: 3,
  updatedAt: "2026-10-02T10:00:00Z",
};

const PHONE = {
  id: 5,
  mac: "805ec0123456",
  serial: "",
  vendor: "yealink",
  model: "T54W",
  label: "Front desk",
  deviceId: 3,
  extensionId: 7,
  extensionNumber: "101",
  templateId: null,
  blf: [],
  enabled: true,
  tokenExposed: false,
  uaMismatch: false,
  bootArmed: false,
  bootReclaimed: false,
  redirectStatus: { state: "registered" },
  firstFetchAt: null,
  lastFetchAt: null,
  lastFetchIp: null,
  lastFetchUa: null,
  lastFetchFile: null,
  firmwareSeen: null,
  renderError: false,
  createdAt: "2026-10-01T10:00:00Z",
  updatedAt: "2026-10-01T10:00:00Z",
};

const SNOM_PHONE = {
  ...PHONE,
  id: 6,
  mac: "000413aabbcc",
  vendor: "snom",
  model: "D785",
  label: "Hall",
};

const VALIDATION_400 = {
  error: {
    code: "invalid_template",
    message: "template has errors",
    fields: [
      { path: "files[0].body", line: 3, message: "unknown variable .Foo" },
    ],
  },
};

function api(more: Routes = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/prov/templates": () => json({ items: [BUILTIN, COPY] }),
    "GET /api/v1/phones": () => json({ items: [PHONE, SNOM_PHONE] }),
    ...more,
  });
}

async function editCopy() {
  fireEvent.click(
    await screen.findByRole("button", { name: "Edit Yealink T5x (copy)" }),
  );
  await screen.findByRole("heading", { name: "Edit Yealink T5x (copy)" });
}

function gutterLine(n: number) {
  const gutter = document.querySelector(".prov-editor__gutter");
  const li = gutter?.querySelectorAll("li")[n - 1];
  if (!li) throw new Error(`no gutter line ${n}`);
  return li;
}

/** Line 3 of the first file is marked and labelled; line 2 is not. */
async function expectLine3Error() {
  expect(
    await screen.findByText("Line 3: unknown variable .Foo"),
  ).toBeInTheDocument();
  const line3 = gutterLine(3);
  expect(line3).toHaveAttribute("data-error");
  expect(line3).toHaveAttribute("aria-label", "Line 3: unknown variable .Foo");
  expect(line3).toHaveAttribute("title", "Line 3: unknown variable .Foo");
  expect(gutterLine(2)).not.toHaveAttribute("data-error");
  expect(gutterLine(2)).not.toHaveAttribute("aria-label");
}

describe("ProvTemplates", () => {
  it("lists built-ins and copies; built-ins are view and copy only", async () => {
    api();
    renderApp("/phones/templates");

    expect(
      await screen.findByRole("heading", { name: "Phone templates" }),
    ).toBeInTheDocument();
    const table = await screen.findByRole("table");
    const builtinRow = within(table).getByText("Yealink T5x").closest("tr")!;
    expect(within(builtinRow).getByText("Built-in")).toBeInTheDocument();
    expect(
      within(builtinRow).getByRole("button", { name: "View Yealink T5x" }),
    ).toBeInTheDocument();
    expect(
      within(builtinRow).getByRole("button", {
        name: "Copy Yealink T5x to edit",
      }),
    ).toBeInTheDocument();
    expect(
      within(builtinRow).queryByRole("button", { name: /^Edit/ }),
    ).not.toBeInTheDocument();
    expect(
      within(builtinRow).queryByRole("button", { name: /^Delete/ }),
    ).not.toBeInTheDocument();

    const copyRow = within(table)
      .getByText("Yealink T5x (copy)")
      .closest("tr")!;
    expect(
      within(copyRow).getByText("Copy of yealink-t5x"),
    ).toBeInTheDocument();
    expect(within(copyRow).getByText("2")).toBeInTheDocument();

    // View shows the bodies read-only.
    fireEvent.click(
      within(builtinRow).getByRole("button", { name: "View Yealink T5x" }),
    );
    const dialog = screen.getByRole("dialog", { name: "Yealink T5x" });
    expect(
      within(dialog).getByText(/account\.1\.user_name/),
    ).toBeInTheDocument();
    expect(within(dialog).queryByRole("textbox")).not.toBeInTheDocument();
  });

  it("copies a built-in and opens the editor on the copy", async () => {
    const calls = api({
      "POST /api/v1/prov/templates/builtin%3Ayealink-t5x/copy": () =>
        json(COPY, 201),
    });
    renderApp("/phones/templates");

    fireEvent.click(
      await screen.findByRole("button", { name: "Copy Yealink T5x to edit" }),
    );
    expect(
      await screen.findByRole("heading", { name: "Edit Yealink T5x (copy)" }),
    ).toBeInTheDocument();
    expect(screen.getByLabelText("Name")).toHaveValue("Yealink T5x (copy)");
    expect(screen.getByLabelText("Body of {mac}.cfg")).toHaveValue(
      COPY.files[0]!.body,
    );
    expect(
      calls.some((c) => c.method === "POST" && c.url.endsWith("/copy")),
    ).toBe(true);
  });

  it("asks before deleting a copy and says the built-in comes back", async () => {
    const calls = api({ "DELETE /api/v1/prov/templates/42": noContent });
    renderApp("/phones/templates");

    fireEvent.click(
      await screen.findByRole("button", { name: "Delete Yealink T5x (copy)" }),
    );
    const dialog = screen.getByRole("dialog");
    expect(dialog).toHaveTextContent(
      /deleting the copy restores the built-in yealink-t5x/i,
    );
    fireEvent.click(
      within(dialog).getByRole("button", { name: "Delete template" }),
    );
    await waitFor(() =>
      expect(screen.queryByText("Yealink T5x (copy)")).not.toBeInTheDocument(),
    );
    expect(
      calls.some(
        (c) => c.method === "DELETE" && c.url === "/api/v1/prov/templates/42",
      ),
    ).toBe(true);
  });

  it("shows a save's validation error at its line", async () => {
    const calls = api({
      "PATCH /api/v1/prov/templates/42": () => json(VALIDATION_400, 400),
    });
    renderApp("/phones/templates");
    await editCopy();

    fireEvent.click(screen.getByRole("button", { name: "Save" }));
    await expectLine3Error();
    const patch = calls.find((c) => c.method === "PATCH");
    expect(patch?.body).toMatchObject({ name: "Yealink T5x (copy)" });
  });

  it("shows validate errors at their line, file, field and form", async () => {
    api({
      "POST /api/v1/prov/templates/validate": [
        () =>
          json(
            {
              error: {
                code: "invalid_template",
                message: "template has errors",
                fields: [
                  {
                    path: "files[1].body",
                    line: 1,
                    message: 'function "nope" not defined',
                  },
                  { path: "files[1].pattern", message: "unknown placeholder" },
                  { path: "modelGlob", message: "bad glob" },
                  { path: "files", message: "two files share a pattern" },
                ],
              },
            },
            400,
          ),
        () => json(VALIDATION_400, 400),
        () => json({ ok: true }),
      ],
    });
    renderApp("/phones/templates");
    await editCopy();

    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    // The editor switches to the second file, which has the first error.
    expect(
      await screen.findByText('Line 1: function "nope" not defined'),
    ).toBeInTheDocument();
    expect(
      screen.getByLabelText("Body of y000000000{model}.cfg"),
    ).toBeInTheDocument();
    expect(gutterLine(1)).toHaveAttribute("data-error");
    expect(screen.getByText("unknown placeholder")).toBeInTheDocument();
    expect(screen.getByText("bad glob")).toBeInTheDocument();
    expect(screen.getByText(/two files share a pattern/)).toBeInTheDocument();

    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    await expectLine3Error();

    fireEvent.click(screen.getByRole("button", { name: "Validate" }));
    expect(await screen.findByText("Template is valid")).toBeInTheDocument();
    expect(gutterLine(3)).not.toHaveAttribute("data-error");
  });

  it("previews a phone's file as the server masks it, from the preview route only", async () => {
    const masked =
      "account.1.password = ********\nstatic.security.user_password = admin:********\n";
    const calls = api({
      "GET /api/v1/phones/5/preview?file=805ec0123456.cfg": () =>
        new Response(masked, {
          status: 200,
          headers: { "Content-Type": "text/plain" },
        }),
    });
    renderApp("/phones/templates");
    await editCopy();

    expect(
      screen.getByText(
        "Renders what this phone gets now, with secrets masked by the server; save first to preview your changes. The phone must resolve to this template.",
      ),
    ).toBeInTheDocument();
    const phoneSelect = await screen.findByLabelText("Phone");
    // Only phones of the template's vendor are offered.
    const options = within(phoneSelect).getAllByRole("option");
    expect(options).toHaveLength(1);
    expect(options[0]).toHaveTextContent("Front desk");
    expect(
      within(screen.getByLabelText("File")).getByRole("option", {
        name: "805ec0123456.cfg",
      }),
    ).toBeInTheDocument();

    const before = calls.length;
    fireEvent.click(screen.getByRole("button", { name: "Preview" }));

    const pre = await screen.findByText(/account\.1\.password = \*{8}/);
    expect(pre.textContent).toBe(masked);
    const made = calls.slice(before);
    expect(made).toEqual([
      {
        method: "GET",
        url: `/api/v1/phones/5/preview?file=${encodeURIComponent("805ec0123456.cfg")}`,
        body: undefined,
      },
    ]);
    // Nothing the editor did sent phone data anywhere else.
    for (const c of calls) {
      expect(c.method).toBe("GET");
      if (c.url.includes(PHONE.mac))
        expect(c.url).toMatch(/^\/api\/v1\/phones\/5\/preview\?/);
    }
  });
});
