import type { CSSProperties, ReactNode } from "react";
import { cx } from "./cx";

const METER_TONES = {
  mint: "var(--az-brand-mint)",
  sky: "var(--az-brand-sky)",
  "sky-light": "var(--az-sky-light)",
  warn: "var(--az-warn)",
  bad: "var(--az-bad)",
} as const;

/** The allowed meter tone values. */
export type MeterTone = keyof typeof METER_TONES;

/** One meter segment entry. */
export interface MeterSegment {
  value: number;
  tone?: MeterTone;
}

/** Props for <Meter>. */
export interface MeterProps {
  value?: number;
  max?: number;
  /** Stacked segments instead of one bar. */
  segments?: ReadonlyArray<MeterSegment>;
  /** "auto" turns warn at 75% and bad at 90%. */
  tone?: MeterTone | "auto";
  label?: string;
  valueLabel?: ReactNode;
  size?: "md" | "lg";
  className?: string;
  style?: CSSProperties;
}

/** A thin usage bar (role="meter"). */
export function Meter({
  value = 0,
  max = 100,
  segments,
  tone = "mint",
  label,
  valueLabel,
  size = "md",
  className,
  style,
}: MeterProps) {
  const base: MeterTone = tone === "auto" ? "mint" : tone;
  const auto: MeterTone =
    value / max >= 0.9 ? "bad" : value / max >= 0.75 ? "warn" : base;
  const bars = segments
    ? segments.map((s) => ({
        value: s.value,
        color: METER_TONES[s.tone ?? "mint"],
      }))
    : [{ value, color: METER_TONES[tone === "auto" ? auto : base] }];
  return (
    <div
      className={cx("az-meter", size === "lg" && "az-meter--lg", className)}
      style={style}
    >
      {(label || valueLabel) && (
        <div className="az-meter__head">
          <span>{label}</span>
          {valueLabel && <span className="az-meter__value">{valueLabel}</span>}
        </div>
      )}
      <div
        className="az-meter__track"
        role="meter"
        aria-valuenow={value}
        aria-valuemin={0}
        aria-valuemax={max}
        aria-label={label}
      >
        {bars.map((b, i) => (
          <span
            key={i}
            className="az-meter__bar"
            style={{
              width: `${Math.max(0, Math.min(100, (b.value / max) * 100))}%`,
              background: b.color,
            }}
          />
        ))}
      </div>
    </div>
  );
}
