import type { CSSProperties } from "react";

/** Props for <Sparkline>. */
export interface SparklineProps {
  data: ReadonlyArray<number>;
  color?: string;
  height?: number;
  fill?: boolean;
  className?: string;
  style?: CSSProperties;
}

/** A tiny decorative trend line; put the number it illustrates in text. */
export function Sparkline({
  data,
  color = "var(--az-brand-mint)",
  height = 45,
  fill = true,
  className,
  style,
}: SparklineProps) {
  if (data.length < 2) {
    return <div className={className} style={{ height, ...style }} />;
  }
  const max = Math.max(...data);
  const min = Math.min(...data);
  const span = max - min || 1;
  const pts = data
    .map(
      (v, i) =>
        `${(i / (data.length - 1)) * 100},${height - 2 - ((v - min) / span) * (height - 4)}`,
    )
    .join(" ");
  return (
    <svg
      className={className}
      viewBox={`0 0 100 ${height}`}
      preserveAspectRatio="none"
      style={{
        width: "100%",
        height,
        display: "block",
        overflow: "visible",
        ...style,
      }}
      aria-hidden="true"
    >
      {fill && (
        <polygon
          points={`0,${height} ${pts} 100,${height}`}
          style={{ fill: color, opacity: 0.14 }}
        />
      )}
      <polyline
        points={pts}
        fill="none"
        strokeWidth="1.75"
        strokeLinejoin="round"
        strokeLinecap="round"
        vectorEffect="non-scaling-stroke"
        style={{ stroke: color }}
      />
    </svg>
  );
}
