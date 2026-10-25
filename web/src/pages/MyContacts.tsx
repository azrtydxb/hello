import { useEffect, useState } from "react";
import { ApiError, errorMessage } from "../api";
import { myContactsApi, type Book } from "../api/phonebook";
import {
  Alert,
  EmptyState,
  PageHeader,
  Spinner,
} from "../design/azrty/components";
import { EntriesView } from "./Phonebooks";

type Access =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "unlinked" }
  | { status: "ready"; count: number };

/**
 * The signed-in user's own contacts (spec P-9): a personal address book
 * delivered to the phones of the user's extension and used to name the
 * user's callers. Any role may keep one; a user an administrator has not
 * linked to an extension has none.
 */
export function MyContacts() {
  const [access, setAccess] = useState<Access>({ status: "loading" });

  useEffect(() => {
    const controller = new AbortController();
    myContactsApi
      .list("", controller.signal)
      .then((p) =>
        setAccess({
          status: "ready",
          count: p.items.length + (p.next ? 1 : 0),
        }),
      )
      .catch((err: unknown) => {
        if (controller.signal.aborted) return;
        setAccess(
          err instanceof ApiError && err.code === "no_extension"
            ? { status: "unlinked" }
            : { status: "error", message: errorMessage(err) },
        );
      });
    return () => controller.abort();
  }, []);

  // The shape the shared entries view expects of a book; only its name and
  // count are shown.
  const book: Book = {
    id: "me",
    kind: "personal",
    name: "My contacts",
    version: 1,
    entryCount: access.status === "ready" ? access.count : 0,
    createdAt: "",
    updatedAt: "",
  };

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="My contacts"
        description="Your own address book. It shows on the desk phones of your extension and names your callers; nobody else, administrators included, can read it."
      />
      <div className="prov-stack">
        {access.status === "loading" && <Spinner label="Loading contacts…" />}
        {access.status === "error" && (
          <Alert tone="bad" title="Could not load your contacts">
            {access.message}
          </Alert>
        )}
        {access.status === "unlinked" && (
          <EmptyState
            icon="book-user"
            title="Your account is not linked to an extension"
            description="Personal contacts belong to the phones of your extension. Ask an administrator to link your account to it on the Users page, then come back."
          />
        )}
        {access.status === "ready" && (
          <EntriesView
            book={book}
            api={myContactsApi}
            selfService
            onCount={(count) => setAccess({ status: "ready", count })}
          />
        )}
      </div>
    </section>
  );
}
