import type { CSSProperties, ReactNode } from "react";
import { cx } from "./cx";
import { Icon } from "./Icon";
import { Sparkline } from "./Sparkline";

/** Props for <StatCard>. */
export interface StatCardProps {
  label: ReactNode;
  /** The number; unknown is "—", never 0. */
  value: ReactNode;
  unit?: ReactNode;
  icon?: string;
  sub?: ReactNode;
  delta?: { value: ReactNode; direction?: "up" | "down" };
  trend?: ReadonlyArray<number>;
  trendColor?: string;
  className?: string;
  style?: CSSProperties;
}

/** A headline number card with optional delta and sparkline. */
export function StatCard({
  label,
  value,
  unit,
  icon,
  sub,
  delta,
  trend,
  trendColor,
  className,
  style,
}: StatCardProps) {
  return (
    <div className={cx("az-card", "az-stat", className)} style={style}>
      <div className="az-stat__head">
        <span>{label}</span>
        {icon && <Icon name={icon} size={15} />}
      </div>
      <div className="az-stat__value">
        {value}
        {unit && <span className="az-stat__unit">{unit}</span>}
      </div>
      {(sub || delta) && (
        <div className="az-stat__sub">
          {delta && (
            <span className={`az-stat__delta--${delta.direction || "up"}`}>
              {delta.value}
            </span>
          )}
          {sub && <span>{sub}</span>}
        </div>
      )}
      {trend && (
        <div className="az-stat__spark">
          <Sparkline data={trend} color={trendColor} height={36} />
        </div>
      )}
    </div>
  );
}
