import { useEffect, useState, type ReactNode } from "react";
import { errorMessage, type Id } from "../../api";
import { listMyExtensions, type SelfExtension } from "../../api/self";
import {
  Alert,
  EmptyState,
  Select,
  Spinner,
} from "../../design/azrty/components";

export type MyExtensions =
  | { status: "loading" }
  | { status: "error"; message: string }
  | { status: "ready"; items: SelfExtension[] };

/** The signed-in user's own extensions. */
export function useMyExtensions(): [
  MyExtensions,
  (updated: SelfExtension) => void,
] {
  const [state, setState] = useState<MyExtensions>({ status: "loading" });
  useEffect(() => {
    const ctl = new AbortController();
    listMyExtensions(ctl.signal)
      .then((items) => setState({ status: "ready", items }))
      .catch((err: unknown) => {
        if (!ctl.signal.aborted)
          setState({ status: "error", message: errorMessage(err) });
      });
    return () => ctl.abort();
  }, []);
  function replace(updated: SelfExtension) {
    setState((s) =>
      s.status === "ready"
        ? {
            status: "ready",
            items: s.items.map((x) =>
              String(x.id) === String(updated.id) ? updated : x,
            ),
          }
        : s,
    );
  }
  return [state, replace];
}

/**
 * Loads the user's extensions and renders `children` for the chosen one; a
 * user with several picks one, a user with none is told to ask an
 * administrator.
 */
export function WithExtension({
  children,
}: {
  children: (
    ext: SelfExtension,
    replace: (updated: SelfExtension) => void,
  ) => ReactNode;
}) {
  const [exts, replace] = useMyExtensions();
  const [chosen, setChosen] = useState<Id | null>(null);
  if (exts.status === "loading")
    return <Spinner label="Loading your extension…" />;
  if (exts.status === "error")
    return (
      <Alert tone="bad" title="Could not load your extension">
        {exts.message}
      </Alert>
    );
  if (exts.items.length === 0)
    return (
      <EmptyState
        icon="phone-off"
        title="No extension linked"
        description="Your account is not linked to an extension yet. Ask an administrator to link it."
      />
    );
  const current =
    exts.items.find((x) => String(x.id) === String(chosen)) ?? exts.items[0];
  if (!current) return null;
  return (
    <>
      {exts.items.length > 1 && (
        <Select
          label="Extension"
          value={String(current.id)}
          options={exts.items.map((x) => ({
            value: String(x.id),
            label: `${x.number} ${x.name}`,
          }))}
          onChange={(e) => setChosen(e.target.value)}
        />
      )}
      <div key={String(current.id)}>{children(current, replace)}</div>
    </>
  );
}
