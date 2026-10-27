// Syslog lines as a table with a minimum-severity filter, shared by the
// phone's Syslog tab and the unknown-sources page (phone-feature-parity S-27).
import { useState } from "react";
import { formatTime } from "../../format";
import type { SyslogLine } from "../../api/prov";
import {
  Badge,
  Select,
  Table,
  type TableColumn,
} from "../../design/azrty/components";

const SEVERITY = [
  "emerg",
  "alert",
  "crit",
  "error",
  "warning",
  "notice",
  "info",
  "debug",
];

function severityTone(n: number): "bad" | "warn" | "neutral" {
  if (n >= 0 && n <= 3) return "bad";
  if (n === 4) return "warn";
  return "neutral";
}

/** The filter choices: the least severe level shown (a higher number is less severe). */
export const SEVERITY_FILTERS = [
  { value: "7", label: "All levels" },
  { value: "6", label: "Info and above" },
  { value: "4", label: "Warning and above" },
  { value: "3", label: "Error and above" },
] as const;

/**
 * Keep the lines at or above `max` severity (0 emergency to 7 debug). A line
 * without a priority (-1) is Hello's own note or unclassified, and always
 * stays.
 */
export function filterBySeverity(
  lines: SyslogLine[],
  max: number,
): SyslogLine[] {
  return lines.filter((l) => l.severity < 0 || l.severity <= max);
}

export function SyslogTable({
  lines,
  caption,
  showSource = false,
}: {
  lines: SyslogLine[];
  caption: string;
  showSource?: boolean;
}) {
  const [max, setMax] = useState("7");
  const shown = filterBySeverity(lines, Number(max));
  const columns: TableColumn<SyslogLine>[] = [
    { key: "at", label: "Time", render: (l) => formatTime(l.at) },
    ...(showSource
      ? [
          {
            key: "source",
            label: "Source",
            mono: true,
          } as TableColumn<SyslogLine>,
        ]
      : []),
    {
      key: "severity",
      label: "Level",
      render: (l) => (
        <Badge tone={severityTone(l.severity)}>
          {SEVERITY[l.severity] ?? "—"}
        </Badge>
      ),
    },
    { key: "message", label: "Message", mono: true },
  ];
  return (
    <div className="prov-stack">
      <Select
        id={`syslog-level-${caption}`}
        label="Show"
        value={max}
        onChange={(e) => setMax(e.target.value)}
        options={SEVERITY_FILTERS.map((f) => ({
          value: f.value,
          label: f.label,
        }))}
      />
      {shown.length === 0 ? (
        <p className="prov-muted">No line matches this level.</p>
      ) : (
        <Table
          caption={caption}
          columns={columns}
          rows={shown}
          rowKey={(l, i) => `${l.at}-${i}`}
        />
      )}
    </div>
  );
}
