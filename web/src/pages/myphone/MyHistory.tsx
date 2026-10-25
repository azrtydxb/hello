import { useEffect, useState } from "react";
import { errorMessage } from "../../api";
import { listMyCalls, type SelfCall } from "../../api/self";
import {
  Alert,
  Badge,
  Button,
  EmptyState,
  PageHeader,
  Spinner,
  Table,
} from "../../design/azrty/components";
import { formatDuration, formatTime } from "../../format";

/** The calls of the user's own extensions. */
export function MyHistory() {
  const [rows, setRows] = useState<SelfCall[]>([]);
  const [next, setNext] = useState("");
  const [state, setState] = useState<"loading" | "ready" | "error">("loading");
  const [error, setError] = useState("");

  function show(page: { items: SelfCall[]; next: string }, append: boolean) {
    setRows((r) => (append ? [...r, ...page.items] : page.items));
    setNext(page.next);
    setState("ready");
  }

  function fail(err: unknown) {
    setError(errorMessage(err));
    setState("error");
  }

  useEffect(() => {
    const ctl = new AbortController();
    listMyCalls(undefined, ctl.signal)
      .then((page) => show(page, false))
      .catch((err: unknown) => {
        if (!ctl.signal.aborted) fail(err);
      });
    return () => ctl.abort();
  }, []);

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="My phone"
        title="Call history"
        description="Calls made and received on your extensions."
      />
      {state === "loading" && <Spinner label="Loading calls…" />}
      {state === "error" && (
        <Alert tone="bad" title="Could not load your calls">
          {error}
        </Alert>
      )}
      {state === "ready" && rows.length === 0 && (
        <EmptyState icon="history" title="No calls yet" />
      )}
      {rows.length > 0 && (
        <>
          <Table
            caption="Your calls"
            rows={rows}
            rowKey={(c) => String(c.id)}
            columns={[
              {
                key: "time",
                label: "Time",
                render: (c) => formatTime(c.startTime),
              },
              {
                key: "from",
                label: "From",
                mono: true,
                render: (c) => c.source || "—",
              },
              {
                key: "to",
                label: "To",
                mono: true,
                render: (c) => c.destination || "—",
              },
              {
                key: "dir",
                label: "Direction",
                primary: false,
                render: (c) => c.direction || c.kind,
              },
              {
                key: "result",
                label: "Result",
                primary: false,
                render: (c) =>
                  c.answered ? (
                    <Badge tone="good">Answered</Badge>
                  ) : (
                    <Badge tone="neutral">Not answered</Badge>
                  ),
              },
              {
                key: "len",
                label: "Length",
                align: "right",
                mono: true,
                primary: false,
                render: (c) => formatDuration(c.durationMs),
              },
            ]}
          />
          {next && (
            <p>
              <Button
                variant="secondary"
                onClick={() =>
                  void listMyCalls(next)
                    .then((p) => show(p, true))
                    .catch(fail)
                }
              >
                Load more
              </Button>
            </p>
          )}
        </>
      )}
    </section>
  );
}
