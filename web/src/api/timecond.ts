// Time and date routing client (migration 00020): schedules, holiday
// calendars and time conditions, with the day/night override and the
// "what would happen" evaluation.

import { list, request, type Id, type IvrDestinationKind } from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

// --- schedules -------------------------------------------------------------

/** One open-hours range; day 0 is Sunday and an end before the start crosses midnight. */
export interface DayRange {
  day: number;
  /** HH:MM */
  start: string;
  /** HH:MM, or 24:00 for the end of the day. */
  end: string;
}

export interface SchedulePayload {
  name: string;
  timeZone: string;
  ranges: DayRange[];
  calendarIds: Id[];
}

export interface HoursSchedule extends SchedulePayload {
  id: Id;
  /** Names of the calendars, read-only. */
  calendars: string[];
  createdAt: string;
  updatedAt: string;
}

export const listSchedules = (signal?: AbortSignal) =>
  list<HoursSchedule>("/api/v1/schedules", signal);
export const createSchedule = (body: SchedulePayload) =>
  request<HoursSchedule>("POST", "/api/v1/schedules", { body });
export const putSchedule = (scheduleId: Id, body: SchedulePayload) =>
  request<HoursSchedule>("PUT", `/api/v1/schedules/${seg(scheduleId)}`, {
    body,
  });
export const deleteSchedule = (scheduleId: Id) =>
  request<void>("DELETE", `/api/v1/schedules/${seg(scheduleId)}`);

/** GET /api/v1/time-settings: the zone a new schedule gets. */
export const getDefaultTimeZone = async (signal?: AbortSignal) =>
  (
    await request<{ timeZone: string }>("GET", "/api/v1/time-settings", {
      signal,
    })
  ).timeZone;

// --- calendars -------------------------------------------------------------

export interface HoursRange {
  start: string;
  end: string;
}

/** One exception: dates are YYYY-MM-DD, or MM-DD when yearly. */
export interface CalendarEntry {
  id?: Id;
  label: string;
  start: string;
  end: string;
  yearly: boolean;
  allDay: boolean;
  startTime: string;
  endTime: string;
  /** closed: a holiday; open: replaces the weekly hours with `hours`. */
  mode: "closed" | "open";
  hours: HoursRange[];
}

export interface CalendarPayload {
  name: string;
  description: string;
  entries: CalendarEntry[];
}

export interface Calendar extends CalendarPayload {
  id: Id;
  /** Names of the schedules that consult it, read-only. */
  schedules: string[];
  createdAt: string;
  updatedAt: string;
}

export const listCalendars = (signal?: AbortSignal) =>
  list<Calendar>("/api/v1/calendars", signal);
export const createCalendar = (body: CalendarPayload) =>
  request<Calendar>("POST", "/api/v1/calendars", { body });
export const putCalendar = (calendarId: Id, body: CalendarPayload) =>
  request<Calendar>("PUT", `/api/v1/calendars/${seg(calendarId)}`, { body });
export const deleteCalendar = (calendarId: Id) =>
  request<void>("DELETE", `/api/v1/calendars/${seg(calendarId)}`);

/** POST /api/v1/calendars/{id}/import: a list of dates or an iCalendar file. */
export const importCalendar = (
  calendarId: Id,
  text: string,
  replace: boolean,
) =>
  request<{ imported: number; calendar: Calendar }>(
    "POST",
    `/api/v1/calendars/${seg(calendarId)}/import`,
    { body: { text, replace } },
  );

// --- time conditions -------------------------------------------------------

export type Override = "auto" | "open" | "closed";

/** A destination; thenVoicemail continues an announcement into a voicemail box. */
export interface TcDestination {
  kind: IvrDestinationKind;
  target: string;
  thenVoicemail?: string;
}

export interface TimeConditionPayload {
  name: string;
  scheduleId: Id;
  open: TcDestination;
  closed: TcDestination;
  holiday: TcDestination | null;
  toggleNumber: string;
}

export interface TimeCondition extends TimeConditionPayload {
  id: Id;
  scheduleName: string;
  override: Override;
  overrideAt: string | null;
  overrideBy: string;
  createdAt: string;
  updatedAt: string;
}

export const listTimeConditions = (signal?: AbortSignal) =>
  list<TimeCondition>("/api/v1/time-conditions", signal);
export const createTimeCondition = (body: TimeConditionPayload) =>
  request<TimeCondition>("POST", "/api/v1/time-conditions", { body });
export const putTimeCondition = (tcId: Id, body: TimeConditionPayload) =>
  request<TimeCondition>("PUT", `/api/v1/time-conditions/${seg(tcId)}`, {
    body,
  });
export const deleteTimeCondition = (tcId: Id) =>
  request<void>("DELETE", `/api/v1/time-conditions/${seg(tcId)}`);

/** PUT .../override: force open or closed, or back to the schedule. */
export const setTimeConditionOverride = (tcId: Id, override: Override) =>
  request<TimeCondition>(
    "PUT",
    `/api/v1/time-conditions/${seg(tcId)}/override`,
    { body: { override } },
  );

export interface TcStep {
  condition: string;
  branch: "open" | "closed" | "holiday";
  why: string;
  forced: boolean;
  destination: TcDestination;
}

export interface TcEvaluation {
  name: string;
  at: string;
  override: Override;
  decision: {
    branch: "open" | "closed" | "holiday";
    why: string;
    forced: boolean;
    destination: TcDestination;
    verdict: { open: boolean; holiday: boolean; reason: string; local: string };
  };
  resolution: { steps: TcStep[]; destination: TcDestination };
  error?: string;
}

/**
 * GET .../evaluate: what the time condition would do now, or at `at` (an
 * RFC 3339 time, or YYYY-MM-DDTHH:MM in the schedule's zone).
 */
export const evaluateTimeCondition = (
  tcId: Id,
  at?: string,
  ignoreOverride = false,
  signal?: AbortSignal,
) => {
  const q = new URLSearchParams();
  if (at) q.set("at", at);
  if (ignoreOverride) q.set("ignoreOverride", "true");
  const qs = q.toString();
  return request<TcEvaluation>(
    "GET",
    `/api/v1/time-conditions/${seg(tcId)}/evaluate${qs ? `?${qs}` : ""}`,
    { signal },
  );
};
