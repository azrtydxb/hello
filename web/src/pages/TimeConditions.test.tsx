import { fireEvent, screen, waitFor, within } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { json, ME, mockApi, renderApp } from "../test/api";

const CREATED = "2026-10-25T10:00:00Z";
const EXTENSIONS = [
  { id: 1, number: "100", name: "Reception", createdAt: CREATED, updatedAt: CREATED },
  { id: 2, number: "200", name: "Kitchen", createdAt: CREATED, updatedAt: CREATED },
];
const SCHEDULE = {
  id: 3,
  name: "office",
  timeZone: "Europe/Brussels",
  ranges: [
    { day: 1, start: "09:00", end: "12:00" },
    { day: 1, start: "13:00", end: "17:00" },
  ],
  calendarIds: [4],
  calendars: ["holidays"],
  createdAt: CREATED,
  updatedAt: CREATED,
};
const CALENDAR = {
  id: 4,
  name: "holidays",
  description: "",
  entries: [
    {
      id: 1,
      label: "Christmas Day",
      start: "2026-12-25",
      end: "2026-12-25",
      yearly: false,
      allDay: true,
      startTime: "",
      endTime: "",
      mode: "closed",
      hours: [],
    },
  ],
  schedules: ["office"],
  createdAt: CREATED,
  updatedAt: CREATED,
};
const TC = {
  id: 7,
  name: "main",
  scheduleId: 3,
  scheduleName: "office",
  open: { kind: "ring_group", target: "sales" },
  closed: { kind: "announcement", target: "closed-msg", thenVoicemail: "200" },
  holiday: null,
  override: "auto",
  overrideAt: null,
  overrideBy: "",
  toggleNumber: "1",
  createdAt: CREATED,
  updatedAt: CREATED,
};

function setup(extra: Parameters<typeof mockApi>[0] = {}) {
  return mockApi({
    ...ME,
    "GET /api/v1/extensions": () => json({ items: EXTENSIONS }),
    "GET /api/v1/announcements": () =>
      json({ items: [{ id: 5, name: "closed-msg", createdAt: CREATED }] }),
    "GET /api/v1/ring-groups": () =>
      json({ items: [{ id: 1, name: "sales", members: [] }] }),
    "GET /api/v1/paging-groups": () => json({ items: [] }),
    "GET /api/v1/ivr-menus": () => json({ items: [] }),
    "GET /api/v1/schedules": () => json({ items: [SCHEDULE] }),
    "GET /api/v1/calendars": () => json({ items: [CALENDAR] }),
    "GET /api/v1/time-conditions": () => json({ items: [TC] }),
    "GET /api/v1/time-settings": () => json({ timeZone: "Europe/Brussels" }),
    ...extra,
  });
}

describe("TimeConditions", () => {
  it("lists time conditions with their destinations and switches the override", async () => {
    const calls = setup({
      "PUT /api/v1/time-conditions/7/override": () =>
        json({ ...TC, override: "closed", overrideBy: "admin" }),
    });
    renderApp("/time-conditions");
    const card = await screen.findByRole("listitem", {
      name: "Time condition main",
    });
    expect(within(card).getByText(/Ring group sales/)).toBeInTheDocument();
    expect(
      within(card).getByText(/Announcement closed-msg, then voicemail 200/),
    ).toBeInTheDocument();
    fireEvent.click(within(card).getByRole("radio", { name: "Closed" }));
    await waitFor(() =>
      expect(calls).toContainEqual(
        expect.objectContaining({
          method: "PUT",
          url: "/api/v1/time-conditions/7/override",
          body: { override: "closed" },
        }),
      ),
    );
    expect(await screen.findByText("Forced closed")).toBeInTheDocument();
  });

  it("creates a time condition with a holiday destination", async () => {
    const calls = setup({
      "POST /api/v1/time-conditions": () => json({ ...TC, id: 8, name: "side" }, 201),
    });
    renderApp("/time-conditions");
    const [create] = await screen.findAllByRole("button", {
      name: "New time condition",
    });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "side" } });
    const schedule = screen.getByLabelText("Schedule");
    await waitFor(() =>
      expect(schedule.querySelectorAll("option").length).toBe(2),
    );
    fireEvent.change(schedule, { target: { value: "3" } });
    fireEvent.change(screen.getByLabelText("Toggle number"), { target: { value: "2" } });
    fireEvent.change(screen.getByLabelText("When open"), {
      target: { value: "ring_group" },
    });
    fireEvent.change(screen.getByLabelText("Ring group"), {
      target: { value: "sales" },
    });
    fireEvent.click(screen.getByLabelText("A different destination on holidays"));
    fireEvent.change(screen.getByLabelText("On a holiday"), {
      target: { value: "announcement" },
    });
    fireEvent.change(screen.getByLabelText("Announcement"), {
      target: { value: "closed-msg" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Create time condition" }));
    await waitFor(() =>
      expect(calls).toContainEqual(
        expect.objectContaining({
          method: "POST",
          url: "/api/v1/time-conditions",
          body: {
            name: "side",
            scheduleId: 3,
            toggleNumber: "2",
            open: { kind: "ring_group", target: "sales" },
            closed: { kind: "hangup", target: "" },
            holiday: { kind: "announcement", target: "closed-msg" },
          },
        }),
      ),
    );
  });

  it("asks what the time condition would do at a time", async () => {
    const calls = setup({
      "GET /api/v1/time-conditions/7/evaluate": () =>
        json({
          name: "main",
          at: "2026-06-07T08:00:00Z",
          override: "auto",
          decision: {
            branch: "closed",
            why: "no open hours on Sunday",
            forced: false,
            destination: TC.closed,
            verdict: { open: false, holiday: false, reason: "no open hours on Sunday", local: "Sun 2026-06-07 10:00 Europe/Brussels" },
          },
          resolution: { steps: [], destination: TC.closed },
        }),
      "GET /api/v1/time-conditions/7/evaluate?at=2026-06-07T10%3A00": () =>
        json({
          name: "main",
          at: "2026-06-07T08:00:00Z",
          override: "auto",
          decision: {
            branch: "closed",
            why: "no open hours on Sunday",
            forced: false,
            destination: TC.closed,
            verdict: { open: false, holiday: false, reason: "no open hours on Sunday", local: "Sun 2026-06-07 10:00 Europe/Brussels" },
          },
          resolution: { steps: [], destination: TC.closed },
        }),
    });
    renderApp("/time-conditions");
    fireEvent.click(await screen.findByRole("button", { name: "What would main do" }));
    expect(await screen.findByText(/no open hours on Sunday/)).toBeInTheDocument();
    fireEvent.change(screen.getByLabelText("At (schedule's time zone)"), {
      target: { value: "2026-06-07T10:00" },
    });
    fireEvent.click(screen.getByRole("button", { name: "Evaluate" }));
    await waitFor(() =>
      expect(calls.map((c) => c.url)).toContain(
        "/api/v1/time-conditions/7/evaluate?at=2026-06-07T10%3A00",
      ),
    );
  });

  it("creates a schedule in the default time zone with two ranges on a day", async () => {
    const calls = setup({
      "POST /api/v1/schedules": () => json({ ...SCHEDULE, id: 9, name: "late" }, 201),
    });
    renderApp("/time-conditions");
    fireEvent.click(await screen.findByRole("tab", { name: "Schedules" }));
    const [create] = await screen.findAllByRole("button", { name: "New schedule" });
    await waitFor(() => expect(create).toBeEnabled());
    fireEvent.click(create!);
    fireEvent.change(screen.getByLabelText("Name"), { target: { value: "late" } });
    await waitFor(() =>
      expect(screen.getByLabelText("Time zone")).toHaveValue("Europe/Brussels"),
    );
    const add = screen.getByRole("button", { name: "Add a range on Monday" });
    fireEvent.click(add);
    fireEvent.click(add);
    fireEvent.change(screen.getAllByLabelText("Monday end")[0]!, {
      target: { value: "12:00" },
    });
    fireEvent.change(screen.getAllByLabelText("Monday start")[1]!, {
      target: { value: "13:00" },
    });
    fireEvent.click(screen.getByLabelText(/^holidays/));
    fireEvent.click(screen.getByRole("button", { name: "Create schedule" }));
    await waitFor(() =>
      expect(calls).toContainEqual(
        expect.objectContaining({
          method: "POST",
          url: "/api/v1/schedules",
          body: {
            name: "late",
            timeZone: "Europe/Brussels",
            ranges: [
              { day: 1, start: "09:00", end: "12:00" },
              { day: 1, start: "13:00", end: "17:00" },
            ],
            calendarIds: [4],
          },
        }),
      ),
    );
  });

  it("imports dates into a calendar and shows the server's line errors", async () => {
    const calls = setup({
      "POST /api/v1/calendars/4/import": [
        () =>
          json(
            { error: { code: "invalid", message: "invalid", fields: [{ path: "lines[2]", message: "must start with a date" }] } },
            400,
          ),
        () => json({ imported: 1, calendar: { ...CALENDAR, entries: [...CALENDAR.entries, { ...CALENDAR.entries[0], id: 2, label: "New year", start: "01-01", end: "01-01", yearly: true }] } }),
      ],
    });
    renderApp("/time-conditions");
    fireEvent.click(await screen.findByRole("tab", { name: "Calendars" }));
    fireEvent.click(await screen.findByRole("button", { name: "Edit calendar holidays" }));
    const box = await screen.findByLabelText("Dates to import");
    fireEvent.change(box, { target: { value: "oops" } });
    fireEvent.click(screen.getByRole("button", { name: "Import dates" }));
    expect(await screen.findByText(/lines\[2\]: must start with a date/)).toBeInTheDocument();
    fireEvent.change(box, { target: { value: "01-01 New year" } });
    fireEvent.click(screen.getByRole("button", { name: "Import dates" }));
    await waitFor(() =>
      expect(calls.filter((c) => c.url === "/api/v1/calendars/4/import")).toHaveLength(2),
    );
    expect(calls.filter((c) => c.url === "/api/v1/calendars/4/import")[1]!.body).toEqual({
      text: "01-01 New year",
      replace: false,
    });
    await waitFor(() => expect(screen.getByLabelText("Label 2")).toHaveValue("New year"));
  });
});
