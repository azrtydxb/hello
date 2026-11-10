// Address-book API client for the Phonebooks and Phone groups pages: the
// HTTP JSON of spec phonebooks-ptt (P-1, P-2, P-7).

import { listItems, request, type Id } from "../api";

const seg = (value: Id) => encodeURIComponent(String(value));

/** What a book is for; the company and extensions books exist exactly once. */
export type BookKind = "company" | "named" | "extensions" | "personal";

export interface Book {
  id: Id;
  kind: BookKind;
  name: string;
  version: number;
  entryCount: number;
  createdAt: string;
  updatedAt: string;
}

/** Number labels, in the order CSV columns use. */
export const NUMBER_LABELS = ["work", "mobile", "home", "other"] as const;
export type NumberLabel = (typeof NUMBER_LABELS)[number];

export interface EntryNumber {
  label: NumberLabel | string;
  number: string;
}

/** What an administrator writes for an entry. */
export interface EntryInput {
  firstName: string;
  lastName: string;
  company: string;
  numbers: EntryNumber[];
  note: string;
}

export interface Entry extends EntryInput {
  id: Id;
  bookId: Id;
  createdAt: string;
  updatedAt: string;
}

export interface Group {
  id: Id;
  name: string;
  description: string;
  includeExtensions: boolean;
  includeCompany: boolean;
  /** The named books assigned to the group, in delivery order. */
  bookIds: Id[];
  /**
   * Catalog paging channels the group's phones listen on and send to; empty
   * means every enabled paging channel of the catalog.
   */
  multicast?: MulticastChannel[];
  /** Push-to-talk and group paging settings (Grandstream WP phones only). */
  ptt?: Ptt;
  /** How the group's phones take a page and an intercom call. */
  pagingOptions?: PagingOptions;
  /** The group's phones are given the LDAP directory. */
  ldapEnabled?: boolean;
  phoneCount: number;
  createdAt: string;
  updatedAt: string;
}

export type MulticastCodec = "PCMU" | "PCMA" | "G722";
export type MulticastRole = "listen" | "send" | "both";
/** paging: phones listen, paging groups send; ptt: Grandstream WP PTT. */
export type ChannelKind = "paging" | "ptt";

/** A channel of the multicast catalog (Paging > Multicast channels). */
export interface CatalogChannelInput {
  label: string;
  /** IPv4 multicast outside 224.0.0.0/24. */
  address: string;
  port: number;
  codec: MulticastCodec;
  /** Priority or channel number, 1 (highest) to 25. */
  priority: number;
  kind: ChannelKind;
  enabled: boolean;
}

export interface CatalogChannel extends CatalogChannelInput {
  id: Id;
}

/** A group's link to a catalog paging channel; the list replaces the group's. */
export interface MulticastInput {
  channelId: Id;
  role: MulticastRole;
}

/** A catalog channel as a group uses it. */
export interface MulticastChannel {
  id: Id;
  label: string;
  address: string;
  port: number;
  codec: MulticastCodec;
  role: MulticastRole;
  priority: number;
  enabled: boolean;
}

export interface PttChannel {
  channel: number;
  label: string;
  transmit: boolean;
  subscribe: boolean;
}

/** Channels 1 to 25 are push-to-talk, 26 to 50 group paging. */
export const MAX_PTT_CHANNEL = 25;
export const MIN_GROUP_CHANNEL = 26;
export const MAX_GROUP_CHANNEL = 50;

/**
 * Push-to-talk and group paging of a phone group: one multicast address,
 * channels 1 to 25 (PTT) and 26 to 50 (group paging).
 */
export interface Ptt {
  enabled: boolean;
  /** The catalog PTT channel; null uses the catalog's first PTT channel. */
  channelId: Id | null;
  /** The effective host:port (read-only), default 224.0.1.116:5001. */
  address?: string;
  /** The effective codec (read-only). */
  codec?: "PCMU" | "G722";
  /** 1, 2, 24 or 25. */
  defaultChannel: number;
  /** 1 to 25. */
  priorityChannel: number;
  /** 1 to 25. */
  emergencyChannel: number;
  acceptWhileBusy: boolean;
  groupPaging: boolean;
  /** 26, 27, 49 or 50. */
  defaultGroup: number;
  /** 26 to 50. */
  priorityGroup: number;
  /** 26 to 50. */
  emergencyGroup: number;
  channels: PttChannel[];
}

/** How a group's phones take a page and an intercom call. */
export interface PagingOptions {
  /** Pages play in Do Not Disturb (Grandstream WP). */
  allowInDnd: boolean;
  /** 0 off; otherwise a page of this priority or higher interrupts a call (0 to 10). */
  barge: number;
  /** A higher-priority page interrupts a lower one. */
  priorityActive: boolean;
  /** Paging timeout in seconds, 0 for none (0 to 3600; WP8x6). */
  timeout: number;
  /** An intercom call cuts into a call; off by default. */
  intercomBarge: boolean;
}

export const defaultPagingOptions = (): PagingOptions => ({
  allowInDnd: false,
  barge: 0,
  priorityActive: true,
  timeout: 0,
  intercomBarge: false,
});

export interface GroupInput {
  name: string;
  description: string;
  includeExtensions: boolean;
  includeCompany: boolean;
}

/** One page of a cursor list; `next` is "" on the last page. */
export interface Page<T> {
  items: T[];
  next: string;
}

async function page<T>(path: string, signal?: AbortSignal): Promise<Page<T>> {
  const body = await request<unknown>("GET", path, { signal });
  const items = listItems<T>(body, path);
  const next = (body as Record<string, unknown>).next;
  return { items, next: typeof next === "string" ? next : "" };
}

/** Every page of a list, following the cursor. */
async function all<T>(path: string, signal?: AbortSignal): Promise<T[]> {
  const out: T[] = [];
  let cursor = "";
  do {
    const sep = path.includes("?") ? "&" : "?";
    const p = await page<T>(
      cursor ? `${path}${sep}cursor=${encodeURIComponent(cursor)}` : path,
      signal,
    );
    out.push(...p.items);
    cursor = p.next;
  } while (cursor);
  return out;
}

// --- books ---------------------------------------------------------------------------

export const listBooks = (signal?: AbortSignal) =>
  all<Book>("/api/v1/phonebooks?limit=200", signal);

export const createBook = (name: string) =>
  request<Book>("POST", "/api/v1/phonebooks", { body: { name } });

export const renameBook = (bookId: Id, name: string) =>
  request<Book>("PUT", `/api/v1/phonebooks/${seg(bookId)}`, {
    body: { name },
  });

export const deleteBook = (bookId: Id) =>
  request<void>("DELETE", `/api/v1/phonebooks/${seg(bookId)}`);

// --- entries -------------------------------------------------------------------------

/** One page of a book's entries, by id; the extensions book lists its visible extensions. */
export const listEntries = (bookId: Id, cursor = "", signal?: AbortSignal) =>
  page<Entry>(
    `/api/v1/phonebooks/${seg(bookId)}/entries?limit=200${
      cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""
    }`,
    signal,
  );

export const createEntry = (bookId: Id, input: EntryInput) =>
  request<Entry>("POST", `/api/v1/phonebooks/${seg(bookId)}/entries`, {
    body: input,
  });

export const updateEntry = (bookId: Id, entryId: Id, input: EntryInput) =>
  request<Entry>(
    "PUT",
    `/api/v1/phonebooks/${seg(bookId)}/entries/${seg(entryId)}`,
    { body: input },
  );

export const deleteEntry = (bookId: Id, entryId: Id) =>
  request<void>(
    "DELETE",
    `/api/v1/phonebooks/${seg(bookId)}/entries/${seg(entryId)}`,
  );

// --- CSV -----------------------------------------------------------------------------

export interface RowError {
  /** 1 is the header row, so the first entry is row 2. */
  row: number;
  column: string;
  reason: string;
}

export interface RowWarning {
  row: number;
  reason: string;
}

/** The answer of an import, a dry run or not. */
export interface ImportResult {
  ok: boolean;
  dryRun: boolean;
  mode: "append" | "replace";
  rows: number;
  imported: number;
  errors: RowError[];
  warnings: RowWarning[];
}

/**
 * POST /api/v1/phonebooks/{id}/import: the CSV is the multipart part "file".
 * A dry run writes nothing. An import with row errors answers 400 with the
 * same result, which is thrown as an error by `request`; its fields are
 * not the row errors, so a dry run is the way to read them.
 */
export function importCsv(
  bookId: Id,
  csv: string,
  opts: { dryRun: boolean; mode: "append" | "replace" },
): Promise<ImportResult> {
  const form = new FormData();
  form.set("file", new Blob([csv], { type: "text/csv" }), "phonebook.csv");
  const q = new URLSearchParams({
    dry_run: String(opts.dryRun),
    mode: opts.mode,
  });
  return request<ImportResult>(
    "POST",
    `/api/v1/phonebooks/${seg(bookId)}/import?${q.toString()}`,
    { rawBody: form },
  );
}

/** The URL of a book's CSV export (a download the browser makes with its session). */
export const exportUrl = (bookId: Id) =>
  `/api/v1/phonebooks/${seg(bookId)}/export`;

// --- one list of entries, shared or personal ------------------------------------------

/**
 * The operations of one list of entries: a shared book's (by id) or the
 * signed-in user's own contacts (spec P-9), which the entries view and its
 * dialogs work with alike.
 */
export interface EntriesApi {
  list(cursor: string, signal?: AbortSignal): Promise<Page<Entry>>;
  create(input: EntryInput): Promise<Entry>;
  update(entryId: Id, input: EntryInput): Promise<Entry>;
  remove(entryId: Id): Promise<void>;
  importCsv(
    csv: string,
    opts: { dryRun: boolean; mode: "append" | "replace" },
  ): Promise<ImportResult>;
  /** The URL of the CSV export. */
  exportUrl: string;
}

/** The entries of a shared book. */
export const bookEntriesApi = (bookId: Id): EntriesApi => ({
  list: (cursor, signal) => listEntries(bookId, cursor, signal),
  create: (input) => createEntry(bookId, input),
  update: (entryId, input) => updateEntry(bookId, entryId, input),
  remove: (entryId) => deleteEntry(bookId, entryId),
  importCsv: (csv, opts) => importCsv(bookId, csv, opts),
  exportUrl: exportUrl(bookId),
});

// --- my contacts (spec P-9) -----------------------------------------------------------

/** The signed-in user's own contacts: the owner is always the caller. */
export const myContactsApi: EntriesApi = {
  list: (cursor, signal) =>
    page<Entry>(
      `/api/v1/me/contacts?limit=200${
        cursor ? `&cursor=${encodeURIComponent(cursor)}` : ""
      }`,
      signal,
    ),
  create: (input) =>
    request<Entry>("POST", "/api/v1/me/contacts", { body: input }),
  update: (entryId, input) =>
    request<Entry>("PUT", `/api/v1/me/contacts/${seg(entryId)}`, {
      body: input,
    }),
  remove: (entryId) =>
    request<void>("DELETE", `/api/v1/me/contacts/${seg(entryId)}`),
  importCsv: (csv, opts) => {
    const form = new FormData();
    form.set("file", new Blob([csv], { type: "text/csv" }), "contacts.csv");
    const q = new URLSearchParams({
      dry_run: String(opts.dryRun),
      mode: opts.mode,
    });
    return request<ImportResult>(
      "POST",
      `/api/v1/me/contacts/import?${q.toString()}`,
      { rawBody: form },
    );
  },
  exportUrl: "/api/v1/me/contacts/export",
};

// --- phone groups --------------------------------------------------------------------

export const listGroups = (signal?: AbortSignal) =>
  all<Group>("/api/v1/phone-groups?limit=200", signal);

export const createGroup = (input: GroupInput) =>
  request<Group>("POST", "/api/v1/phone-groups", { body: input });

export const updateGroup = (groupId: Id, input: GroupInput) =>
  request<Group>("PUT", `/api/v1/phone-groups/${seg(groupId)}`, {
    body: input,
  });

export const deleteGroup = (groupId: Id) =>
  request<void>("DELETE", `/api/v1/phone-groups/${seg(groupId)}`);

/** PUT /api/v1/phone-groups/{id}/books: the group's named books, in delivery order. */
export const setGroupBooks = (groupId: Id, bookIds: Id[]) =>
  request<Group>("PUT", `/api/v1/phone-groups/${seg(groupId)}/books`, {
    body: { bookIds },
  });

export const listMulticastChannels = async (signal?: AbortSignal) => {
  const path = "/api/v1/multicast-channels";
  return listItems<CatalogChannel>(
    await request<unknown>("GET", path, { signal }),
    path,
  );
};

export const createMulticastChannel = (input: CatalogChannelInput) =>
  request<CatalogChannel>("POST", "/api/v1/multicast-channels", {
    body: input,
  });

export const updateMulticastChannel = (id: Id, input: CatalogChannelInput) =>
  request<CatalogChannel>("PUT", `/api/v1/multicast-channels/${seg(id)}`, {
    body: input,
  });

export const deleteMulticastChannel = (id: Id) =>
  request<void>("DELETE", `/api/v1/multicast-channels/${seg(id)}`);

/**
 * PUT /api/v1/phone-groups/{id}/multicast: replaces the group's catalog
 * channels and, when given, its paging options.
 */
export const setGroupMulticast = (
  groupId: Id,
  channels: MulticastInput[],
  options?: PagingOptions,
) =>
  request<Group>("PUT", `/api/v1/phone-groups/${seg(groupId)}/multicast`, {
    body: options ? { channels, options } : { channels },
  });

/** GET|PUT /api/v1/phone-groups/{id}/ldap (pbx-gaps S-26). */
export interface GroupLdap {
  enabled: boolean;
  served: boolean;
  host: string;
  port: number;
  base: string;
  phoneCount: number;
}

export const getGroupLdap = (groupId: Id, signal?: AbortSignal) =>
  request<GroupLdap>("GET", `/api/v1/phone-groups/${seg(groupId)}/ldap`, {
    signal,
  });

export const setGroupLdap = (groupId: Id, enabled: boolean) =>
  request<GroupLdap>("PUT", `/api/v1/phone-groups/${seg(groupId)}/ldap`, {
    body: { enabled },
  });

/** PUT /api/v1/phone-groups/{id}/ptt: replaces the group's push-to-talk settings. */
export const setGroupPtt = (groupId: Id, ptt: Ptt) =>
  request<Group>("PUT", `/api/v1/phone-groups/${seg(groupId)}/ptt`, {
    body: {
      enabled: ptt.enabled,
      channelId: ptt.channelId,
      defaultChannel: ptt.defaultChannel,
      priorityChannel: ptt.priorityChannel,
      emergencyChannel: ptt.emergencyChannel,
      acceptWhileBusy: ptt.acceptWhileBusy,
      groupPaging: ptt.groupPaging,
      defaultGroup: ptt.defaultGroup,
      priorityGroup: ptt.priorityGroup,
      emergencyGroup: ptt.emergencyGroup,
      channels: ptt.channels,
    },
  });

// --- display -------------------------------------------------------------------------

/** "First Last", or the company of a company-only entry. */
export function entryName(
  e: Pick<EntryInput, "firstName" | "lastName" | "company">,
): string {
  const person = `${e.firstName} ${e.lastName}`.trim();
  return person || e.company || "—";
}

export const BOOK_KIND_LABEL: Record<BookKind, string> = {
  company: "Company",
  named: "Named",
  extensions: "Extensions",
  personal: "Personal",
};
