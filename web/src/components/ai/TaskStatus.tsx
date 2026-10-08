import { Alert, Spinner } from "../../design/azrty/components";
import type { AITask } from "../../api/aiagent";
import { PlainText } from "./PlainText";

/** A running assistant task, or why it failed. Nothing for a finished one. */
export function TaskStatus({ task }: { task: AITask }) {
  if (task.status === "queued" || task.status === "running") {
    return (
      <Spinner
        label={
          task.status === "queued"
            ? "Waiting to start…"
            : "The assistant is working…"
        }
      />
    );
  }
  if (task.status === "failed") {
    return (
      <Alert tone="bad" title="The assistant could not answer">
        <PlainText
          text={`${task.errorCode ?? "error"}${task.errorMessage ? `: ${task.errorMessage}` : ""}`}
        />
      </Alert>
    );
  }
  return null;
}
