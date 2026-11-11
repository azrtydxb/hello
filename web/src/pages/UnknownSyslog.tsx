import { useEffect, useState } from "react";
import { errorMessage } from "../api";
import {
  listUnknownSyslog,
  listUnknownSyslogSources,
  type SyslogLine,
} from "../api/prov";
import { Alert, Button, PageHeader, Spinner } from "../design/azrty/components";
import { PhonesTabs } from "./phones/ui";
import { SyslogTable } from "./phones/syslog";

/**
 * Syslog from addresses no provisioned phone owns (phone-feature-parity
 * S-27): a phone behind NAT, an unprovisioned phone, or another sender. Hello
 * keeps their lines under the source address for 24 h.
 */
export function UnknownSyslog() {
  const [sources, setSources] = useState<string[] | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [ip, setIp] = useState<string | null>(null);
  const [lines, setLines] = useState<SyslogLine[] | null>(null);

  useEffect(() => {
    const c = new AbortController();
    listUnknownSyslogSources(c.signal)
      .then(setSources)
      .catch((err: unknown) => {
        if (!c.signal.aborted) setError(errorMessage(err));
      });
    return () => c.abort();
  }, []);

  useEffect(() => {
    if (ip === null) return;
    const c = new AbortController();
    listUnknownSyslog(ip, 200, c.signal)
      .then((page) => setLines(page.items))
      .catch((err: unknown) => {
        if (!c.signal.aborted) setError(errorMessage(err));
      });
    return () => c.abort();
  }, [ip]);

  function pick(next: string) {
    setLines(null);
    setError(null);
    setIp(next);
  }

  return (
    <section aria-labelledby="page-title">
      <PageHeader
        eyebrow="Phones"
        title="Unknown syslog"
        description="Syslog lines from addresses that no provisioned phone owns, kept under the sender's address for 24 hours."
      />
      <PhonesTabs />
      <div className="prov-stack">
        {error && (
          <Alert tone="bad" title="Could not load the syslog">
            {error}
          </Alert>
        )}
        {sources === null && !error && <Spinner label="Loading senders…" />}
        {sources !== null && sources.length === 0 && (
          <p className="prov-muted">
            No unknown sender has sent syslog in the last 24 hours.
          </p>
        )}
        {sources !== null && sources.length > 0 && (
          <ul className="pb-books" aria-label="Unknown senders">
            {sources.map((s) => (
              <li key={s} className="pb-books__item">
                <span className="cf-mono">{s}</span>
                <Button
                  variant="secondary"
                  size="sm"
                  aria-pressed={ip === s}
                  onClick={() => pick(s)}
                >
                  Show lines
                </Button>
              </li>
            ))}
          </ul>
        )}
        {ip !== null && lines === null && !error && (
          <Spinner label="Loading lines…" />
        )}
        {lines !== null && (
          <SyslogTable caption={`Syslog from ${ip}`} lines={lines} showSource />
        )}
      </div>
    </section>
  );
}
