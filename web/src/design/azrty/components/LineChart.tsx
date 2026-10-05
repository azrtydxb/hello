import type { CSSProperties } from "react";
import { cx } from "./cx";

/** One line series entry. */
export interface LineSeries {
  name?: string;
  data: ReadonlyArray<number>;
  /** Defaults to the chart series colour for its position. */
  color?: string;
}

/** Props for <LineChart>. */
export interface LineChartProps {
  series: ReadonlyArray<LineSeries>;
  labels?: ReadonlyArray<string>;
  height?: number;
  yTicks?: number;
  format?: (v: number) => string;
  /** 12% area fill under the first series. */
  area?: boolean;
  /** Accessible summary of what the chart shows. */
  label?: string;
  className?: string;
  style?: CSSProperties;
}

/** A multi-series line chart with a dashed y grid. */
export function LineChart({
  series,
  labels = [],
  height = 160,
  yTicks = 4,
  format = (v) => String(v),
  area = true,
  label,
  className,
  style,
}: LineChartProps) {
  const all = series.flatMap((s) => s.data);
  const max = Math.max(1, ...all) * 1.1;
  const n = Math.max(2, ...series.map((s) => s.data.length));
  const x = (i: number) => (i / (n - 1)) * 100;
  const y = (v: number) => height - (v / max) * height;
  const ticks = Array.from(
    { length: yTicks + 1 },
    (_, i) => (max * (yTicks - i)) / yTicks,
  );
  return (
    <div className={cx("az-chart", className)} style={style}>
      <div className="az-chart__scale" style={{ height }}>
        {ticks.map((t, i) => (
          <span key={i} style={{ top: `${(y(t) / height) * 100}%` }}>
            {format(Math.round(t))}
          </span>
        ))}
      </div>
      <svg
        viewBox={`0 0 100 ${height}`}
        preserveAspectRatio="none"
        style={{ width: "100%", height, display: "block", overflow: "visible" }}
        role="img"
        aria-label={label}
      >
        {ticks.map((t, i) => (
          <line
            key={i}
            x1="0"
            x2="100"
            y1={y(t)}
            y2={y(t)}
            strokeWidth="1"
            vectorEffect="non-scaling-stroke"
            strokeDasharray={i === yTicks ? undefined : "2 4"}
            style={{ stroke: "var(--az-border)" }}
          />
        ))}
        {series.map((s, si) => {
          const c = s.color || `var(--az-chart-${si + 1})`;
          const pts = s.data.map((v, i) => `${x(i)},${y(v)}`).join(" ");
          return (
            <g key={si}>
              {area && si === 0 && (
                <polygon
                  points={`0,${height} ${pts} ${x(s.data.length - 1)},${height}`}
                  style={{ fill: c, opacity: 0.12 }}
                />
              )}
              <polyline
                points={pts}
                fill="none"
                strokeWidth="2"
                strokeLinejoin="round"
                strokeLinecap="round"
                vectorEffect="non-scaling-stroke"
                style={{ stroke: c }}
              />
            </g>
          );
        })}
      </svg>
      {labels.length > 0 && (
        <div className="az-chart__x">
          {labels.map((l, i) => (
            <span key={i}>{l}</span>
          ))}
        </div>
      )}
    </div>
  );
}
