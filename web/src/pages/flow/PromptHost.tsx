// "Generate prompt" (spec P7): the text prompt editor of the Announcements
// page, opened from the canvas. Generation calls the speech provider and
// stores the audio, which a batch cannot roll back, so it happens at once and
// is not part of the draft; the canvas reloads and shows the announcement.

import { useEffect, useState } from "react";
import { errorMessage, request, type Announcement } from "../../api";
import type { FlowNode } from "../../api/flow";
import { Alert, Drawer, Spinner } from "../../design/azrty/components";
import { PromptEditor } from "../../forms/PromptForm";
import { idOf } from "./objects";

export function PromptHost({
  node,
  onClose,
  onDone,
}: {
  /** The announcement to write the text of; null for a new one. */
  node: FlowNode | null;
  onClose: () => void;
  onDone: (id: string | number, message: string) => void | Promise<void>;
}) {
  const [announcement, setAnnouncement] = useState<Announcement | null>(null);
  const [error, setError] = useState("");
  useEffect(() => {
    if (!node) return;
    const ctl = new AbortController();
    request<Announcement>(
      "GET",
      `/api/v1/announcements/${encodeURIComponent(idOf(node.id))}`,
      { signal: ctl.signal },
    )
      .then(setAnnouncement)
      .catch((e: unknown) => {
        if (!ctl.signal.aborted) setError(errorMessage(e));
      });
    return () => ctl.abort();
  }, [node]);

  if (node && !announcement) {
    return (
      <Drawer title={node.label} onClose={onClose} width={480}>
        {error ? (
          <Alert tone="bad" title="Could not read the announcement">
            {error}
          </Alert>
        ) : (
          <Spinner label="Loading the announcement" />
        )}
      </Drawer>
    );
  }
  return (
    <PromptEditor
      announcement={node ? announcement : null}
      onClose={onClose}
      onDone={(a, message) => void onDone(a.id, message)}
    />
  );
}
