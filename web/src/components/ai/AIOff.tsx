import { Link } from "react-router";
import { EmptyState } from "../../design/azrty/components";

const REASONS: Record<string, string> = {
  not_configured:
    "No model endpoint is configured. Set HELLO_AI_BASE_URL and HELLO_AI_MODEL on hello-control.",
  incomplete_configuration:
    "The model endpoint is only partly configured: both HELLO_AI_BASE_URL and HELLO_AI_MODEL are needed.",
  endpoint_not_private:
    "The model endpoint is not on a private network, and public endpoints are not allowed.",
};

/** What every AI page shows while the agent is off. */
export function AIOff({ reason }: { reason?: string | null }) {
  return (
    <EmptyState
      icon="bot"
      title="The AI agent is off"
      description={
        <>
          {(reason && REASONS[reason]) ?? reason ?? "AI is not enabled."}{" "}
          <Link to="/ai/status">See the AI status</Link>.
        </>
      }
    />
  );
}
