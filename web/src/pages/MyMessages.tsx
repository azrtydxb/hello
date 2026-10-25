import { useEffect, useState } from "react";
import { ApiError, errorMessage } from "../api";
import { myMessagesApi } from "../api/messages";
import {
  Alert,
  EmptyState,
  PageHeader,
  Spinner,
} from "../design/azrty/components";
import { Workspace } from "./messages/Workspace";

type Access =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "unlinked" }
  | { status: "ready" };

/**
 * The signed-in user's own text messages: those their extension sent and
 * received, and sending from it. Any role may use it; a user an
 * administrator has not linked to an extension has none.
 */
export function MyMessages() {
  const [access, setAccess] = useState<Access>({ status: "loading" });

  useEffect(() => {
    const controller = new AbortController();
    myMessagesApi
      .conversations({}, controller.signal)
      .then(() => setAccess({ status: "ready" }))
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

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Directory"
        title="My messages"
        description="Text messages to and from your extension. They arrive on your desk phone too; a phone that is switched off gets them when it registers again."
      />
      {access.status === "loading" && <Spinner label="Loading messages…" />}
      {access.status === "error" && (
        <Alert tone="bad" title="Could not load your messages">
          {access.message}
        </Alert>
      )}
      {access.status === "unlinked" && (
        <EmptyState
          icon="message-square"
          title="Your account is not linked to an extension"
          description="Messages belong to your extension. Ask an administrator to link your account to it on the Users page, then come back."
        />
      )}
      {access.status === "ready" && (
        <Workspace
          api={myMessagesApi}
          canRead
          canSend
          allowBroadcast={false}
          heading="My conversations"
        />
      )}
    </section>
  );
}
